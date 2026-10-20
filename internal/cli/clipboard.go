package cli

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/4nkitd/rustdesk-cli/internal/hbb"
	pb "github.com/4nkitd/rustdesk-cli/internal/hbb/proto"
)

func newClipboardCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use: "clipboard", Short: "explicit remote text clipboard get/set",
		Long: "Read or send remote UTF-8 clipboard text, limited to 1 MiB. Never accesses the local clipboard. Uses a default remote-desktop session; no automatic clipboard sync.",
	}
	get := &cobra.Command{
		Use: "get [<ref>]", Short: "wait for one remote text clipboard update",
		Long: "Wait up to --timeout for a remote text clipboard broadcast. RustDesk may only publish changes, so an unchanged or empty clipboard can time out. Clipboard and keyboard permission are required. Writes exact text to stdout, or a JSON object with --json.",
		RunE: runClipboardGet,
	}
	set := &cobra.Command{
		Use: "set [<ref>] [text]", Short: "send explicit text or --stdin to the remote clipboard",
		Long: "Send explicit UTF-8 text, or use --stdin to read at most 1 MiB from stdin. Empty text is allowed, but older peers may ignore it. RustDesk does not acknowledge clipboard application; success means submitted, not applied.",
		RunE: runClipboardSet,
	}
	set.Flags().Bool("stdin", false, "read text from stdin instead of a text argument")
	cmd.AddCommand(get, set)
	return cmd
}

func runClipboardGet(cmd *cobra.Command, args []string) error {
	peerID, password, rest, err := resolveTarget(cmd, args)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return usageErrorf("usage: rdcli clipboard get [<ref>]")
	}
	clip, err := hbb.NewClipboard(dialOpts(cmd, peerID, password, pb.ConnType_DEFAULT_CONN))
	if err != nil {
		return err
	}
	defer clip.Close()
	text, err := clip.ReadText()
	if err != nil {
		return err
	}
	return writeClipboardGet(cmd, text)
}

func writeClipboardGet(cmd *cobra.Command, text string) error {
	if isJSONFlag(cmd) {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
			Version int    `json:"version"`
			Text    string `json:"text"`
		}{1, text})
	}
	_, err := io.WriteString(cmd.OutOrStdout(), text)
	return err
}

func runClipboardSet(cmd *cobra.Command, args []string) error {
	peerID, password, rest, err := resolveTarget(cmd, args)
	if err != nil {
		return err
	}
	stdin, _ := cmd.Flags().GetBool("stdin")
	text, err := clipboardInput(rest, stdin, cmd.InOrStdin())
	if err != nil {
		return err
	}
	clip, err := hbb.NewClipboard(dialOpts(cmd, peerID, password, pb.ConnType_DEFAULT_CONN))
	if err != nil {
		return err
	}
	defer clip.Close()
	if err := clip.WriteText(text); err != nil {
		return err
	}
	if isJSONFlag(cmd) {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
			Version      int    `json:"version"`
			Status       string `json:"status"`
			Bytes        int    `json:"bytes"`
			Acknowledged bool   `json:"acknowledged"`
		}{1, "submitted", len(text), false})
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), "clipboard text submitted; remote application is unconfirmed")
	return err
}

func clipboardInput(args []string, stdin bool, input io.Reader) (string, error) {
	var text string
	if stdin {
		if len(args) != 0 {
			return "", usageErrorf("--stdin cannot be combined with a text argument")
		}
		data, err := io.ReadAll(io.LimitReader(input, hbb.MaxClipboardTextBytes+1))
		if err != nil {
			return "", fmt.Errorf("clipboard stdin: %w", err)
		}
		text = string(data)
	} else {
		if len(args) != 1 {
			return "", usageErrorf("provide one text argument or --stdin: rdcli clipboard set [<ref>] [text]")
		}
		text = args[0]
	}
	if err := hbb.ValidateClipboardText(text); err != nil {
		return "", err
	}
	return text, nil
}

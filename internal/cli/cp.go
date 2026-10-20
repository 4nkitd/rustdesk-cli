package cli

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/4nkitd/rustdesk-cli/internal/hbb"

	pb "github.com/4nkitd/rustdesk-cli/internal/hbb/proto"
)

func newCpCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cp [<ref>] <src> <dst>",
		Short: "copy files between local and remote peer",
		RunE:  runCp,
	}
	cmd.Flags().BoolP("recursive", "r", false, "copy directories recursively")
	cmd.Flags().StringP("direction", "d", "", "transfer direction: up (local->remote) or down (remote->local)")
	cmd.Flags().Bool("resume", false, "unsupported: remote-prefix freshness cannot be verified")
	cmd.Flags().Bool("overwrite", false, "allow replacing destination files; required for uploads")
	cmd.Flags().Bool("skip", false, "skip existing destination files")
	cmd.Flags().Bool("include-hidden", false, "include hidden files and directories")
	cmd.MarkFlagsMutuallyExclusive("overwrite", "skip")
	return cmd
}

func runCp(cmd *cobra.Command, args []string) error {
	peerID, password, remArgs, err := resolveTarget(cmd, args)
	if err != nil {
		return err
	}
	if len(remArgs) != 2 {
		return usageErrorf("usage: rdcli cp [<ref>] <src> <dst>")
	}
	src, dst := remArgs[0], remArgs[1]
	recursive, _ := cmd.Flags().GetBool("recursive")
	direction, _ := cmd.Flags().GetString("direction")
	resume, _ := cmd.Flags().GetBool("resume")
	skip, _ := cmd.Flags().GetBool("skip")
	overwrite, _ := cmd.Flags().GetBool("overwrite")
	includeHidden, _ := cmd.Flags().GetBool("include-hidden")
	if skip && overwrite {
		return usageErrorf("--skip and --overwrite are mutually exclusive")
	}

	up := true
	switch strings.ToLower(direction) {
	case "":
		return usageErrorf("specify --direction up or down; local and remote paths are ambiguous")
	case "up", "to-remote":
		up = true
	case "down", "to-local":
		up = false
	default:
		return usageErrorf("invalid direction %q (want up or down)", direction)
	}
	if resume {
		return usageErrorf("--resume is unavailable: remote-prefix freshness cannot be verified; retry a fresh transfer")
	}
	if up && !overwrite {
		return usageErrorf("uploads require --overwrite; the peer protocol cannot guarantee atomic no-clobber or skip")
	}

	opts := dialOpts(cmd, peerID, password, pb.ConnType_FILE_TRANSFER)
	remoteDir := dst
	if !up {
		remoteDir = src
	}
	ft, err := hbb.NewFileTransfer(opts, remoteDir)
	if err != nil {
		return fmt.Errorf("cp: %w", err)
	}
	defer ft.Close()
	policy := hbb.TransferPolicy{Existing: "error", Resume: resume, IncludeHidden: includeHidden}
	if overwrite {
		policy.Existing = "overwrite"
	}
	if skip {
		policy.Existing = "skip"
	}
	if err := ft.SetPolicy(policy); err != nil {
		return err
	}

	progress := func(filesDone, filesTotal int, doneBytes, totalBytes int64) {
		if filesTotal == 0 {
			return
		}
		pct := 0
		if totalBytes > 0 {
			pct = int(float64(doneBytes) / float64(totalBytes) * 100)
		} else if filesDone == filesTotal {
			pct = 100
		}
		if pct > 100 {
			pct = 100
		}
		fmt.Fprintf(os.Stderr, "\r%s %d/%d files %d%%", progressSpinner(pct), filesDone, filesTotal, pct)
	}

	start := time.Now()
	if up {
		err = ft.Send(src, dst, recursive, progress)
	} else {
		err = ft.Receive(src, dst, progress)
	}
	if err != nil {
		return fmt.Errorf("cp: %w", err)
	}
	fmt.Fprintf(os.Stderr, "\rcopied in %s\n", time.Since(start).Round(time.Millisecond))
	return nil
}

func isRemotePath(p string) bool {
	if strings.HasPrefix(p, "/") {
		return true
	}
	if len(p) >= 2 && p[1] == ':' && ((p[0] >= 'A' && p[0] <= 'Z') || (p[0] >= 'a' && p[0] <= 'z')) {
		return true
	}
	return false
}

func progressSpinner(pct int) string {
	const barLen = 20
	filled := pct * barLen / 100
	return "[" + strings.Repeat("=", filled) + strings.Repeat(" ", barLen-filled) + "]"
}

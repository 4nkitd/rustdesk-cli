package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/4nkitd/rustdesk-cli/internal/hbb"
	pb "github.com/4nkitd/rustdesk-cli/internal/hbb/proto"
)

type fileOperationJSON struct {
	Version   int    `json:"version"`
	Operation string `json:"operation"`
	Path      string `json:"path,omitempty"`
	NewName   string `json:"new_name,omitempty"`
	Success   bool   `json:"success"`
	Error     string `json:"error,omitempty"`
}

func fileOperationError(cmd *cobra.Command, result fileOperationJSON, err error) error {
	if isJSONFlag(cmd) {
		result.Version = 1
		result.Success = false
		result.Error = err.Error()
		if writeErr := json.NewEncoder(cmd.OutOrStdout()).Encode(result); writeErr != nil {
			return fmt.Errorf("%s: %w (writing JSON: %v)", result.Operation, err, writeErr)
		}
	}
	return fmt.Errorf("%s: %w", result.Operation, err)
}

func newMkdirCmd() *cobra.Command {
	return &cobra.Command{
		Use: "mkdir [<ref>] <remote_path>", Short: "create a remote directory, including missing parents",
		RunE: runFileMutation,
	}
}

func newRmCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use: "rm [<ref>] <remote_path>", Short: "delete a remote file or an explicitly selected empty directory",
		RunE: runFileMutation,
	}
	cmd.Flags().Bool("confirm-delete", false, "explicitly authorize deletion (required unless --force)")
	cmd.Flags().BoolP("force", "f", false, "explicitly authorize deletion; remote errors are still reported")
	cmd.Flags().BoolP("dir", "d", false, "remove an empty directory instead of a file; never recursive")
	return cmd
}

func newRenameCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use: "rename [<ref>] <remote_path> <new_name>", Short: "rename within the same remote directory; requires --overwrite consent",
		RunE: runFileMutation,
	}
	cmd.Flags().Bool("overwrite", false, "authorize possible replacement of an existing destination; required for every rename")
	return cmd
}

func runFileMutation(cmd *cobra.Command, args []string) error {
	result := fileOperationJSON{Version: 1, Operation: cmd.Name()}
	fail := func(err error) error { return fileOperationError(cmd, result, err) }
	peerID, password, remArgs, err := resolveTarget(cmd, args)
	if err != nil {
		return fail(err)
	}
	wantArgs := 1
	if cmd.Name() == "rename" {
		wantArgs = 2
	}
	if len(remArgs) != wantArgs {
		return fail(usageErrorf("usage: rdcli %s", cmd.Use))
	}
	result.Path = remArgs[0]
	if err := hbb.ValidateRemoteMutationPath(result.Path); err != nil {
		return fail(usageErrorf("%v", err))
	}
	if cmd.Name() == "rename" {
		result.NewName = remArgs[1]
		if err := hbb.ValidateRemoteNewName(result.NewName); err != nil {
			return fail(usageErrorf("%v", err))
		}
		overwrite, _ := cmd.Flags().GetBool("overwrite")
		if !overwrite {
			return fail(usageErrorf("rename may replace an existing destination: RustDesk cannot guarantee atomic no-clobber; explicit --overwrite is required; --yes only permits insecure transport"))
		}
	}
	if cmd.Name() == "rm" {
		confirm, _ := cmd.Flags().GetBool("confirm-delete")
		force, _ := cmd.Flags().GetBool("force")
		if !confirm && !force {
			return fail(usageErrorf("deletion requires --confirm-delete or --force; --yes only permits insecure transport"))
		}
	}
	opts := dialOpts(cmd, peerID, password, pb.ConnType_FILE_TRANSFER)
	ft, err := hbb.NewFileOperations(opts)
	if err != nil {
		return fail(err)
	}
	defer ft.Close()
	switch cmd.Name() {
	case "mkdir":
		err = ft.Mkdir(result.Path)
	case "rm":
		dir, _ := cmd.Flags().GetBool("dir")
		if dir {
			err = ft.RemoveEmptyDir(result.Path)
		} else {
			err = ft.RemoveFile(result.Path)
		}
	case "rename":
		err = ft.Rename(result.Path, result.NewName)
	}
	if err != nil {
		return fail(err)
	}
	result.Success = true
	if isJSONFlag(cmd) {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", result.Operation, result.Path)
	return err
}

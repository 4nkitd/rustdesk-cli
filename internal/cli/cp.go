package cli

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"rdcli/internal/hbb"

	pb "rdcli/internal/hbb/proto"
)

func newCpCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cp [<ref>] <src> <dst>",
		Short: "copy files between local and remote peer",
		RunE:  runCp,
	}
	cmd.Flags().BoolP("recursive", "r", false, "copy directories recursively")
	cmd.Flags().StringP("direction", "d", "", "transfer direction: up (local->remote) or down (remote->local)")
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

	up := true
	switch strings.ToLower(direction) {
	case "":
		up = isRemotePath(dst)
	case "up", "to-remote":
		up = true
	case "down", "to-local":
		up = false
	default:
		return usageErrorf("invalid direction %q (want up or down)", direction)
	}

	opts := dialOpts(cmd, peerID, password, pb.ConnType_FILE_TRANSFER)
	remoteDir := dst
	if !up {
		remoteDir = src
	}
	ft, err := hbb.NewFileTransfer(opts, remoteDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cp: %v\n", err)
		os.Exit(1)
		return nil
	}
	defer ft.Close()

	progress := func(filesDone, filesTotal int, doneBytes, totalBytes int64) {
		if filesTotal == 0 {
			return
		}
		pct := 0
		if totalBytes > 0 {
			pct = int(float64(doneBytes) / float64(totalBytes) * 100)
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
		fmt.Fprintf(os.Stderr, "\rcp: %v\n", err)
		os.Exit(1)
		return nil
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

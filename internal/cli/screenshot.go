package cli

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/4nkitd/rustdesk-cli/internal/hbb"
	pb "github.com/4nkitd/rustdesk-cli/internal/hbb/proto"
)

type screenshotJSON struct {
	Version     int    `json:"version"`
	PeerID      string `json:"peer_id"`
	PeerVersion string `json:"peer_version"`
	Display     int32  `json:"display"`
	Output      string `json:"output"`
	Format      string `json:"format"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	Bytes       int    `json:"bytes"`
}

func newScreenshotCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "screenshot [<ref>] --output <file>",
		Short: "save a remote display screenshot as PNG (RustDesk 1.4.0+)",
		Args:  cobra.MaximumNArgs(1),
		RunE:  runScreenshot,
	}
	cmd.Flags().Int32("display", 0, "zero-based remote display index")
	cmd.Flags().StringP("output", "o", "", "local PNG file (required; stdout is not supported)")
	cmd.Flags().Bool("overwrite", false, "replace an existing regular output file")
	return cmd
}

func runScreenshot(cmd *cobra.Command, args []string) error {
	output, _ := cmd.Flags().GetString("output")
	if strings.TrimSpace(output) == "" || output == "-" {
		return usageErrorf("screenshot requires --output <file>; stdout is not supported")
	}
	display, _ := cmd.Flags().GetInt32("display")
	if display < 0 {
		return usageErrorf("screenshot: --display must be non-negative")
	}
	peerID, password, remArgs, err := resolveTarget(cmd, args)
	if err != nil {
		return err
	}
	if len(remArgs) != 0 {
		return usageErrorf("usage: rdcli screenshot [<ref>] --output <file>")
	}
	overwrite, _ := cmd.Flags().GetBool("overwrite")
	root, name, err := screenshotOutputRoot(output, overwrite)
	if err != nil {
		return err
	}
	defer root.Close()
	shot, err := hbb.TakeScreenshot(dialOpts(cmd, peerID, password, pb.ConnType_DEFAULT_CONN), display)
	if err != nil {
		return err
	}
	if err := writeScreenshotFile(root, name, shot.Data, overwrite); err != nil {
		return fmt.Errorf("screenshot: save: %w", err)
	}
	metadata := screenshotJSON{
		Version: 1, PeerID: peerID, PeerVersion: shot.PeerVersion,
		Display: display, Output: output, Format: "png",
		Width: shot.Width, Height: shot.Height, Bytes: len(shot.Data),
	}
	if isJSONFlag(cmd) {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(metadata)
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "Saved %dx%d PNG to %s (%d bytes)\n", shot.Width, shot.Height, output, len(shot.Data))
	return err
}

func screenshotOutputRoot(path string, overwrite bool) (*os.Root, string, error) {
	if path == "" || os.IsPathSeparator(path[len(path)-1]) {
		return nil, "", errors.New("screenshot: output must name a file")
	}
	path = filepath.Clean(path)
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, "", fmt.Errorf("screenshot: output directory: %w", err)
	}
	name := filepath.Base(path)
	if err := checkScreenshotOutput(root, name, overwrite); err != nil {
		root.Close()
		return nil, "", err
	}
	return root, name, nil
}

func checkScreenshotOutput(root *os.Root, name string, overwrite bool) error {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("screenshot: output %q is not a regular file", name)
	}
	if !overwrite {
		return fmt.Errorf("screenshot: output %q already exists; use --overwrite to replace it", name)
	}
	return nil
}

func writeScreenshotFile(root *os.Root, name string, data []byte, overwrite bool) error {
	temp := ".rdcli-screenshot-" + rand.Text() + ".tmp"
	file, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer root.Remove(temp)
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := checkScreenshotOutput(root, name, overwrite); err != nil {
		return err
	}
	if overwrite {
		return root.Rename(temp, name)
	}
	// A hard link publishes the completed file without a check-then-rename overwrite race.
	return root.Link(temp, name)
}

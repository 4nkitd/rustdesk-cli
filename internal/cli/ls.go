package cli

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/4nkitd/rustdesk-cli/internal/hbb"

	pb "github.com/4nkitd/rustdesk-cli/internal/hbb/proto"
)

type lsEntryJSON struct {
	Name         string `json:"name"`
	Type         string `json:"type"`
	Size         uint64 `json:"size"`
	ModifiedTime uint64 `json:"modified_time"`
	IsHidden     bool   `json:"is_hidden"`
}

type lsJSON struct {
	Version int           `json:"version"`
	Path    string        `json:"path"`
	Entries []lsEntryJSON `json:"entries"`
}

func newLsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ls [<ref>] [remote_path]",
		Short: "remote directory listing",
		RunE:  runLs,
	}
	cmd.Flags().BoolP("all", "a", false, "include hidden entries")
	return cmd
}

func runLs(cmd *cobra.Command, args []string) error {
	result := fileOperationJSON{Operation: "ls"}
	fail := func(err error) error { return fileOperationError(cmd, result, err) }
	peerID, password, remArgs, err := resolveTarget(cmd, args)
	if err != nil {
		return fail(err)
	}
	if len(remArgs) > 1 {
		return fail(usageErrorf("usage: rdcli %s", cmd.Use))
	}
	remotePath := "."
	if len(remArgs) >= 1 {
		remotePath = remArgs[0]
	}
	result.Path = remotePath
	opts := dialOpts(cmd, peerID, password, pb.ConnType_FILE_TRANSFER)
	all, _ := cmd.Flags().GetBool("all")
	dir, err := hbb.ReadDirWithHidden(opts, remotePath, all)
	if err != nil {
		return fail(err)
	}
	entries := append([]*pb.FileEntry(nil), dir.GetEntries()...)
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].GetEntryType() != entries[j].GetEntryType() {
			return entries[i].GetEntryType() == pb.FileType_Dir
		}
		return entries[i].GetName() < entries[j].GetName()
	})

	if isJSONFlag(cmd) {
		out := lsJSON{Version: 1, Path: dir.GetPath(), Entries: make([]lsEntryJSON, 0, len(entries))}
		for _, e := range entries {
			out.Entries = append(out.Entries, lsEntryJSON{
				Name:         e.GetName(),
				Type:         lsTypeName(e.GetEntryType()),
				Size:         e.GetSize(),
				ModifiedTime: e.GetModifiedTime(),
				IsHidden:     e.GetIsHidden(),
			})
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(out)
	}

	rows := [][]string{{"NAME", "TYPE", "SIZE", "MODIFIED"}}
	for _, e := range entries {
		rows = append(rows, []string{e.GetName(), lsTypeName(e.GetEntryType()), humanSize(e.GetSize()), humanTime(e.GetModifiedTime())})
	}
	printAligned(rows)
	return nil
}

func lsTypeName(t pb.FileType) string {
	switch t {
	case pb.FileType_Dir, pb.FileType_DirLink, pb.FileType_DirDrive:
		return "dir"
	default:
		return "file"
	}
}

func humanSize(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func humanTime(sec uint64) string {
	if sec == 0 {
		return ""
	}
	return time.Unix(int64(sec), 0).Format("2006-01-02 15:04")
}

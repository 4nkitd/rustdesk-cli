package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"rdcli/internal/config"
	"rdcli/internal/hbb"

	"github.com/spf13/cobra"
)

func newTagCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tag",
		Short: "tag management",
	}

	addCmd := &cobra.Command{
		Use:   "add <id> <tag>",
		Short: "add tag to device",
		RunE:  runTagAdd,
	}
	rmCmd := &cobra.Command{
		Use:   "rm <id> <tag>",
		Short: "remove tag from device",
		RunE:  runTagRm,
	}
	lsCmd := &cobra.Command{
		Use:   "ls [id]",
		Short: "list tags",
		RunE:  runTagLs,
	}
	cmd.AddCommand(addCmd, rmCmd, lsCmd)
	return cmd
}

func runTagAdd(cmd *cobra.Command, args []string) error {
	if len(args) != 2 {
		return usageErrorf("usage: rdcli tag add <id> <tag>")
	}
	ref, tag := args[0], args[1]
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "tag add: %v\n", err)
		os.Exit(1)
		return nil
	}
	id, _, _ := ResolveRef(cfg, ref)
	src, err := hbb.NewDeviceSource(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tag add: %v\n", err)
		os.Exit(1)
		return nil
	}
	if err := src.AddTag(id, tag); err != nil {
		fmt.Fprintf(os.Stderr, "tag add: %v\n", err)
		os.Exit(1)
		return nil
	}
	fmt.Printf("added tag %q to %s\n", tag, id)
	return nil
}

func runTagRm(cmd *cobra.Command, args []string) error {
	if len(args) != 2 {
		return usageErrorf("usage: rdcli tag rm <id> <tag>")
	}
	ref, tag := args[0], args[1]
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "tag rm: %v\n", err)
		os.Exit(1)
		return nil
	}
	id, _, _ := ResolveRef(cfg, ref)
	src, err := hbb.NewDeviceSource(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tag rm: %v\n", err)
		os.Exit(1)
		return nil
	}
	if err := src.RemoveTag(id, tag); err != nil {
		fmt.Fprintf(os.Stderr, "tag rm: %v\n", err)
		os.Exit(1)
		return nil
	}
	fmt.Printf("removed tag %q from %s\n", tag, id)
	return nil
}

func runTagLs(cmd *cobra.Command, args []string) error {
	if len(args) > 1 {
		return usageErrorf("usage: rdcli tag ls [id]")
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "tag ls: %v\n", err)
		os.Exit(1)
		return nil
	}
	src, err := hbb.NewDeviceSource(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tag ls: %v\n", err)
		os.Exit(1)
		return nil
	}
	if len(args) == 1 {
		id, _, _ := ResolveRef(cfg, args[0])
		tags := sortedCopy(src.Tags(id))
		if isJSONFlag(cmd) {
			printJSONTags(map[string][]string{id: tags})
			return nil
		}
		fmt.Println(strings.Join(tags, ","))
		return nil
	}
	devs, err := src.List()
	if err != nil {
		fmt.Fprintf(os.Stderr, "tag ls: %v\n", err)
		os.Exit(1)
		return nil
	}
	all := map[string][]string{}
	for _, d := range devs {
		tags := sortedCopy(src.Tags(d.ID))
		if len(tags) > 0 {
			all[d.ID] = tags
		}
	}
	if isJSONFlag(cmd) {
		printJSONTags(all)
		return nil
	}
	ids := make([]string, 0, len(all))
	for id := range all {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		fmt.Printf("%s: %s\n", id, strings.Join(all[id], ","))
	}
	return nil
}

func newTagSource() (hbb.DeviceSource, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	return hbb.NewDeviceSource(cfg)
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	if out == nil {
		out = []string{}
	}
	return out
}

type tagsJSON struct {
	Version int                 `json:"version"`
	Tags    map[string][]string `json:"tags"`
}

func printJSONTags(tags map[string][]string) {
	for id, t := range tags {
		if t == nil {
			tags[id] = []string{}
		}
	}
	json.NewEncoder(os.Stdout).Encode(tagsJSON{Version: 1, Tags: tags})
}

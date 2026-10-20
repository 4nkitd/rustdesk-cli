package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/4nkitd/rustdesk-cli/internal/config"
	"github.com/4nkitd/rustdesk-cli/internal/hbb"

	"github.com/spf13/cobra"
)

type deviceJSON struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Tags     []string `json:"tags"`
	Online   bool     `json:"online"`
	Platform string   `json:"platform"`
	LastSeen string   `json:"last_seen"`
	Source   string   `json:"source"`
}

type devicesJSON struct {
	Version int          `json:"version"`
	Devices []deviceJSON `json:"devices"`
}

func newDevicesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "devices",
		Short: "list machines",
		RunE:  runDevices,
	}
	cmd.Flags().String("tag", "", "filter devices by tag")
	cmd.Flags().Bool("online", false, "filter online devices")

	searchCmd := &cobra.Command{
		Use:   "search <query>",
		Short: "search devices by id/name/tag",
		RunE:  runDevicesSearch,
	}
	cmd.AddCommand(searchCmd)
	return cmd
}

func runDevices(cmd *cobra.Command, args []string) error {
	if len(args) != 0 {
		return usageErrorf("usage: rdcli devices [--tag <tag>] [--online] [--json]")
	}
	tagFilter, _ := cmd.Flags().GetString("tag")
	onlineOnly, _ := cmd.Flags().GetBool("online")

	devs, err := listDevices(cmd, "", tagFilter, onlineOnly)
	if err != nil {
		fmt.Fprintf(os.Stderr, "devices: %v\n", err)
		os.Exit(1)
		return nil
	}
	printDevices(cmd, devs)
	return nil
}

func runDevicesSearch(cmd *cobra.Command, args []string) error {
	if len(args) != 1 {
		return usageErrorf("usage: rdcli devices search <query> [--json]")
	}
	devs, err := listDevices(cmd, args[0], "", false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "devices search: %v\n", err)
		os.Exit(1)
		return nil
	}
	printDevices(cmd, devs)
	return nil
}

func listDevices(cmd *cobra.Command, query, tagFilter string, onlineOnly bool) ([]hbb.Device, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	source, err := hbb.NewDeviceSource(cfg)
	if err != nil {
		return nil, fmt.Errorf("address book: %w", err)
	}
	devs, err := source.List()
	if err != nil {
		return nil, err
	}

	for i := range devs {
		if devs[i].Name == "" {
			if p, ok := cfg.Peers[devs[i].ID]; ok && p.Name != "" {
				devs[i].Name = p.Name
			}
		}
	}

	ids := make([]string, 0, len(devs))
	for _, d := range devs {
		ids = append(ids, d.ID)
	}
	online, err := hbb.BatchOnline(resolveServer(cmd, cfg), ids, getTimeoutFlag(cmd))
	if err != nil {
		return nil, fmt.Errorf("online check: %w", err)
	}

	q := strings.ToLower(query)
	out := make([]hbb.Device, 0, len(devs))
	for _, d := range devs {
		if query != "" && !matchQuery(d, q) {
			continue
		}
		if tagFilter != "" && !hasTagString(d.Tags, tagFilter) {
			continue
		}
		if onlineOnly && !online[d.ID] {
			continue
		}
		d.Online = online[d.ID]
		out = append(out, d)
	}
	return out, nil
}

func resolveServer(cmd *cobra.Command, cfg *config.Config) string {
	if s, _ := cmd.Flags().GetString("server"); s != "" {
		return s
	}
	if s := os.Getenv("RDC_SERVER"); s != "" {
		return s
	}
	if cfg != nil && cfg.Server != "" {
		return cfg.Server
	}
	return hbb.DefaultRendezvousServer
}

func matchQuery(d hbb.Device, q string) bool {
	if strings.Contains(strings.ToLower(d.ID), q) {
		return true
	}
	if strings.Contains(strings.ToLower(d.Name), q) {
		return true
	}
	for _, t := range d.Tags {
		if strings.Contains(strings.ToLower(t), q) {
			return true
		}
	}
	return false
}

func hasTagString(tags []string, tag string) bool {
	for _, t := range tags {
		if t == tag {
			return true
		}
	}
	return false
}

func printDevices(cmd *cobra.Command, devs []hbb.Device) {
	if isJSONFlag(cmd) {
		out := devicesJSON{Version: 1, Devices: make([]deviceJSON, 0, len(devs))}
		for _, d := range devs {
			tags := d.Tags
			if tags == nil {
				tags = []string{}
			}
			out.Devices = append(out.Devices, deviceJSON{
				ID:       d.ID,
				Name:     d.Name,
				Tags:     tags,
				Online:   d.Online,
				Platform: d.Platform,
				LastSeen: d.LastSeen,
				Source:   d.Source,
			})
		}
		json.NewEncoder(os.Stdout).Encode(out)
		return
	}

	rows := [][]string{{"ID", "NAME", "TAGS", "ONLINE", "PLATFORM", "LAST_SEEN"}}
	for _, d := range devs {
		online := "no"
		if d.Online {
			online = "yes"
		}
		rows = append(rows, []string{d.ID, d.Name, strings.Join(d.Tags, ","), online, d.Platform, d.LastSeen})
	}
	printAligned(rows)
}

func printAligned(rows [][]string) {
	if len(rows) == 0 {
		return
	}
	widths := make([]int, len(rows[0]))
	for _, r := range rows {
		for i, c := range r {
			if len(c) > widths[i] {
				widths[i] = len(c)
			}
		}
	}
	for _, r := range rows {
		for i, c := range r {
			if i > 0 {
				fmt.Print("  ")
			}
			fmt.Print(c)
			fmt.Print(strings.Repeat(" ", widths[i]-len(c)))
		}
		fmt.Println()
	}
}

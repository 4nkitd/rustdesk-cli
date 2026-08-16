package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"rdcli/internal/hbb"

	"github.com/spf13/cobra"
)

type pingJSON struct {
	Version   int    `json:"version"`
	ID        string `json:"id"`
	Online    bool   `json:"online"`
	LatencyMs int64  `json:"latency_ms"`
}

func newPingCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ping [<ref>]",
		Short: "check if peer is online",
		Args:  maxNArgs(1, "usage: rdcli ping <id>"),
		RunE:  runPing,
	}
}

func runPing(cmd *cobra.Command, args []string) error {
	peerID, _, remArgs, err := resolveTarget(cmd, args)
	if err != nil {
		return err
	}
	if len(remArgs) > 0 {
		return usageErrorf("unexpected arguments: %v", remArgs)
	}

	server := getServerFlag(cmd)
	timeout := getTimeoutFlag(cmd)

	start := time.Now()
	online, err := hbb.CheckOnline(server, peerID, timeout)
	latency := time.Since(start)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ping failed: %v\n", err)
		os.Exit(1)
		return nil
	}

	if isJSONFlag(cmd) {
		json.NewEncoder(os.Stdout).Encode(pingJSON{Version: 1, ID: peerID, Online: online, LatencyMs: latency.Milliseconds()})
	} else if online {
		fmt.Printf("online (%s)\n", latency.Round(time.Millisecond))
	} else {
		fmt.Printf("offline (%s)\n", latency.Round(time.Millisecond))
	}

	if !online {
		os.Exit(1)
	}
	return nil
}

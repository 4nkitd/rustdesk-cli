package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"rdcli/internal/config"
	"rdcli/internal/hbb"
	pb "rdcli/internal/hbb/proto"
)

func newConnectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "connect <id|name|tag>",
		Short: "save machine password and remember it",
		Args:  exactArgs(1, "usage: rdcli connect <id|name|tag>"),
		RunE:  runConnect,
	}
	cmd.Flags().StringP("password", "p", "", "machine password")
	return cmd
}

func newDisconnectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "disconnect <id|name|tag>",
		Short: "forget saved machine password",
		Args:  exactArgs(1, "usage: rdcli disconnect <id|name|tag>"),
		RunE:  runDisconnect,
	}
	return cmd
}

func runConnect(cmd *cobra.Command, args []string) error {
	ref := args[0]
	password, _ := cmd.Flags().GetString("password")
	if password == "" {
		password = os.Getenv("RDC_PASSWORD")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// Resolve to find the underlying ID
	id, _, _ := ResolveRef(cfg, ref)
	cfg.SetPeersPassword(id, password)

	// If this is a new peer (ref is raw ID, not in config), add it
	if _, ok := cfg.Peers[id]; !ok {
		cfg.SetPeersPassword(id, password)
	}

	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Printf("connected to %s\n", id)
	return nil
}

func runDisconnect(cmd *cobra.Command, args []string) error {
	ref := args[0]
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	id, _, _ := ResolveRef(cfg, ref)
	cfg.SetPeersPassword(id, "")

	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Printf("disconnected from %s\n", id)
	return nil
}

func dialOpts(cmd *cobra.Command, peerID, password string, connType pb.ConnType) hbb.DialOpts {
	if password == "" {
		password = getPasswordFlag(cmd)
	}
	return hbb.DialOpts{
		Server:     getServerFlag(cmd),
		PeerID:     peerID,
		Password:   password,
		Key:        getKeyFlag(cmd),
		Token:      getTokenFlag(cmd),
		ForceRelay: isRelayFlag(cmd),
		Yes:        isYesFlag(cmd),
		ConnType:   connType,
		Timeout:    getTimeoutFlag(cmd),
	}
}

package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/4nkitd/rustdesk-cli/internal/config"
	"github.com/4nkitd/rustdesk-cli/internal/hbb"
	pb "github.com/4nkitd/rustdesk-cli/internal/hbb/proto"
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
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	id, _, err := ResolveRef(cfg, ref)
	if err != nil {
		return err
	}
	password, _ := cmd.Flags().GetString("password")
	if password == "" {
		password = os.Getenv("RDC_PASSWORD")
	}
	cfg.SetPeersPassword(id, password)

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

	id, _, err := ResolveRef(cfg, ref)
	if err != nil {
		return err
	}
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
		OTP:        getOTPFlag(cmd),
		Key:        getKeyFlag(cmd),
		Token:      getTokenFlag(cmd),
		ForceRelay: isRelayFlag(cmd),
		Yes:        isYesFlag(cmd),
		ConnType:   connType,
		Timeout:    getTimeoutFlag(cmd),
	}
}

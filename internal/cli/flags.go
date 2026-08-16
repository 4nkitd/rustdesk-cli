package cli

import (
	"fmt"
	"os"
	"time"

	"rdcli/internal/config"
	"rdcli/internal/hbb"

	"github.com/spf13/cobra"
)

func registerCommonFlags(cmd *cobra.Command) {
	cmd.PersistentFlags().String("password", "", "password for peer (env: RDC_PASSWORD)")
	cmd.PersistentFlags().String("server", "", "rendezvous server override (env: RDC_SERVER)")
	cmd.PersistentFlags().String("key", "", "licence key for self-hosted server (env: RDC_KEY)")
	cmd.PersistentFlags().String("token", "", "login token (env: RDC_TOKEN)")
	cmd.PersistentFlags().Bool("relay", false, "force relay connection")
	cmd.PersistentFlags().Bool("yes", false, "auto-accept insecure connection")
	cmd.PersistentFlags().Bool("json", false, "output in JSON format")
	cmd.PersistentFlags().Duration("timeout", 10*time.Second, "connection timeout")
}

func resolveTarget(cmd *cobra.Command, args []string) (peerID, password string, remArgs []string, err error) {
	connectFlag, _ := cmd.Flags().GetString("connect")
	if connectFlag != "" {
		cfg, err := config.Load()
		if err != nil {
			return "", "", nil, fmt.Errorf("config: %w", err)
		}
		id, pw, err := ResolveRef(cfg, connectFlag)
		if err != nil {
			return "", "", nil, err
		}
		if fp := getPasswordFlag(cmd); fp != "" {
			pw = fp
		}
		return id, pw, args, nil
	}
	if len(args) == 0 {
		return "", "", nil, usageErrorf("no machine specified: rdcli <cmd> <id> [args]")
	}
	ref := args[0]
	cfg, err := config.Load()
	if err != nil {
		return "", "", nil, fmt.Errorf("config: %w", err)
	}
	id, pw, err := ResolveRef(cfg, ref)
	if err != nil {
		return "", "", nil, err
	}
	if fp := getPasswordFlag(cmd); fp != "" {
		pw = fp
	}
	return id, pw, args[1:], nil
}

func getPasswordFlag(cmd *cobra.Command) string {
	val, _ := cmd.Flags().GetString("password")
	if val != "" {
		return val
	}
	return os.Getenv("RDC_PASSWORD")
}

func getServerFlag(cmd *cobra.Command) string {
	val, _ := cmd.Flags().GetString("server")
	if val != "" {
		return val
	}
	if env := os.Getenv("RDC_SERVER"); env != "" {
		return env
	}
	return hbb.DefaultRendezvousServer
}

func getKeyFlag(cmd *cobra.Command) string {
	val, _ := cmd.Flags().GetString("key")
	if val != "" {
		return val
	}
	return os.Getenv("RDC_KEY")
}

func getTokenFlag(cmd *cobra.Command) string {
	val, _ := cmd.Flags().GetString("token")
	if val != "" {
		return val
	}
	if env := os.Getenv("RDC_TOKEN"); env != "" {
		return env
	}
	cfg, err := config.Load()
	if err == nil && cfg.AccessToken != "" {
		return cfg.AccessToken
	}
	return config.GUIAccessToken()
}

func getTimeoutFlag(cmd *cobra.Command) time.Duration {
	val, _ := cmd.Flags().GetDuration("timeout")
	if val > 0 {
		return val
	}
	return 10 * time.Second
}

func isJSONFlag(cmd *cobra.Command) bool {
	val, _ := cmd.Flags().GetBool("json")
	return val
}

func isRelayFlag(cmd *cobra.Command) bool {
	val, _ := cmd.Flags().GetBool("relay")
	return val
}

func isYesFlag(cmd *cobra.Command) bool {
	val, _ := cmd.Flags().GetBool("yes")
	return val
}

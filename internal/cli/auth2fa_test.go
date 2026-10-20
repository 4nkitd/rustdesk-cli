package cli

import (
	"os"
	"path/filepath"
	"testing"

	pb "github.com/4nkitd/rustdesk-cli/internal/hbb/proto"
	"github.com/spf13/cobra"
)

func TestAuth2FAFlag(t *testing.T) {
	for _, tc := range []struct {
		name, env, want string
		args            []string
	}{
		{"unset", "", "", nil},
		{"environment", "012345", "012345", nil},
		{"explicit", "012345", "654321", []string{"--otp=654321"}},
		{"explicit empty", "012345", "", []string{"--otp="}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("RDC_OTP", tc.env)
			root := &cobra.Command{Use: "test"}
			registerCommonFlags(root)
			child := &cobra.Command{Use: "peer", RunE: func(cmd *cobra.Command, args []string) error {
				opts := dialOpts(cmd, "peer", "password", pb.ConnType_TERMINAL)
				if opts.OTP != tc.want {
					t.Error("OTP flag/environment precedence failed")
				}
				if opts.ConnType != pb.ConnType_TERMINAL || opts.Password != "password" {
					t.Error("dial options changed")
				}
				return nil
			}}
			root.AddCommand(child)
			root.SetArgs(append([]string{"peer", "--token=test-token", "--server=fixture.example", "--key=test-key"}, tc.args...))
			if err := root.Execute(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAuth2FATokenServerScope(t *testing.T) {
	for _, tc := range []struct {
		name, cfgServer, cfgToken, server, envToken, want string
		args                                              []string
	}{
		{"public config to custom", "", "config-token", "other.example", "", "", nil},
		{"public GUI to custom", "", "", "other.example", "", "", nil},
		{"GUI to configured custom", "custom.example", "", "", "", "", nil},
		{"matching custom config", "custom.example", "config-token", "CUSTOM.EXAMPLE:21116", "", "config-token", nil},
		{"configured custom", "custom.example", "config-token", "", "", "config-token", nil},
		{"different custom", "custom.example", "config-token", "other.example", "", "", nil},
		{"different port", "custom.example", "config-token", "custom.example:21117", "", "", nil},
		{"custom config to public", "custom.example", "config-token", "rs-ny.rustdesk.com", "", "", nil},
		{"public GUI", "", "", "", "", "gui-token", nil},
		{"explicit token", "", "config-token", "other.example", "", "explicit-token", []string{"--token=explicit-token"}},
		{"explicit environment", "", "config-token", "other.example", "env-token", "env-token", nil},
		{"explicit empty", "", "config-token", "", "env-token", "", []string{"--token="}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("RDC_CONFIG", filepath.Join(home, "config.toml"))
			t.Setenv("RDC_SERVER", tc.server)
			t.Setenv("RDC_TOKEN", tc.envToken)
			data := "server = \"" + tc.cfgServer + "\"\naccess_token = \"" + tc.cfgToken + "\"\n"
			if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			guiDir := filepath.Join(home, ".config", "rustdesk")
			if err := os.MkdirAll(guiDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(guiDir, "RustDesk_local.toml"), []byte("[options]\naccess_token = 'gui-token'\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := &cobra.Command{Use: "test", RunE: func(cmd *cobra.Command, args []string) error {
				if getTokenFlag(cmd) != tc.want {
					t.Error("token crossed server scope or explicit selection was lost")
				}
				if tc.server == "" && tc.cfgServer != "" && getServerFlag(cmd) != tc.cfgServer {
					t.Error("configured rendezvous server ignored")
				}
				return nil
			}}
			registerCommonFlags(cmd)
			cmd.SetArgs(append([]string{}, tc.args...))
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

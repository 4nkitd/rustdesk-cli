package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

var contractNewCommands = []string{
	"screenshot", "clipboard", "info", "doctor", "mkdir", "rm", "rename",
}

var contractExistingCommands = []string{
	"connect", "disconnect", "auth", "version", "ping", "tunnel", "ls", "cp",
	"sh", "devices", "tag", "import-gui", "import",
}

func newContractRoot(t *testing.T) *cobra.Command {
	t.Helper()
	home := t.TempDir()
	for _, env := range os.Environ() {
		key, _, _ := strings.Cut(env, "=")
		if strings.HasPrefix(key, "RDC_") {
			t.Setenv(key, "")
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	t.Setenv("RDC_CONFIG", filepath.Join(home, "rdcli.toml"))
	root := NewRootCmd()
	root.SilenceErrors = true
	root.SilenceUsage = true
	return root
}

func findContractCommand(t *testing.T, root *cobra.Command, name string) *cobra.Command {
	t.Helper()
	cmd, remaining, err := root.Find([]string{name})
	if err != nil {
		t.Fatalf("find %q: %v", name, err)
	}
	if cmd == root || cmd.Name() != name || len(remaining) != 0 {
		t.Fatalf("command %q is not registered: found %q, remaining %q", name, cmd.Name(), remaining)
	}
	return cmd
}

func TestCommandContractRegistration(t *testing.T) {
	root := newContractRoot(t)
	for _, names := range [][]string{contractExistingCommands, contractNewCommands} {
		for _, name := range names {
			t.Run(name, func(t *testing.T) {
				cmd := findContractCommand(t, root, name)
				if cmd.Parent() != root {
					t.Errorf("%q must be a root command", name)
				}
			})
		}
	}
	for _, name := range contractNewCommands {
		t.Run(name+"_discoverable", func(t *testing.T) {
			cmd := findContractCommand(t, root, name)
			if !cmd.IsAvailableCommand() {
				t.Errorf("%q must be available in root help", name)
			}
		})
	}
}

func TestCommandContractRootHelp(t *testing.T) {
	root := newContractRoot(t)
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetArgs([]string{"--help"})
	if err := root.Execute(); err != nil {
		t.Fatalf("root help: %v", err)
	}
	listed := make(map[string]bool)
	for _, line := range strings.Split(output.String(), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 1 {
			listed[fields[0]] = true
		}
	}
	for _, name := range contractNewCommands {
		if !listed[name] {
			t.Errorf("root help does not list %q:\n%s", name, output.String())
		}
	}
}

func TestCommandContractHelp(t *testing.T) {
	for _, names := range [][]string{contractExistingCommands, contractNewCommands} {
		for _, name := range names {
			t.Run(name, func(t *testing.T) {
				root := newContractRoot(t)
				cmd := findContractCommand(t, root, name)
				var output bytes.Buffer
				root.SetOut(&output)
				root.SetErr(&output)
				root.SetArgs([]string{name, "--help"})
				if err := root.Execute(); err != nil {
					t.Fatalf("%s --help: %v", name, err)
				}
				for _, want := range []string{cmd.CommandPath(), "--json", "--timeout"} {
					if !strings.Contains(output.String(), want) {
						t.Errorf("help missing %q:\n%s", want, output.String())
					}
				}
				if name == "screenshot" {
					for _, flag := range []string{"--display", "--output"} {
						if !strings.Contains(output.String(), flag) {
							t.Errorf("screenshot help missing %s", flag)
						}
					}
				}
			})
		}
	}
}

func TestCommandContractInheritedFlags(t *testing.T) {
	for _, name := range contractNewCommands {
		t.Run(name, func(t *testing.T) {
			root := newContractRoot(t)
			cmd := findContractCommand(t, root, name)
			for _, flag := range []string{"json", "timeout"} {
				if got := cmd.InheritedFlags().Lookup(flag); got == nil || got != root.PersistentFlags().Lookup(flag) {
					t.Errorf("%s must inherit --%s from the root", name, flag)
				}
			}
			if err := cmd.ParseFlags([]string{"--json", "--timeout=25ms"}); err != nil {
				t.Fatalf("parse inherited flags: %v", err)
			}
			if got, err := cmd.Flags().GetBool("json"); err != nil || !got {
				t.Errorf("--json = %v, %v; want true", got, err)
			}
			if got, err := cmd.Flags().GetDuration("timeout"); err != nil || got != 25*time.Millisecond {
				t.Errorf("--timeout = %v, %v; want 25ms", got, err)
			}
		})
	}
}

func TestCommandContractRejectsInvalidFlags(t *testing.T) {
	cases := []struct {
		name string
		arg  string
	}{
		{"unknown", "--rdcli-contract-unknown-flag"},
		{"json", "--json=not-a-bool"},
		{"timeout", "--timeout=not-a-duration"},
		{"missing_timeout", "--timeout"},
	}
	for _, name := range contractNewCommands {
		for _, tc := range cases {
			t.Run(name+"/"+tc.name, func(t *testing.T) {
				root := newContractRoot(t)
				cmd := findContractCommand(t, root, name)
				// ParseFlags avoids handlers, auth discovery, and remote operations.
				if err := cmd.ParseFlags([]string{tc.arg}); err == nil {
					t.Errorf("%s accepted invalid flag %q", name, tc.arg)
				}
			})
		}
	}
}

func TestCommandContractScreenshotFlags(t *testing.T) {
	root := newContractRoot(t)
	cmd := findContractCommand(t, root, "screenshot")
	for _, name := range []string{"display", "output"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Fatalf("screenshot missing --%s", name)
		}
	}
	output := filepath.Join(t.TempDir(), "screen capture.png")
	if err := cmd.ParseFlags([]string{"--display=0", "--output=" + output, "--", "-peer-ref"}); err != nil {
		t.Fatalf("parse screenshot flags: %v", err)
	}
	if got, err := cmd.Flags().GetString("output"); err != nil || got != output {
		t.Errorf("--output = %q, %v; want %q", got, err, output)
	}
	if args := cmd.Flags().Args(); len(args) != 1 || args[0] != "-peer-ref" {
		t.Errorf("arguments after -- = %q; want [-peer-ref]", args)
	}
}

package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func newDocumentedRoot(t *testing.T) *cobra.Command {
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
	return NewRootCmd()
}

func parseDocumentedCommand(t *testing.T, argv []string, path string) *cobra.Command {
	t.Helper()
	root := newDocumentedRoot(t)
	cmd, remaining, err := root.Find(argv)
	if err != nil {
		t.Fatalf("find %q: %v", argv, err)
	}
	if cmd.CommandPath() != "rdcli "+path {
		t.Fatalf("command = %q; want %q", cmd.CommandPath(), "rdcli "+path)
	}
	if err := cmd.ParseFlags(remaining); err != nil {
		t.Fatalf("parse %q: %v", argv, err)
	}
	return cmd
}

func checkDocumentedFlags(t *testing.T, cmd *cobra.Command, want map[string]string) {
	t.Helper()
	for name, value := range want {
		flag := cmd.Flags().Lookup(name)
		if flag == nil {
			t.Errorf("missing --%s", name)
		} else if got := flag.Value.String(); got != value {
			t.Errorf("--%s = %q; want %q", name, got, value)
		}
	}
}

func TestDocumentedCommandExamples(t *testing.T) {
	cases := []struct {
		name  string
		argv  []string
		path  string
		args  []string
		flags map[string]string
	}{
		{
			name: "cp_upload", path: "cp",
			argv:  []string{"cp", "-d", "up", "--overwrite", "./backup.zip", "C:/Users/jane/Desktop/"},
			args:  []string{"./backup.zip", "C:/Users/jane/Desktop/"},
			flags: map[string]string{"direction": "up", "overwrite": "true", "skip": "false", "resume": "false"},
		},
		{
			name: "cp_download", path: "cp",
			argv:  []string{"cp", "-d", "down", "C:/Users/jane/Desktop/backup.zip", "./"},
			args:  []string{"C:/Users/jane/Desktop/backup.zip", "./"},
			flags: map[string]string{"direction": "down", "overwrite": "false", "skip": "false", "resume": "false"},
		},
		{
			name: "cp_recursive_upload", path: "cp",
			argv:  []string{"cp", "-d", "up", "--overwrite", "-r", "./project", "C:/Users/jane/project"},
			args:  []string{"./project", "C:/Users/jane/project"},
			flags: map[string]string{"direction": "up", "overwrite": "true", "recursive": "true"},
		},
		{
			name: "cp_resume_download", path: "cp",
			argv:  []string{"cp", "-d", "down", "--resume", "C:/Users/jane/Desktop/backup.zip", "./"},
			args:  []string{"C:/Users/jane/Desktop/backup.zip", "./"},
			flags: map[string]string{"direction": "down", "resume": "true", "overwrite": "false"},
		},
		{
			name: "cp_skip_hidden_download", path: "cp",
			argv:  []string{"cp", "--direction", "down", "--skip", "--include-hidden", "C:/Users/jane/project", "./"},
			args:  []string{"C:/Users/jane/project", "./"},
			flags: map[string]string{"direction": "down", "skip": "true", "include-hidden": "true", "overwrite": "false"},
		},
		{
			name: "screenshot_display_output", path: "screenshot",
			argv:  []string{"screenshot", "--display", "0", "--output", "./screen.png"},
			flags: map[string]string{"display": "0", "output": "./screen.png", "overwrite": "false"},
		},
		{
			name: "screenshot_overwrite_json", path: "screenshot",
			argv:  []string{"screenshot", "--output", "./screen.png", "--overwrite", "--json"},
			flags: map[string]string{"display": "0", "output": "./screen.png", "overwrite": "true", "json": "true"},
		},
		{
			name: "screenshot_output_shorthand", path: "screenshot",
			argv:  []string{"screenshot", "--display", "1", "-o", "./screen capture.png"},
			flags: map[string]string{"display": "1", "output": "./screen capture.png"},
		},
		{
			name: "clipboard_get", path: "clipboard get",
			argv:  []string{"clipboard", "get", "--timeout", "15s"},
			flags: map[string]string{"timeout": "15s"},
		},
		{
			name: "clipboard_set_text", path: "clipboard set",
			argv:  []string{"clipboard", "set", "explicit text"},
			args:  []string{"explicit text"},
			flags: map[string]string{"stdin": "false"},
		},
		{
			name: "clipboard_set_empty", path: "clipboard set",
			argv:  []string{"clipboard", "set", ""},
			args:  []string{""},
			flags: map[string]string{"stdin": "false"},
		},
		{
			name: "clipboard_set_stdin", path: "clipboard set",
			argv:  []string{"clipboard", "set", "--stdin"},
			flags: map[string]string{"stdin": "true"},
		},
		{
			name: "mkdir", path: "mkdir",
			argv: []string{"mkdir", "C:/Users/jane/work/logs"},
			args: []string{"C:/Users/jane/work/logs"},
		},
		{
			name: "rm_file", path: "rm",
			argv:  []string{"rm", "C:/Users/jane/work/old.txt", "--confirm-delete"},
			args:  []string{"C:/Users/jane/work/old.txt"},
			flags: map[string]string{"confirm-delete": "true", "dir": "false", "force": "false"},
		},
		{
			name: "rm_empty_directory", path: "rm",
			argv:  []string{"rm", "C:/Users/jane/work/empty", "--dir", "--confirm-delete"},
			args:  []string{"C:/Users/jane/work/empty"},
			flags: map[string]string{"confirm-delete": "true", "dir": "true"},
		},
		{
			name: "rm_force", path: "rm",
			argv:  []string{"rm", "C:/Users/jane/work/old.txt", "--force"},
			args:  []string{"C:/Users/jane/work/old.txt"},
			flags: map[string]string{"force": "true", "confirm-delete": "false"},
		},
		{
			name: "rename_overwrite", path: "rename",
			argv:  []string{"rename", "C:/Users/jane/work/old.txt", "new.txt", "--overwrite"},
			args:  []string{"C:/Users/jane/work/old.txt", "new.txt"},
			flags: map[string]string{"overwrite": "true"},
		},
		{
			name: "info_json", path: "info",
			argv: []string{"info", "--json"}, flags: map[string]string{"json": "true"},
		},
		{
			name: "doctor", path: "doctor",
			argv: []string{"doctor"},
		},
		{
			name: "sh_command_timeout", path: "sh",
			argv:  []string{"sh", "--command-timeout", "30s", "exit 7"},
			args:  []string{"exit 7"},
			flags: map[string]string{"command-timeout": "30s", "timeout": "10s"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			peer := "home-pc"
			if tc.path == "sh" {
				peer = "linux-box"
			}
			argv := append([]string{"-c", peer}, tc.argv...)
			cmd := parseDocumentedCommand(t, argv, tc.path)
			if got := cmd.Flags().Args(); !slices.Equal(got, tc.args) {
				t.Errorf("positional arguments = %q; want %q", got, tc.args)
			}
			checkDocumentedFlags(t, cmd, map[string]string{"connect": peer})
			checkDocumentedFlags(t, cmd, tc.flags)
			if err := cmd.ValidateArgs(cmd.Flags().Args()); err != nil {
				t.Errorf("validate arguments: %v", err)
			}
			if err := cmd.ValidateFlagGroups(); err != nil {
				t.Errorf("validate flag groups: %v", err)
			}
		})
	}
}

func TestDocumentedOTPFlag(t *testing.T) {
	for _, path := range []string{"cp", "screenshot", "clipboard get", "clipboard set", "rename", "rm", "info", "doctor", "sh"} {
		t.Run(path, func(t *testing.T) {
			argv := append([]string{"--otp", "00123456", "-c", "home-pc"}, strings.Fields(path)...)
			cmd := parseDocumentedCommand(t, argv, path)
			checkDocumentedFlags(t, cmd, map[string]string{"otp": "00123456"})
			if cmd.InheritedFlags().Lookup("otp") != cmd.Root().PersistentFlags().Lookup("otp") {
				t.Error("--otp must be inherited from the root")
			}
		})
	}
}

func TestDocumentedPolicyDefaults(t *testing.T) {
	for path, flags := range map[string]map[string]string{
		"cp":         {"direction": "", "overwrite": "false", "skip": "false", "resume": "false", "include-hidden": "false"},
		"screenshot": {"display": "0", "output": "", "overwrite": "false"},
		"rename":     {"overwrite": "false"},
		"rm":         {"confirm-delete": "false", "force": "false", "dir": "false"},
		"sh":         {"command-timeout": "5m0s", "timeout": "10s"},
	} {
		t.Run(path, func(t *testing.T) {
			cmd := parseDocumentedCommand(t, []string{"--yes", path}, path)
			checkDocumentedFlags(t, cmd, flags)
		})
	}
}

func TestDocumentedCopyPoliciesMutuallyExclusive(t *testing.T) {
	cmd := parseDocumentedCommand(t, []string{"cp", "-d", "down", "--overwrite", "--skip"}, "cp")
	if err := cmd.ValidateFlagGroups(); err == nil {
		t.Error("--overwrite and --skip must be mutually exclusive")
	}
}

func TestDocumentedCommandHelp(t *testing.T) {
	for path, terms := range map[string][]string{
		"cp":            {"--direction", "--overwrite", "--skip", "--resume", "--include-hidden"},
		"screenshot":    {"--display", "--output", "--overwrite"},
		"clipboard get": {"--timeout", "--json"},
		"clipboard set": {"--stdin", "--json"},
		"rename":        {"--overwrite"},
		"rm":            {"--confirm-delete", "--force", "--dir"},
		"info":          {"--json", "--otp", "RDC_OTP"},
		"doctor":        {"--json", "--otp", "RDC_OTP"},
		"sh":            {"--command-timeout"},
	} {
		t.Run(path, func(t *testing.T) {
			cmd := parseDocumentedCommand(t, strings.Fields(path), path)
			var output bytes.Buffer
			cmd.SetOut(&output)
			cmd.SetErr(&output)
			if err := cmd.Help(); err != nil {
				t.Fatalf("help: %v", err)
			}
			for _, term := range terms {
				if !strings.Contains(output.String(), term) {
					t.Errorf("help missing %q:\n%s", term, output.String())
				}
			}
		})
	}
}

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/4nkitd/rustdesk-cli/internal/cli"
)

func TestExitContractHelperProcess(t *testing.T) {
	if os.Getenv("RDC_EXIT_CONTRACT_HELPER") != "1" {
		return
	}
	var args []string
	for i, arg := range os.Args {
		if arg == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	if len(args) == 0 {
		t.Fatal("missing helper mode")
	}
	switch args[0] {
	case "main":
		os.Args = append([]string{"rdcli"}, args[1:]...)
		main()
		os.Exit(0)
	case "remote":
		if len(args) != 2 {
			t.Fatal("missing remote exit code")
		}
		code, err := strconv.Atoi(args[1])
		if err != nil {
			t.Fatal(err)
		}
		err = fmt.Errorf("sh: %w", fmt.Errorf("terminal: %w", &cli.CommandExitError{Code: code}))
		os.Exit(commandExitCode(err))
	case "wrapped-usage":
		os.Exit(commandExitCode(fmt.Errorf("command: %w", &cli.UsageError{})))
	case "wrapped-runtime":
		os.Exit(commandExitCode(fmt.Errorf("command: %w", errors.New("transport failed"))))
	default:
		t.Fatalf("unknown helper mode %q", args[0])
	}
}

func runExitContractProcess(t *testing.T, brokenConfig bool, args ...string) (int, string, string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	configPath := filepath.Join(home, "rdcli.toml")
	if brokenConfig {
		if err := os.Mkdir(configPath, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	commandArgs := append([]string{"-test.run=^TestExitContractHelperProcess$", "--"}, args...)
	cmd := exec.CommandContext(ctx, executable, commandArgs...)
	cmd.Dir = home
	cmd.Env = []string{
		"RDC_EXIT_CONTRACT_HELPER=1",
		"HOME=" + home,
		"USERPROFILE=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"APPDATA=" + filepath.Join(home, "AppData", "Roaming"),
		"LOCALAPPDATA=" + filepath.Join(home, "AppData", "Local"),
		"TMPDIR=" + home,
		"TMP=" + home,
		"TEMP=" + home,
		"RDC_CONFIG=" + configPath,
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("helper timed out: %v; stdout=%q stderr=%q", ctx.Err(), stdout.String(), stderr.String())
	}
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("start helper: %v", err)
		}
		code = exitErr.ExitCode()
	}
	return code, stdout.String(), stderr.String()
}

func TestExitContractUsageErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"missing_argument", []string{"connect"}, "usage: rdcli connect <id|name|tag>"},
		{"extra_argument", []string{"disconnect", "123456789", "extra"}, "usage: rdcli disconnect <id|name|tag>"},
		{"missing_target", []string{"ls"}, "no machine specified"},
		{"missing_output", []string{"screenshot"}, "screenshot requires --output"},
		{"unknown_command", []string{"exit-contract-unknown-command"}, "unknown command"},
		{"unknown_auth_command", []string{"auth", "exit-contract-unknown-command"}, "unknown command"},
		{"unknown_tag_command", []string{"tag", "exit-contract-unknown-command"}, "unknown command"},
		{"unknown_root_flag", []string{"--exit-contract-unknown-flag"}, "unknown flag"},
		{"unknown_command_flag", []string{"screenshot", "--exit-contract-unknown-flag"}, "unknown flag"},
		{"invalid_bool", []string{"version", "--json=not-a-bool"}, "invalid argument"},
		{"invalid_duration", []string{"version", "--timeout=not-a-duration"}, "invalid argument"},
		{"missing_flag_value", []string{"version", "--timeout"}, "flag needs an argument"},
		{"cobra_missing_argument", []string{"auth", "set"}, "accepts 1 arg(s), received 0"},
		{"cobra_extra_argument", []string{"screenshot", "first", "second"}, "accepts at most 1 arg(s), received 2"},
		{"conflicting_flags", []string{"cp", "--overwrite", "--skip"}, "none of the others can be"},
		{"completion_extra_argument", []string{"completion", "bash", "extra"}, "unknown command"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := runExitContractProcess(t, false, append([]string{"main"}, tc.args...)...)
			if code != 2 {
				t.Errorf("usage errors must exit 2, runtime errors exit 1: got %d; stderr=%q", code, stderr)
			}
			if stdout != "" {
				t.Errorf("stdout = %q; want empty", stdout)
			}
			if !strings.Contains(stderr, tc.want) || strings.Count(stderr, "\n") != 1 {
				t.Errorf("stderr = %q; want one diagnostic containing %q", stderr, tc.want)
			}
		})
	}
}

func TestExitContractRuntimeError(t *testing.T) {
	code, stdout, stderr := runExitContractProcess(t, true, "main", "auth", "status")
	if code != 1 || stdout != "" || stderr != "auth: could not load config\n" {
		t.Fatalf("got exit=%d stdout=%q stderr=%q; want exit=1 and only the config error on stderr", code, stdout, stderr)
	}
}

func TestExitContractWrappedErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want int
	}{
		{"usage", []string{"wrapped-usage"}, 2},
		{"runtime", []string{"wrapped-runtime"}, 1},
		{"remote_one", []string{"remote", "1"}, 1},
		{"remote_usage_status", []string{"remote", "2"}, 2},
		{"remote_twenty_seven", []string{"remote", "27"}, 27},
		{"remote_max", []string{"remote", "255"}, 255},
		{"remote_negative", []string{"remote", "-1"}, 1},
		{"remote_zero", []string{"remote", "0"}, 1},
		{"remote_overflow", []string{"remote", "256"}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := runExitContractProcess(t, false, tc.args...)
			if code != tc.want || stdout != "" || stderr != "" {
				t.Fatalf("got exit=%d stdout=%q stderr=%q; want exit=%d and no output from exit-code mapping", code, stdout, stderr, tc.want)
			}
		})
	}
}

func TestExitContractSuccessOutput(t *testing.T) {
	code, stdout, stderr := runExitContractProcess(t, false, "main", "auth", "logout")
	if code != 0 || stdout != "no token stored\n" || stderr != "" {
		t.Fatalf("got exit=%d stdout=%q stderr=%q; want exit=0 and only the logout result on stdout", code, stdout, stderr)
	}
}

func TestExitContractCommandGroupHelp(t *testing.T) {
	for _, args := range [][]string{{"main"}, {"main", "auth"}, {"main", "tag"}, {"main", "clipboard"}, {"main", "completion"}} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			code, stdout, stderr := runExitContractProcess(t, true, args...)
			if code != 0 || stderr != "" || !strings.Contains(stdout, "Usage:") {
				t.Fatalf("got exit=%d stdout=%q stderr=%q; want exit=0 and only help on stdout", code, stdout, stderr)
			}
		})
	}
}

func TestExitContractHelpWithoutCredentials(t *testing.T) {
	for _, name := range []string{
		"", "connect", "disconnect", "auth", "version", "ping", "tunnel", "ls", "cp",
		"sh", "devices", "tag", "import-gui", "import", "clipboard", "info", "doctor",
		"mkdir", "rm", "rename", "screenshot",
	} {
		t.Run("help_"+name, func(t *testing.T) {
			args := []string{"main"}
			if name != "" {
				args = append(args, name)
			}
			args = append(args, "--help")
			code, stdout, stderr := runExitContractProcess(t, true, args...)
			if code != 0 || stderr != "" {
				t.Fatalf("got exit=%d stderr=%q; want exit=0 and empty stderr", code, stderr)
			}
			for _, want := range []string{"Usage:", "rdcli", "--timeout"} {
				if !strings.Contains(stdout, want) {
					t.Errorf("help missing %q: %q", want, stdout)
				}
			}
		})
	}
}

func TestExitContractVersionWithoutCredentials(t *testing.T) {
	code, stdout, stderr := runExitContractProcess(t, true, "main", "version")
	if code != 0 || stderr != "" || !regexp.MustCompile(`\Ardcli \S+\n\z`).MatchString(stdout) {
		t.Fatalf("got exit=%d stdout=%q stderr=%q; want exit=0 and only a version line on stdout", code, stdout, stderr)
	}
}

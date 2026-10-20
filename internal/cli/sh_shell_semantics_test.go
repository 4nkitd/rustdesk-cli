package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestShShellSemantics(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/bin/sh required")
	}
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Fatal(err)
	}
	const token = "rdcli-shell-semantics-fixture"
	for _, tc := range []struct {
		name    string
		command string
		output  string
		code    int
		syntax  bool
	}{
		{
			name:    "multiline",
			command: "value=first\nprintf '%s\\n' \"$value\"\nprintf '%s' second\n# trailing comment",
			output:  "first\nsecond",
		},
		{
			name:    "quotes and literal newline",
			command: "printf '%s\\n' \"single'quote\" 'double\"quote' 'literal\nnewline' 'back\\slash' '$HOME; $(false)'",
			output:  "single'quote\ndouble\"quote\nliteral\nnewline\nback\\slash\n$HOME; $(false)\n",
		},
		{
			name:    "here document",
			command: "cat <<'FIXTURE'\n'quoted' \"text\" $HOME\nsecond line\nFIXTURE\nprintf tail",
			output:  "'quoted' \"text\" $HOME\nsecond line\ntail",
		},
		{name: "false", command: "false", code: 1},
		{name: "nonzero last command", command: "printf before; (exit 37)", output: "before", code: 37},
		{name: "false followed by success", command: "false; printf after", output: "after"},
		{name: "explicit successful exit", command: "printf before; exit 0; printf unreachable", output: "before"},
		{name: "explicit failed exit", command: "printf before; exit 23; printf unreachable", output: "before", code: 23},
		{name: "set errexit", command: "set -e\nprintf before\nfalse\nprintf unreachable", output: "before", code: 1},
		{name: "errexit conditional", command: "set -e\nif false; then printf unreachable; fi\nprintf after", output: "after"},
		{name: "no output", command: ":"},
		{name: "empty command", command: ""},
		{name: "stderr", command: "printf out; printf err >&2; exit 9", output: "outerr", code: 9},
		{name: "binary output", command: "printf 'a\\000b\\n\\n'", output: "a\x00b\n\n"},
		{
			name:    "marker-like output",
			command: "printf '\\036other:exit:0\\037\\036rdcli-shell-semantics-fixture:start\\037rdcli-shell-semantics-fixture:exit:0\\n\\036rdcli-shell-semantics-fixture:exi'; exit 17",
			output:  "\x1eother:exit:0\x1f\x1erdcli-shell-semantics-fixture:start\x1frdcli-shell-semantics-fixture:exit:0\n\x1erdcli-shell-semantics-fixture:exi",
			code:    17,
		},
		{name: "shell syntax error", command: "if then", code: 2, syntax: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			run := func(script string, wantCode int) []byte {
				t.Helper()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, "/bin/sh", "-c", script)
				cmd.Args[0] = "sh"
				cmd.Dir = dir
				cmd.Env = []string{"PATH=/bin:/usr/bin", "LC_ALL=C", "HOME=" + dir, "TMPDIR=" + dir}
				cmd.WaitDelay = time.Second
				wire, err := cmd.CombinedOutput()
				if ctx.Err() != nil {
					t.Fatalf("shell exceeded bounded context: %v; output %q", ctx.Err(), wire)
				}
				if wantCode == 0 {
					if err != nil {
						t.Fatalf("wrapper failed: %v; output %q", err, wire)
					}
				} else {
					var status *exec.ExitError
					if !errors.As(err, &status) || status.ExitCode() != wantCode {
						t.Fatalf("direct shell status = %v, want %d; output %q", err, wantCode, wire)
					}
				}
				return wire
			}
			want := tc.output
			if tc.syntax {
				want = string(run(tc.command, tc.code))
				if !strings.Contains(want, "Syntax error") && !strings.Contains(want, "syntax error") {
					t.Fatalf("shell syntax fixture produced no diagnostic: %q", want)
				}
			}
			// The remote PTY consumes CRLF; a noninteractive shell pipe does not.
			script := strings.TrimSuffix(shCommandScript(tc.command, token), "\r\n")
			wire := run(script, 0)
			for split := 0; split <= len(wire); split++ {
				reader := io.MultiReader(bytes.NewReader(wire[:split]), bytes.NewReader(wire[split:]))
				var output bytes.Buffer
				err := readShCompletion(reader, &output, token)
				if output.String() != want {
					t.Fatalf("split %d: output = %q, want %q; parser error %v; wire %q", split, output.String(), want, err, wire)
				}
				if tc.code == 0 {
					if err != nil {
						t.Fatalf("split %d: unexpected completion error: %v", split, err)
					}
				} else {
					var status *CommandExitError
					if !errors.As(err, &status) || status.ExitCode() != tc.code {
						t.Fatalf("split %d: completion status = %v, want %d", split, err, tc.code)
					}
				}
			}
		})
	}
}

package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/4nkitd/rustdesk-cli/internal/config"
)

func authSafetyHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("RDC_CONFIG", filepath.Join(home, "path-credential-canary", "config.toml"))
	for _, name := range []string{"RDC_TOKEN", "RDC_PASSWORD", "RDC_KEY", "RDC_SERVER"} {
		t.Setenv(name, name+"-credential-canary")
	}
	return home
}

func authSafetyGUI(t *testing.T, home, token string) {
	t.Helper()
	path := filepath.Join(home, ".config", "rustdesk", "RustDesk_local.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data := fmt.Sprintf("[options]\naccess_token = %q\nuser_info = '{\"name\":\"identity-credential-canary\",\"access_token\":\"nested-credential-canary\"}'\n", token)
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func authSafetyExecute(args ...string) (string, error) {
	cmd := NewRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestAuthStatusSafetyOutput(t *testing.T) {
	for _, tc := range []struct {
		name, token, guiToken, source string
		password                      bool
	}{
		{name: "absent"},
		{name: "config", token: "config-credential-canary", source: "config", password: true},
		{name: "gui", guiToken: "gui-credential-canary", source: "gui"},
		{name: "config precedence", token: "config-credential-canary", guiToken: "gui-credential-canary", source: "config"},
		{name: "short token", token: "tiny", source: "config"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := authSafetyHome(t)
			authSafetyGUI(t, home, tc.guiToken)
			cfg := &config.Config{
				AccessToken: tc.token,
				Server:      "https://user:server-credential-canary@example.invalid/?token=query-credential-canary",
				APIPassword: "api-credential-canary",
				Peers:       map[string]config.Peer{"123": {Name: "peer-credential-canary"}},
			}
			if tc.password {
				cfg.SetPeersPassword("456", "password-credential-canary")
			}
			if err := cfg.Save(); err != nil {
				t.Fatal(err)
			}

			out, err := authSafetyExecute("auth", "status", "--json")
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err := json.Unmarshal([]byte(out), &got); err != nil {
				t.Fatalf("invalid JSON: %v", err)
			}
			want := map[string]any{"version": float64(2), "logged_in": tc.source != "", "password_set": tc.password}
			if tc.source != "" {
				want["source"] = tc.source
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("status must contain only allowed fields: got %v, want %v", got, want)
			}

			out, err = authSafetyExecute("auth", "status")
			if err != nil {
				t.Fatal(err)
			}
			wantText := "not logged in\nsource:   -\n"
			if tc.source != "" {
				wantText = fmt.Sprintf("logged in (%s)\nsource:   %s\n", tc.source, tc.source)
			}
			if tc.password {
				wantText += "saved passwords: 1 peer(s)\n"
			}
			if out != wantText {
				t.Fatalf("unexpected status text: %q", out)
			}
		})
	}
}

func TestAuthStatusSafetyLoadErrors(t *testing.T) {
	for _, args := range [][]string{
		{"auth", "status"},
		{"auth", "status", "--json"},
		{"auth", "import-gui"},
		{"auth", "logout"},
		{"auth", "set", "argument-credential-canary"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			home := authSafetyHome(t)
			authSafetyGUI(t, home, "gui-credential-canary")
			if err := os.MkdirAll(filepath.Dir(config.Path()), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(config.Path(), []byte("[tags]\npeer = reflected-credential-canary\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			out, err := authSafetyExecute(args...)
			if err == nil || err.Error() != "auth: could not load config" {
				t.Fatalf("expected sanitized load error, got %v", err)
			}
			if strings.Contains(out, "credential-canary") {
				t.Fatalf("credential reflected in output: %q", out)
			}
		})
	}
}

func TestAuthStatusSafetySaveErrors(t *testing.T) {
	for _, args := range [][]string{
		{"auth", "import-gui"},
		{"auth", "logout"},
		{"auth", "set", "argument-credential-canary"},
	} {
		t.Run(args[1], func(t *testing.T) {
			home := authSafetyHome(t)
			authSafetyGUI(t, home, "gui-credential-canary")
			cfg := &config.Config{AccessToken: "config-credential-canary"}
			if err := cfg.Save(); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(home, "reflected-credential-canary")
			if err := os.Symlink(config.Path(), link); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}
			t.Setenv("RDC_CONFIG", link)
			out, err := authSafetyExecute(args...)
			if err == nil || err.Error() != "auth: could not save config" {
				t.Fatalf("expected sanitized save error, got %v", err)
			}
			if strings.Contains(out, "credential-canary") {
				t.Fatalf("credential reflected in output: %q", out)
			}
		})
	}
}

func TestAuthStatusSafetyMutationOutput(t *testing.T) {
	home := authSafetyHome(t)
	authSafetyGUI(t, home, "gui-credential-canary")
	for _, tc := range []struct {
		args  []string
		want  string
		token string
	}{
		{[]string{"auth", "set", "argument-credential-canary"}, "token saved\n", "argument-credential-canary"},
		{[]string{"auth", "import-gui"}, "imported token from RustDesk desktop app config\n", "gui-credential-canary"},
		{[]string{"auth", "logout"}, "logged out\n", ""},
		{[]string{"auth", "logout"}, "no token stored\n", ""},
	} {
		out, err := authSafetyExecute(tc.args...)
		if err != nil {
			t.Fatal(err)
		}
		if out != tc.want {
			t.Fatalf("unexpected auth output: %q", out)
		}
		cfg, err := config.Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.AccessToken != tc.token {
			t.Fatal("stored token differs from expected fixture")
		}
	}
}

package cli

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/cobra"

	"github.com/4nkitd/rustdesk-cli/internal/config"
)

func TestResolveRefAmbiguousCandidates(t *testing.T) {
	tests := []struct {
		name string
		cfg  *config.Config
	}{
		{
			name: "name substrings",
			cfg: &config.Config{Peers: map[string]config.Peer{
				"3003": {Name: "prod backup", Password: "fixture-password-3"},
				"1001": {Name: "PROD", Password: "fixture-password-1"},
				"2002": {Name: "production", Password: "fixture-password-2"},
			}},
		},
		{
			name: "shared tags",
			cfg: &config.Config{Tags: map[string][]string{
				"3003": {"prod"}, "1001": {"PROD"}, "2002": {"prod", "PROD"},
			}},
		},
		{
			name: "name and tag collision",
			cfg: &config.Config{
				Peers: map[string]config.Peer{
					"1001": {Name: "production", Password: "fixture-password-1"},
					"2002": {Name: "staging", Password: "fixture-password-2"},
				},
				Tags: map[string][]string{"3003": {"prod"}, "2002": {"PROD"}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for i := 0; i < 50; i++ {
				id, password, err := ResolveRef(tt.cfg, "prod")
				want := `ambiguous machine reference "prod": matches 1001, 2002, 3003; use an exact ID`
				if err == nil || err.Error() != want {
					t.Fatalf("error = %v, want %q", err, want)
				}
				if id != "" || password != "" {
					t.Fatalf("ambiguous reference selected id=%q password=%q", id, password)
				}
			}
		})
	}
}

func TestResolveRefDeduplicatesNameAndTags(t *testing.T) {
	cfg := &config.Config{
		Peers: map[string]config.Peer{"1001": {Name: "production", Password: "fixture-password"}},
		Tags:  map[string][]string{"1001": {"prod", "PROD", "prod"}},
	}
	id, password, err := ResolveRef(cfg, "prod")
	if err != nil || id != "1001" || password != "fixture-password" {
		t.Fatalf("id=%q password=%q err=%v", id, password, err)
	}
}

func TestResolveRefExactIDWins(t *testing.T) {
	cfg := &config.Config{
		Peers: map[string]config.Peer{
			"1001": {Name: "other", Password: "fixture-password"},
			"2002": {Name: "1001 and 3003", Password: "wrong-fixture-password"},
		},
		Tags: map[string][]string{"2002": {"1001", "3003"}, "3003": {"other"}},
	}
	for _, ref := range []string{"1001", "3003"} {
		id, password, err := ResolveRef(cfg, ref)
		if err != nil || id != ref || password != cfg.Peers[ref].Password {
			t.Fatalf("ref=%q id=%q password=%q err=%v", ref, id, password, err)
		}
	}
}

func TestResolveRefEmptyFailsClosed(t *testing.T) {
	cfg := &config.Config{
		Peers: map[string]config.Peer{
			"":     {Password: "empty-key-fixture-password"},
			"1001": {Name: "production", Password: "fixture-password"},
		},
		Tags: map[string][]string{"1001": {"", " \t\n"}},
	}
	for _, ref := range []string{"", " ", "\t\n", " \t\n", "\u2003"} {
		id, password, err := ResolveRef(cfg, ref)
		if err == nil || id != "" || password != "" {
			t.Fatalf("ref=%q id=%q password=%q err=%v", ref, id, password, err)
		}
	}
}

func TestResolveRefRawTargets(t *testing.T) {
	for _, ref := range []string{
		"9999", "123456789", "001234567",
		"kali-box", "Custom_ID-123", "abcdef", "a123456789012345",
		"192.0.2.10", "192.0.2.10:21118", "192.0.2.10:1", "192.0.2.10:65535",
		"2001:db8::1", "::1", "[2001:db8::1]:21118", "[::1]:21118",
	} {
		t.Run(ref, func(t *testing.T) {
			id, password, err := ResolveRef(&config.Config{}, ref)
			if err != nil || id != ref || password != "" {
				t.Fatalf("id=%q password=%q err=%v", id, password, err)
			}
		})
	}
}

func TestResolveRefRejectsUnsupportedRawTargets(t *testing.T) {
	cfg := &config.Config{Peers: map[string]config.Peer{
		"1001": {Name: "kali", Password: "fixture-password"},
	}}
	for _, ref := range []string{
		"klai", "prod", "abcde", "a1234567890123456", "1custom", "_custom",
		"unknown.example", "unknown.example:21118", "123456789@server", "123456789/r",
		" 1001", "1001 ", "12 34", "192.0.2.999", "192.0.2.10:0",
		"192.0.2.10:65536", "192.0.2.10:-1", "192.0.2.10:+1", "192.0.2.10:http",
		"192.0.2.10:", "[::1]", "[::1]:0", "[::1]:65536", "fe80::1%en0",
	} {
		t.Run(ref, func(t *testing.T) {
			id, password, err := ResolveRef(cfg, ref)
			if err == nil || id != "" || password != "" {
				t.Fatalf("id=%q password=%q err=%v", id, password, err)
			}
			if strings.Contains(err.Error(), "fixture-password") {
				t.Fatalf("error disclosed a password: %v", err)
			}
		})
	}
}

func TestResolveRefTagWithoutPeer(t *testing.T) {
	cfg := &config.Config{Tags: map[string][]string{"1001": {"prod"}}}
	id, password, err := ResolveRef(cfg, "PROD")
	if err != nil || id != "1001" || password != "" {
		t.Fatalf("id=%q password=%q err=%v", id, password, err)
	}
}

func TestResolveRefRejectsEmptyConfiguredCandidate(t *testing.T) {
	cfg := &config.Config{Peers: map[string]config.Peer{
		"": {Name: "production", Password: "fixture-password"},
	}}
	id, password, err := ResolveRef(cfg, "prod")
	if err == nil || id != "" || password != "" {
		t.Fatalf("id=%q password=%q err=%v", id, password, err)
	}
}

func TestResolveTargetAmbiguityDoesNotSelectCredentials(t *testing.T) {
	t.Setenv("RDC_CONFIG", filepath.Join(t.TempDir(), "config.toml"))
	cfg := &config.Config{
		Peers: map[string]config.Peer{
			"1001": {Name: "production", Password: "fixture-password"},
		},
		Tags: map[string][]string{"2002": {"prod"}},
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	for _, useFlag := range []bool{false, true} {
		cmd := &cobra.Command{Use: "fixture"}
		cmd.Flags().String("connect", "", "")
		cmd.Flags().String("password", "override-fixture-password", "")
		args := []string{"prod", "/fixture"}
		if useFlag {
			if err := cmd.Flags().Set("connect", "prod"); err != nil {
				t.Fatal(err)
			}
			args = []string{"/fixture"}
		}
		id, password, remaining, err := resolveTarget(cmd, args)
		if err == nil || !strings.Contains(err.Error(), "ambiguous machine reference") {
			t.Fatalf("useFlag=%v error=%v", useFlag, err)
		}
		if id != "" || password != "" || remaining != nil {
			t.Fatalf("useFlag=%v id=%q password=%q remaining=%v", useFlag, id, password, remaining)
		}
	}
}

func TestTagCommandsRejectInvalidReferencesBeforeSideEffects(t *testing.T) {
	for _, source := range []string{"local", "api"} {
		for _, operation := range []string{"add", "rm", "ls"} {
			for _, ref := range []struct {
				name string
				text string
				want string
			}{
				{"ambiguous", "prod", "ambiguous machine reference"},
				{"unknown", "prdo", "unknown machine reference"},
				{"empty", "", "machine reference must not be empty"},
				{"whitespace", " \t", "machine reference must not be empty"},
			} {
				t.Run(source+"/"+operation+"/"+ref.name, func(t *testing.T) {
					path := filepath.Join(t.TempDir(), "config.toml")
					t.Setenv("RDC_CONFIG", path)
					cfg := &config.Config{
						Peers: map[string]config.Peer{
							"1001": {Name: "production", Password: "fixture-password"},
						},
						Tags: map[string][]string{
							"": {"fixture-tag"}, "1001": {"fixture-tag"}, "2002": {"prod"},
						},
					}
					var requests atomic.Int64
					if source == "api" {
						server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							requests.Add(1)
							w.Header().Set("Content-Type", "application/json")
							if r.URL.Path == "/api/login" {
								io.WriteString(w, `{"access_token":"fixture-token"}`)
							} else {
								io.WriteString(w, `[]`)
							}
						}))
						t.Cleanup(server.Close)
						cfg.APIServer = server.URL
						cfg.APIUsername = "fixture-user"
						cfg.APIPassword = "fixture-api-password"
					}
					if err := cfg.Save(); err != nil {
						t.Fatal(err)
					}
					before, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					beforeInfo, err := os.Stat(path)
					if err != nil {
						t.Fatal(err)
					}
					root := NewRootCmd()
					root.SetOut(io.Discard)
					root.SetErr(io.Discard)
					args := []string{"tag", operation, ref.text}
					if operation != "ls" {
						args = append(args, "fixture-tag")
					}
					root.SetArgs(args)
					err = root.Execute()
					if err == nil || !strings.Contains(err.Error(), "tag "+operation+": "+ref.want) {
						t.Errorf("command error = %v, want %q", err, ref.want)
					}
					if n := requests.Load(); n != 0 {
						t.Errorf("invalid reference made %d API requests", n)
					}
					after, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(before, after) {
						t.Error("invalid reference changed config contents")
					}
					afterInfo, err := os.Stat(path)
					if err != nil {
						t.Fatal(err)
					}
					if !os.SameFile(beforeInfo, afterInfo) {
						t.Error("invalid reference replaced the config file")
					}
				})
			}
		}
	}
}

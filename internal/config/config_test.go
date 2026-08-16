package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if content != "" {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
	}
	t.Setenv("RDC_CONFIG", path)
	return path
}

func TestLoadAndSaveRoundtrip(t *testing.T) {
	writeTempConfig(t, `server = "srv.example.com:21116"
key = "lickey"
api_server = "https://hbbs.example.com"
api_username = "alice"
api_password = "secret"

[peers."123456789"]
password = "pw1"

[peers."987654321"]
password = "pw2"

[tags]
"123456789" = ["work", "prod"]
"987654321" = ["dev"]
`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.Server != "srv.example.com:21116" {
		t.Fatalf("Server got %q", cfg.Server)
	}
	if cfg.Key != "lickey" {
		t.Fatalf("Key got %q", cfg.Key)
	}
	if cfg.APIServer != "https://hbbs.example.com" {
		t.Fatalf("APIServer got %q", cfg.APIServer)
	}
	if cfg.APIUsername != "alice" || cfg.APIPassword != "secret" {
		t.Fatalf("API creds got %q/%q", cfg.APIUsername, cfg.APIPassword)
	}
	if got := cfg.PasswordForPeer("123456789"); got != "pw1" {
		t.Fatalf("PasswordForPeer(123456789) got %q", got)
	}
	if got := cfg.PasswordForPeer("987654321"); got != "pw2" {
		t.Fatalf("PasswordForPeer(987654321) got %q", got)
	}
	if got := cfg.TagsFor("123456789"); len(got) != 2 || got[0] != "prod" || got[1] != "work" {
		t.Fatalf("TagsFor(123456789) got %v", got)
	}

	if err := cfg.Save(); err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	got, err := os.ReadFile(Path())
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	want := `server = "srv.example.com:21116"
key = "lickey"
access_token = ""
api_server = "https://hbbs.example.com"
api_username = "alice"
api_password = "secret"

[peers."123456789"]
password = "pw1"

[peers."987654321"]
password = "pw2"

[tags]
"123456789" = ["prod", "work"]
"987654321" = ["dev"]
`
	if string(got) != want {
		t.Fatalf("saved config mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}

	reloaded, err := Load()
	if err != nil {
		t.Fatalf("reload failed: %v", err)
	}
	if got := reloaded.TagsFor("123456789"); len(got) != 2 || got[0] != "prod" || got[1] != "work" {
		t.Fatalf("reloaded TagsFor got %v", got)
	}
	if got := reloaded.PasswordForPeer("987654321"); got != "pw2" {
		t.Fatalf("reloaded PasswordForPeer got %q", got)
	}
}

func TestLoadMissingFileReturnsEmpty(t *testing.T) {
	writeTempConfig(t, "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load missing file: %v", err)
	}
	if cfg.Server != "" || cfg.Key != "" || cfg.APIServer != "" {
		t.Fatalf("expected empty config, got %+v", cfg)
	}
	if cfg.Peers == nil || cfg.Tags == nil {
		t.Fatalf("maps must be initialized")
	}
}

func TestParseInlineCommentAndBareValue(t *testing.T) {
	writeTempConfig(t, "server = public # comment\nkey = mykey\n")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.Server != "public" {
		t.Fatalf("Server got %q", cfg.Server)
	}
	if cfg.Key != "mykey" {
		t.Fatalf("Key got %q", cfg.Key)
	}
}

func TestAddRemoveTagAndSetPeersPassword(t *testing.T) {
	writeTempConfig(t, "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	cfg.AddTag("42", "b")
	cfg.AddTag("42", "a")
	cfg.AddTag("42", "b")
	if got := cfg.TagsFor("42"); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("TagsFor(42) got %v", got)
	}
	cfg.RemoveTag("42", "a")
	if got := cfg.TagsFor("42"); len(got) != 1 || got[0] != "b" {
		t.Fatalf("TagsFor(42) after remove got %v", got)
	}
	cfg.RemoveTag("42", "b")
	if _, ok := cfg.Tags["42"]; ok {
		t.Fatalf("expected empty tag entry removed")
	}
	cfg.SetPeersPassword("42", "p0")
	if got := cfg.PasswordForPeer("42"); got != "p0" {
		t.Fatalf("PasswordForPeer got %q", got)
	}
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	data, _ := os.ReadFile(Path())
	if strings.Count(string(data), "[tags]") != 0 {
		t.Fatalf("saved config should not contain [tags] header after all tags removed:\n%s", data)
	}
	if !strings.Contains(string(data), "[peers.\"42\"]") || !strings.Contains(string(data), `password = "p0"`) {
		t.Fatalf("saved config should keep peers password:\n%s", data)
	}
}

func TestPathRespectsEnv(t *testing.T) {
	t.Setenv("RDC_CONFIG", "/tmp/custom-rdc.toml")
	if got := Path(); got != "/tmp/custom-rdc.toml" {
		t.Fatalf("Path got %q", got)
	}
}

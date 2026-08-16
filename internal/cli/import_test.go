package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"rdcli/internal/config"
)

func TestParsePeerFile(t *testing.T) {
	dir := t.TempDir()
	tomlContent := `password = [48, 48, 65, 66]  # array of ASCII bytes
[info]
username = "anki7"
hostname = "kali"
platform = "Windows"
[options]
local_dir = "/Users/ankityadav/Desktop"
remote_dir = '{"anki7":"C:\\Users\\anki7\\Desktop"}'
`
	peerFile := filepath.Join(dir, "123456789.toml")
	if err := os.WriteFile(peerFile, []byte(tomlContent), 0o600); err != nil {
		t.Fatalf("write peer file: %v", err)
	}

	id, password, name, info, err := parsePeerFile(peerFile)
	if err != nil {
		t.Fatalf("parsePeerFile failed: %v", err)
	}

	if id != "123456789" {
		t.Errorf("id got %q, want %q", id, "123456789")
	}
	if password != "00AB" {
		t.Errorf("password got %q, want %q", password, "00AB")
	}
	if name != "anki7-kali" {
		t.Errorf("name got %q, want %q", name, "anki7-kali")
	}
	if info["platform"] != "Windows" {
		t.Errorf("platform got %q, want %q", info["platform"], "Windows")
	}
}

func TestRunImport(t *testing.T) {
	peersDir := t.TempDir()
	peer1 := `password = [48, 48, 65]
[info]
username = "user1"
hostname = "host1"
platform = "Linux"
`
	peer2 := `password = [48, 48, 66]
[info]
username = "user2"
platform = "Windows"
`
	if err := os.WriteFile(filepath.Join(peersDir, "111.toml"), []byte(peer1), 0o600); err != nil {
		t.Fatalf("write peer 1: %v", err)
	}
	if err := os.WriteFile(filepath.Join(peersDir, "222.toml"), []byte(peer2), 0o600); err != nil {
		t.Fatalf("write peer 2: %v", err)
	}

	cfgFile := filepath.Join(t.TempDir(), "config.toml")
	t.Setenv("RDC_CONFIG", cfgFile)

	rootCmd := NewRootCmd()
	rootCmd.SetArgs([]string{"import", "--source", peersDir})
	var outBuf bytes.Buffer
	rootCmd.SetOut(&outBuf)

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("import command failed: %v", err)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config failed: %v", err)
	}

	if len(cfg.Peers) != 2 {
		t.Fatalf("expected 2 peers in config, got %d", len(cfg.Peers))
	}
	if p1 := cfg.Peers["111"]; p1.Password != "00A" || p1.Name != "user1-host1" {
		t.Errorf("peer 111 got %+v", p1)
	}
	if p2 := cfg.Peers["222"]; p2.Password != "00B" || p2.Name != "user2" {
		t.Errorf("peer 222 got %+v", p2)
	}
	if tags1 := cfg.TagsFor("111"); len(tags1) != 1 || tags1[0] != "linux" {
		t.Errorf("peer 111 tags got %v", tags1)
	}
	if tags2 := cfg.TagsFor("222"); len(tags2) != 1 || tags2[0] != "windows" {
		t.Errorf("peer 222 tags got %v", tags2)
	}

	// Test import again without --force (should skip both)
	rootCmd2 := NewRootCmd()
	rootCmd2.SetArgs([]string{"import", "--source", peersDir})
	if err := rootCmd2.Execute(); err != nil {
		t.Fatalf("second import failed: %v", err)
	}

	// Modify peer file and test --force
	peer1Modified := `password = [48, 48, 67]
[info]
username = "user1-mod"
platform = "Linux"
`
	if err := os.WriteFile(filepath.Join(peersDir, "111.toml"), []byte(peer1Modified), 0o600); err != nil {
		t.Fatalf("write modified peer 1: %v", err)
	}

	rootCmd3 := NewRootCmd()
	rootCmd3.SetArgs([]string{"import", "--source", peersDir, "--force"})
	if err := rootCmd3.Execute(); err != nil {
		t.Fatalf("forced import failed: %v", err)
	}

	cfgReloaded, _ := config.Load()
	if p1 := cfgReloaded.Peers["111"]; p1.Password != "00C" || p1.Name != "user1-mod" {
		t.Errorf("forced peer 111 got %+v, want password 00C and name user1-mod", p1)
	}
}

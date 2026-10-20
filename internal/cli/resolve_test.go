package cli

import (
	"path/filepath"
	"testing"

	"github.com/4nkitd/rustdesk-cli/internal/config"
)

func TestResolveRef(t *testing.T) {
	cfg := &config.Config{
		Peers: map[string]config.Peer{
			"1001": {Name: "Kali-Linux", Password: "pass-kali"},
			"1002": {Name: "Ubuntu-Server", Password: "pass-ubuntu"},
		},
		Tags: map[string][]string{
			"1001": {"dev", "security"},
			"1002": {"prod", "web"},
		},
	}

	tests := []struct {
		name         string
		ref          string
		wantID       string
		wantPassword string
	}{
		{"exact ID match", "1001", "1001", "pass-kali"},
		{"name substring match", "kali", "1001", "pass-kali"},
		{"name case-insensitive match", "UBUNTU", "1002", "pass-ubuntu"},
		{"tag match", "security", "1001", "pass-kali"},
		{"tag match case-insensitive", "PROD", "1002", "pass-ubuntu"},
		{"raw ID fallback", "9999", "9999", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotID, gotPassword, err := ResolveRef(cfg, tt.ref)
			if err != nil {
				t.Fatalf("ResolveRef(%q) unexpected error: %v", tt.ref, err)
			}
			if gotID != tt.wantID {
				t.Errorf("ResolveRef(%q) gotID = %q, want %q", tt.ref, gotID, tt.wantID)
			}
			if gotPassword != tt.wantPassword {
				t.Errorf("ResolveRef(%q) gotPassword = %q, want %q", tt.ref, gotPassword, tt.wantPassword)
			}
		})
	}
}

func TestConnectAndDisconnectCmd(t *testing.T) {
	tmpDir := t.TempDir()
	cfgFile := filepath.Join(tmpDir, "config.toml")
	t.Setenv("RDC_CONFIG", cfgFile)

	root := NewRootCmd()
	root.SetArgs([]string{"connect", "kali-box", "-p", "secret123"})
	if err := root.Execute(); err != nil {
		t.Fatalf("connect failed: %v", err)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config failed: %v", err)
	}
	if p := cfg.Peers["kali-box"]; p.Password != "secret123" {
		t.Errorf("peer password got %q, want %q", p.Password, "secret123")
	}

	rootDisconnect := NewRootCmd()
	rootDisconnect.SetArgs([]string{"disconnect", "kali-box"})
	if err := rootDisconnect.Execute(); err != nil {
		t.Fatalf("disconnect failed: %v", err)
	}

	cfgReloaded, _ := config.Load()
	if p := cfgReloaded.Peers["kali-box"]; p.Password != "" {
		t.Errorf("peer password after disconnect got %q, want %q", p.Password, "")
	}
}

func TestResolveTargetFlagsAndArgs(t *testing.T) {
	tmpDir := t.TempDir()
	cfgFile := filepath.Join(tmpDir, "config.toml")
	t.Setenv("RDC_CONFIG", cfgFile)

	cfg := &config.Config{
		Peers: map[string]config.Peer{
			"1001": {Name: "kali", Password: "pass-kali"},
		},
		Tags: map[string][]string{},
	}
	_ = cfg.Save()

	// Test positional arg
	cmdPos := NewRootCmd()
	cmdPos.SetArgs([]string{"ls", "1001", "/tmp"})
	subCmdPos, argsPos, err := cmdPos.Find([]string{"ls", "1001", "/tmp"})
	if err != nil {
		t.Fatalf("find cmd failed: %v", err)
	}
	_ = subCmdPos.ParseFlags(argsPos)

	var peerID, pw string
	var remArgs []string
	peerID, pw, remArgs, err = resolveTarget(subCmdPos, subCmdPos.Flags().Args())
	if err != nil {
		t.Fatalf("resolveTarget with positional arg failed: %v", err)
	}
	if peerID != "1001" || pw != "pass-kali" {
		t.Errorf("got peerID=%q, pw=%q", peerID, pw)
	}
	if len(remArgs) != 1 || remArgs[0] != "/tmp" {
		t.Errorf("got remArgs=%v, want [/tmp]", remArgs)
	}
}

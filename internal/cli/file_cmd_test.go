package cli

import (
	"testing"
)

func TestIsRemotePath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"/remote/path", true},
		{"/C:/remote/path", true},
		{"/file.txt", true},
		{"relative/path", false},
		{"file.txt", false},
		{"./file.txt", false},
	}

	for _, tt := range tests {
		got := isRemotePath(tt.path)
		if got != tt.want {
			t.Errorf("isRemotePath(%q) = %v; want %v", tt.path, got, tt.want)
		}
	}
}

func TestLsCmdStructure(t *testing.T) {
	cmd := newLsCmd()
	if cmd.Name() != "ls" {
		t.Errorf("expected command name 'ls', got %q", cmd.Name())
	}
	if cmd.Use != "ls [<ref>] [remote_path]" {
		t.Errorf("unexpected usage: %q", cmd.Use)
	}
}

func TestCpCmdStructure(t *testing.T) {
	cmd := newCpCmd()
	if cmd.Name() != "cp" {
		t.Errorf("expected command name 'cp', got %q", cmd.Name())
	}
	if cmd.Use != "cp [<ref>] <src> <dst>" {
		t.Errorf("unexpected usage: %q", cmd.Use)
	}
	recFlag := cmd.Flags().Lookup("recursive")
	if recFlag == nil {
		t.Fatalf("expected --recursive flag on cp command")
	}
	if recFlag.Shorthand != "r" {
		t.Errorf("expected shorthand 'r', got %q", recFlag.Shorthand)
	}
}

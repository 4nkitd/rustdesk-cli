package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfiguredKeyScopeAndPrecedence(t *testing.T) {
	t.Setenv("RDC_CONFIG", filepath.Join(t.TempDir(), "config.toml"))
	t.Setenv("RDC_KEY", "")
	t.Setenv("RDC_SERVER", "")
	if err := os.WriteFile(os.Getenv("RDC_CONFIG"), []byte("server = \"fixture.invalid\"\nkey = \"configured-public-key\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		flags []string
		env   string
		want  string
	}{
		{"configured", nil, "", "configured-public-key"},
		{"normalized server", []string{"--server", "FIXTURE.invalid:21116"}, "", "configured-public-key"},
		{"different server", []string{"--server", "other.invalid"}, "", ""},
		{"environment", nil, "environment-public-key", "environment-public-key"},
		{"flag", []string{"--key", "flag-public-key"}, "environment-public-key", "flag-public-key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("RDC_KEY", tc.env)
			cmd := NewRootCmd()
			if err := cmd.ParseFlags(tc.flags); err != nil {
				t.Fatal(err)
			}
			if got := getKeyFlag(cmd); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

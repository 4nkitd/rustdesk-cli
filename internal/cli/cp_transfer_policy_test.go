package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestCpTransferPolicyRejectsUnsafeRequests(t *testing.T) {
	t.Setenv("RDC_CONFIG", filepath.Join(t.TempDir(), "config.toml"))
	t.Setenv("RDC_PASSWORD", "")
	for _, tc := range []struct {
		name  string
		flags []string
		want  string
	}{
		{"ambiguous absolute destination", nil, "specify --direction"},
		{"upload without permission", []string{"--direction", "up"}, "uploads require --overwrite"},
		{"global yes is not overwrite", []string{"--direction", "up", "--yes"}, "uploads require --overwrite"},
		{"unsafe upload skip", []string{"--direction", "up", "--skip"}, "cannot guarantee atomic no-clobber"},
		{"unsupported upload resume", []string{"--direction", "up", "--overwrite", "--resume"}, "--resume is unavailable"},
		{"unsupported download resume", []string{"--direction", "down", "--resume"}, "--resume is unavailable"},
		{"conflicting policies", []string{"--direction", "down", "--overwrite", "--skip"}, "mutually exclusive"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newCpCmd()
			registerCommonFlags(cmd)
			if err := cmd.ParseFlags(tc.flags); err != nil {
				t.Fatal(err)
			}
			err := runCp(cmd, []string{"123456789", "local-file", "/local/absolute/destination"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v, want %q", err, tc.want)
			}
		})
	}
}

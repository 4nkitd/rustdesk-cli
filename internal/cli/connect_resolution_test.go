package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestConnectCommandsRejectUnresolvedReferences(t *testing.T) {
	for _, action := range []string{"connect", "disconnect"} {
		for _, ref := range []string{"oops", "prod"} {
			t.Run(action+"/"+ref, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "config.toml")
				t.Setenv("RDC_CONFIG", path)
				t.Setenv("RDC_PASSWORD", "replacement-fixture-password")
				before := []byte("[peers.\"111111111\"]\nname = \"prod-one\"\npassword = \"fixture-one\"\n[peers.\"222222222\"]\nname = \"prod-two\"\npassword = \"fixture-two\"\n")
				if err := os.WriteFile(path, before, 0o600); err != nil {
					t.Fatal(err)
				}
				var cmd *cobra.Command
				if action == "connect" {
					cmd = newConnectCmd()
				} else {
					cmd = newDisconnectCmd()
				}
				cmd.SetOut(io.Discard)
				cmd.SetErr(io.Discard)
				cmd.SetArgs([]string{ref})
				err := cmd.Execute()
				want := "unknown machine reference"
				if ref == "prod" {
					want = "ambiguous machine reference"
				}
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("expected resolution failure, got %v", err)
				}
				after, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(before, after) {
					t.Fatal("failed reference resolution changed saved credentials")
				}
			})
		}
	}
}

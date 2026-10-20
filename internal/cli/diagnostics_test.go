package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/4nkitd/rustdesk-cli/internal/hbb"
)

func TestDiagnosticsCommandsMissingTargetJSON(t *testing.T) {
	for _, cmd := range []*cobra.Command{newInfoCmd(), newDoctorCmd()} {
		t.Run(cmd.Name(), func(t *testing.T) {
			registerCommonFlags(cmd)
			var output bytes.Buffer
			cmd.SetOut(&output)
			cmd.SetErr(&bytes.Buffer{})
			cmd.SilenceUsage = true
			cmd.SilenceErrors = true
			cmd.SetArgs([]string{"--json"})
			err := cmd.Execute()
			var de *hbb.DiagnosticError
			if !errors.As(err, &de) || de.Stage != "resolve" {
				t.Fatalf("expected structured resolve failure, got %v", err)
			}
			var report hbb.Diagnostics
			if err := json.Unmarshal(output.Bytes(), &report); err != nil {
				t.Fatalf("invalid JSON output %q: %v", output.String(), err)
			}
			if report.OK || report.Version != 1 || report.Error == nil || report.Error.Code != "invalid_target" || report.Secured != nil {
				t.Fatalf("unexpected report: %+v", report)
			}
		})
	}
}

func TestDiagnosticsJSONRetainsUnknownAndFalse(t *testing.T) {
	cmd := newInfoCmd()
	registerCommonFlags(cmd)
	if err := cmd.ParseFlags([]string{"--json"}); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	cmd.SetOut(&output)
	no := false
	result := hbb.Diagnostics{
		Version: 1, OK: true, Stage: "complete", Transport: "direct", Secured: &no,
		Capabilities: map[string]string{"terminal": "unknown"},
		Permissions:  map[string]*bool{"file": &no, "terminal": nil},
	}
	if err := writeDiagnostics(cmd, result); err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{`"secured":false`, `"file":false`, `"terminal":null`, `"terminal":"unknown"`} {
		if !strings.Contains(output.String(), fragment) {
			t.Fatalf("output lost observed/unknown distinction %s: %s", fragment, output.String())
		}
	}
}

func TestDiagnosticsTextExplainsObservations(t *testing.T) {
	cmd := newDoctorCmd()
	var output bytes.Buffer
	cmd.SetOut(&output)
	yes := true
	version := "1.4.1"
	result := hbb.Diagnostics{
		Version: 1, OK: true, Stage: "complete", Transport: "relay", Secured: &yes,
		Peer:         &hbb.DiagnosticPeer{Version: &version},
		Capabilities: map[string]string{"terminal": "advertised"},
		Permissions:  map[string]*bool{"terminal": nil},
	}
	if err := writeDiagnostics(cmd, result); err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"transport: relay", "secured: true", "version: 1.4.1", "hostname: unknown", "capability terminal: advertised", "observed permission terminal: unknown"} {
		if !strings.Contains(output.String(), fragment) {
			t.Fatalf("missing %q in %s", fragment, output.String())
		}
	}
}

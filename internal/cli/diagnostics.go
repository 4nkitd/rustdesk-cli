package cli

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/spf13/cobra"

	"github.com/4nkitd/rustdesk-cli/internal/hbb"
	pb "github.com/4nkitd/rustdesk-cli/internal/hbb/proto"
)

func newInfoCmd() *cobra.Command {
	return &cobra.Command{
		Use: "info [<ref>]", Short: "read peer information from a live authenticated connection",
		Long: diagnosticHelp,
		Args: maxNArgs(1, "usage: rdcli info [<id|name|tag>]"), RunE: runDiagnostics,
	}
}

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use: "doctor [<ref>]", Short: "probe one peer connection and authentication without remote changes",
		Long: diagnosticHelp,
		Args: maxNArgs(1, "usage: rdcli doctor [<id|name|tag>]"), RunE: runDiagnostics,
	}
}

const diagnosticHelp = `Connect to one peer, authenticate in a read-only session, and close it.
The timeout applies to each connection phase and separately to authentication.
Capabilities are peer advertisements. Permissions are announcements observed
during this short probe; missing announcements remain unknown. Success does
not establish permission to run terminals, tunnels, or file operations.
Supply --otp or RDC_OTP when the peer requires two-factor authentication.
Diagnostics makes no screenshot request and retains no screen content, but
the default session may briefly start desktop capture on the peer.`

func runDiagnostics(cmd *cobra.Command, args []string) error {
	peerID, password, remaining, err := resolveTarget(cmd, args)
	if err != nil || len(remaining) != 0 {
		de := &hbb.DiagnosticError{Stage: "resolve", Code: "invalid_target", Message: "could not resolve one peer target"}
		return writeDiagnostics(cmd, hbb.Diagnostics{Version: 1, Stage: de.Stage, Transport: "unknown", Error: de})
	}
	result := hbb.Probe(dialOpts(cmd, peerID, password, pb.ConnType_DEFAULT_CONN))
	return writeDiagnostics(cmd, result)
}

func writeDiagnostics(cmd *cobra.Command, result hbb.Diagnostics) error {
	if isJSONFlag(cmd) {
		if err := json.NewEncoder(cmd.OutOrStdout()).Encode(result); err != nil {
			return err
		}
	} else {
		out := cmd.OutOrStdout()
		status := "failed"
		if result.OK {
			status = "ok"
		}
		fmt.Fprintf(out, "%s (%s, %dms)\n", status, result.Stage, result.ElapsedMS)
		fmt.Fprintf(out, "transport: %s\nsecured: %s\n", result.Transport, diagnosticBool(result.Secured))
		if pi := result.Peer; pi != nil {
			fmt.Fprintf(out, "version: %s\nplatform: %s\nhostname: %s\n", diagnosticString(pi.Version), diagnosticString(pi.Platform), diagnosticString(pi.Hostname))
		}
		for _, name := range sortedDiagnosticKeys(result.Capabilities) {
			fmt.Fprintf(out, "capability %s: %s\n", name, result.Capabilities[name])
		}
		for _, name := range sortedDiagnosticKeys(result.Permissions) {
			fmt.Fprintf(out, "observed permission %s: %s\n", name, diagnosticBool(result.Permissions[name]))
		}
		if result.Error != nil {
			fmt.Fprintf(out, "error: %s (%s)\n", result.Error.Message, result.Error.Code)
		}
	}
	if result.Error != nil {
		return result.Error
	}
	return nil
}

func sortedDiagnosticKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func diagnosticBool(value *bool) string {
	if value == nil {
		return "unknown"
	}
	if *value {
		return "true"
	}
	return "false"
}

func diagnosticString(value *string) string {
	if value == nil {
		return "unknown"
	}
	return *value
}

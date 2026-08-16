package cli

import (
	"github.com/spf13/cobra"
)

func newImportCmd() *cobra.Command {
	cmd := newImportGuiCmd()
	cmd.Use = "import"
	cmd.Hidden = true
	return cmd
}

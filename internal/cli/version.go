package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

var version = "v0.1.0"

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "print version",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Printf("rdcli %s\n", version)
			return nil
		},
	}
}

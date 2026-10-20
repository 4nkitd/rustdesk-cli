package cli

import "github.com/spf13/cobra"

func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "rdcli",
		Short: "headless RustDesk client CLI",
	}
	registerCommonFlags(root)
	root.PersistentFlags().StringP("connect", "c", "", "machine ref (id, name, or tag)")

	root.AddCommand(
		newConnectCmd(),
		newDisconnectCmd(),
		newAuthCmd(),
		newVersionCmd(),
		newPingCmd(),
		newTunnelCmd(),
		newLsCmd(),
		newCpCmd(),
		newShCmd(),
		newDevicesCmd(),
		newTagCmd(),
		newImportGuiCmd(),
		newImportCmd(),
		newClipboardCmd(),
		newInfoCmd(),
		newDoctorCmd(),
		newMkdirCmd(),
		newRmCmd(),
		newRenameCmd(),
		newScreenshotCmd(),
	)
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()
	configureUsageErrors(root)
	return root
}

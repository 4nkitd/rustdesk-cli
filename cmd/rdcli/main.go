package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/4nkitd/rustdesk-cli/internal/cli"
)

func main() {
	root := cli.NewRootCmd()
	root.SilenceUsage = true
	root.SilenceErrors = true
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(commandExitCode(err))
	}
}

func commandExitCode(err error) int {
	var ue *cli.UsageError
	if errors.As(err, &ue) {
		return 2
	}
	var status interface{ ExitCode() int }
	if errors.As(err, &status) {
		if code := status.ExitCode(); code > 0 && code <= 255 {
			return code
		}
	}
	return 1
}

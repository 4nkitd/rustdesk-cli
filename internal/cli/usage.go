package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

type UsageError struct {
	msg string
}

func (e *UsageError) Error() string { return e.msg }

func usageErrorf(format string, args ...any) error {
	return &UsageError{msg: fmt.Sprintf(format, args...)}
}

func maxNArgs(n int, usage string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) > n {
			return &UsageError{msg: usage}
		}
		return nil
	}
}

func exactArgs(n int, usage string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) != n {
			return &UsageError{msg: usage}
		}
		return nil
	}
}

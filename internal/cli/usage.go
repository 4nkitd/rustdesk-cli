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

func configureUsageErrors(cmd *cobra.Command) {
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return usageErrorf("%s", err)
	})
	// Cobra skips Args validation on non-runnable command groups.
	if !cmd.Runnable() {
		cmd.Args = cobra.NoArgs
		cmd.RunE = func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		}
	}
	validateArgs := cmd.Args
	cmd.Args = func(cmd *cobra.Command, args []string) error {
		if validateArgs != nil {
			if err := validateArgs(cmd, args); err != nil {
				return usageErrorf("%s", err)
			}
		}
		if err := cmd.ValidateRequiredFlags(); err != nil {
			return usageErrorf("%s", err)
		}
		if err := cmd.ValidateFlagGroups(); err != nil {
			return usageErrorf("%s", err)
		}
		return nil
	}
	for _, child := range cmd.Commands() {
		configureUsageErrors(child)
	}
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

package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/4nkitd/rustdesk-cli/internal/cli"
)

func TestShProcessExitStatus(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want int
	}{
		{errors.New("transport failed"), 1},
		{&cli.UsageError{}, 2},
		{fmt.Errorf("wrapped: %w", &cli.CommandExitError{Code: 27}), 27},
		{&cli.CommandExitError{Code: 255}, 255},
		{&cli.CommandExitError{Code: -1}, 1},
		{&cli.CommandExitError{Code: 256}, 1},
		{&cli.CommandExitError{Code: 0}, 1},
	} {
		if got := commandExitCode(tc.err); got != tc.want {
			t.Fatalf("%v: got %d, want %d", tc.err, got, tc.want)
		}
	}
}

package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/4nkitd/rustdesk-cli/internal/hbb"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	pb "github.com/4nkitd/rustdesk-cli/internal/hbb/proto"
)

const oneShotCommandTimeout = 5 * time.Minute

func newShCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sh [<ref>] [command]",
		Short: "remote shell session or command execution",
		Long:  "Open an interactive remote terminal, or run a command using POSIX sh on Linux/macOS.\nOne-shot completion and exit status are confirmed by the command, not output silence.",
		RunE:  runSh,
	}
	cmd.Flags().Duration("idle", 300*time.Millisecond, "ignored; completion requires a command status")
	_ = cmd.Flags().MarkDeprecated("idle", "use --command-timeout; output silence no longer ends commands")
	cmd.Flags().Duration("command-timeout", oneShotCommandTimeout, "maximum one-shot command duration after opening the terminal")
	return cmd
}

func runSh(cmd *cobra.Command, args []string) error {
	peerID, password, remArgs, err := resolveTarget(cmd, args)
	if err != nil {
		return err
	}
	opts := dialOpts(cmd, peerID, password, pb.ConnType_TERMINAL)

	if len(remArgs) > 0 {
		command := strings.Join(remArgs, " ")
		timeout, _ := cmd.Flags().GetDuration("command-timeout")
		if timeout <= 0 {
			return usageErrorf("--command-timeout must be greater than zero")
		}
		return runShOneShotContext(cmd.Context(), opts, command, timeout, cmd.OutOrStdout())
	}

	return runShInteractiveContext(cmd.Context(), opts)
}

func runShInteractive(opts hbb.DialOpts) error {
	return runShInteractiveContext(context.Background(), opts)
}

func runShInteractiveContext(ctx context.Context, opts hbb.DialOpts) error {
	stdoutFd := int(os.Stdout.Fd())
	cols, rows, err := term.GetSize(stdoutFd)
	if err != nil || cols <= 0 || rows <= 0 {
		cols = 80
		rows = 24
	}

	ts, err := hbb.OpenTerminal(opts, uint32(rows), uint32(cols))
	if err != nil {
		return err
	}
	defer ts.Close()
	stop := context.AfterFunc(ctx, func() { ts.Close() })
	defer stop()
	stdinFd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(stdinFd)
	if err == nil {
		defer term.Restore(stdinFd, oldState)
	}

	sigCh := make(chan os.Signal, 1)
	notifySigwinch(sigCh)
	defer signal.Stop(sigCh)
	done := make(chan struct{})
	defer close(done)

	go func() {
		for {
			select {
			case <-done:
				return
			case <-sigCh:
				if c, r, err := term.GetSize(stdoutFd); err == nil && c > 0 && r > 0 {
					_ = ts.Resize(uint32(r), uint32(c))
				}
			}
		}
	}()

	inputErr := make(chan error, 1)
	inputCtx, cancelInput := context.WithCancel(ctx)
	defer cancelInput()
	go func() {
		if err := copyShInput(inputCtx, os.Stdin, ts); err != nil && err != io.EOF {
			inputErr <- err
			ts.Close()
		}
	}()

	buf := make([]byte, 4096)
	for {
		n, err := ts.Read(buf)
		if n > 0 {
			if _, werr := os.Stdout.Write(buf[:n]); werr != nil {
				return werr
			}
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			select {
			case input := <-inputErr:
				return input
			default:
			}
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

func runShOneShot(opts hbb.DialOpts, command string, timeout time.Duration) error {
	return runShOneShotContext(context.Background(), opts, command, timeout, os.Stdout)
}

func runShOneShotContext(ctx context.Context, opts hbb.DialOpts, command string, timeout time.Duration, output io.Writer) error {
	if timeout <= 0 {
		return usageErrorf("--command-timeout must be greater than zero")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	cols, rows, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || cols <= 0 || rows <= 0 {
		cols = 80
		rows = 24
	}

	ts, err := hbb.OpenTerminal(opts, uint32(rows), uint32(cols))
	if err != nil {
		return err
	}
	defer ts.Close()
	switch strings.ToLower(ts.Platform()) {
	case "linux", "mac os", "macos":
	default:
		return fmt.Errorf("one-shot commands require a POSIX shell on Linux/macOS; peer platform is %q", ts.Platform())
	}
	if err := ts.SetDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { ts.Close() })
	defer stop()
	err = executeShCommand(ts, output, command)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

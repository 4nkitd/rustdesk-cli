package cli

import (
	"os"
	"os/signal"
	"strings"
	"time"

	"rdcli/internal/hbb"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	pb "rdcli/internal/hbb/proto"
)

const (
	oneShotMinWait = 1 * time.Second
	oneShotIdle    = 300 * time.Millisecond
)

func newShCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sh [<ref>] [command]",
		Short: "remote shell session or command execution",
		RunE:  runSh,
	}
	cmd.Flags().Duration("idle", oneShotIdle, "one-shot idle timeout after output")
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
		idle, _ := cmd.Flags().GetDuration("idle")
		return runShOneShot(opts, command, idle)
	}

	return runShInteractive(opts)
}

func runShInteractive(opts hbb.DialOpts) error {
	stdoutFd := int(os.Stdout.Fd())
	cols, rows, err := term.GetSize(stdoutFd)
	if err != nil || cols <= 0 || rows <= 0 {
		cols = 80
		rows = 24
	}

	stdinFd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(stdinFd)
	if err == nil {
		defer term.Restore(stdinFd, oldState)
	}

	ts, err := hbb.OpenTerminal(opts, uint32(rows), uint32(cols))
	if err != nil {
		return err
	}
	defer ts.Close()

	sigCh := make(chan os.Signal, 1)
	notifySigwinch(sigCh)
	defer signal.Stop(sigCh)

	go func() {
		for range sigCh {
			if c, r, err := term.GetSize(stdoutFd); err == nil && c > 0 && r > 0 {
				_ = ts.Resize(uint32(r), uint32(c))
			}
		}
	}()

	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				if _, werr := ts.Write(buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	buf := make([]byte, 4096)
	for {
		n, err := ts.Read(buf)
		if n > 0 {
			os.Stdout.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}

	return nil
}

func runShOneShot(opts hbb.DialOpts, command string, idle time.Duration) error {
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

	if _, err := ts.Write([]byte(command + "\n")); err != nil {
		return err
	}

	if idle <= 0 {
		idle = oneShotIdle
	}

	outCh := make(chan []byte, 100)
	errCh := make(chan error, 1)

	go func() {
		for {
			buf := make([]byte, 4096)
			n, err := ts.Read(buf)
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				outCh <- chunk
			}
			if err != nil {
				errCh <- err
				return
			}
		}
	}()

	start := time.Now()
	firstOutput := false
	var idleC <-chan time.Time
	var idleTimer *time.Timer
	armIdle := func() {
		if idleTimer == nil {
			idleTimer = time.NewTimer(idle)
		} else {
			if !idleTimer.Stop() {
				select {
				case <-idleTimer.C:
				default:
				}
			}
			idleTimer.Reset(idle)
		}
		idleC = idleTimer.C
	}

	for {
		select {
		case chunk := <-outCh:
			os.Stdout.Write(chunk)
			firstOutput = true
			armIdle()
		case <-errCh:
			for {
				select {
				case chunk := <-outCh:
					os.Stdout.Write(chunk)
				default:
					return nil
				}
			}
		case <-idleC:
			if firstOutput && time.Since(start) >= oneShotMinWait {
				return nil
			}
			idleC = nil
		}
	}
}

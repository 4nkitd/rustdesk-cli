//go:build unix

package cli

import (
	"context"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

func copyShInput(ctx context.Context, input *os.File, output io.Writer) error {
	fds := []unix.PollFd{{Fd: int32(input.Fd()), Events: unix.POLLIN}}
	buf := make([]byte, 1024)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := unix.Poll(fds, 100)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return err
		}
		if n == 0 {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err = input.Read(buf)
		if n > 0 {
			written, werr := output.Write(buf[:n])
			if werr != nil {
				return werr
			}
			if written != n {
				return io.ErrShortWrite
			}
		}
		if err != nil {
			return err
		}
	}
}

//go:build !unix

package cli

import (
	"context"
	"io"
	"os"
)

func copyShInput(ctx context.Context, input *os.File, output io.Writer) error {
	buf := make([]byte, 1024)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := input.Read(buf)
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

package cli

import (
	"errors"
	"io"
	"os"
)

func newTunnelStdio(input, output *os.File) (io.ReadWriteCloser, error) {
	return nil, errors.New("tunnel --once is unsupported on Windows because stdin cannot be cancelled safely; use -L without --once")
}

//go:build unix

package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

type tunnelStdioFD struct {
	fd    int
	flags int
	file  *os.File
}

type tunnelStdio struct {
	in      tunnelStdioFD
	out     tunnelStdioFD
	done    chan struct{}
	readMu  sync.Mutex
	writeMu sync.Mutex
	once    sync.Once
	err     error
}

// newTunnelStdio borrows the files exclusively until Close. Duplicates share
// status flags, so Close restores them after both I/O operations have stopped.
// The caller's files remain open.
func newTunnelStdio(input, output *os.File) (io.ReadWriteCloser, error) {
	in, err := duplicateTunnelFD(input)
	if err != nil {
		return nil, fmt.Errorf("tunnel stdin: %w", err)
	}
	out, err := duplicateTunnelFD(output)
	if err != nil {
		unix.Close(in.fd)
		return nil, fmt.Errorf("tunnel stdout: %w", err)
	}
	s := &tunnelStdio{in: in, out: out, done: make(chan struct{})}
	for _, f := range []tunnelStdioFD{in, out} {
		if _, err := unix.FcntlInt(uintptr(f.fd), unix.F_SETFL, f.flags|unix.O_NONBLOCK); err != nil {
			s.Close()
			return nil, fmt.Errorf("nonblocking tunnel stdio: %w", err)
		}
	}
	return s, nil
}

func duplicateTunnelFD(file *os.File) (tunnelStdioFD, error) {
	f := tunnelStdioFD{fd: -1, file: file}
	raw, err := file.SyscallConn()
	if err != nil {
		return f, err
	}
	var controlErr error
	err = raw.Control(func(fd uintptr) {
		var stat unix.Stat_t
		if controlErr = unix.Fstat(int(fd), &stat); controlErr != nil {
			return
		}
		switch stat.Mode & unix.S_IFMT {
		case unix.S_IFIFO, unix.S_IFSOCK:
		case unix.S_IFCHR:
			if !term.IsTerminal(int(fd)) {
				controlErr = errors.New("tunnel --once requires pipe, socket, or terminal stdio; this device cannot be cancelled safely")
				return
			}
		default:
			controlErr = errors.New("tunnel --once requires pipe, socket, or terminal stdio; regular files cannot be cancelled safely")
			return
		}
		f.flags, controlErr = unix.FcntlInt(fd, unix.F_GETFL, 0)
		if controlErr == nil {
			f.fd, controlErr = unix.FcntlInt(fd, unix.F_DUPFD_CLOEXEC, 0)
		}
	})
	if err != nil {
		return f, err
	}
	return f, controlErr
}

func (s *tunnelStdio) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	s.readMu.Lock()
	defer s.readMu.Unlock()
	for {
		if err := s.checkClosed(); err != nil {
			return 0, err
		}
		n, err := unix.Read(s.in.fd, p)
		if err == unix.EINTR {
			continue
		}
		if err == unix.EAGAIN || err == unix.EWOULDBLOCK {
			if err := s.wait(s.in.fd, unix.POLLIN); err != nil {
				return 0, err
			}
			continue
		}
		if n == 0 && err == nil {
			err = io.EOF
		}
		return max(n, 0), err
	}
}

func (s *tunnelStdio) Write(p []byte) (int, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	total := 0
	for len(p) > 0 {
		if err := s.checkClosed(); err != nil {
			return total, err
		}
		n, err := unix.Write(s.out.fd, p)
		if n > 0 {
			total += n
			p = p[n:]
		}
		if err == unix.EINTR {
			continue
		}
		if err == unix.EAGAIN || err == unix.EWOULDBLOCK {
			if err := s.wait(s.out.fd, unix.POLLOUT); err != nil {
				return total, err
			}
			continue
		}
		if err != nil {
			return total, err
		}
		if n == 0 {
			return total, io.ErrShortWrite
		}
	}
	return total, nil
}

func (s *tunnelStdio) checkClosed() error {
	select {
	case <-s.done:
		return os.ErrClosed
	default:
		return nil
	}
}

func (s *tunnelStdio) wait(fd int, events int16) error {
	fds := []unix.PollFd{{Fd: int32(fd), Events: events}}
	for {
		if err := s.checkClosed(); err != nil {
			return err
		}
		n, err := unix.Poll(fds, 50)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return err
		}
		if n > 0 {
			return s.checkClosed()
		}
	}
}

func (s *tunnelStdio) Close() error {
	s.once.Do(func() {
		close(s.done)
		s.readMu.Lock()
		defer s.readMu.Unlock()
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
		for _, f := range []tunnelStdioFD{s.out, s.in} {
			_, err := unix.FcntlInt(uintptr(f.fd), unix.F_SETFL, f.flags)
			s.err = errors.Join(s.err, err, unix.Close(f.fd))
		}
	})
	return s.err
}

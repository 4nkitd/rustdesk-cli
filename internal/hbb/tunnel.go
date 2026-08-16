package hbb

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sync"

	pb "rdcli/internal/hbb/proto"
)

// Tunnel runs a single port-forward session: it logs in to the peer as a
// port-forward connection, then pipes raw bytes bidirectionally.
type Tunnel struct {
	conn *Conn
}

func NewTunnel(opts DialOpts, host string, port int32) (*Tunnel, error) {
	opts.ConnType = pb.ConnType_PORT_FORWARD
	c, err := Connect(opts)
	if err != nil {
		return nil, err
	}
	if _, err := c.Login(LoginTunnel, LoginSpec{Host: host, Port: port}); err != nil {
		c.Close()
		return nil, err
	}
	c.SetRaw()
	return &Tunnel{conn: c}, nil
}

func (t *Tunnel) Pipe(local io.ReadWriteCloser) error {
	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := io.Copy(local, t.conn.sc)
		errCh <- copyErr(err, "peer")
	}()
	go func() {
		defer wg.Done()
		_, err := io.Copy(t.conn.sc, local)
		errCh <- copyErr(err, "local")
	}()
	wg.Wait()
	close(errCh)
	var first error
	for err := range errCh {
		if err != nil && first == nil {
			first = err
		}
	}
	if first == nil {
		return errors.New("tunnel closed")
	}
	return first
}

func (t *Tunnel) Close() error {
	return t.conn.Close()
}

func copyErr(err error, side string) error {
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return fmt.Errorf("%s side: %w", side, err)
}

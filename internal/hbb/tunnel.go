package hbb

import (
	"errors"
	"fmt"
	"io"
	"net"

	pb "github.com/4nkitd/rustdesk-cli/internal/hbb/proto"
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

// Pipe owns both streams. Closing local must interrupt pending reads and writes.
func (t *Tunnel) Pipe(local io.ReadWriteCloser) error {
	errCh := make(chan error, 2)
	go func() {
		_, err := io.Copy(local, t.conn.sc)
		errCh <- copyErr(err, "peer")
	}()
	go func() {
		_, err := io.Copy(t.conn.sc, local)
		errCh <- copyErr(err, "local")
	}()
	first := <-errCh
	_ = t.Close()
	_ = local.Close()
	<-errCh
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

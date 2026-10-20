package hbb

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"

	pb "github.com/4nkitd/rustdesk-cli/internal/hbb/proto"
)

type TerminalSession struct {
	conn   *Conn
	termID int32
	rows   uint32
	cols   uint32

	mu        sync.Mutex
	buf       []byte
	closed    bool
	err       error
	closeOnce sync.Once
	closeErr  error
	deadline  time.Time
}

const maxTerminalBufferedOutput = 1 << 20

var terminalZstdDecoder, _ = zstd.NewReader(nil, zstd.WithDecoderMaxMemory(maxTerminalBufferedOutput))

type TerminalExitError struct {
	Code int
}

func (e *TerminalExitError) Error() string {
	return fmt.Sprintf("remote terminal exited with status %d", e.Code)
}

func (e *TerminalExitError) ExitCode() int { return e.Code }

func (ts *TerminalSession) Platform() string { return ts.conn.PeerInfo().GetPlatform() }

func (ts *TerminalSession) SetDeadline(deadline time.Time) error {
	ts.mu.Lock()
	ts.deadline = deadline
	ts.mu.Unlock()
	return ts.conn.sc.SetDeadline(deadline)
}

// OpenTerminal connects and opens a terminal session on the peer.
func OpenTerminal(opts DialOpts, rows, cols uint32) (*TerminalSession, error) {
	opts.ConnType = pb.ConnType_TERMINAL
	conn, err := Connect(opts)
	if err != nil {
		return nil, err
	}

	pi, err := conn.Login(LoginTerminal, LoginSpec{})
	if err != nil {
		conn.Close()
		return nil, err
	}
	if !terminalSupported(pi) {
		conn.Close()
		return nil, fmt.Errorf("peer version %s does not advertise terminal support; requires terminal-enabled RustDesk 1.4.1 or newer", pi.GetVersion())
	}

	return openTerminalOnConn(conn, rows, cols)
}

func openTerminalOnConn(conn *Conn, rows, cols uint32) (*TerminalSession, error) {
	timeout := conn.opts.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	deadline := time.Now().Add(timeout)
	if err := conn.sc.SetDeadline(deadline); err != nil {
		conn.Close()
		return nil, err
	}
	openMsg := &pb.Message{
		Union: &pb.Message_TerminalAction{
			TerminalAction: &pb.TerminalAction{
				Union: &pb.TerminalAction_Open{
					Open: &pb.OpenTerminal{
						TerminalId: 0,
						Rows:       rows,
						Cols:       cols,
					},
				},
			},
		},
	}
	if err := conn.Send(openMsg); err != nil {
		conn.Close()
		return nil, fmt.Errorf("send open terminal: %w", err)
	}

	ts := &TerminalSession{
		conn:   conn,
		termID: 0,
		rows:   rows,
		cols:   cols,
	}

	type earlyData struct {
		id   int32
		data []byte
	}
	var early []earlyData
	buffered := 0
	for {
		if !time.Now().Before(deadline) {
			conn.Close()
			return nil, fmt.Errorf("open terminal: %w", os.ErrDeadlineExceeded)
		}
		msg, err := conn.Recv()
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("recv open terminal response: %w", err)
		}

		if tr := msg.GetTerminalResponse(); tr != nil {
			switch u := tr.GetUnion().(type) {
			case *pb.TerminalResponse_Opened:
				opened := u.Opened
				if !opened.GetSuccess() {
					conn.Close()
					errMsg := opened.GetMessage()
					if errMsg == "" {
						errMsg = "failed to open terminal"
					}
					return nil, errors.New(errMsg)
				}
				ts.termID = opened.GetTerminalId()
				matched := 0
				for _, chunk := range early {
					if chunk.id == ts.termID {
						matched += len(chunk.data)
					}
				}
				if matched > 0 {
					ts.buf = make([]byte, 0, matched)
				}
				for _, chunk := range early {
					if chunk.id == ts.termID {
						ts.buf = append(ts.buf, chunk.data...)
					}
				}
				if err := conn.sc.SetDeadline(time.Time{}); err != nil {
					conn.Close()
					return nil, err
				}
				return ts, nil
			case *pb.TerminalResponse_Error:
				if u.Error.GetTerminalId() != ts.termID {
					continue
				}
				conn.Close()
				return nil, fmt.Errorf("terminal error: %s", u.Error.GetMessage())
			case *pb.TerminalResponse_Closed:
				if u.Closed.GetTerminalId() != ts.termID {
					continue
				}
				conn.Close()
				return nil, errors.New("terminal closed")
			case *pb.TerminalResponse_Data:
				data, err := decodeTerminalData(u.Data)
				if err != nil {
					conn.Close()
					return nil, err
				}
				if len(data) == 0 {
					continue
				}
				buffered += len(data)
				if buffered > maxTerminalBufferedOutput || len(early) >= 1024 {
					conn.Close()
					return nil, errors.New("terminal output before open exceeded buffer limit")
				}
				early = append(early, earlyData{id: u.Data.GetTerminalId(), data: data})
			}
		} else if td := msg.GetTestDelay(); td != nil {
			if err := conn.Send(&pb.Message{
				Union: &pb.Message_TestDelay{TestDelay: td},
			}); err != nil {
				conn.Close()
				return nil, err
			}
		} else if reason := msg.GetMisc().GetCloseReason(); reason != "" {
			conn.Close()
			return nil, fmt.Errorf("peer closed: %s", reason)
		}
	}
}

// Read reads output from the terminal.
func (ts *TerminalSession) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	for {
		ts.mu.Lock()
		if !ts.deadline.IsZero() && !time.Now().Before(ts.deadline) {
			ts.mu.Unlock()
			return 0, fmt.Errorf("terminal deadline: %w", os.ErrDeadlineExceeded)
		}
		if len(ts.buf) > 0 {
			n := copy(p, ts.buf)
			ts.buf = ts.buf[n:]
			if len(ts.buf) == 0 {
				ts.buf = nil
			}
			ts.mu.Unlock()
			return n, nil
		}
		if ts.closed {
			err := ts.err
			if err == nil {
				err = io.EOF
			}
			ts.mu.Unlock()
			return 0, err
		}
		ts.mu.Unlock()

		msg, err := ts.conn.Recv()
		if err != nil {
			ts.mu.Lock()
			ts.closed = true
			ts.err = fmt.Errorf("terminal disconnected before close response: %w", err)
			if len(ts.buf) > 0 {
				n := copy(p, ts.buf)
				ts.buf = ts.buf[n:]
				if len(ts.buf) == 0 {
					ts.buf = nil
				}
				ts.mu.Unlock()
				return n, nil
			}
			retErr := ts.err
			ts.mu.Unlock()
			return 0, retErr
		}

		if tr := msg.GetTerminalResponse(); tr != nil {
			switch u := tr.GetUnion().(type) {
			case *pb.TerminalResponse_Data:
				if u.Data.GetTerminalId() != ts.termID {
					continue
				}
				data, err := decodeTerminalData(u.Data)
				ts.mu.Lock()
				ts.buf = data
				if err != nil {
					ts.closed = true
					ts.err = err
				}
				ts.mu.Unlock()
			case *pb.TerminalResponse_Closed:
				if u.Closed.GetTerminalId() != ts.termID {
					continue
				}
				ts.mu.Lock()
				ts.closed = true
				ts.err = io.EOF
				if code := u.Closed.GetExitCode(); code != 0 {
					ts.err = &TerminalExitError{Code: int(code)}
				}
				ts.mu.Unlock()
			case *pb.TerminalResponse_Error:
				if id := u.Error.GetTerminalId(); id != ts.termID {
					continue
				}
				ts.mu.Lock()
				ts.closed = true
				errMsg := u.Error.GetMessage()
				if errMsg == "" {
					errMsg = "terminal error"
				}
				ts.err = errors.New(errMsg)
				ts.mu.Unlock()
			}
		} else if td := msg.GetTestDelay(); td != nil {
			if err := ts.conn.Send(&pb.Message{
				Union: &pb.Message_TestDelay{TestDelay: td},
			}); err != nil {
				return 0, fmt.Errorf("terminal heartbeat: %w", err)
			}
		} else if reason := msg.GetMisc().GetCloseReason(); reason != "" {
			return 0, fmt.Errorf("peer closed: %s", reason)
		}
	}
}

// Write sends input to the terminal.
func (ts *TerminalSession) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	ts.mu.Lock()
	if ts.closed {
		ts.mu.Unlock()
		return 0, io.ErrClosedPipe
	}
	termID := ts.termID
	ts.mu.Unlock()

	msg := &pb.Message{
		Union: &pb.Message_TerminalAction{
			TerminalAction: &pb.TerminalAction{
				Union: &pb.TerminalAction_Data{
					Data: &pb.TerminalData{
						TerminalId: termID,
						Data:       p,
					},
				},
			},
		},
	}
	if err := ts.conn.Send(msg); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Resize sends a terminal resize event.
func (ts *TerminalSession) Resize(rows, cols uint32) error {
	ts.mu.Lock()
	if ts.closed {
		ts.mu.Unlock()
		return io.ErrClosedPipe
	}
	ts.rows = rows
	ts.cols = cols
	termID := ts.termID
	ts.mu.Unlock()

	msg := &pb.Message{
		Union: &pb.Message_TerminalAction{
			TerminalAction: &pb.TerminalAction{
				Union: &pb.TerminalAction_Resize{
					Resize: &pb.ResizeTerminal{
						TerminalId: termID,
						Rows:       rows,
						Cols:       cols,
					},
				},
			},
		},
	}
	return ts.conn.Send(msg)
}

// Close closes the terminal session and the underlying connection.
func (ts *TerminalSession) Close() error {
	ts.closeOnce.Do(func() {
		ts.mu.Lock()
		wasClosed := ts.closed
		ts.closed = true
		termID := ts.termID
		ts.mu.Unlock()
		if wasClosed {
			ts.closeErr = ts.conn.Close()
			return
		}

		msg := &pb.Message{
			Union: &pb.Message_TerminalAction{
				TerminalAction: &pb.TerminalAction{
					Union: &pb.TerminalAction_Close{
						Close: &pb.CloseTerminal{
							TerminalId: termID,
						},
					},
				},
			},
		}
		// Bound cleanup even if the peer has stopped reading.
		_ = ts.conn.sc.SetWriteDeadline(time.Now().Add(time.Second))
		ts.closeErr = errors.Join(ts.conn.Send(msg), ts.conn.Close())
	})
	return ts.closeErr
}

func terminalSupported(pi *pb.PeerInfo) bool {
	return pi.GetFeatures().GetTerminal()
}

func decodeTerminalData(td *pb.TerminalData) ([]byte, error) {
	if td == nil {
		return nil, nil
	}
	data := td.GetData()
	if td.GetCompressed() {
		decomp, err := terminalZstdDecoder.DecodeAll(data, nil)
		if err != nil {
			return nil, fmt.Errorf("decode terminal output: %w", err)
		}
		data = decomp
	}
	if len(data) > maxTerminalBufferedOutput {
		return nil, errors.New("terminal output exceeded buffer limit")
	}
	if len(data) == 0 {
		return nil, nil
	}
	// DecodeAll can reserve capacity based on skippable frames rather than output size.
	retained := make([]byte, len(data))
	copy(retained, data)
	return retained, nil
}

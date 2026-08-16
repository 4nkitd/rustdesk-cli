package hbb

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sync"

	pb "rdcli/internal/hbb/proto"
)

type TerminalSession struct {
	conn   *Conn
	termID int32
	rows   uint32
	cols   uint32

	mu     sync.Mutex
	buf    []byte
	closed bool
	err    error
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
		return nil, fmt.Errorf("remote terminal is not supported by the remote side (peer version %s, need 1.4.1 or higher)", pi.GetVersion())
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

	for {
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
				return ts, nil
			case *pb.TerminalResponse_Error:
				conn.Close()
				return nil, fmt.Errorf("terminal error: %s", u.Error.GetMessage())
			case *pb.TerminalResponse_Closed:
				conn.Close()
				return nil, errors.New("terminal closed")
			case *pb.TerminalResponse_Data:
				data := decompressTerminalData(u.Data)
				ts.buf = append(ts.buf, data...)
			}
		} else if td := msg.GetTestDelay(); td != nil {
			_ = conn.Send(&pb.Message{
				Union: &pb.Message_TestDelay{TestDelay: td},
			})
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
		if len(ts.buf) > 0 {
			n := copy(p, ts.buf)
			ts.buf = ts.buf[n:]
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
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				ts.err = io.EOF
			} else {
				ts.err = err
			}
			if len(ts.buf) > 0 {
				n := copy(p, ts.buf)
				ts.buf = ts.buf[n:]
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
				data := decompressTerminalData(u.Data)
				ts.mu.Lock()
				ts.buf = append(ts.buf, data...)
				ts.mu.Unlock()
			case *pb.TerminalResponse_Closed:
				ts.mu.Lock()
				ts.closed = true
				ts.err = io.EOF
				ts.mu.Unlock()
			case *pb.TerminalResponse_Error:
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
			_ = ts.conn.Send(&pb.Message{
				Union: &pb.Message_TestDelay{TestDelay: td},
			})
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
	ts.mu.Lock()
	if ts.closed {
		ts.mu.Unlock()
		return nil
	}
	ts.closed = true
	termID := ts.termID
	ts.mu.Unlock()

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
	_ = ts.conn.Send(msg)
	return ts.conn.Close()
}

func terminalSupported(pi *pb.PeerInfo) bool {
	if pi == nil {
		return false
	}
	if f := pi.GetFeatures(); f != nil && f.GetTerminal() {
		return true
	}
	return versionAtLeast(pi.GetVersion(), "1.4.1")
}

func decompressTerminalData(td *pb.TerminalData) []byte {
	if td == nil {
		return nil
	}
	data := td.GetData()
	if td.GetCompressed() {
		if decomp, err := zstdDec.DecodeAll(data, nil); err == nil {
			data = decomp
		}
	}
	return data
}

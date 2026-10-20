package hbb

import (
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	pb "github.com/4nkitd/rustdesk-cli/internal/hbb/proto"
)

func adversarialFileOperations() []struct {
	name string
	run  func(*FileTransfer) error
	id   func(*pb.FileAction) int32
} {
	return []struct {
		name string
		run  func(*FileTransfer) error
		id   func(*pb.FileAction) int32
	}{
		{"mkdir", func(ft *FileTransfer) error { return ft.Mkdir("/fixture/new") }, func(a *pb.FileAction) int32 { return a.GetCreate().GetId() }},
		{"remove file", func(ft *FileTransfer) error { return ft.RemoveFile("/fixture/file") }, func(a *pb.FileAction) int32 { return a.GetRemoveFile().GetId() }},
		{"remove directory", func(ft *FileTransfer) error { return ft.RemoveEmptyDir("/fixture/empty") }, func(a *pb.FileAction) int32 { return a.GetRemoveDir().GetId() }},
		{"rename", func(ft *FileTransfer) error { return ft.Rename("/fixture/old", "new") }, func(a *pb.FileAction) int32 { return a.GetRename().GetId() }},
	}
}

func TestFileOperationsAdversarialUnmatchedCompletion(t *testing.T) {
	for _, op := range adversarialFileOperations() {
		for _, mismatch := range []struct {
			name    string
			idDelta int32
			fileNum int32
		}{
			{"previous job", -1, 0},
			{"future job", 1, 0},
			{"next file", 0, 1},
			{"negative file", 0, -1},
		} {
			for _, errorReply := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/error=%t", op.name, mismatch.name, errorReply), func(t *testing.T) {
					ft, peer := fileOpsPipe(t, time.Second)
					done := make(chan error, 1)
					go func() {
						defer peer.Close()
						msg, err := readProtoMessage(peer)
						if err != nil {
							done <- err
							return
						}
						id := op.id(msg.GetFileAction())
						if id != 12 {
							done <- fmt.Errorf("unexpected mutation: %v", msg)
							return
						}
						response := &pb.FileResponse{Union: &pb.FileResponse_Done{Done: &pb.FileTransferDone{Id: id + mismatch.idDelta, FileNum: mismatch.fileNum}}}
						if errorReply {
							response = &pb.FileResponse{Union: &pb.FileResponse_Error{Error: &pb.FileTransferError{Id: id + mismatch.idDelta, FileNum: mismatch.fileNum, Error: "unrelated permission denied"}}}
						}
						done <- fileOpsResponse(peer, response)
					}()
					err := op.run(ft)
					if !errors.Is(err, io.EOF) {
						t.Errorf("unmatched reply satisfied operation: got %v, want EOF after ignored reply", err)
					}
					if err := <-done; err != nil {
						t.Fatal(err)
					}
				})
			}
		}
	}
}

func TestFileOperationsAdversarialErrorsWithoutHomeListing(t *testing.T) {
	for _, op := range adversarialFileOperations() {
		for _, reason := range []string{"permission denied", "read-only filesystem", "target does not exist"} {
			t.Run(op.name+"/"+reason, func(t *testing.T) {
				opts, done := setupFileOpsPeer(t, func(peer *SecureConn, login *pb.LoginRequest) error {
					if login.GetFileTransfer() == nil {
						return errors.New("missing file-transfer login")
					}
					msg, err := readProtoMessage(peer)
					if err != nil {
						return err
					}
					id := op.id(msg.GetFileAction())
					if id <= 0 {
						return fmt.Errorf("unexpected mutation: %v", msg)
					}
					return fileOpsResponse(peer, &pb.FileResponse{Union: &pb.FileResponse_Error{Error: &pb.FileTransferError{Id: id, FileNum: 0, Error: reason}}})
				})
				ft, err := NewFileOperations(opts)
				if err != nil {
					t.Fatalf("constructor waited for unrelated home listing: %v", err)
				}
				defer ft.Close()
				if err := op.run(ft); err == nil || err.Error() != reason {
					t.Errorf("mutation error = %v, want %q", err, reason)
				}
				if err := <-done; err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestFileOperationsAdversarialPeerCloseReason(t *testing.T) {
	for _, op := range adversarialFileOperations() {
		t.Run(op.name, func(t *testing.T) {
			ft, peer := fileOpsPipe(t, time.Second)
			done := make(chan error, 1)
			go func() {
				msg, err := readProtoMessage(peer)
				if err != nil {
					done <- err
					return
				}
				if op.id(msg.GetFileAction()) != 12 {
					done <- fmt.Errorf("unexpected mutation: %v", msg)
					return
				}
				done <- writeProtoMessage(peer, &pb.Message{Union: &pb.Message_Misc{Misc: &pb.Misc{Union: &pb.Misc_CloseReason{CloseReason: "file transfer permission revoked"}}}})
			}()
			if err := op.run(ft); err == nil || !strings.Contains(err.Error(), "file transfer permission revoked") {
				t.Errorf("close reason was lost: %v", err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFileOperationsAdversarialUnrelatedFloodDeadline(t *testing.T) {
	for _, op := range adversarialFileOperations() {
		t.Run(op.name, func(t *testing.T) {
			ft, peer := fileOpsPipe(t, 60*time.Millisecond)
			if err := peer.SetDeadline(time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				defer peer.Close()
				msg, err := readProtoMessage(peer)
				if err != nil {
					done <- err
					return
				}
				id := op.id(msg.GetFileAction())
				if id != 12 {
					done <- fmt.Errorf("unexpected mutation: %v", msg)
					return
				}
				responses := []*pb.FileResponse{
					{Union: &pb.FileResponse_Dir{Dir: &pb.FileDirectory{Path: "/home"}}},
					{Union: &pb.FileResponse_Error{Error: &pb.FileTransferError{Id: 0, Error: "home unavailable"}}},
					{Union: &pb.FileResponse_Done{Done: &pb.FileTransferDone{Id: id + 1}}},
					{Union: &pb.FileResponse_Error{Error: &pb.FileTransferError{Id: id, FileNum: 1, Error: "other file denied"}}},
				}
				for sent := 0; ; sent++ {
					if err := fileOpsResponse(peer, responses[sent%len(responses)]); err != nil {
						if sent < len(responses) {
							done <- fmt.Errorf("flood stopped after %d replies: %w", sent, err)
						} else {
							done <- nil
						}
						return
					}
				}
			}()
			started := time.Now()
			err := op.run(ft)
			elapsed := time.Since(started)
			ft.Close()
			var netErr net.Error
			if !errors.As(err, &netErr) || !netErr.Timeout() {
				t.Errorf("flood result = %v, want deadline error", err)
			}
			if elapsed > 500*time.Millisecond {
				t.Errorf("unrelated replies extended 60ms operation deadline to %v", elapsed)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFileOperationsAdversarialOldPeerRenameBeforeMutation(t *testing.T) {
	for _, version := range []string{"", "1.1.9", "1.2.7", "invalid"} {
		t.Run(version, func(t *testing.T) {
			ft, peer := fileOpsPipe(t, 50*time.Millisecond)
			ft.conn.peerInfo.Version = version
			if err := ft.Rename("/fixture/old", "new"); err == nil || !strings.Contains(err.Error(), "requires RustDesk 1.3.0") {
				t.Fatalf("expected version rejection before network I/O, got %v", err)
			}
			if ft.nextID != 12 {
				t.Fatalf("rejected rename consumed job ID: next = %d, want 12", ft.nextID)
			}
			if err := peer.SetReadDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
				t.Fatal(err)
			}
			msg, err := readProtoMessage(peer)
			var netErr net.Error
			if !errors.As(err, &netErr) || !netErr.Timeout() {
				t.Fatalf("old peer received mutation: %v, %v", msg, err)
			}
		})
	}
}

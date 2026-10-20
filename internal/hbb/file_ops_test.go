package hbb

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	pb "github.com/4nkitd/rustdesk-cli/internal/hbb/proto"
)

func fileOpsPipe(t *testing.T, timeout time.Duration) (*FileTransfer, *SecureConn) {
	t.Helper()
	client, peer := net.Pipe()
	sc, remote := NewSecureConn(client), NewSecureConn(peer)
	key := [32]byte{1, 2, 3, 4}
	sc.SetKey(key)
	remote.SetKey(key)
	t.Cleanup(func() { sc.Close(); remote.Close() })
	remote.SetDeadline(time.Now().Add(2 * time.Second))
	return &FileTransfer{conn: &Conn{sc: sc, opts: DialOpts{Timeout: timeout}, peerInfo: &pb.PeerInfo{Version: "1.3.0"}}, nextID: 12}, remote
}

func fileOpsResponse(sc *SecureConn, response *pb.FileResponse) error {
	return writeProtoMessage(sc, &pb.Message{Union: &pb.Message_FileResponse{FileResponse: response}})
}

func TestFileOperationsEncryptedMatching(t *testing.T) {
	tests := []struct {
		name  string
		run   func(*FileTransfer) error
		check func(*testing.T, *pb.FileAction) int32
	}{
		{"mkdir", func(ft *FileTransfer) error { return ft.Mkdir("/work/new/nested") }, func(t *testing.T, a *pb.FileAction) int32 {
			v := a.GetCreate()
			if v == nil || v.GetPath() != "/work/new/nested" {
				t.Errorf("unexpected create: %v", a)
			}
			return v.GetId()
		}},
		{"file", func(ft *FileTransfer) error { return ft.RemoveFile("/work/old") }, func(t *testing.T, a *pb.FileAction) int32 {
			v := a.GetRemoveFile()
			if v == nil || v.GetPath() != "/work/old" || v.GetFileNum() != 0 {
				t.Errorf("unexpected remove: %v", a)
			}
			return v.GetId()
		}},
		{"directory", func(ft *FileTransfer) error { return ft.RemoveEmptyDir("/work/empty") }, func(t *testing.T, a *pb.FileAction) int32 {
			v := a.GetRemoveDir()
			if v == nil || v.GetPath() != "/work/empty" || v.GetRecursive() {
				t.Errorf("unexpected remove dir: %v", a)
			}
			return v.GetId()
		}},
		{"rename", func(ft *FileTransfer) error { return ft.Rename(`C:\work\old`, "new") }, func(t *testing.T, a *pb.FileAction) int32 {
			v := a.GetRename()
			if v == nil || v.GetPath() != `C:\work\old` || v.GetNewName() != "new" {
				t.Errorf("unexpected rename: %v", a)
			}
			return v.GetId()
		}},
	}
	for _, tt := range tests {
		for _, denied := range []bool{false, true} {
			name := tt.name + "/success"
			if denied {
				name = tt.name + "/error"
			}
			t.Run(name, func(t *testing.T) {
				ft, peer := fileOpsPipe(t, time.Second)
				done := make(chan error, 1)
				go func() {
					msg, err := readProtoMessage(peer)
					if err != nil {
						done <- err
						return
					}
					id := tt.check(t, msg.GetFileAction())
					if id != 12 {
						t.Errorf("id = %d, want 12", id)
					}
					responses := []*pb.FileResponse{
						{Union: &pb.FileResponse_Dir{Dir: &pb.FileDirectory{Path: "/home"}}},
						{Union: &pb.FileResponse_Error{Error: &pb.FileTransferError{Id: 0, Error: "home unavailable"}}},
						{Union: &pb.FileResponse_Done{Done: &pb.FileTransferDone{Id: id + 1}}},
						{Union: &pb.FileResponse_Error{Error: &pb.FileTransferError{Id: id + 1, Error: "unrelated job"}}},
						{Union: &pb.FileResponse_Done{Done: &pb.FileTransferDone{Id: id, FileNum: 1}}},
						{Union: &pb.FileResponse_Error{Error: &pb.FileTransferError{Id: id, FileNum: 1, Error: "unrelated file"}}},
					}
					if denied {
						responses = append(responses, &pb.FileResponse{Union: &pb.FileResponse_Error{Error: &pb.FileTransferError{Id: id, Error: "permission denied"}}})
					} else {
						responses = append(responses, &pb.FileResponse{Union: &pb.FileResponse_Done{Done: &pb.FileTransferDone{Id: id}}})
					}
					for _, response := range responses {
						if err := fileOpsResponse(peer, response); err != nil {
							done <- err
							return
						}
					}
					done <- nil
				}()
				err := tt.run(ft)
				if denied && (err == nil || err.Error() != "permission denied") {
					t.Fatalf("got %v, want permission denied", err)
				}
				if !denied && err != nil {
					t.Fatal(err)
				}
				if err := <-done; err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestFileOperationsRenameRejectsUnsupportedPeer(t *testing.T) {
	for _, version := range []string{"", "1.2.7", "1.2.6", "invalid"} {
		t.Run(version, func(t *testing.T) {
			ft, peer := fileOpsPipe(t, time.Second)
			ft.conn.peerInfo.Version = version
			err := ft.Rename("/work/file", "new")
			if err == nil || !strings.Contains(err.Error(), "requires RustDesk 1.3.0") {
				t.Fatalf("expected version gate, got %v", err)
			}
			if err := peer.SetReadDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
				t.Fatal(err)
			}
			_, err = peer.ReadMessage()
			var netErr net.Error
			if !errors.As(err, &netErr) || !netErr.Timeout() {
				t.Fatalf("unsupported peer received a request: %v", err)
			}
		})
	}
}

func TestFileOperationsReadDirHiddenEncrypted(t *testing.T) {
	for _, tc := range []struct {
		name         string
		hidden       bool
		responsePath string
		remoteError  string
		absent       bool
	}{
		{name: "visible", responsePath: "/tmp"},
		{name: "all", hidden: true, responsePath: "/tmp"},
		{name: "home fallback", hidden: true, responsePath: "/home"},
		{name: "target error", remoteError: "target unavailable"},
		{name: "target absent", absent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts, done := setupFileOpsPeer(t, func(sc *SecureConn, login *pb.LoginRequest) error {
				ft := login.GetFileTransfer()
				if ft == nil || ft.GetDir() != "/tmp" || ft.GetShowHidden() != tc.hidden {
					return fmt.Errorf("unexpected file-transfer login: %v", login)
				}
				if !tc.absent {
					if err := fileOpsResponse(sc, &pb.FileResponse{Union: &pb.FileResponse_Dir{Dir: &pb.FileDirectory{Id: 99, Path: "/unrelated"}}}); err != nil {
						return err
					}
					var response *pb.FileResponse
					if tc.remoteError != "" {
						response = &pb.FileResponse{Union: &pb.FileResponse_Error{Error: &pb.FileTransferError{Error: tc.remoteError}}}
					} else {
						entries := []*pb.FileEntry{{Name: "visible"}}
						if tc.hidden {
							entries = append(entries, &pb.FileEntry{Name: ".hidden", IsHidden: true})
						}
						response = &pb.FileResponse{Union: &pb.FileResponse_Dir{Dir: &pb.FileDirectory{Path: tc.responsePath, Entries: entries}}}
					}
					if err := fileOpsResponse(sc, response); err != nil {
						return err
					}
				}
				msg, err := readProtoMessage(sc)
				if !errors.Is(err, io.EOF) {
					return fmt.Errorf("expected close without ReadDir action, got %v: %v", msg, err)
				}
				return nil
			})
			dir, err := ReadDirWithHidden(opts, "/tmp", tc.hidden)
			switch {
			case tc.absent:
				var netErr net.Error
				if !errors.As(err, &netErr) || !netErr.Timeout() {
					t.Fatalf("expected timeout, got %v", err)
				}
			case tc.remoteError != "":
				if err == nil || err.Error() != tc.remoteError {
					t.Fatalf("expected target error, got %v", err)
				}
			case tc.responsePath != "/tmp":
				if err == nil || dir != nil {
					t.Fatalf("returned unrelated home listing: %v, %v", dir, err)
				}
			default:
				if err != nil {
					t.Fatal(err)
				}
				want := 1
				if tc.hidden {
					want = 2
				}
				if dir.GetPath() != "/tmp" || len(dir.GetEntries()) != want {
					t.Fatalf("unexpected directory: %v", dir)
				}
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFileOperationsDoNotDependOnHomeListing(t *testing.T) {
	for _, startup := range []string{"absent", "error", "directory"} {
		t.Run(startup, func(t *testing.T) {
			opts, done := setupFileOpsPeer(t, func(sc *SecureConn, login *pb.LoginRequest) error {
				if login.GetFileTransfer() == nil {
					return errors.New("missing file-transfer login")
				}
				switch startup {
				case "error":
					if err := fileOpsResponse(sc, &pb.FileResponse{Union: &pb.FileResponse_Error{Error: &pb.FileTransferError{Error: "home unavailable"}}}); err != nil {
						return err
					}
				case "directory":
					if err := fileOpsResponse(sc, &pb.FileResponse{Union: &pb.FileResponse_Dir{Dir: &pb.FileDirectory{Path: "/home"}}}); err != nil {
						return err
					}
				}
				msg, err := readProtoMessage(sc)
				if err != nil {
					return err
				}
				create := msg.GetFileAction().GetCreate()
				if create == nil || create.GetId() <= 0 || create.GetPath() != "/tmp/new" {
					return fmt.Errorf("unexpected mutation: %v", msg)
				}
				return fileOpsResponse(sc, &pb.FileResponse{Union: &pb.FileResponse_Done{Done: &pb.FileTransferDone{Id: create.GetId()}}})
			})
			ft, err := NewFileOperations(opts)
			if err != nil {
				t.Fatal(err)
			}
			defer ft.Close()
			if err := ft.Mkdir("/tmp/new"); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func setupFileOpsPeer(t *testing.T, handler func(*SecureConn, *pb.LoginRequest) error) (DialOpts, <-chan error) {
	t.Helper()
	return setupFileOpsPeerPlatform(t, "Linux", handler)
}

func setupFileOpsPeerPlatform(t *testing.T, platform string, handler func(*SecureConn, *pb.LoginRequest) error) (DialOpts, <-chan error) {
	t.Helper()
	serverPub, serverPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	peerPub, peerPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	done := make(chan error, 1)
	go func() {
		done <- func() error {
			conn, err := listener.Accept()
			if err != nil {
				return err
			}
			defer conn.Close()
			if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
				return err
			}
			sc := NewSecureConn(conn)
			kp, err := GenerateKeyPair()
			if err != nil {
				return err
			}
			sid := mustSignIDPk(t, peerPriv, "test-peer", kp.Pub)
			if err := writeProtoMessage(sc, &pb.Message{Union: &pb.Message_SignedId{SignedId: &pb.SignedId{Id: sid}}}); err != nil {
				return err
			}
			msg, err := readProtoMessage(sc)
			if err != nil {
				return err
			}
			pk := msg.GetPublicKey()
			if pk == nil {
				return errors.New("missing public key")
			}
			key, err := DecryptSymmetricKey(pk.GetSymmetricValue(), pk.GetAsymmetricValue(), kp.Priv)
			if err != nil {
				return err
			}
			sc.SetKey(key)
			if err := writeProtoMessage(sc, &pb.Message{Union: &pb.Message_Hash{Hash: &pb.Hash{Salt: "mock", Challenge: "fileops"}}}); err != nil {
				return err
			}
			msg, err = readProtoMessage(sc)
			if err != nil {
				return err
			}
			login := msg.GetLoginRequest()
			if login == nil {
				return errors.New("missing login")
			}
			if err := writeProtoMessage(sc, &pb.Message{Union: &pb.Message_LoginResponse{LoginResponse: &pb.LoginResponse{Union: &pb.LoginResponse_PeerInfo{PeerInfo: &pb.PeerInfo{Version: "1.4.1", Platform: platform}}}}}); err != nil {
				return err
			}
			return handler(sc, login)
		}()
	}()
	server := runMockRendezvous(t, serverPriv, "test-peer", listener.Addr().(*net.TCPAddr).AddrPort(), peerPub)
	return DialOpts{Server: server, PeerID: "test-peer", Key: base64.StdEncoding.EncodeToString(serverPub), Timeout: 300 * time.Millisecond}, done
}

func TestFileOperationsBoundedIO(t *testing.T) {
	for _, readRequest := range []bool{false, true} {
		t.Run(map[bool]string{false: "blocked write", true: "blocked read"}[readRequest], func(t *testing.T) {
			ft, peer := fileOpsPipe(t, 30*time.Millisecond)
			done := make(chan error, 1)
			if readRequest {
				go func() { _, err := readProtoMessage(peer); done <- err }()
			}
			err := ft.Mkdir("/work/new")
			var netErr net.Error
			if !errors.As(err, &netErr) || !netErr.Timeout() {
				t.Fatalf("expected timeout, got %v", err)
			}
			if readRequest {
				if err := <-done; err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestFileOperationsKeepaliveWriteBounded(t *testing.T) {
	for _, operation := range []string{"mutation", "listing"} {
		t.Run(operation, func(t *testing.T) {
			ft, peer := fileOpsPipe(t, 50*time.Millisecond)
			peerDone := make(chan error, 1)
			go func() {
				if operation == "mutation" {
					if _, err := readProtoMessage(peer); err != nil {
						peerDone <- err
						return
					}
				}
				peerDone <- writeProtoMessage(peer, &pb.Message{Union: &pb.Message_TestDelay{TestDelay: &pb.TestDelay{Time: 1}}})
			}()
			done := make(chan error, 1)
			go func() {
				if operation == "mutation" {
					done <- ft.Mkdir("/work/new")
					return
				}
				if err := ft.beginFileOperation(); err != nil {
					done <- err
					return
				}
				_, err := ft.waitFileDirectory()
				done <- err
			}()
			select {
			case err := <-done:
				var netErr net.Error
				if !errors.As(err, &netErr) || !netErr.Timeout() {
					t.Fatalf("expected keepalive write timeout, got %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("keepalive write exceeded the operation deadline")
			}
			if err := <-peerDone; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFileOperationsListingPathIdentity(t *testing.T) {
	for _, tc := range []struct {
		name      string
		platform  string
		requested string
		returned  string
		match     bool
	}{
		{"missing parent fallback", "Linux", "/home/mock/missing/..", "/home/mock", false},
		{"symlink parent ambiguity", "Linux", "/home/mock/link/..", "/home/mock", false},
		{"parent retained", "Linux", "/home/mock/exists/..", "/home/mock/exists/..", true},
		{"POSIX literal backslash", "Linux", `/home/mock/a\b`, `/home/mock/a\b`, true},
		{"POSIX backslash is not separator", "Linux", `/home/mock/a\b`, "/home/mock/a/b", false},
		{"POSIX case", "Linux", "/home/mock/Case", "/home/mock/case", false},
		{"unknown platform", "", `C:\Work`, "C:/Work", false},
		{"Windows slashes and drive case", "Windows", `C:\Work\Folder`, "c:/Work/Folder", true},
		{"Windows directory case ambiguity", "Windows", `C:\Work\Case`, `c:\Work\case`, false},
		{"Windows missing parent fallback", "Windows", `C:\Work\missing\..`, "c:/Work", false},
		{"Windows verbatim path", "Windows", `\\?\C:\Work`, `\\?\C:\Work`, true},
		{"Windows verbatim slashes", "Windows", `\\?\C:\Work`, "//?/C:/Work", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts, done := setupFileOpsPeerPlatform(t, tc.platform, func(sc *SecureConn, login *pb.LoginRequest) error {
				if login.GetFileTransfer().GetDir() != tc.requested {
					return fmt.Errorf("requested path changed in login: %v", login)
				}
				return fileOpsResponse(sc, &pb.FileResponse{Union: &pb.FileResponse_Dir{Dir: &pb.FileDirectory{Path: tc.returned}}})
			})
			dir, err := ReadDirWithHidden(opts, tc.requested, true)
			if tc.match {
				if err != nil || dir.GetPath() != tc.returned {
					t.Fatalf("rejected matching directory: %v, %v", dir, err)
				}
			} else if err == nil || dir != nil || !strings.Contains(err.Error(), "but peer returned") {
				t.Fatalf("accepted ambiguous or fallback directory: %v, %v", dir, err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFileOperationsRejectDangerousPaths(t *testing.T) {
	for _, p := range []string{"", " ", "/", "//", "///", ".", "..", "../", "a/..", "/a/..", "/a/.", "C:", `C:\`, `/C:/`, `C:\a\..`, `C:\...`, `C:\. `, `\\server\share`, `\\server\share\..`, `\\?\C:\file`, `\\.\C:\file`, "file\x00name"} {
		t.Run(p, func(t *testing.T) {
			if err := ValidateRemoteMutationPath(p); err == nil {
				t.Fatalf("accepted %q", p)
			}
			ft := &FileTransfer{}
			for _, err := range []error{ft.Mkdir(p), ft.RemoveFile(p), ft.RemoveEmptyDir(p), ft.Rename(p, "safe")} {
				if err == nil {
					t.Fatalf("mutation accepted %q", p)
				}
			}
		})
	}
	for _, p := range []string{"file", ".hidden", "/work/file", `C:\work\file`, `\\server\share\file`, "/work/empty/"} {
		if err := ValidateRemoteMutationPath(p); err != nil {
			t.Errorf("rejected %q: %v", p, err)
		}
	}
	for _, name := range []string{"", ".", "..", "...", ". ", "/tmp/new", `C:\new`, "nested/new", `nested\new`, "a:b", "a\x00b"} {
		if err := (&FileTransfer{}).Rename("/work/file", name); err == nil {
			t.Errorf("rename accepted %q", name)
		}
	}
}

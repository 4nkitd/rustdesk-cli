package hbb

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/4nkitd/rustdesk-cli/internal/hbb/proto"
)

type uploadContainmentConn struct {
	net.Conn
	input   *bytes.Reader
	written bytes.Buffer
}

func (c *uploadContainmentConn) Write(p []byte) (int, error)      { return c.written.Write(p) }
func (c *uploadContainmentConn) SetReadDeadline(time.Time) error  { return nil }
func (c *uploadContainmentConn) SetWriteDeadline(time.Time) error { return nil }
func (c *uploadContainmentConn) SetDeadline(time.Time) error      { return nil }
func (c *uploadContainmentConn) Read(p []byte) (int, error) {
	if c.input == nil {
		return 0, io.EOF
	}
	return c.input.Read(p)
}

func uploadContainmentSession(input []byte) (*FileTransfer, *uploadContainmentConn) {
	wire := &uploadContainmentConn{input: bytes.NewReader(input)}
	return &FileTransfer{conn: &Conn{sc: NewSecureConn(wire)}, nextID: 1}, wire
}

func uploadContainmentFrame(t *testing.T, response *pb.FileResponse) []byte {
	t.Helper()
	data, err := proto.Marshal(&pb.Message{Union: &pb.Message_FileResponse{FileResponse: response}})
	if err != nil {
		t.Fatal(err)
	}
	return Encode(data)
}

func uploadContainmentBlocks(t *testing.T, wire *uploadContainmentConn) []byte {
	t.Helper()
	data := wire.written.Bytes()
	var contents []byte
	for len(data) > 0 {
		frame, ok := DecodeFrame(&data)
		if !ok {
			t.Fatal("truncated outgoing frame")
		}
		msg := &pb.Message{}
		if err := proto.Unmarshal(frame, msg); err != nil {
			t.Fatal(err)
		}
		if block := msg.GetFileResponse().GetBlock(); block != nil {
			blockData := block.GetData()
			if block.GetCompressed() {
				var err error
				blockData, err = zstdDec.DecodeAll(blockData, nil)
				if err != nil {
					t.Fatal(err)
				}
			}
			contents = append(contents, blockData...)
		}
	}
	return contents
}

func uploadContainmentWrite(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	stamp := time.Unix(1700000000, 123000000)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
}

func uploadContainmentJobs(t *testing.T, ft *FileTransfer, path string, recursive bool) []job {
	t.Helper()
	jobs, err := ft.buildSendJobs(path, "/remote", recursive)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) == 0 {
		t.Fatal("empty manifest")
	}
	t.Cleanup(func() { jobs[0].source.root.Close() })
	return jobs
}

func TestUploadContainmentAncestorReplacement(t *testing.T) {
	for _, replacement := range []string{"outside-symlink", "new-directory", "inside-symlink"} {
		t.Run(replacement, func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			selected := filepath.Join(root, "sub", "file.bin")
			escaped := filepath.Join(outside, "file.bin")
			uploadContainmentWrite(t, selected, "inside!")
			uploadContainmentWrite(t, escaped, "SECRET!")
			ft, wire := uploadContainmentSession(nil)
			jobs := uploadContainmentJobs(t, ft, root, true)
			if len(jobs) != 2 {
				t.Fatalf("jobs=%d", len(jobs))
			}
			outsideInfo, err := os.Stat(escaped)
			if err != nil {
				t.Fatal(err)
			}
			if outsideInfo.Size() != jobs[1].identities[0].Size() || !outsideInfo.ModTime().Equal(jobs[1].identities[0].ModTime()) {
				t.Fatal("attack fixture must preserve leaf size and mtime")
			}
			if err := os.Rename(filepath.Join(root, "sub"), filepath.Join(root, "saved")); err != nil {
				t.Fatal(err)
			}
			switch replacement {
			case "outside-symlink":
				err = os.Symlink(outside, filepath.Join(root, "sub"))
			case "inside-symlink":
				err = os.Symlink("saved", filepath.Join(root, "sub"))
			case "new-directory":
				uploadContainmentWrite(t, selected, "SECRET!")
			}
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = ft.sendJob(jobs[1], nil, 0, 1, 0, 7)
			if err == nil || !strings.Contains(err.Error(), "upload directory") {
				t.Fatalf("expected ancestor-change rejection, got %v", err)
			}
			if got := uploadContainmentBlocks(t, wire); len(got) != 0 {
				t.Fatalf("sent bytes after ancestor replacement: %q", got)
			}
		})
	}
}

func TestUploadContainmentLeafIdentity(t *testing.T) {
	for _, replacement := range []string{"file", "symlink"} {
		t.Run(replacement, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "file.bin")
			uploadContainmentWrite(t, path, "inside!")
			ft, wire := uploadContainmentSession(nil)
			jobs := uploadContainmentJobs(t, ft, path, false)
			if err := os.Rename(path, filepath.Join(root, "saved.bin")); err != nil {
				t.Fatal(err)
			}
			if replacement == "symlink" {
				if err := os.Symlink("saved.bin", path); err != nil {
					t.Fatal(err)
				}
			} else {
				uploadContainmentWrite(t, path, "SECRET!")
			}
			_, _, err := ft.sendJob(jobs[0], nil, 0, 1, 0, 7)
			if err == nil || !strings.Contains(err.Error(), "source changed") {
				t.Fatalf("expected identity rejection, got %v", err)
			}
			if got := uploadContainmentBlocks(t, wire); len(got) != 0 {
				t.Fatalf("sent substituted leaf: %q", got)
			}
		})
	}
}

func TestUploadContainmentRootRemainsPinned(t *testing.T) {
	base, outside := t.TempDir(), t.TempDir()
	root := filepath.Join(base, "selected")
	uploadContainmentWrite(t, filepath.Join(root, "file.bin"), "inside!")
	uploadContainmentWrite(t, filepath.Join(outside, "file.bin"), "SECRET!")
	ack := uploadContainmentFrame(t, &pb.FileResponse{Union: &pb.FileResponse_Done{Done: &pb.FileTransferDone{Id: 1, FileNum: 1}}})
	ft, wire := uploadContainmentSession(ack)
	jobs := uploadContainmentJobs(t, ft, root, true)
	if err := os.Rename(root, filepath.Join(base, "moved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, root); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ft.sendJob(jobs[0], nil, 0, 1, 0, 7); err != nil {
		t.Fatal(err)
	}
	if got := string(uploadContainmentBlocks(t, wire)); got != "inside!" {
		t.Fatalf("pinned root sent %q", got)
	}
}

func TestUploadContainmentExistingSymlinkPolicy(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	uploadContainmentWrite(t, filepath.Join(real, "file.bin"), "inside!")
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	ft := &FileTransfer{}
	jobs := uploadContainmentJobs(t, ft, filepath.Join(alias, "file.bin"), false)
	if len(jobs[0].files) != 1 {
		t.Fatal("symlinked ancestor should still work")
	}
	if _, err := ft.buildSendJobs(alias, "/remote", true); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("selected symlink should have an explicit error, got %v", err)
	}
	if err := os.Symlink("file.bin", filepath.Join(real, "link")); err != nil {
		t.Fatal(err)
	}
	jobs = uploadContainmentJobs(t, ft, real, true)
	if len(jobs[0].files) != 1 || jobs[0].files[0].GetName() != "file.bin" {
		t.Fatal("recursive symlink policy changed")
	}
}

func TestUploadContainmentManifestLimits(t *testing.T) {
	root := t.TempDir()
	uploadContainmentWrite(t, filepath.Join(root, "a.bin"), "a")
	uploadContainmentWrite(t, filepath.Join(root, "b.bin"), "b")
	ft := &FileTransfer{}
	jobs := uploadContainmentJobs(t, ft, root, true)
	for _, budget := range []transferListBudget{
		{entries: maxTransferListEntries - 1},
		{pathBytes: maxTransferListPathBytes - 1},
	} {
		if _, _, _, err := jobs[0].source.readDirectory(".", true, &budget); err == nil {
			t.Fatal("unbounded directory scan")
		}
	}
	if _, err := ft.buildSendJobs(root, strings.Repeat("r", maxTransferPathBytes+1), true); err == nil {
		t.Fatal("accepted oversized remote path")
	}
	deep := root
	for i := 0; i <= maxTransferDepth; i++ {
		deep = filepath.Join(deep, "d")
	}
	if err := os.MkdirAll(deep, 0o700); err != nil {
		t.Fatal(err)
	}
	if jobs, err := ft.buildSendJobs(root, "/remote", true); err == nil {
		jobs[0].source.root.Close()
		t.Fatal("accepted excessive upload depth")
	}
}

func TestUploadContainmentPeerListLimits(t *testing.T) {
	chunk := func(entries []*pb.FileEntry) []byte {
		return uploadContainmentFrame(t, &pb.FileResponse{Union: &pb.FileResponse_Dir{Dir: &pb.FileDirectory{Entries: entries}}})
	}
	entries := make([]*pb.FileEntry, 4096)
	for i := range entries {
		entries[i] = &pb.FileEntry{Name: "x"}
	}
	var wire []byte
	for remaining := maxTransferListEntries; remaining > 0; {
		n := min(remaining, len(entries))
		wire = append(wire, chunk(entries[:n])...)
		remaining -= n
	}
	done := uploadContainmentFrame(t, &pb.FileResponse{Union: &pb.FileResponse_Done{Done: &pb.FileTransferDone{}}})
	ft, _ := uploadContainmentSession(append(wire, done...))
	if got, err := ft.readAllFilesList(); err != nil || len(got) != maxTransferListEntries {
		t.Fatalf("valid chunks at aggregate boundary: entries=%d, error=%v", len(got), err)
	}
	ft, _ = uploadContainmentSession(append(wire, chunk(entries[:1])...))
	if _, err := ft.readAllFilesList(); err == nil || !strings.Contains(err.Error(), "entry limit") {
		t.Fatalf("accumulated peer chunks were not bounded: %v", err)
	}
	for _, name := range []string{strings.Repeat("x", maxTransferPathBytes+1), strings.Repeat("a/", maxTransferDepth) + "a", strings.Repeat("a\\", maxTransferDepth) + "a"} {
		ft, _ := uploadContainmentSession(chunk([]*pb.FileEntry{{Name: name}}))
		if _, err := ft.ReadDir("/remote"); err == nil {
			t.Fatal("accepted oversized/deep peer path")
		}
	}
	budget := transferListBudget{pathBytes: maxTransferListPathBytes - 3}
	if err := budget.addDirectory(&pb.FileDirectory{Entries: []*pb.FileEntry{{Name: "aa"}}}); err != nil {
		t.Fatal(err)
	}
	if err := budget.addDirectory(&pb.FileDirectory{Entries: []*pb.FileEntry{{Name: "bb"}}}); err == nil {
		t.Fatal("peer chunk path bytes did not accumulate")
	}
}

func uploadContainmentEncryptedPair(t *testing.T, timeout time.Duration) (*FileTransfer, *SecureConn) {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	client, err := net.DialTimeout("tcp4", ln.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	server, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })
	key := [32]byte{1, 2, 3}
	clientSC, serverSC := NewSecureConn(client), NewSecureConn(server)
	clientSC.SetKey(key)
	serverSC.SetKey(key)
	return &FileTransfer{conn: &Conn{sc: clientSC, opts: DialOpts{Timeout: timeout}}, nextID: 1}, serverSC
}

func TestUploadContainmentSilentEncryptedPeerTimesOut(t *testing.T) {
	for _, phase := range []string{"completion", "digest"} {
		t.Run(phase, func(t *testing.T) {
			ft, peer := uploadContainmentEncryptedPair(t, 100*time.Millisecond)
			ft.overwriteEnabled = phase == "digest"
			path := filepath.Join(t.TempDir(), "file.bin")
			uploadContainmentWrite(t, path, "inside!")
			jobs := uploadContainmentJobs(t, ft, path, false)
			peerReachedPhase := make(chan error, 1)
			release := make(chan struct{})
			defer close(release)
			go func() {
				for {
					msg, err := readProtoMessage(peer)
					if err != nil {
						peerReachedPhase <- err
						return
					}
					fr := msg.GetFileResponse()
					if phase == "digest" && fr.GetDigest() != nil || phase == "completion" && fr.GetDone() != nil {
						peerReachedPhase <- nil
						<-release
						return
					}
				}
			}()
			started := time.Now()
			_, _, err := ft.sendJob(jobs[0], nil, 0, 1, 0, 7)
			var netErr net.Error
			if !errors.As(err, &netErr) || !netErr.Timeout() {
				t.Fatalf("connected silent peer did not time out: %v", err)
			}
			if !strings.Contains(err.Error(), map[string]string{"completion": "not acknowledged", "digest": "digest not received"}[phase]) {
				t.Fatalf("unclear phase error: %v", err)
			}
			if elapsed := time.Since(started); elapsed > time.Second {
				t.Fatalf("phase timeout exceeded budget: %v", elapsed)
			}
			select {
			case err := <-peerReachedPhase:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("peer never received the phase request")
			}
		})
	}
}

func TestUploadContainmentActiveStreamOutlivesOperationTimeout(t *testing.T) {
	const timeout = 200 * time.Millisecond
	ft, peer := uploadContainmentEncryptedPair(t, timeout)
	path := filepath.Join(t.TempDir(), "file.bin")
	content := strings.Repeat("x", fileBlockSize*3)
	uploadContainmentWrite(t, path, content)
	jobs := uploadContainmentJobs(t, ft, path, false)
	peerDone := make(chan error, 1)
	go func() {
		blocks := 0
		for {
			msg, err := readProtoMessage(peer)
			if err != nil {
				peerDone <- err
				return
			}
			fr := msg.GetFileResponse()
			if fr.GetBlock() != nil {
				blocks++
			}
			if done := fr.GetDone(); done != nil {
				if blocks != 3 || done.GetFileNum() != 1 {
					peerDone <- fmt.Errorf("got %d blocks and completion file number %d", blocks, done.GetFileNum())
					return
				}
				peerDone <- writeProtoMessage(peer, msg)
				return
			}
		}
	}()
	started := time.Now()
	_, sent, err := ft.sendJob(jobs[0], func(int, int, int64, int64) {
		time.Sleep(timeout / 2)
	}, 0, 1, 0, int64(len(content)))
	if err != nil || sent != int64(len(content)) {
		t.Fatalf("active upload failed: sent=%d, err=%v", sent, err)
	}
	if time.Since(started) <= timeout {
		t.Fatal("test did not exceed operation timeout")
	}
	select {
	case err := <-peerDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("peer did not finish acknowledging upload")
	}
}

func TestUploadDirectoryOperationsAbsoluteTimeout(t *testing.T) {
	for _, operation := range []string{"read", "create"} {
		for _, traffic := range []string{"silent", "irrelevant", "keepalive", "wrong-job"} {
			if operation == "read" && traffic == "wrong-job" {
				continue
			}
			t.Run(operation+"/"+traffic, func(t *testing.T) {
				ft, peer := uploadContainmentEncryptedPair(t, 100*time.Millisecond)
				stop := make(chan struct{})
				peerDone := make(chan struct{})
				requestRead := make(chan error, 1)
				go func() {
					defer close(peerDone)
					_, err := readProtoMessage(peer)
					requestRead <- err
					if err != nil {
						return
					}
					if traffic == "silent" {
						<-stop
						return
					}
					msg := &pb.Message{Union: &pb.Message_Misc{Misc: &pb.Misc{}}}
					if traffic == "keepalive" {
						msg = &pb.Message{Union: &pb.Message_TestDelay{TestDelay: &pb.TestDelay{}}}
					}
					ticker := time.NewTicker(10 * time.Millisecond)
					defer ticker.Stop()
					count := 0
					for {
						select {
						case <-stop:
							return
						case <-ticker.C:
							if traffic == "wrong-job" {
								fr := &pb.FileResponse{Union: &pb.FileResponse_Done{Done: &pb.FileTransferDone{Id: 99}}}
								if count%2 == 1 {
									fr = &pb.FileResponse{Union: &pb.FileResponse_Error{Error: &pb.FileTransferError{Id: 99, Error: "unrelated operation"}}}
								}
								msg = &pb.Message{Union: &pb.Message_FileResponse{FileResponse: fr}}
							}
							if err := writeProtoMessage(peer, msg); err != nil {
								return
							}
							count++
						}
					}
				}()
				defer func() {
					close(stop)
					peer.Close()
					<-peerDone
				}()
				done := make(chan error, 1)
				go func() {
					if operation == "read" {
						_, err := ft.ReadDir("/remote")
						done <- err
					} else {
						done <- ft.CreateDir("/remote/new")
					}
				}()
				select {
				case err := <-done:
					var netErr net.Error
					if !errors.As(err, &netErr) || !netErr.Timeout() {
						t.Fatalf("expected absolute operation timeout, got %v", err)
					}
				case <-time.After(time.Second):
					t.Fatal("directory operation deadline was missing or renewed")
				}
				select {
				case err := <-requestRead:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(time.Second):
					t.Fatal("encrypted peer did not receive the request")
				}
			})
		}
	}
}

type uploadOperationDeadlineConn struct {
	net.Conn
	first    time.Time
	last     time.Time
	extended bool
}

func (c *uploadOperationDeadlineConn) record(t time.Time) {
	if c.first.IsZero() && !t.IsZero() {
		c.first = t
	}
	if t.After(c.first) {
		c.extended = true
	}
	c.last = t
}

func (c *uploadOperationDeadlineConn) SetDeadline(t time.Time) error {
	c.record(t)
	return c.Conn.SetDeadline(t)
}

func (c *uploadOperationDeadlineConn) SetWriteDeadline(t time.Time) error {
	c.record(t)
	return c.Conn.SetWriteDeadline(t)
}

func (c *uploadOperationDeadlineConn) SetReadDeadline(t time.Time) error {
	c.record(t)
	return c.Conn.SetReadDeadline(t)
}

func TestUploadDirectoryKeepaliveWriteUsesOperationDeadline(t *testing.T) {
	for _, operation := range []string{"read", "create"} {
		t.Run(operation, func(t *testing.T) {
			a, b := net.Pipe()
			defer a.Close()
			defer b.Close()
			tracked := &uploadOperationDeadlineConn{Conn: a}
			client, peer := NewSecureConn(tracked), NewSecureConn(b)
			key := [32]byte{1, 2, 3}
			client.SetKey(key)
			peer.SetKey(key)
			ft := &FileTransfer{conn: &Conn{sc: client, opts: DialOpts{Timeout: 100 * time.Millisecond}}, nextID: 1}
			peerSent := make(chan error, 1)
			go func() {
				if _, err := readProtoMessage(peer); err != nil {
					peerSent <- err
					return
				}
				peerSent <- writeProtoMessage(peer, &pb.Message{Union: &pb.Message_TestDelay{TestDelay: &pb.TestDelay{}}})
			}()
			done := make(chan error, 1)
			go func() {
				if operation == "read" {
					_, err := ft.ReadDir("/remote")
					done <- err
				} else {
					done <- ft.CreateDir("/remote/new")
				}
			}()
			select {
			case err := <-done:
				var netErr net.Error
				if !errors.As(err, &netErr) || !netErr.Timeout() {
					t.Fatalf("blocked keepalive write did not time out: %v", err)
				}
				var opErr *net.OpError
				if !errors.As(err, &opErr) || opErr.Op != "write" {
					t.Fatalf("expected keepalive reply write to block, got %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("keepalive reply write hung")
			}
			if tracked.extended || tracked.first.IsZero() || !tracked.last.IsZero() {
				t.Fatalf("operation deadline extended or leaked: first=%v last=%v extended=%v", tracked.first, tracked.last, tracked.extended)
			}
			select {
			case err := <-peerSent:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("peer did not finish sending keepalive")
			}
		})
	}
}

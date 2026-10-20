package hbb

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pb "github.com/4nkitd/rustdesk-cli/internal/hbb/proto"
)

func TestTransferPolicyUploadCompletion(t *testing.T) {
	for _, outcome := range []string{"ack", "disk full", "write error", "disconnect", "wrong file"} {
		t.Run(outcome, func(t *testing.T) {
			content := make([]byte, 5*fileBlockSize+137)
			if _, err := rand.Read(content); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "payload.bin")
			if err := os.WriteFile(path, content, 0o600); err != nil {
				t.Fatal(err)
			}
			atDone, release := make(chan struct{}), make(chan struct{})
			var received []byte
			var blocks int
			opts, peerDone := setupFileTestEnv(t, "1.4.9", func(sc *SecureConn) {
				expectCreateDir(t, sc, "/remote")
				msg, err := readProtoMessage(sc)
				if err != nil {
					t.Error(err)
					return
				}
				id := msg.GetFileAction().GetReceive().GetId()
				msg, err = readProtoMessage(sc)
				if err != nil {
					t.Error(err)
					return
				}
				digest := msg.GetFileResponse().GetDigest()
				if digest == nil || digest.GetFileSize() != uint64(len(content)) {
					t.Error("missing initial upload digest")
					return
				}
				writeProtoMessage(sc, &pb.Message{Union: &pb.Message_FileAction{FileAction: &pb.FileAction{Union: &pb.FileAction_SendConfirm{SendConfirm: &pb.FileTransferSendConfirmRequest{Id: id, Union: &pb.FileTransferSendConfirmRequest_OffsetBlk{}}}}}})
				for {
					msg, err := readProtoMessage(sc)
					if err != nil {
						t.Error(err)
						return
					}
					fr := msg.GetFileResponse()
					if fr == nil {
						continue
					}
					if block := fr.GetBlock(); block != nil {
						data := block.GetData()
						if block.GetCompressed() {
							data, err = zstdDec.DecodeAll(data, nil)
							if err != nil {
								t.Error(err)
								return
							}
						}
						received = append(received, data...)
						blocks++
						if outcome == "write error" && blocks == 3 {
							resumeResponse(sc, &pb.FileResponse{Union: &pb.FileResponse_Error{Error: &pb.FileTransferError{Id: id, Error: "write failed"}}})
						}
						time.Sleep(time.Millisecond)
					}
					if done := fr.GetDone(); done != nil {
						if done.GetFileNum() != 1 {
							t.Errorf("job Done file number = %d", done.GetFileNum())
						}
						close(atDone)
						<-release
						switch outcome {
						case "ack":
							writeProtoMessage(sc, &pb.Message{Union: &pb.Message_FileResponse{FileResponse: &pb.FileResponse{Union: &pb.FileResponse_Done{Done: done}}}})
						case "wrong file":
							writeProtoMessage(sc, &pb.Message{Union: &pb.Message_FileResponse{FileResponse: &pb.FileResponse{Union: &pb.FileResponse_Done{Done: &pb.FileTransferDone{Id: id, FileNum: 0}}}}})
						case "disk full":
							writeProtoMessage(sc, &pb.Message{Union: &pb.Message_FileResponse{FileResponse: &pb.FileResponse{Union: &pb.FileResponse_Error{Error: &pb.FileTransferError{Id: id, Error: "disk full"}}}}})
						}
						return
					}
				}
			})
			ft, err := NewFileTransfer(opts, "/remote")
			if err != nil {
				t.Fatal(err)
			}
			defer ft.Close()
			result := make(chan error, 1)
			go func() { result <- ft.Send(path, "/remote", false, nil) }()
			if outcome == "write error" {
				close(release)
				err = <-result
			} else {
				select {
				case <-atDone:
				case err := <-result:
					t.Fatalf("returned before Done: %v", err)
				case <-time.After(6 * time.Second):
					t.Fatal("upload stalled")
				}
				select {
				case err := <-result:
					t.Fatalf("returned before acknowledgment: %v", err)
				default:
				}
				close(release)
				err = <-result
			}
			<-peerDone
			if outcome == "ack" && err != nil {
				t.Fatal(err)
			}
			if outcome != "ack" && err == nil {
				t.Fatal("unacknowledged upload succeeded")
			}
			if outcome == "disk full" && !strings.Contains(err.Error(), "disk full") {
				t.Fatal(err)
			}
			if outcome == "write error" && !strings.Contains(err.Error(), "write failed") {
				t.Fatal(err)
			}
			if blocks < 4 || !bytes.Equal(received, content) {
				t.Fatalf("truncated upload: blocks=%d bytes=%d", blocks, len(received))
			}
		})
	}
}

func TestTransferPolicyUploadMultipleFiles(t *testing.T) {
	dir := t.TempDir()
	contents := map[string]string{"a": strings.Repeat("first", fileBlockSize), "b": strings.Repeat("second", fileBlockSize), "empty": ""}
	for name, data := range contents {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var received = map[string]string{}
	opts, done := setupFileTestEnv(t, "1.4.9", func(sc *SecureConn) {
		expectCreateDir(t, sc, "/remote")
		msg, err := readProtoMessage(sc)
		if err != nil {
			t.Error(err)
			return
		}
		req := msg.GetFileAction().GetReceive()
		for {
			msg, err := readProtoMessage(sc)
			if err != nil {
				t.Error(err)
				return
			}
			fr := msg.GetFileResponse()
			if fr == nil {
				continue
			}
			if d := fr.GetDigest(); d != nil {
				writeProtoMessage(sc, &pb.Message{Union: &pb.Message_FileAction{FileAction: &pb.FileAction{Union: &pb.FileAction_SendConfirm{SendConfirm: &pb.FileTransferSendConfirmRequest{Id: req.GetId(), FileNum: d.GetFileNum(), Union: &pb.FileTransferSendConfirmRequest_OffsetBlk{}}}}}})
			}
			if b := fr.GetBlock(); b != nil {
				if int(b.GetFileNum()) >= len(req.GetFiles()) {
					t.Error("invalid block index")
					return
				}
				data := b.GetData()
				if b.GetCompressed() {
					data, err = zstdDec.DecodeAll(data, nil)
					if err != nil {
						t.Error(err)
						return
					}
				}
				name := req.GetFiles()[b.GetFileNum()].GetName()
				received[name] += string(data)
			}
			if d := fr.GetDone(); d != nil {
				if d.GetFileNum() != int32(len(req.GetFiles())) || len(received) != len(contents) {
					t.Errorf("premature Done: %v, files=%d", d, len(received))
				}
				resumeResponse(sc, &pb.FileResponse{Union: &pb.FileResponse_Done{Done: &pb.FileTransferDone{Id: req.GetId() + 1, FileNum: d.GetFileNum()}}})
				resumeResponse(sc, fr)
				return
			}
		}
	})
	ft, err := NewFileTransfer(opts, "/remote")
	if err != nil {
		t.Fatal(err)
	}
	defer ft.Close()
	var files int
	var bytesDone, bytesTotal int64
	if err := ft.Send(dir, "/remote", true, func(n, _ int, d, total int64) { files, bytesDone, bytesTotal = n, d, total }); err != nil {
		t.Fatal(err)
	}
	<-done
	for name, want := range contents {
		if got, ok := received[name]; !ok || got != want {
			t.Errorf("%s: got %d bytes, want %d", name, len(got), len(want))
		}
	}
	if files != len(contents) || bytesDone != bytesTotal {
		t.Errorf("progress: %d files, %d/%d bytes", files, bytesDone, bytesTotal)
	}
}

func TestTransferPolicyUploadRejectsChangedSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	os.WriteFile(path, []byte("original"), 0o600)
	opts, done := setupFileTestEnv(t, "1.4.9", func(sc *SecureConn) {
		expectCreateDir(t, sc, "/remote")
		msg, err := readProtoMessage(sc)
		if err != nil {
			return
		}
		id := msg.GetFileAction().GetReceive().GetId()
		if _, err := readProtoMessage(sc); err != nil {
			return
		}
		os.WriteFile(path, []byte("short"), 0o600)
		writeProtoMessage(sc, &pb.Message{Union: &pb.Message_FileAction{FileAction: &pb.FileAction{Union: &pb.FileAction_SendConfirm{SendConfirm: &pb.FileTransferSendConfirmRequest{Id: id, Union: &pb.FileTransferSendConfirmRequest_OffsetBlk{}}}}}})
		for {
			msg, err := readProtoMessage(sc)
			if err != nil {
				return
			}
			if fr := msg.GetFileResponse(); fr != nil && (fr.GetDone() != nil || fr.GetBlock() != nil) {
				t.Error("sent changed source or completion")
				return
			}
		}
	})
	ft, err := NewFileTransfer(opts, "/remote")
	if err != nil {
		t.Fatal(err)
	}
	err = ft.Send(path, "/remote", false, nil)
	ft.Close()
	<-done
	if err == nil || !strings.Contains(err.Error(), "source changed") {
		t.Fatalf("error=%v", err)
	}
}

func TestTransferPolicyHiddenAndSymlinks(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"visible", ".hidden"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, ".private"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".private", "secret"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "visible"), filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	ft := &FileTransfer{}
	jobs, err := ft.buildSendJobs(dir, "/remote", true)
	if err != nil || len(jobs) != 1 || len(jobs[0].files) != 1 {
		t.Fatalf("default jobs: %+v %v", jobs, err)
	}
	ft.SetPolicy(TransferPolicy{IncludeHidden: true})
	jobs, err = ft.buildSendJobs(dir, "/remote", true)
	if err != nil || len(jobs) != 2 || len(jobs[0].files) != 2 {
		t.Fatalf("hidden jobs: %+v %v", jobs, err)
	}
	if _, err := ft.buildSendJobs(filepath.Join(dir, "link"), "/remote", false); err == nil {
		t.Fatal("followed source symlink")
	}
}

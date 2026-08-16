package hbb

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "rdcli/internal/hbb/proto"
)

func runMockFilePeer(t *testing.T, peerEdPriv ed25519.PrivateKey, peerID, version string, handler func(sc *SecureConn)) (*netip.AddrPort, chan struct{}) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("peer listen: %v", err)
	}
	addr := ln.Addr().(*net.TCPAddr).AddrPort()
	done := make(chan struct{})

	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		sc := NewSecureConn(conn)

		kp, err := GenerateKeyPair()
		if err != nil {
			return
		}
		sid := mustSignIDPk(t, peerEdPriv, peerID, kp.Pub)
		msg := &pb.Message{Union: &pb.Message_SignedId{SignedId: &pb.SignedId{Id: sid}}}
		data, _ := proto.Marshal(msg)
		_, _ = sc.Write(data)

		raw, err := sc.ReadMessage()
		if err != nil {
			return
		}
		in := &pb.Message{}
		if err := proto.Unmarshal(raw, in); err != nil {
			return
		}
		pk := in.GetPublicKey()
		if pk == nil {
			return
		}
		key, err := DecryptSymmetricKey(pk.GetSymmetricValue(), pk.GetAsymmetricValue(), kp.Priv)
		if err != nil {
			return
		}
		sc.SetKey(key)

		hash := &pb.Message{Union: &pb.Message_Hash{Hash: &pb.Hash{Salt: "salt123", Challenge: "ch123"}}}
		data, _ = proto.Marshal(hash)
		_, _ = sc.Write(data)

		raw, err = sc.ReadMessage()
		if err != nil {
			return
		}
		lr := &pb.Message{}
		if err := proto.Unmarshal(raw, lr); err != nil {
			return
		}
		if lr.GetLoginRequest() == nil {
			return
		}

		pi := &pb.Message{Union: &pb.Message_LoginResponse{
			LoginResponse: &pb.LoginResponse{
				Union: &pb.LoginResponse_PeerInfo{
					PeerInfo: &pb.PeerInfo{Version: version, Platform: "Linux", Hostname: "mock"},
				},
			},
		}}
		data, _ = proto.Marshal(pi)
		_, _ = sc.Write(data)

		handler(sc)
	}()

	return &addr, done
}

func setupFileTestEnv(t *testing.T, version string, handler func(sc *SecureConn)) (DialOpts, chan struct{}) {
	t.Helper()
	serverPub, serverPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen server key: %v", err)
	}
	peerPub, peerPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen peer key: %v", err)
	}

	peerAddr, done := runMockFilePeer(t, peerPriv, "test-peer", version, handler)
	rvServer := runMockRendezvous(t, serverPriv, "test-peer", *peerAddr, peerPub)

	opts := DialOpts{
		Server:   rvServer,
		PeerID:   "test-peer",
		Password: "",
		Key:      base64.StdEncoding.EncodeToString(serverPub),
		Timeout:  5 * time.Second,
	}
	return opts, done
}

func readProtoMessage(sc *SecureConn) (*pb.Message, error) {
	raw, err := sc.ReadMessage()
	if err != nil {
		return nil, err
	}
	msg := &pb.Message{}
	if err := proto.Unmarshal(raw, msg); err != nil {
		return nil, err
	}
	return msg, nil
}

func writeProtoMessage(sc *SecureConn, msg *pb.Message) error {
	data, err := proto.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = sc.Write(data)
	return err
}

func TestFileReadDir(t *testing.T) {
	opts, done := setupFileTestEnv(t, "1.4.1", func(sc *SecureConn) {
		msg, err := readProtoMessage(sc)
		if err != nil {
			return
		}
		fa := msg.GetFileAction()
		if fa == nil || fa.GetReadDir() == nil {
			return
		}
		resp := &pb.Message{
			Union: &pb.Message_FileResponse{
				FileResponse: &pb.FileResponse{
					Union: &pb.FileResponse_Dir{
						Dir: &pb.FileDirectory{
							Path: "/tmp",
							Entries: []*pb.FileEntry{
								{Name: "file1.txt", Size: 100, EntryType: pb.FileType_File},
								{Name: "sub", EntryType: pb.FileType_Dir},
							},
						},
					},
				},
			},
		}
		_ = writeProtoMessage(sc, resp)
	})

	ft, err := NewFileTransfer(opts, "/tmp")
	if err != nil {
		t.Fatalf("NewFileTransfer failed: %v", err)
	}
	defer ft.Close()
	dir, err := ft.ReadDir("/tmp")
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}
	if len(dir.GetEntries()) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(dir.GetEntries()))
	}
	if dir.GetEntries()[0].GetName() != "file1.txt" || dir.GetEntries()[0].GetSize() != 100 {
		t.Errorf("unexpected entry 0: %+v", dir.GetEntries()[0])
	}
	if dir.GetEntries()[1].GetName() != "sub" || dir.GetEntries()[1].GetEntryType() != pb.FileType_Dir {
		t.Errorf("unexpected entry 1: %+v", dir.GetEntries()[1])
	}
	<-done
}

func expectCreateDir(t *testing.T, sc *SecureConn, path string) {
	t.Helper()
	msg, err := readProtoMessage(sc)
	if err != nil {
		t.Fatalf("read create request: %v", err)
	}
	fa := msg.GetFileAction()
	if fa == nil || fa.GetCreate() == nil {
		t.Fatalf("expected create action for %s, got %T", path, msg.GetUnion())
	}
	if fa.GetCreate().GetPath() != path {
		t.Fatalf("create path got %q, want %q", fa.GetCreate().GetPath(), path)
	}
	resp := &pb.Message{
		Union: &pb.Message_FileResponse{
			FileResponse: &pb.FileResponse{
				Union: &pb.FileResponse_Done{
					Done: &pb.FileTransferDone{Id: fa.GetCreate().GetId()},
				},
			},
		},
	}
	_ = writeProtoMessage(sc, resp)
}

func TestFileSendNoOverwrite(t *testing.T) {
	tmpDir := t.TempDir()
	localFile := filepath.Join(tmpDir, "test.txt")
	content := []byte("hello file transfer world")
	if err := os.WriteFile(localFile, content, 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	var receivedData []byte
	opts, done := setupFileTestEnv(t, "1.1.9", func(sc *SecureConn) {
		expectCreateDir(t, sc, "/remote")
		msg, err := readProtoMessage(sc)
		if err != nil {
			return
		}
		fa := msg.GetFileAction()
		if fa == nil || fa.GetReceive() == nil {
			return
		}
		req := fa.GetReceive()
		if len(req.GetFiles()) != 1 || req.GetFiles()[0].GetName() != "test.txt" {
			return
		}
		for {
			msg2, err := readProtoMessage(sc)
			if err != nil {
				return
			}
			if fr := msg2.GetFileResponse(); fr != nil {
				if blk := fr.GetBlock(); blk != nil {
					data := blk.GetData()
					if blk.GetCompressed() {
						plain, err := zstdDec.DecodeAll(data, nil)
						if err != nil {
							return
						}
						data = plain
					}
					receivedData = append(receivedData, data...)
				}
				if fr.GetDone() != nil {
					return
				}
			}
		}
	})

	ft, err := NewFileTransfer(opts, "/tmp")
	if err != nil {
		t.Fatalf("NewFileTransfer failed: %v", err)
	}
	defer ft.Close()
	if err := ft.Send(localFile, "/remote", false, nil); err != nil {
		t.Fatalf("Send failed: %v", err)
	}
	ft.Close()
	<-done
	if string(receivedData) != string(content) {
		t.Fatalf("received data mismatch: got %q, want %q", string(receivedData), string(content))
	}
}

func TestFileSendWithOverwrite(t *testing.T) {
	tmpDir := t.TempDir()
	localFile := filepath.Join(tmpDir, "test.txt")
	content := []byte("hello file transfer world with overwrite detection")
	if err := os.WriteFile(localFile, content, 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	var receivedData []byte
	var confirmOffset uint32
	confirmOffset = 999
	opts, done := setupFileTestEnv(t, "1.4.1", func(sc *SecureConn) {
		expectCreateDir(t, sc, "/remote")
		msg, err := readProtoMessage(sc)
		if err != nil {
			return
		}
		fa := msg.GetFileAction()
		if fa == nil || fa.GetReceive() == nil {
			return
		}
		req := fa.GetReceive()
		digest := &pb.Message{
			Union: &pb.Message_FileResponse{
				FileResponse: &pb.FileResponse{
					Union: &pb.FileResponse_Digest{
						Digest: &pb.FileTransferDigest{Id: req.GetId(), FileNum: 0, IsUpload: true},
					},
				},
			},
		}
		_ = writeProtoMessage(sc, digest)

		msg2, err := readProtoMessage(sc)
		if err != nil {
			t.Logf("DEBUG read msg2 err: %v", err)
			return
		}
		t.Logf("DEBUG msg2 union: %T", msg2.GetUnion())
		cf := msg2.GetFileAction().GetSendConfirm()
		if cf == nil {
			t.Logf("DEBUG no send confirm")
			return
		}
		confirmOffset = cf.GetOffsetBlk()
		for {
			msg3, err := readProtoMessage(sc)
			if err != nil {
				t.Logf("DEBUG block loop read err: %v", err)
				return
			}
			fr := msg3.GetFileResponse()
			if fr == nil {
				t.Logf("DEBUG msg3 not file response: %T", msg3.GetUnion())
				continue
			}
			if blk := fr.GetBlock(); blk != nil {
				data := blk.GetData()
				t.Logf("DEBUG block file_num=%d len=%d compressed=%v", blk.GetFileNum(), len(data), blk.GetCompressed())
				if blk.GetCompressed() {
					plain, err := zstdDec.DecodeAll(data, nil)
					if err != nil {
						t.Logf("DEBUG decompress err: %v", err)
						return
					}
					data = plain
				}
				receivedData = append(receivedData, data...)
			}
			if fr.GetDone() != nil {
				t.Logf("DEBUG got done file_num=%d", fr.GetDone().GetFileNum())
				return
			}
		}
	})

	ft, err := NewFileTransfer(opts, "/tmp")
	if err != nil {
		t.Fatalf("NewFileTransfer failed: %v", err)
	}
	defer ft.Close()
	if err := ft.Send(localFile, "/remote", false, nil); err != nil {
		t.Fatalf("Send failed: %v", err)
	}
	ft.Close()
	<-done
	if confirmOffset != 0 {
		t.Fatalf("expected confirm offset 0, got %d", confirmOffset)
	}
	if string(receivedData) != string(content) {
		t.Fatalf("received data mismatch: got %q, want %q", string(receivedData), string(content))
	}
}

func TestFileSendSkip(t *testing.T) {
	tmpDir := t.TempDir()
	localFile := filepath.Join(tmpDir, "skip.txt")
	content := []byte("will be skipped")
	if err := os.WriteFile(localFile, content, 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	opts, done := setupFileTestEnv(t, "1.4.1", func(sc *SecureConn) {
		expectCreateDir(t, sc, "/remote")
		msg, err := readProtoMessage(sc)
		if err != nil {
			return
		}
		fa := msg.GetFileAction()
		if fa == nil || fa.GetReceive() == nil {
			return
		}
		req := fa.GetReceive()
		skip := &pb.Message{
			Union: &pb.Message_FileAction{
				FileAction: &pb.FileAction{
					Union: &pb.FileAction_SendConfirm{
						SendConfirm: &pb.FileTransferSendConfirmRequest{
							Id:      req.GetId(),
							FileNum: 0,
							Union:   &pb.FileTransferSendConfirmRequest_Skip{Skip: true},
						},
					},
				},
			},
		}
		_ = writeProtoMessage(sc, skip)
		// Client should skip the file entirely and send no blocks; expect done.
		for {
			msg2, err := readProtoMessage(sc)
			if err != nil {
				return
			}
			if fr := msg2.GetFileResponse(); fr != nil && fr.GetDone() != nil {
				return
			}
		}
	})

	ft, err := NewFileTransfer(opts, "/tmp")
	if err != nil {
		t.Fatalf("NewFileTransfer failed: %v", err)
	}
	defer ft.Close()
	if err := ft.Send(localFile, "/remote", false, nil); err != nil {
		t.Fatalf("Send failed: %v", err)
	}
	<-done
}

func TestFileReceiveWithOverwrite(t *testing.T) {
	tmpDir := t.TempDir()
	localDest := filepath.Join(tmpDir, "received.txt")
	content := []byte("payload from remote")

	opts, done := setupFileTestEnv(t, "1.4.1", func(sc *SecureConn) {
		msg, err := readProtoMessage(sc)
		if err != nil {
			return
		}
		fa := msg.GetFileAction()
		if fa == nil || fa.GetSend() == nil {
			return
		}
		reqID := fa.GetSend().GetId()

		dirResp := &pb.Message{
			Union: &pb.Message_FileResponse{
				FileResponse: &pb.FileResponse{
					Union: &pb.FileResponse_Dir{
						Dir: &pb.FileDirectory{
							Entries: []*pb.FileEntry{
								{Name: "received.txt", Size: uint64(len(content)), EntryType: pb.FileType_File},
							},
						},
					},
				},
			},
		}
		_ = writeProtoMessage(sc, dirResp)

		digest := &pb.Message{
			Union: &pb.Message_FileResponse{
				FileResponse: &pb.FileResponse{
					Union: &pb.FileResponse_Digest{
						Digest: &pb.FileTransferDigest{Id: reqID, FileNum: 0, IsUpload: false},
					},
				},
			},
		}
		_ = writeProtoMessage(sc, digest)

		msg2, err := readProtoMessage(sc)
		if err != nil {
			return
		}
		cf := msg2.GetFileAction().GetSendConfirm()
		if cf == nil || cf.GetOffsetBlk() != 0 {
			return
		}

		blkResp := &pb.Message{
			Union: &pb.Message_FileResponse{
				FileResponse: &pb.FileResponse{
					Union: &pb.FileResponse_Block{
						Block: &pb.FileTransferBlock{Id: reqID, FileNum: 0, Data: content},
					},
				},
			},
		}
		_ = writeProtoMessage(sc, blkResp)

		doneResp := &pb.Message{
			Union: &pb.Message_FileResponse{
				FileResponse: &pb.FileResponse{
					Union: &pb.FileResponse_Done{
						Done: &pb.FileTransferDone{Id: reqID, FileNum: 0},
					},
				},
			},
		}
		_ = writeProtoMessage(sc, doneResp)
	})

	ft, err := NewFileTransfer(opts, "/tmp")
	if err != nil {
		t.Fatalf("NewFileTransfer failed: %v", err)
	}
	defer ft.Close()
	if err := ft.Receive("/remote/received.txt", localDest, nil); err != nil {
		t.Fatalf("Receive failed: %v", err)
	}
	got, err := os.ReadFile(localDest)
	if err != nil {
		t.Fatalf("read destination file: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("received file content mismatch: got %q, want %q", string(got), string(content))
	}
	<-done
}

func TestFileReceiveDir(t *testing.T) {
	tmpDir := t.TempDir()
	contentA := []byte("content a")
	contentB := []byte("content b in subdir")

	opts, done := setupFileTestEnv(t, "1.1.9", func(sc *SecureConn) {
		msg, err := readProtoMessage(sc)
		if err != nil {
			return
		}
		fa := msg.GetFileAction()
		if fa == nil || fa.GetSend() == nil {
			return
		}
		reqID := fa.GetSend().GetId()

		dirResp := &pb.Message{
			Union: &pb.Message_FileResponse{
				FileResponse: &pb.FileResponse{
					Union: &pb.FileResponse_Dir{
						Dir: &pb.FileDirectory{
							Entries: []*pb.FileEntry{
								{Name: "a.txt", Size: uint64(len(contentA)), EntryType: pb.FileType_File},
								{Name: "sub/b.txt", Size: uint64(len(contentB)), EntryType: pb.FileType_File},
							},
						},
					},
				},
			},
		}
		_ = writeProtoMessage(sc, dirResp)

		for i, data := range [][]byte{contentA, contentB} {
			blkResp := &pb.Message{
				Union: &pb.Message_FileResponse{
					FileResponse: &pb.FileResponse{
						Union: &pb.FileResponse_Block{
							Block: &pb.FileTransferBlock{Id: reqID, FileNum: int32(i), Data: data},
						},
					},
				},
			}
			_ = writeProtoMessage(sc, blkResp)
			doneResp := &pb.Message{
				Union: &pb.Message_FileResponse{
					FileResponse: &pb.FileResponse{
						Union: &pb.FileResponse_Done{
							Done: &pb.FileTransferDone{Id: reqID, FileNum: int32(i)},
						},
					},
				},
			}
			_ = writeProtoMessage(sc, doneResp)
		}
	})

	ft, err := NewFileTransfer(opts, "/tmp")
	if err != nil {
		t.Fatalf("NewFileTransfer failed: %v", err)
	}
	defer ft.Close()
	target := filepath.Join(tmpDir, "dl")
	if err := ft.Receive("/remote/dir", target, nil); err != nil {
		t.Fatalf("Receive dir failed: %v", err)
	}
	gotA, err := os.ReadFile(filepath.Join(target, "a.txt"))
	if err != nil {
		t.Fatalf("read a.txt: %v", err)
	}
	if string(gotA) != string(contentA) {
		t.Fatalf("a.txt mismatch: got %q, want %q", string(gotA), string(contentA))
	}
	gotB, err := os.ReadFile(filepath.Join(target, "sub", "b.txt"))
	if err != nil {
		t.Fatalf("read sub/b.txt: %v", err)
	}
	if string(gotB) != string(contentB) {
		t.Fatalf("sub/b.txt mismatch: got %q, want %q", string(gotB), string(contentB))
	}
	<-done
}

func TestFileReceiveSkipIdentical(t *testing.T) {
	tmpDir := t.TempDir()
	localDest := filepath.Join(tmpDir, "same.txt")
	content := []byte("identical content")
	if err := os.WriteFile(localDest, content, 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	mt := time.Now().Add(-time.Hour)
	_ = os.Chtimes(localDest, mt, mt)

	opts, done := setupFileTestEnv(t, "1.4.1", func(sc *SecureConn) {
		msg, err := readProtoMessage(sc)
		if err != nil {
			return
		}
		fa := msg.GetFileAction()
		if fa == nil || fa.GetSend() == nil {
			return
		}
		reqID := fa.GetSend().GetId()

		dirResp := &pb.Message{
			Union: &pb.Message_FileResponse{
				FileResponse: &pb.FileResponse{
					Union: &pb.FileResponse_Dir{
						Dir: &pb.FileDirectory{
							Entries: []*pb.FileEntry{
								{Name: "same.txt", Size: uint64(len(content)), ModifiedTime: uint64(mt.Unix()), EntryType: pb.FileType_File},
							},
						},
					},
				},
			},
		}
		_ = writeProtoMessage(sc, dirResp)

		digest := &pb.Message{
			Union: &pb.Message_FileResponse{
				FileResponse: &pb.FileResponse{
					Union: &pb.FileResponse_Digest{
						Digest: &pb.FileTransferDigest{
							Id: reqID, FileNum: 0, IsUpload: false,
							LastModified: uint64(mt.Unix()), FileSize: uint64(len(content)),
						},
					},
				},
			},
		}
		_ = writeProtoMessage(sc, digest)

		msg2, err := readProtoMessage(sc)
		if err != nil {
			return
		}
		cf := msg2.GetFileAction().GetSendConfirm()
		if cf == nil || !cf.GetSkip() {
			t.Errorf("expected skip confirm")
		}

		doneResp := &pb.Message{
			Union: &pb.Message_FileResponse{
				FileResponse: &pb.FileResponse{
					Union: &pb.FileResponse_Done{
						Done: &pb.FileTransferDone{Id: reqID, FileNum: 0},
					},
				},
			},
		}
		_ = writeProtoMessage(sc, doneResp)
	})

	ft, err := NewFileTransfer(opts, "/tmp")
	if err != nil {
		t.Fatalf("NewFileTransfer failed: %v", err)
	}
	defer ft.Close()
	if err := ft.Receive("/remote/same.txt", localDest, nil); err != nil {
		t.Fatalf("Receive failed: %v", err)
	}
	<-done
}

func TestFileSendResumeOffset(t *testing.T) {
	tmpDir := t.TempDir()
	localFile := filepath.Join(tmpDir, "resume.bin")
	content := []byte("partial resume test payload that is long enough")
	if err := os.WriteFile(localFile, content, 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	var confirmOffset uint32
	confirmOffset = 999
	var receivedData []byte
	opts, done := setupFileTestEnv(t, "1.4.1", func(sc *SecureConn) {
		expectCreateDir(t, sc, "/remote")
		msg, err := readProtoMessage(sc)
		if err != nil {
			return
		}
		fa := msg.GetFileAction()
		if fa == nil || fa.GetReceive() == nil {
			return
		}
		req := fa.GetReceive()
		digest := &pb.Message{
			Union: &pb.Message_FileResponse{
				FileResponse: &pb.FileResponse{
					Union: &pb.FileResponse_Digest{
						Digest: &pb.FileTransferDigest{
							Id: req.GetId(), FileNum: 0, IsUpload: true, IsResume: true,
							IsIdentical: true, TransferredSize: 12,
						},
					},
				},
			},
		}
		_ = writeProtoMessage(sc, digest)

		msg2, err := readProtoMessage(sc)
		if err != nil {
			t.Logf("DEBUG read msg2 err: %v", err)
			return
		}
		t.Logf("DEBUG msg2 union: %T", msg2.GetUnion())
		cf := msg2.GetFileAction().GetSendConfirm()
		if cf == nil {
			t.Logf("DEBUG no send confirm")
			return
		}
		confirmOffset = cf.GetOffsetBlk()
		for {
			msg3, err := readProtoMessage(sc)
			if err != nil {
				t.Logf("DEBUG block loop read err: %v", err)
				return
			}
			fr := msg3.GetFileResponse()
			if fr == nil {
				t.Logf("DEBUG msg3 not file response: %T", msg3.GetUnion())
				continue
			}
			if blk := fr.GetBlock(); blk != nil {
				data := blk.GetData()
				t.Logf("DEBUG block file_num=%d len=%d compressed=%v", blk.GetFileNum(), len(data), blk.GetCompressed())
				if blk.GetCompressed() {
					plain, err := zstdDec.DecodeAll(data, nil)
					if err != nil {
						t.Logf("DEBUG decompress err: %v", err)
						return
					}
					data = plain
				}
				receivedData = append(receivedData, data...)
			}
			if fr.GetDone() != nil {
				t.Logf("DEBUG got done file_num=%d", fr.GetDone().GetFileNum())
				return
			}
		}
	})

	ft, err := NewFileTransfer(opts, "/tmp")
	if err != nil {
		t.Fatalf("NewFileTransfer failed: %v", err)
	}
	defer ft.Close()
	if err := ft.Send(localFile, "/remote", false, nil); err != nil {
		t.Fatalf("Send failed: %v", err)
	}
	ft.Close()
	<-done
	if confirmOffset != 12 {
		t.Fatalf("expected resume confirm offset 12, got %d", confirmOffset)
	}
	if string(receivedData) != string(content[12:]) {
		t.Fatalf("resume data mismatch: got %q, want %q", string(receivedData), string(content[12:]))
	}
}

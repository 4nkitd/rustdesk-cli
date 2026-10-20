package hbb

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/4nkitd/rustdesk-cli/internal/hbb/proto"
)

func TestDownloadName(t *testing.T) {
	for _, name := range []string{"../outside", "sub/../../outside", "/absolute", `C:\secret`, `..\outside`, "a/../b", "a\x00b", "a//b", ".", ""} {
		if _, err := downloadName(name); err == nil {
			t.Errorf("accepted %q", name)
		}
	}
	for _, name := range []string{"a.txt", "sub/a.txt", `sub\a.txt`, ".hidden"} {
		if _, err := downloadName(name); err != nil {
			t.Errorf("rejected %q: %v", name, err)
		}
	}
}

func TestDownloadConfinesSymlinksAndPreservesUnownedPartial(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	if f, err := openDownload(root, "escape/evil"); err == nil {
		f.Close()
		t.Fatal("followed symlink outside root")
	}
	victim := filepath.Join(outside, "victim")
	if err := os.WriteFile(victim, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(dir, "file.download")); err != nil {
		t.Fatal(err)
	}
	f, err := openDownload(root, "file")
	if err == nil {
		f.Close()
		t.Fatal("replaced unowned symlink partial")
	}
	if target, err := os.Readlink(filepath.Join(dir, "file.download")); err != nil || target != victim {
		t.Fatal("partial symlink changed")
	}
	if err := root.Remove("file.download"); err != nil {
		t.Fatal(err)
	}
	f, err = openDownload(root, "file")
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("stale")
	f.Close()
	f, err = openDownload(root, "file")
	if err == nil {
		f.Close()
		t.Fatal("replaced unowned regular partial")
	}
	data, _ := root.ReadFile("file.download")
	if string(data) != "stale" {
		t.Fatalf("unowned partial changed: %q", data)
	}
	data, _ = os.ReadFile(victim)
	if string(data) != "untouched" {
		t.Fatalf("symlink target changed: %q", data)
	}
}

func TestDownloadRejectsDuplicateNames(t *testing.T) {
	for _, names := range [][]string{{"a", "a"}, {"A", "a"}, {"a.download", "a"}} {
		files := []*pb.FileEntry{{Name: names[0]}, {Name: names[1]}}
		root, _, err := downloadRoot(t.TempDir(), files)
		if root != nil {
			root.Close()
		}
		if err == nil {
			t.Fatalf("accepted %v", names)
		}
	}
}

func TestReceiveRejectsMalformedPeer(t *testing.T) {
	for _, tc := range []struct {
		name string
		file string
		num  int32
		data string
		done bool
	}{
		{name: "traversal", file: "../outside", num: 0, data: "x"},
		{name: "negative block", file: "safe", num: -1, data: "x"},
		{name: "oversized block", file: "safe", num: 0, data: "xx"},
		{name: "truncated file", file: "safe", num: 0, done: true},
		{name: "negative done", file: "safe", num: -1, done: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts, done := setupFileTestEnv(t, "1.4.1", func(sc *SecureConn) {
				msg, err := readProtoMessage(sc)
				if err != nil {
					return
				}
				id := msg.GetFileAction().GetSend().GetId()
				writeProtoMessage(sc, &pb.Message{Union: &pb.Message_FileResponse{FileResponse: &pb.FileResponse{
					Union: &pb.FileResponse_Dir{Dir: &pb.FileDirectory{Id: id, Entries: []*pb.FileEntry{{Name: tc.file, EntryType: pb.FileType_File, Size: 1}}}},
				}}})
				resp := &pb.FileResponse{Union: &pb.FileResponse_Block{Block: &pb.FileTransferBlock{Id: id, FileNum: tc.num, Data: []byte(tc.data)}}}
				if tc.done {
					resp.Union = &pb.FileResponse_Done{Done: &pb.FileTransferDone{Id: id, FileNum: tc.num}}
				}
				writeProtoMessage(sc, &pb.Message{Union: &pb.Message_FileResponse{FileResponse: resp}})
			})
			ft, err := NewFileTransfer(opts, ".")
			if err != nil {
				t.Fatal(err)
			}
			dst := t.TempDir()
			if err := ft.Receive("remote", dst, nil); err == nil {
				t.Fatal("accepted malformed peer response")
			}
			ft.Close()
			<-done
			if _, err := os.Stat(filepath.Join(dst, "safe")); !os.IsNotExist(err) {
				t.Fatal("published incomplete file")
			}
		})
	}
}

func TestReceiveUpstreamJobDone(t *testing.T) {
	opts, done := setupFileTestEnv(t, "1.4.1", func(sc *SecureConn) {
		msg, err := readProtoMessage(sc)
		if err != nil {
			return
		}
		id := msg.GetFileAction().GetSend().GetId()
		entries := []*pb.FileEntry{
			{Name: "a", EntryType: pb.FileType_File, Size: 1},
			{Name: "empty", EntryType: pb.FileType_File},
			{Name: "sub/b", EntryType: pb.FileType_File, Size: 1},
		}
		writeProtoMessage(sc, &pb.Message{Union: &pb.Message_FileResponse{FileResponse: &pb.FileResponse{Union: &pb.FileResponse_Dir{Dir: &pb.FileDirectory{Id: id, Entries: entries}}}}})
		for _, n := range []int32{0, 2} {
			writeProtoMessage(sc, &pb.Message{Union: &pb.Message_FileResponse{FileResponse: &pb.FileResponse{Union: &pb.FileResponse_Block{Block: &pb.FileTransferBlock{Id: id, FileNum: n, Data: []byte("x")}}}}})
		}
		writeProtoMessage(sc, &pb.Message{Union: &pb.Message_FileResponse{FileResponse: &pb.FileResponse{Union: &pb.FileResponse_Done{Done: &pb.FileTransferDone{Id: id, FileNum: 3}}}}})
	})
	ft, err := NewFileTransfer(opts, ".")
	if err != nil {
		t.Fatal(err)
	}
	defer ft.Close()
	dst := t.TempDir()
	if err := ft.Receive("remote", dst, nil); err != nil {
		t.Fatal(err)
	}
	<-done
	for _, name := range []string{"a", "empty", "sub/b"} {
		data, err := os.ReadFile(filepath.Join(dst, name))
		if err != nil {
			t.Fatal(err)
		}
		want := "x"
		if name == "empty" {
			want = ""
		}
		if string(data) != want {
			t.Fatalf("%s: %q", name, data)
		}
	}
}

func TestReceiveSingleEmptyEntryName(t *testing.T) {
	opts, done := setupFileTestEnv(t, "1.4.1", func(sc *SecureConn) {
		msg, err := readProtoMessage(sc)
		if err != nil {
			return
		}
		id := msg.GetFileAction().GetSend().GetId()
		writeProtoMessage(sc, &pb.Message{Union: &pb.Message_FileResponse{FileResponse: &pb.FileResponse{Union: &pb.FileResponse_Dir{Dir: &pb.FileDirectory{Id: id, Entries: []*pb.FileEntry{{EntryType: pb.FileType_File}}}}}}})
		writeProtoMessage(sc, &pb.Message{Union: &pb.Message_FileResponse{FileResponse: &pb.FileResponse{Union: &pb.FileResponse_Done{Done: &pb.FileTransferDone{Id: id, FileNum: 1}}}}})
	})
	ft, err := NewFileTransfer(opts, ".")
	if err != nil {
		t.Fatal(err)
	}
	defer ft.Close()
	dst := t.TempDir()
	if err := ft.Receive("/remote/empty", dst, nil); err != nil {
		t.Fatal(err)
	}
	<-done
	if _, err := os.Stat(filepath.Join(dst, "empty")); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(dst, "\x00") {
		t.Fatal("invalid test path")
	}
}

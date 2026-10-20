package hbb

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	pb "github.com/4nkitd/rustdesk-cli/internal/hbb/proto"
)

func resumeResponse(sc *SecureConn, response *pb.FileResponse) error {
	return writeProtoMessage(sc, &pb.Message{Union: &pb.Message_FileResponse{FileResponse: response}})
}

func downloadPolicyFixture(t *testing.T, dir string, policy TransferPolicy, afterConfirm func(), progress ProgressFunc) error {
	t.Helper()
	opts, done := setupFileTestEnv(t, "1.4.9", func(sc *SecureConn) {
		msg, err := readProtoMessage(sc)
		if err != nil {
			return
		}
		id := msg.GetFileAction().GetSend().GetId()
		resumeResponse(sc, &pb.FileResponse{Union: &pb.FileResponse_Dir{Dir: &pb.FileDirectory{Id: 0}}})
		resumeResponse(sc, &pb.FileResponse{Union: &pb.FileResponse_Dir{Dir: &pb.FileDirectory{Id: id, Entries: []*pb.FileEntry{{Name: "file", EntryType: pb.FileType_File, Size: 3}}}}})
		resumeResponse(sc, &pb.FileResponse{Union: &pb.FileResponse_Digest{Digest: &pb.FileTransferDigest{Id: id, FileSize: 3}}})
		confirm, err := readProtoMessage(sc)
		if err != nil {
			return
		}
		if confirm.GetFileAction().GetSendConfirm().GetOffsetBlk() != 0 {
			t.Error("fresh download requested a nonzero offset")
			return
		}
		if afterConfirm != nil {
			afterConfirm()
		}
		if !confirm.GetFileAction().GetSendConfirm().GetSkip() {
			resumeResponse(sc, &pb.FileResponse{Union: &pb.FileResponse_Block{Block: &pb.FileTransferBlock{Id: id}}})
			resumeResponse(sc, &pb.FileResponse{Union: &pb.FileResponse_Block{Block: &pb.FileTransferBlock{Id: id, Data: []byte("new")}}})
		}
		resumeResponse(sc, &pb.FileResponse{Union: &pb.FileResponse_Done{Done: &pb.FileTransferDone{Id: id, FileNum: 1}}})
	})
	ft, err := NewFileTransfer(opts, "/remote")
	if err != nil {
		t.Fatal(err)
	}
	if err := ft.SetPolicy(policy); err != nil {
		ft.Close()
		t.Fatal(err)
	}
	err = ft.Receive("/remote/file", dir, progress)
	ft.Close()
	<-done
	return err
}

func TestDownloadPolicyPreservesExisting(t *testing.T) {
	for _, policy := range []string{"error", "skip", "overwrite", "race", "legacy partial", "permission"} {
		t.Run(policy, func(t *testing.T) {
			if policy == "permission" && (os.Geteuid() == 0 || runtime.GOOS == "windows") {
				t.Skip("requires Unix permission enforcement")
			}
			dir := t.TempDir()
			path := filepath.Join(dir, "file")
			if policy != "race" && policy != "legacy partial" {
				if err := os.WriteFile(path, []byte("original"), 0o640); err != nil {
					t.Fatal(err)
				}
			}
			if policy == "legacy partial" {
				if err := os.WriteFile(path+".download", []byte("unowned"), 0o640); err != nil {
					t.Fatal(err)
				}
			}
			if policy == "permission" {
				if err := os.Chmod(dir, 0o500); err != nil {
					t.Fatal(err)
				}
				defer os.Chmod(dir, 0o700)
			}
			existing := policy
			if existing == "race" || existing == "legacy partial" {
				existing = "error"
			}
			if existing == "permission" {
				existing = "overwrite"
			}
			err := downloadPolicyFixture(t, dir, TransferPolicy{Existing: existing}, func() {
				if policy == "race" {
					if err := os.WriteFile(path, []byte("original"), 0o640); err != nil {
						t.Error(err)
					}
				}
			}, nil)
			if (policy == "error" || policy == "race" || policy == "permission") && err == nil {
				t.Fatal("unexpected successful clobber")
			}
			if (policy == "skip" || policy == "overwrite" || policy == "legacy partial") && err != nil {
				t.Fatal(err)
			}
			want := "original"
			if policy == "overwrite" {
				want = "new"
			}
			if policy == "legacy partial" {
				got, err := os.ReadFile(path)
				if err != nil || string(got) != "new" {
					t.Fatal("fresh file missing")
				}
				path += ".download"
				want = "unowned"
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != want {
				t.Fatalf("%q, want %q: %v", got, want, err)
			}
			if policy != "overwrite" {
				info, _ := os.Stat(path)
				if info.Mode().Perm() != 0o640 {
					t.Error("original permissions changed")
				}
			}
			if _, err := os.Stat(filepath.Join(dir, downloadStageName)); !os.IsNotExist(err) {
				t.Fatal("owned staging was not cleaned")
			}
		})
	}
}

func TestDownloadStageExclusiveOwnership(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	stage, err := newDownloadStage(root)
	if err != nil {
		t.Fatal(err)
	}
	defer stage.close()
	f, err := stage.open()
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString("active partial"); err != nil {
		t.Fatal(err)
	}
	if other, err := newDownloadStage(root); err == nil {
		other.close()
		t.Fatal("concurrent job adopted active staging")
	}
	got, err := stage.root.ReadFile(downloadStagePart)
	if err != nil || string(got) != "active partial" {
		t.Fatalf("active partial changed: %q %v", got, err)
	}
	if stage.identity.Mode().Perm() != 0o700 || stage.part.Mode().Perm() != 0o600 {
		t.Fatal("staging is not private")
	}
	if err := stage.verify(f); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(dir, downloadStageName, downloadStagePart), filepath.Join(t.TempDir(), "outside-link")); err != nil {
		t.Fatal(err)
	}
	if err := stage.verify(f); err == nil {
		t.Fatal("accepted hard-linked staging inode")
	}
}

func TestDownloadForgedCheckpointHardlinkUntouched(t *testing.T) {
	for _, legacy := range []bool{true, false} {
		t.Run(map[bool]string{true: "legacy checkpoint", false: "stale job staging"}[legacy], func(t *testing.T) {
			dir := t.TempDir()
			victim := filepath.Join(t.TempDir(), "victim")
			if err := os.WriteFile(victim, []byte("outside original"), 0o600); err != nil {
				t.Fatal(err)
			}
			before, _ := os.Stat(victim)
			partial := filepath.Join(dir, "file.download")
			if !legacy {
				if err := os.Mkdir(filepath.Join(dir, downloadStageName), 0o700); err != nil {
					t.Fatal(err)
				}
				partial = filepath.Join(dir, downloadStageName, downloadStagePart)
			}
			if err := os.Link(victim, partial); err != nil {
				t.Fatal(err)
			}
			metadata := []byte(`{"Peer":"forged","Path":"/remote/file","Entry":"file","Size":16,"Written":0,"SHA256":"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"}`)
			metaPath := filepath.Join(dir, "file.download.rdcli")
			if err := os.WriteFile(metaPath, metadata, 0o600); err != nil {
				t.Fatal(err)
			}
			err := downloadPolicyFixture(t, dir, TransferPolicy{Existing: "overwrite"}, nil, nil)
			if legacy && err != nil {
				t.Fatal(err)
			}
			if !legacy && err == nil {
				t.Fatal("adopted preexisting staging directory")
			}
			got, err := os.ReadFile(victim)
			if err != nil || string(got) != "outside original" {
				t.Fatalf("outside inode changed: %q %v", got, err)
			}
			after, _ := os.Stat(victim)
			if !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
				t.Fatal("outside inode metadata changed")
			}
			got, err = os.ReadFile(metaPath)
			if err != nil || string(got) != string(metadata) {
				t.Fatal("forged legacy metadata changed")
			}
			linked, err := os.Stat(partial)
			if err != nil || !os.SameFile(linked, after) {
				t.Fatal("unowned partial link removed or replaced")
			}
		})
	}
}

func TestDownloadRejectsUnsafeDirectories(t *testing.T) {
	for _, ancestor := range []bool{false, true} {
		base := t.TempDir()
		if err := os.Chmod(base, 0o777); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(base, 0o700)
		dir := base
		if ancestor {
			dir = filepath.Join(base, "private")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := secureDownloadBase(dir); err == nil {
			t.Fatal("accepted another-user-writable root or ancestor")
		}
		if _, err := os.Stat(filepath.Join(dir, downloadStageName)); !os.IsNotExist(err) {
			t.Fatal("unsafe root acquired staging")
		}
	}
	unsafe := t.TempDir()
	os.Chmod(unsafe, 0o777)
	defer os.Chmod(unsafe, 0o700)
	os.Symlink(t.TempDir(), filepath.Join(unsafe, "redirect"))
	if _, err := secureDownloadBase(filepath.Join(unsafe, "redirect")); err == nil {
		t.Fatal("resolved away unsafe lexical ancestor")
	}
}

type downloadOwnerFixture struct {
	os.FileInfo
	uid   uint32
	links uint64
}

func (info downloadOwnerFixture) Sys() any {
	return &struct {
		Uid   uint32
		Nlink uint64
	}{info.uid, info.links}
}

func TestDownloadRejectsUnsafeFileIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []downloadOwnerFixture{{info, uint32(os.Geteuid() + 1), 1}, {info, uint32(os.Geteuid()), 2}} {
		if err := checkDownloadFileInfo(fixture); err == nil {
			t.Fatal("accepted foreign owner or multiple links")
		}
	}
	os.Chmod(path, 0o666)
	info, _ = os.Stat(path)
	if err := checkDownloadFileInfo(info); err == nil {
		t.Fatal("accepted externally writable staging file")
	}
}

func TestDownloadRejectsLocalPartialTampering(t *testing.T) {
	for _, mutation := range []string{"corrupt", "truncate", "symlink", "replaced directory"} {
		t.Run(mutation, func(t *testing.T) {
			dir := t.TempDir()
			dst := filepath.Join(dir, "file")
			if err := os.WriteFile(dst, []byte("original"), 0o600); err != nil {
				t.Fatal(err)
			}
			err := downloadPolicyFixture(t, dir, TransferPolicy{Existing: "overwrite"}, nil, func(_, _ int, bytes, _ int64) {
				if bytes != 3 {
					return
				}
				partial := filepath.Join(dir, downloadStageName, downloadStagePart)
				switch mutation {
				case "corrupt":
					os.WriteFile(partial, []byte("bad"), 0o600)
				case "truncate":
					os.WriteFile(partial, []byte("x"), 0o600)
				case "symlink":
					os.Remove(partial)
					os.Symlink(dst, partial)
				case "replaced directory":
					os.Rename(filepath.Join(dir, downloadStageName), filepath.Join(dir, "displaced"))
					os.Mkdir(filepath.Join(dir, downloadStageName), 0o700)
					os.WriteFile(partial, []byte("replacement"), 0o600)
				}
			})
			if err == nil {
				t.Fatal("published altered partial")
			}
			got, readErr := os.ReadFile(dst)
			if readErr != nil || string(got) != "original" {
				t.Fatalf("original changed: %q %v", got, readErr)
			}
			if mutation == "replaced directory" {
				data, err := os.ReadFile(filepath.Join(dir, downloadStageName, downloadStagePart))
				if err != nil || string(data) != "replacement" {
					t.Fatal("removed replacement directory contents")
				}
			}
		})
	}
}

func TestDownloadResumeAlwaysRejected(t *testing.T) {
	for _, capable := range []bool{false, true} {
		ft := &FileTransfer{resumeEnabled: capable, policy: TransferPolicy{Resume: true}}
		if err := ft.Receive("/remote/file", t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), "resume is unavailable") {
			t.Fatalf("error=%v", err)
		}
	}
}

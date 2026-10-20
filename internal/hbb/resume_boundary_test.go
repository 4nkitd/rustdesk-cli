package hbb

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/4nkitd/rustdesk-cli/internal/hbb/proto"
)

func TestResumeBoundaryRejectedWithoutMutation(t *testing.T) {
	for _, written := range []uint64{0, 6, 1<<32 - 1, 1<<32 + 1, 1<<63 - 1, 1 << 63, ^uint64(0)} {
		t.Run(fmt.Sprint(written), func(t *testing.T) {
			dir := t.TempDir()
			metadata, err := json.Marshal(map[string]any{"Peer": "forged", "Path": "/remote/file", "Entry": strings.Repeat("x", 20*1024), "Size": ^uint64(0), "Written": written, "SHA256": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"})
			if err != nil {
				t.Fatal(err)
			}
			fixtures := map[string][]byte{"file": []byte("original"), "file.download": []byte("prefixTAIL"), "file.download.rdcli": metadata}
			before := map[string]os.FileInfo{}
			for name, data := range fixtures {
				path := filepath.Join(dir, name)
				if err := os.WriteFile(path, data, 0o640); err != nil {
					t.Fatal(err)
				}
				before[name], err = os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
			}
			ft := &FileTransfer{policy: TransferPolicy{Existing: "overwrite", Resume: true}}
			err = ft.Receive("/remote/file", dir, nil)
			if err == nil || !strings.Contains(err.Error(), "resume is unavailable") {
				t.Fatalf("error=%v", err)
			}
			for name, want := range fixtures {
				path := filepath.Join(dir, name)
				got, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(got, want) {
					t.Fatalf("%s changed: %v", name, err)
				}
				after, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if !os.SameFile(before[name], after) || before[name].Mode() != after.Mode() || !before[name].ModTime().Equal(after.ModTime()) {
					t.Fatalf("%s metadata changed", name)
				}
			}
		})
	}
}

func TestDownloadManifestBoundsBeforeFilesystemMutation(t *testing.T) {
	entry := func(name string) *pb.FileEntry { return &pb.FileEntry{Name: name, EntryType: pb.FileType_File} }
	count := make([]*pb.FileEntry, maxDownloadFiles+1)
	for i := range count {
		count[i] = entry(fmt.Sprint(i))
	}
	aggregate := make([]*pb.FileEntry, 9000)
	for i := range aggregate {
		aggregate[i] = entry(strings.Repeat("a", 250) + "/" + fmt.Sprintf("%04d", i) + strings.Repeat("b", 246))
	}
	for name, files := range map[string][]*pb.FileEntry{
		"count":                              count,
		"aggregate bytes":                    aggregate,
		"depth":                              {entry(strings.Repeat("a/", maxDownloadDepth) + "file")},
		"component bytes":                    {entry(strings.Repeat("a", maxDownloadNameBytes+1))},
		"path bytes":                         {entry(strings.Repeat(strings.Repeat("a", 250)+"/", 17) + "file")},
		"oversized original single filename": {entry(strings.Repeat("x", 20*1024))},
		"reserved staging name":              {entry(downloadStageName + "/part")},
		"nil entry":                          {nil},
		"unsupported file type":              {{Name: "link", EntryType: pb.FileType_FileLink}},
		"size overflow":                      {{Name: "huge", EntryType: pb.FileType_File, Size: ^uint64(0)}},
	} {
		t.Run(name, func(t *testing.T) {
			base := filepath.Join(t.TempDir(), "uncreated")
			root, _, err := downloadRoot(filepath.Join(base, "renamed-output"), files)
			if root != nil {
				root.Close()
			}
			if err == nil {
				t.Fatal("accepted oversized or invalid manifest")
			}
			if _, err := os.Stat(base); !os.IsNotExist(err) {
				t.Fatal("invalid manifest changed filesystem")
			}
		})
	}
}

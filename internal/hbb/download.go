package hbb

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	pb "github.com/4nkitd/rustdesk-cli/internal/hbb/proto"
)

const (
	maxDownloadFiles         = 10000
	maxDownloadPathBytes     = 4096
	maxDownloadNameBytes     = 255
	maxDownloadDepth         = 32
	maxDownloadManifestBytes = 4 * 1024 * 1024
	downloadStageName        = ".rdcli-download"
	downloadStagePart        = "part.download"
)

func downloadName(name string) (string, error) {
	if len(name) > maxDownloadPathBytes {
		return "", errors.New("remote filename exceeds path byte limit")
	}
	name = strings.ReplaceAll(name, `\`, "/")
	if name == "" || strings.ContainsAny(name, ":\x00") {
		return "", fmt.Errorf("unsafe remote filename %q", name)
	}
	parts := strings.Split(name, "/")
	if len(parts) > maxDownloadDepth {
		return "", errors.New("remote filename exceeds download depth limit")
	}
	for _, part := range parts {
		if part == ".." || part == "." || part == "" || len(part) > maxDownloadNameBytes || strings.EqualFold(part, downloadStageName) {
			return "", fmt.Errorf("unsafe remote filename %q", name)
		}
	}
	name = filepath.FromSlash(name)
	if !filepath.IsLocal(name) {
		return "", fmt.Errorf("unsafe remote filename %q", name)
	}
	return name, nil
}

func downloadRoot(localPath string, files []*pb.FileEntry) (*os.Root, []string, error) {
	if len(files) > maxDownloadFiles {
		return nil, nil, errors.New("download manifest exceeds file count limit")
	}
	names := make([]string, len(files))
	seen := map[string]bool{}
	nameBytes := 0
	var totalBytes uint64
	for i, entry := range files {
		if entry == nil {
			return nil, nil, errors.New("nil download manifest entry")
		}
		if entry.GetEntryType() != pb.FileType_File && entry.GetEntryType() != pb.FileType_Dir {
			return nil, nil, errors.New("unsupported remote file type")
		}
		if entry.GetSize() > uint64(^uint64(0)>>1)-totalBytes {
			return nil, nil, errors.New("download size exceeds supported limit")
		}
		totalBytes += entry.GetSize()
		nameBytes += len(entry.GetName())
		if nameBytes > maxDownloadManifestBytes {
			return nil, nil, errors.New("download manifest exceeds filename byte limit")
		}
		name, err := downloadName(entry.GetName())
		if err != nil {
			return nil, nil, err
		}
		key := strings.ToLower(name)
		if seen[key] || seen[key+".download"] || strings.HasSuffix(key, ".download") || strings.Contains(key, ".download.") {
			return nil, nil, fmt.Errorf("duplicate or reserved download filename %q", name)
		}
		seen[key] = true
		names[i] = name
	}
	base := localPath
	single := !strings.HasSuffix(localPath, string(filepath.Separator))
	if fi, err := os.Lstat(filepath.Clean(localPath)); err == nil {
		if fi.IsDir() {
			single = false
		}
		if fi.Mode()&os.ModeSymlink != 0 && (!single || len(files) != 1 || files[0].GetEntryType() == pb.FileType_Dir) {
			return nil, nil, errors.New("download directory is a symlink")
		}
	}
	if single && len(files) == 1 && files[0].GetEntryType() != pb.FileType_Dir {
		base = filepath.Dir(localPath)
		names[0] = filepath.Base(localPath)
		if _, err := downloadName(names[0]); err != nil {
			return nil, nil, err
		}
	}
	base, err := secureDownloadBase(base)
	if err != nil {
		return nil, nil, err
	}
	root, err := os.OpenRoot(base)
	if err == nil {
		if checkErr := checkDownloadParents(root, "file"); checkErr != nil {
			root.Close()
			return nil, nil, checkErr
		}
	}
	return root, names, err
}

func openDownload(root *os.Root, name string) (*os.File, error) {
	if err := checkDownloadParents(root, name); err != nil {
		return nil, err
	}
	if err := root.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		return nil, err
	}
	partial := name + ".download"
	f, err := root.OpenFile(partial, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err == nil {
		err = checkDownloadFileInfo(info)
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func checkDownloadParents(root *os.Root, name string) error {
	for parent := filepath.Dir(name); ; parent = filepath.Dir(parent) {
		info, err := root.Lstat(parent)
		if errors.Is(err, os.ErrNotExist) {
			if parent == "." {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if err := checkDownloadDirectoryInfo(info, false); err != nil {
			return fmt.Errorf("unsafe download directory %s: %w", parent, err)
		}
		if parent == "." {
			break
		}
	}
	return nil
}

// Unix ownership fields are inspected without OS-specific imports. Unsupported
// platforms fail closed rather than infer ownership from permission bits alone.
func downloadOwner(info os.FileInfo) (uint64, uint64, error) {
	value := reflect.ValueOf(info.Sys())
	if value.Kind() == reflect.Pointer && !value.IsNil() {
		value = value.Elem()
	}
	if os.Geteuid() < 0 || value.Kind() != reflect.Struct {
		return 0, 0, errors.New("download ownership checks are unavailable on this platform")
	}
	uid, links := value.FieldByName("Uid"), value.FieldByName("Nlink")
	if !uid.IsValid() || !links.IsValid() || !uid.CanUint() || !links.CanUint() {
		return 0, 0, errors.New("download ownership checks are unavailable on this filesystem")
	}
	return uid.Uint(), links.Uint(), nil
}

func checkDownloadDirectoryInfo(info os.FileInfo, ancestor bool) error {
	uid, _, err := downloadOwner(info)
	if err != nil {
		return err
	}
	if !info.IsDir() || (uid != uint64(os.Geteuid()) && !(ancestor && uid == 0)) {
		return errors.New("directory is not owned by the current user or a trusted ancestor owner")
	}
	if info.Mode().Perm()&0o022 != 0 && !(ancestor && info.Mode()&os.ModeSticky != 0) {
		return errors.New("directory is writable by another user")
	}
	return nil
}

func checkDownloadFileInfo(info os.FileInfo) error {
	uid, links, err := downloadOwner(info)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || uid != uint64(os.Geteuid()) || info.Mode().Perm() != 0o600 || links != 1 {
		return errors.New("download staging file has unsafe ownership, permissions, type, or hard links")
	}
	return nil
}

func secureDownloadBase(base string) (string, error) {
	absolute, err := filepath.Abs(base)
	if err != nil {
		return "", err
	}
	existing := absolute
	for {
		_, err := os.Lstat(existing)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return "", err
		}
		existing = parent
	}
	if err := checkDownloadAncestors(existing); err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", err
	}
	if err := checkDownloadAncestors(resolved); err != nil {
		return "", err
	}
	relative, err := filepath.Rel(existing, absolute)
	if err != nil {
		return "", err
	}
	base = filepath.Join(resolved, relative)
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", err
	}
	info, err := os.Lstat(base)
	if err != nil {
		return "", err
	}
	if err := checkDownloadDirectoryInfo(info, false); err != nil {
		return "", err
	}
	return base, nil
}

func checkDownloadAncestors(path string) error {
	for ; ; path = filepath.Dir(path) {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			uid, _, err := downloadOwner(info)
			if err != nil {
				return err
			}
			if uid != 0 && uid != uint64(os.Geteuid()) {
				return fmt.Errorf("untrusted download ancestor symlink: %s", path)
			}
		} else if err := checkDownloadDirectoryInfo(info, true); err != nil {
			return fmt.Errorf("unsafe download ancestor %s: %w", path, err)
		}
		if filepath.Dir(path) == path {
			break
		}
	}
	return nil
}

type downloadStage struct {
	root        *os.Root
	destination *os.Root
	identity    os.FileInfo
	part        os.FileInfo
}

func newDownloadStage(root *os.Root) (*downloadStage, error) {
	if err := checkDownloadParents(root, "file"); err != nil {
		return nil, err
	}
	if err := root.Mkdir(downloadStageName, 0o700); err != nil {
		return nil, fmt.Errorf("create private download staging directory; refusing to adopt another job: %w", err)
	}
	info, err := root.Lstat(downloadStageName)
	if err != nil {
		return nil, err
	}
	if err := checkDownloadDirectoryInfo(info, false); err != nil {
		return nil, err
	}
	if info.Mode().Perm() != 0o700 {
		return nil, errors.New("download staging directory is not private")
	}
	stage, err := root.OpenRoot(downloadStageName)
	if err != nil {
		return nil, err
	}
	opened, err := stage.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		stage.Close()
		return nil, errors.New("download staging directory changed while opening")
	}
	return &downloadStage{root: stage, destination: root, identity: opened}, nil
}

func (stage *downloadStage) open() (*os.File, error) {
	f, err := openDownload(stage.root, "part")
	if err != nil {
		return nil, err
	}
	stage.part, err = f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func (stage *downloadStage) verify(f *os.File) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if err := checkDownloadFileInfo(info); err != nil {
		return err
	}
	part, err := stage.root.Lstat(downloadStagePart)
	if err != nil {
		return err
	}
	if !part.Mode().IsRegular() || !os.SameFile(info, part) || !os.SameFile(stage.part, part) {
		return errors.New("download staging file was replaced")
	}
	dir, err := stage.destination.Lstat(downloadStageName)
	if err != nil {
		return err
	}
	if err := checkDownloadDirectoryInfo(dir, false); err != nil {
		return err
	}
	if !os.SameFile(stage.identity, dir) || dir.Mode().Perm() != 0o700 {
		return errors.New("download staging directory was replaced or exposed")
	}
	return checkDownloadParents(stage.destination, "file")
}

func (stage *downloadStage) close() {
	if stage.part != nil {
		if info, err := stage.root.Lstat(downloadStagePart); err == nil && info.Mode().IsRegular() && os.SameFile(stage.part, info) {
			_ = stage.root.Remove(downloadStagePart)
		}
	}
	stage.root.Close()
	if info, err := stage.destination.Lstat(downloadStageName); err == nil && os.SameFile(stage.identity, info) {
		_ = stage.destination.Remove(downloadStageName)
	}
}

func (ft *FileTransfer) receiveFiles(remotePath, localPath string, progress ProgressFunc) error {
	if ft.policy.Resume {
		return errors.New("download resume is unavailable: the protocol cannot prove remote-prefix freshness; retry a fresh transfer")
	}
	id := ft.nextJobID()
	if err := ft.sendAction(&pb.FileAction{Union: &pb.FileAction_Send{Send: &pb.FileTransferSendRequest{
		Id: id, Path: remotePath, FileType: pb.FileTransferSendRequest_Generic, IncludeHidden: ft.policy.IncludeHidden,
	}}}); err != nil {
		return err
	}
	var root *os.Root
	var files []*pb.FileEntry
	var names []string
	var cur *os.File
	var stage *downloadStage
	var written uint64
	var digestHash = sha256.New()
	confirmed := false
	next := int32(0)
	skipped := false
	var doneBytes, totalBytes int64
	defer func() {
		closeFile(cur)
		if stage != nil {
			stage.close()
		}
		if root != nil {
			root.Close()
		}
	}()
	report := func() {
		if progress != nil {
			progress(int(next), len(files), doneBytes, totalBytes)
		}
	}
	finish := func() error {
		name, entry := names[next], files[next]
		if skipped {
			doneBytes += int64(entry.GetSize())
		} else if entry.GetEntryType() == pb.FileType_Dir {
			if info, err := root.Lstat(name); err == nil && !info.IsDir() {
				return fmt.Errorf("download directory is not a directory: %s", name)
			} else if err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err := checkDownloadParents(root, filepath.Join(name, "file")); err != nil {
				return err
			}
			if err := root.MkdirAll(name, 0o700); err != nil {
				return err
			}
		} else {
			if written != entry.GetSize() {
				return fmt.Errorf("incomplete download %q: got %d of %d bytes", name, written, entry.GetSize())
			}
			if cur == nil {
				var err error
				cur, err = stage.open()
				if err != nil {
					return err
				}
			}
			if err := cur.Sync(); err != nil {
				return err
			}
			info, err := cur.Stat()
			if err != nil {
				return err
			}
			if uint64(info.Size()) != entry.GetSize() {
				return fmt.Errorf("download size changed before publication: %s", name)
			}
			if _, err := cur.Seek(0, io.SeekStart); err != nil {
				return err
			}
			actual := sha256.New()
			if _, err := io.Copy(actual, cur); err != nil {
				return err
			}
			if hex.EncodeToString(actual.Sum(nil)) != hex.EncodeToString(digestHash.Sum(nil)) {
				return fmt.Errorf("download data changed before publication: %s", name)
			}
			if err := stage.verify(cur); err != nil {
				return err
			}
			if err := checkDownloadParents(root, name); err != nil {
				return err
			}
			if err := root.MkdirAll(filepath.Dir(name), 0o700); err != nil {
				return err
			}
			if err := cur.Close(); err != nil {
				return err
			}
			cur = nil
			partial := filepath.Join(downloadStageName, downloadStagePart)
			if ft.policy.Existing == "skip" || ft.policy.Existing == "error" {
				if err := root.Link(partial, name); err != nil {
					return err
				}
				if err := stage.root.Remove(downloadStagePart); err != nil {
					return err
				}
			} else if err := root.Rename(partial, name); err != nil {
				return err
			}
			stage.part = nil
			if entry.GetModifiedTime() > 0 {
				mt := time.Unix(int64(entry.GetModifiedTime()), 0)
				if err := root.Chtimes(name, mt, mt); err != nil {
					return err
				}
			}
		}
		next++
		written, skipped, confirmed = 0, false, false
		digestHash = sha256.New()
		report()
		return nil
	}
	check := func(num int32) error {
		if root == nil || num < next || int(num) >= len(files) {
			return fmt.Errorf("unexpected file number %d (expected %d)", num, next)
		}
		for next < num {
			if err := finish(); err != nil {
				return err
			}
		}
		return nil
	}
	for {
		msg, err := ft.conn.Recv()
		if err != nil {
			return err
		}
		if td := msg.GetTestDelay(); td != nil {
			if err := ft.conn.Send(&pb.Message{Union: &pb.Message_TestDelay{TestDelay: td}}); err != nil {
				return err
			}
		}
		fr := msg.GetFileResponse()
		if fr == nil {
			continue
		}
		switch u := fr.GetUnion().(type) {
		case *pb.FileResponse_Error:
			if u.Error.GetId() == id {
				if skipped && next == int32(len(files)-1) && u.Error.GetError() == "skipped" {
					return finish()
				}
				return ft.conn.PeerError(u.Error.GetError())
			}
		case *pb.FileResponse_Dir:
			if root != nil || u.Dir.GetId() != id {
				continue
			}
			files = u.Dir.GetEntries()
			// RustDesk uses an empty entry name when the request names a single file.
			if len(files) == 1 && files[0] != nil && files[0].GetName() == "" {
				files[0].Name = filepath.Base(strings.ReplaceAll(remotePath, `\`, "/"))
			}
			root, names, err = downloadRoot(localPath, files)
			if err != nil {
				return err
			}
			for _, f := range files {
				if f.GetEntryType() != pb.FileType_File && f.GetEntryType() != pb.FileType_Dir {
					return errors.New("unsupported remote file type")
				}
				if f.GetSize() > uint64(^uint64(0)>>1)-uint64(totalBytes) {
					return errors.New("download size exceeds supported limit")
				}
				totalBytes += int64(f.GetSize())
			}
			for _, name := range names {
				if err := checkDownloadParents(root, name); err != nil {
					return err
				}
			}
			report()
			if len(files) == 0 {
				return nil
			}
			stage, err = newDownloadStage(root)
			if err != nil {
				return err
			}
		case *pb.FileResponse_Digest:
			d := u.Digest
			if d.GetId() != id {
				continue
			}
			if err := check(d.GetFileNum()); err != nil {
				return err
			}
			if cur != nil || skipped || confirmed {
				return errors.New("unexpected digest after file data")
			}
			if d.GetIsUpload() || d.GetFileSize() != files[next].GetSize() || d.GetLastModified() != files[next].GetModifiedTime() {
				return errors.New("download source metadata changed after listing")
			}
			fi, statErr := root.Lstat(names[next])
			if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
				return statErr
			}
			if statErr == nil && ft.policy.Existing == "error" {
				return fmt.Errorf("destination exists: %s (use --overwrite or --skip)", names[next])
			}
			skipped = statErr == nil && (ft.policy.Existing == "skip" || (ft.policy.Existing == "" && fi.Mode().IsRegular() && fi.Size() == int64(d.GetFileSize()) && fi.ModTime().Unix() == int64(d.GetLastModified())))
			req := &pb.FileTransferSendConfirmRequest{Id: id, FileNum: next}
			if skipped {
				req.Union = &pb.FileTransferSendConfirmRequest_Skip{Skip: true}
			} else {
				cur, err = stage.open()
				if err != nil {
					return err
				}
				req.Union = &pb.FileTransferSendConfirmRequest_OffsetBlk{OffsetBlk: 0}
			}
			confirmed = true
			if err := ft.sendAction(&pb.FileAction{Union: &pb.FileAction_SendConfirm{SendConfirm: req}}); err != nil {
				return err
			}
			report()
		case *pb.FileResponse_Block:
			b := u.Block
			if b.GetId() != id {
				continue
			}
			if err := check(b.GetFileNum()); err != nil {
				return err
			}
			if (ft.policy.Existing == "skip" || ft.policy.Existing == "error") && !confirmed {
				if _, err := root.Lstat(names[next]); err == nil {
					return fmt.Errorf("peer sent data without skip confirmation for %s", names[next])
				} else if !errors.Is(err, os.ErrNotExist) {
					return err
				}
			}
			if skipped || files[next].GetEntryType() == pb.FileType_Dir {
				return errors.New("unexpected data for skipped file or directory")
			}
			data := b.GetData()
			if b.GetCompressed() {
				data, err = zstdDec.DecodeAll(data, nil)
				if err != nil {
					return fmt.Errorf("decompress block: %w", err)
				}
			}
			if uint64(len(data)) > files[next].GetSize()-written {
				return errors.New("download exceeds advertised file size")
			}
			if cur == nil {
				cur, err = stage.open()
				if err != nil {
					return err
				}
			}
			n, err := cur.Write(data)
			if err != nil {
				return err
			}
			written += uint64(n)
			digestHash.Write(data[:n])
			doneBytes += int64(n)
			report()
		case *pb.FileResponse_Done:
			d := u.Done
			if d.GetId() != id {
				continue
			}
			num := d.GetFileNum()
			// Upstream sends one job-level Done with file_num equal to the file count.
			if root != nil && num == int32(len(files)) {
				num--
			}
			if err := check(num); err != nil {
				return err
			}
			if err := finish(); err != nil {
				return err
			}
			if int(next) == len(files) {
				return nil
			}
		}
	}
}

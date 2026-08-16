package hbb

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"

	pb "rdcli/internal/hbb/proto"
)

const (
	fileBlockSize       = 64 * 1024
	maxDecompressedSize = 256 * 1024 * 1024
)

var (
	zstdDec, _ = zstd.NewReader(nil, zstd.WithDecoderMaxMemory(maxDecompressedSize))
	zstdEnc, _ = zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
)

// ProgressFunc receives transfer progress updates. TotalBytes may be 0 until
// the file list is known.
type ProgressFunc func(filesDone, filesTotal int, doneBytes, totalBytes int64)

// FileTransfer is a file-transfer session with a peer.
type FileTransfer struct {
	conn             *Conn
	overwriteEnabled bool
	nextID           int32
}

// NewFileTransfer connects to the peer with a file-transfer login. dir is the
// initial remote directory used in the login request.
func NewFileTransfer(opts DialOpts, dir string) (*FileTransfer, error) {
	opts.ConnType = pb.ConnType_FILE_TRANSFER
	c, err := Connect(opts)
	if err != nil {
		return nil, err
	}
	pi, err := c.Login(LoginFileTransfer, LoginSpec{Dir: dir})
	if err != nil {
		c.Close()
		return nil, err
	}
	return &FileTransfer{
		conn:             c,
		overwriteEnabled: versionAtLeast(pi.GetVersion(), "1.1.10"),
		nextID:           1,
	}, nil
}

func (ft *FileTransfer) Close() error {
	return ft.conn.Close()
}

func (ft *FileTransfer) nextJobID() int32 {
	id := ft.nextID
	ft.nextID++
	return id
}

func (ft *FileTransfer) sendAction(a *pb.FileAction) error {
	return ft.conn.Send(&pb.Message{Union: &pb.Message_FileAction{FileAction: a}})
}

func (ft *FileTransfer) sendBlock(b *pb.FileTransferBlock) error {
	return ft.conn.Send(&pb.Message{Union: &pb.Message_FileResponse{FileResponse: &pb.FileResponse{
		Union: &pb.FileResponse_Block{Block: b},
	}}})
}

func (ft *FileTransfer) sendDone(id int32, fileNum int32) error {
	return ft.conn.Send(&pb.Message{Union: &pb.Message_FileResponse{FileResponse: &pb.FileResponse{
		Union: &pb.FileResponse_Done{Done: &pb.FileTransferDone{Id: id, FileNum: fileNum}},
	}}})
}

// ReadDir lists a remote directory.
func (ft *FileTransfer) ReadDir(path string) (*pb.FileDirectory, error) {
	if err := ft.sendAction(&pb.FileAction{
		Union: &pb.FileAction_ReadDir{ReadDir: &pb.ReadDir{Path: path}},
	}); err != nil {
		return nil, err
	}
	for {
		msg, err := ft.conn.Recv()
		if err != nil {
			return nil, err
		}
		fr := msg.GetFileResponse()
		if fr == nil {
			continue
		}
		switch u := fr.GetUnion().(type) {
		case *pb.FileResponse_Dir:
			return u.Dir, nil
		case *pb.FileResponse_Error:
			return nil, errors.New(u.Error.GetError())
		default:
			return nil, fmt.Errorf("unexpected file response %T", u)
		}
	}
}

// CreateDir creates a remote directory.
func (ft *FileTransfer) CreateDir(path string) error {
	id := ft.nextJobID()
	if err := ft.sendAction(&pb.FileAction{
		Union: &pb.FileAction_Create{Create: &pb.FileDirCreate{Id: id, Path: path}},
	}); err != nil {
		return err
	}
	for {
		msg, err := ft.conn.Recv()
		if err != nil {
			return err
		}
		fr := msg.GetFileResponse()
		if fr == nil {
			continue
		}
		switch u := fr.GetUnion().(type) {
		case *pb.FileResponse_Done:
			return nil
		case *pb.FileResponse_Error:
			return errors.New(u.Error.GetError())
		}
	}
}

type job struct {
	localPath  string
	remotePath string
	files      []*pb.FileEntry
}

// Send uploads localPath to remoteDir. Directories are walked recursively
// when recursive is true; each remote directory is created first and files
// are pushed with one receive job per directory.
func (ft *FileTransfer) Send(localPath, remoteDir string, recursive bool, progress ProgressFunc) error {
	jobs, err := ft.buildSendJobs(localPath, remoteDir, recursive)
	if err != nil {
		return err
	}
	if len(jobs) == 0 {
		return nil
	}
	if err := ft.ensureRemoteDirs(remoteDir, jobs); err != nil {
		return err
	}

	totalFiles := 0
	totalBytes := int64(0)
	for _, j := range jobs {
		totalFiles += len(j.files)
		for _, f := range j.files {
			totalBytes += int64(f.GetSize())
		}
	}
	filesDone := 0
	doneBytes := int64(0)
	if progress != nil {
		progress(filesDone, totalFiles, doneBytes, totalBytes)
	}

	for _, j := range jobs {
		if len(j.files) == 0 {
			continue
		}
		n, d, err := ft.sendJob(j, progress, filesDone, totalFiles, doneBytes, totalBytes)
		filesDone += n
		doneBytes += d
		if err != nil {
			return err
		}
		if progress != nil {
			progress(filesDone, totalFiles, doneBytes, totalBytes)
		}
	}
	return nil
}

func (ft *FileTransfer) buildSendJobs(localPath, remoteDir string, recursive bool) ([]job, error) {
	fi, err := os.Stat(localPath)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", localPath, err)
	}
	if !fi.IsDir() {
		return []job{{localPath: filepath.Dir(localPath), remotePath: remoteDir, files: []*pb.FileEntry{localEntry(localPath, fi)}}}, nil
	}
	if !recursive {
		return nil, fmt.Errorf("%s is a directory (use -r to copy recursively)", localPath)
	}
	var jobs []job
	err = filepath.Walk(localPath, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return nil
		}
		rel, rErr := filepath.Rel(localPath, p)
		if rErr != nil {
			return rErr
		}
		remote := remoteDir
		if rel != "." {
			remote = filepath.Join(remoteDir, filepath.FromSlash(rel))
		}
		entries, eErr := readLocalDirEntries(p)
		if eErr != nil {
			return eErr
		}
		jobs = append(jobs, job{localPath: p, remotePath: remote, files: entries})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return jobs, nil
}

func (ft *FileTransfer) ensureRemoteDirs(remoteDir string, jobs []job) error {
	seen := map[string]bool{}
	var dirs []string
	for _, j := range jobs {
		p := j.remotePath
		for {
			if !seen[p] {
				seen[p] = true
				dirs = append(dirs, p)
			}
			if p == remoteDir {
				break
			}
			parent := filepath.Dir(p)
			if parent == p || len(parent) < len(remoteDir) {
				break
			}
			p = parent
		}
	}
	sort.Slice(dirs, func(a, b int) bool {
		da := strings.Count(dirs[a], string(filepath.Separator))
		db := strings.Count(dirs[b], string(filepath.Separator))
		if da != db {
			return da < db
		}
		return dirs[a] < dirs[b]
	})
	for _, d := range dirs {
		if err := ft.CreateDir(d); err != nil && !strings.Contains(err.Error(), "exists") {
			return fmt.Errorf("create remote dir %s: %w", d, err)
		}
	}
	return nil
}

func (ft *FileTransfer) sendJob(j job, progress ProgressFunc, filesDone, totalFiles int, doneBytes, totalBytes int64) (int, int64, error) {
	id := ft.nextJobID()
	var total int64
	for _, f := range j.files {
		total += int64(f.GetSize())
	}
	if err := ft.sendAction(&pb.FileAction{
		Union: &pb.FileAction_Receive{Receive: &pb.FileTransferReceiveRequest{
			Id:        id,
			Path:      j.remotePath,
			Files:     j.files,
			FileNum:   0,
			TotalSize: uint64(total),
		}},
	}); err != nil {
		return 0, 0, err
	}

	sent := int64(0)
	fileIdx := 0
	for fileIdx < len(j.files) {
		entry := j.files[fileIdx]
		offset := uint32(0)
		if ft.overwriteEnabled {
			d, err := ft.waitUploadDigest(id, fileIdx)
			if err != nil {
				return 0, 0, err
			}
			if d == nil {
				filesDone++
				doneBytes += int64(entry.GetSize())
				if progress != nil {
					progress(filesDone, totalFiles, doneBytes, totalBytes)
				}
				fileIdx++
				continue
			}
			if d.GetIsIdentical() && d.GetIsResume() && d.GetTransferredSize() > 0 {
				offset = uint32(d.GetTransferredSize())
			}
			if err := ft.sendAction(&pb.FileAction{
				Union: &pb.FileAction_SendConfirm{SendConfirm: &pb.FileTransferSendConfirmRequest{
					Id:      id,
					FileNum: int32(fileIdx),
					Union:   &pb.FileTransferSendConfirmRequest_OffsetBlk{OffsetBlk: offset},
				}},
			}); err != nil {
				return 0, 0, err
			}
		}
		n, err := ft.streamFile(id, int32(fileIdx), filepath.Join(j.localPath, entry.GetName()), offset)
		if err != nil {
			return 0, 0, err
		}
		fileIdx++
		filesDone++
		doneBytes += n
		sent += n
		if progress != nil {
			progress(filesDone, totalFiles, doneBytes, totalBytes)
		}
	}
	if len(j.files) > 0 {
		if err := ft.sendDone(id, int32(len(j.files)-1)); err != nil {
			return 0, 0, err
		}
	}
	return len(j.files), sent, nil
}

// waitUploadDigest waits for the peer's overwrite-detection digest for the
// given file of an upload job. It returns nil when the peer asks us to skip
// the file, or the digest when the transfer should proceed. The caller sends
// the SendConfirm with the chosen offset.
func (ft *FileTransfer) waitUploadDigest(id int32, fileIdx int) (*pb.FileTransferDigest, error) {
	for {
		msg, err := ft.conn.Recv()
		if err != nil {
			return nil, err
		}
		if fa := msg.GetFileAction(); fa != nil {
			if sc := fa.GetSendConfirm(); sc != nil && sc.GetId() == id && sc.GetFileNum() == int32(fileIdx) && sc.GetSkip() {
				return nil, nil
			}
			continue
		}
		fr := msg.GetFileResponse()
		if fr == nil {
			continue
		}
		switch u := fr.GetUnion().(type) {
		case *pb.FileResponse_Digest:
			d := u.Digest
			if d.GetId() != id || d.GetFileNum() != int32(fileIdx) {
				continue
			}
			return d, nil
		case *pb.FileResponse_Error:
			return nil, errors.New(u.Error.GetError())
		}
	}
}

// streamFile sends one local file as FileResponse blocks followed by a done.
func (ft *FileTransfer) streamFile(id, fileNum int32, path string, offset uint32) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	if offset > 0 {
		if _, err := f.Seek(int64(offset), io.SeekStart); err != nil {
			return 0, err
		}
	}
	buf := make([]byte, fileBlockSize)
	var sent int64
	for {
		n, rErr := io.ReadFull(f, buf)
		if rErr != nil && rErr != io.ErrUnexpectedEOF && rErr != io.EOF {
			return 0, fmt.Errorf("read %s: %w", path, rErr)
		}
		if n == 0 {
			break
		}
		data := buf[:n]
		compressed := false
		if !isCompressedName(path) {
			if enc := zstdEnc.EncodeAll(data, nil); len(enc) < len(data) {
				data = enc
				compressed = true
			}
		}
		if err := ft.sendBlock(&pb.FileTransferBlock{Id: id, FileNum: fileNum, Data: data, Compressed: compressed}); err != nil {
			return 0, err
		}
		sent += int64(n)
		if rErr != nil {
			break
		}
	}
	if err := ft.sendDone(id, fileNum); err != nil {
		return 0, err
	}
	return sent, nil
}

// Receive downloads remotePath to localPath (file or recursive tree).
func (ft *FileTransfer) Receive(remotePath, localPath string, progress ProgressFunc) error {
	id := ft.nextJobID()
	if err := ft.sendAction(&pb.FileAction{
		Union: &pb.FileAction_Send{Send: &pb.FileTransferSendRequest{
			Id:       id,
			Path:     remotePath,
			FileNum:  0,
			FileType: pb.FileTransferSendRequest_Generic,
		}},
	}); err != nil {
		return err
	}

	var files []*pb.FileEntry
	var cur *os.File
	curNum := int32(-1)
	skipped := map[int32]bool{}
	filesDone := 0
	doneBytes := int64(0)
	totalBytes := int64(0)
	singleTarget := !strings.HasSuffix(localPath, string(filepath.Separator))
	if fi, err := os.Stat(localPath); err == nil && fi.IsDir() {
		singleTarget = false
	}
	dstFor := func(num int32) string {
		if singleTarget && num == 0 && len(files) == 1 {
			return localPath
		}
		if int(num) >= len(files) {
			return localPath
		}
		return filepath.Join(localPath, filepath.FromSlash(files[num].GetName()))
	}
	report := func() {
		if progress != nil {
			progress(filesDone, len(files), doneBytes, totalBytes)
		}
	}

	for {
		msg, err := ft.conn.Recv()
		if err != nil {
			closeFile(cur)
			return err
		}
		fr := msg.GetFileResponse()
		if fr == nil {
			continue
		}
		switch u := fr.GetUnion().(type) {
		case *pb.FileResponse_Error:
			closeFile(cur)
			return errors.New(u.Error.GetError())
		case *pb.FileResponse_Dir:
			if files == nil {
				files = u.Dir.GetEntries()
				for _, f := range files {
					totalBytes += int64(f.GetSize())
				}
				report()
			}
		case *pb.FileResponse_Digest:
			d := u.Digest
			if d.GetId() != id {
				continue
			}
			idx := int(d.GetFileNum())
			if idx >= len(files) {
				continue
			}
			dst := dstFor(d.GetFileNum())
			skip, offset := ft.overwriteDecision(dst, d)
			req := &pb.FileTransferSendConfirmRequest{Id: id, FileNum: d.GetFileNum()}
			if skip {
				req.Union = &pb.FileTransferSendConfirmRequest_Skip{Skip: true}
				skipped[d.GetFileNum()] = true
			} else {
				req.Union = &pb.FileTransferSendConfirmRequest_OffsetBlk{OffsetBlk: offset}
			}
			if err := ft.sendAction(&pb.FileAction{Union: &pb.FileAction_SendConfirm{SendConfirm: req}}); err != nil {
				closeFile(cur)
				return err
			}
		case *pb.FileResponse_Block:
			b := u.Block
			if b.GetId() != id {
				continue
			}
			if b.GetFileNum() != curNum {
				closeFile(cur)
				cur = nil
				curNum = b.GetFileNum()
				if int(curNum) >= len(files) {
					return fmt.Errorf("file number %d out of range", curNum)
				}
				f, err := openDownloadFile(dstFor(curNum))
				if err != nil {
					return err
				}
				cur = f
			}
			data := b.GetData()
			if b.GetCompressed() {
				plain, err := zstdDec.DecodeAll(data, nil)
				if err != nil {
					closeFile(cur)
					return fmt.Errorf("decompress block: %w", err)
				}
				data = plain
			}
			if _, err := cur.Write(data); err != nil {
				closeFile(cur)
				return fmt.Errorf("write %s: %w", dstFor(curNum)+".download", err)
			}
			doneBytes += int64(len(data))
			report()
		case *pb.FileResponse_Done:
			d := u.Done
			if d.GetId() != id {
				continue
			}
			if int(d.GetFileNum()) < len(files) {
				closeFile(cur)
				cur = nil
				if !skipped[d.GetFileNum()] {
					dst := dstFor(d.GetFileNum())
					if err := finalizeDownload(dst, files[d.GetFileNum()]); err != nil {
						return err
					}
				}
				filesDone++
				report()
			}
			if int(d.GetFileNum()) == len(files)-1 {
				closeFile(cur)
				report()
				return nil
			}
		}
	}
}

// overwriteDecision decides skip/resume/overwrite for a download digest.
func (ft *FileTransfer) overwriteDecision(dst string, d *pb.FileTransferDigest) (skip bool, offset uint32) {
	if fi, err := os.Stat(dst); err == nil && !fi.IsDir() {
		if fi.Size() == int64(d.GetFileSize()) && fi.ModTime().Unix() == int64(d.GetLastModified()) {
			return true, 0
		}
	}
	digestPath := dst + ".digest"
	downloadPath := dst + ".download"
	if d.GetIsResume() {
		if data, err := os.ReadFile(digestPath); err == nil {
			var ld struct {
				Size     uint64 `json:"size"`
				Modified uint64 `json:"modified"`
			}
			if json.Unmarshal(data, &ld) == nil && ld.Size == d.GetFileSize() && ld.Modified == d.GetLastModified() {
				if fi, err := os.Stat(downloadPath); err == nil && fi.Size() > 0 {
					return false, uint32(fi.Size())
				}
			}
		}
	}
	return false, 0
}

func openDownloadFile(dst string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", filepath.Dir(dst), err)
	}
	downloadPath := dst + ".download"
	f, err := os.OpenFile(downloadPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", downloadPath, err)
	}
	return f, nil
}

func finalizeDownload(dst string, entry *pb.FileEntry) error {
	downloadPath := dst + ".download"
	_ = os.Remove(dst + ".digest")
	if err := os.Rename(downloadPath, dst); err != nil {
		return fmt.Errorf("finalize %s: %w", dst, err)
	}
	if entry.GetModifiedTime() > 0 {
		mt := time.Unix(int64(entry.GetModifiedTime()), 0)
		_ = os.Chtimes(dst, mt, mt)
	}
	return nil
}

func closeFile(f *os.File) {
	if f != nil {
		_ = f.Close()
	}
}

func readLocalDirEntries(dir string) ([]*pb.FileEntry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(a, b int) bool { return entries[a].Name() < entries[b].Name() })
	out := make([]*pb.FileEntry, 0, len(entries))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return nil, err
		}
		out = append(out, &pb.FileEntry{
			Name:         e.Name(),
			EntryType:    pb.FileType_File,
			Size:         uint64(info.Size()),
			ModifiedTime: uint64(info.ModTime().Unix()),
		})
	}
	return out, nil
}

func localEntry(path string, fi os.FileInfo) *pb.FileEntry {
	return &pb.FileEntry{
		Name:         filepath.Base(path),
		EntryType:    pb.FileType_File,
		Size:         uint64(fi.Size()),
		ModifiedTime: uint64(fi.ModTime().Unix()),
	}
}

func isCompressedName(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".xz", ".gz", ".zip", ".7z", ".rar", ".bz2", ".tgz", ".png", ".jpg":
		return true
	}
	return false
}

func versionAtLeast(v, min string) bool {
	a := parseVersion(v)
	b := parseVersion(min)
	for i := 0; i < 3; i++ {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return true
}

func parseVersion(v string) [3]int {
	var out [3]int
	parts := strings.Split(v, "-")
	nums := strings.Split(parts[0], ".")
	for i := 0; i < 3 && i < len(nums); i++ {
		var n int
		_, _ = fmt.Sscanf(nums[i], "%d", &n)
		out[i] = n
	}
	return out
}

func ListDir(opts DialOpts, remotePath string) ([]*pb.FileEntry, error) {
	ft, err := NewFileTransfer(opts, remotePath)
	if err != nil {
		return nil, err
	}
	defer ft.Close()
	dir, err := ft.ReadDir(remotePath)
	if err != nil {
		return nil, err
	}
	return dir.GetEntries(), nil
}

func ReadAllFilesList(opts DialOpts, remotePath string) ([]*pb.FileEntry, error) {
	ft, err := NewFileTransfer(opts, remotePath)
	if err != nil {
		return nil, err
	}
	defer ft.Close()
	id := ft.nextJobID()
	if err := ft.sendAction(&pb.FileAction{
		Union: &pb.FileAction_AllFiles{AllFiles: &pb.ReadAllFiles{Id: id, Path: remotePath}},
	}); err != nil {
		return nil, err
	}
	var entries []*pb.FileEntry
	for {
		msg, err := ft.conn.Recv()
		if err != nil {
			return nil, err
		}
		fr := msg.GetFileResponse()
		if fr == nil {
			continue
		}
		switch u := fr.GetUnion().(type) {
		case *pb.FileResponse_Dir:
			entries = append(entries, u.Dir.GetEntries()...)
		case *pb.FileResponse_Done:
			return entries, nil
		case *pb.FileResponse_Error:
			return nil, errors.New(u.Error.GetError())
		}
	}
}

type sendProgressAdapter struct{ fn func(uint64, uint64) }

func (a sendProgressAdapter) report(filesDone, filesTotal int, doneBytes, totalBytes int64) {
	if a.fn != nil {
		a.fn(uint64(doneBytes), uint64(totalBytes))
	}
}

func SendFile(opts DialOpts, localPath, remotePath string, recursive bool, onProgress func(sent, total uint64)) error {
	ft, err := NewFileTransfer(opts, remotePath)
	if err != nil {
		return err
	}
	defer ft.Close()
	var progress ProgressFunc
	if onProgress != nil {
		adapter := sendProgressAdapter{fn: onProgress}
		progress = adapter.report
	}
	return ft.Send(localPath, remotePath, recursive, progress)
}

func ReceiveFile(opts DialOpts, remotePath, localPath string, recursive bool, onProgress func(recv, total uint64)) error {
	ft, err := NewFileTransfer(opts, remotePath)
	if err != nil {
		return err
	}
	defer ft.Close()
	var progress ProgressFunc
	if onProgress != nil {
		adapter := sendProgressAdapter{fn: onProgress}
		progress = adapter.report
	}
	return ft.Receive(remotePath, localPath, progress)
}

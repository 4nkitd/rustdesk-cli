package hbb

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/klauspost/compress/zstd"

	pb "github.com/4nkitd/rustdesk-cli/internal/hbb/proto"
)

const (
	fileBlockSize            = 64 * 1024
	maxDecompressedSize      = 256 * 1024 * 1024
	maxTransferListEntries   = 100000
	maxTransferListPathBytes = 16 << 20
	maxTransferPathBytes     = 4096
	maxTransferDepth         = 64
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
	resumeEnabled    bool
	nextID           int32
	policy           TransferPolicy
}

type TransferPolicy struct {
	Existing      string
	Resume        bool
	IncludeHidden bool
}

func (ft *FileTransfer) SetPolicy(policy TransferPolicy) error {
	if policy.Existing != "" && policy.Existing != "error" && policy.Existing != "overwrite" && policy.Existing != "skip" {
		return fmt.Errorf("invalid existing-file policy %q", policy.Existing)
	}
	ft.policy = policy
	return nil
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
		resumeEnabled:    versionAtLeast(pi.GetVersion(), "1.4.2"),
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
	return ft.sendTransferMessage(&pb.Message{Union: &pb.Message_FileAction{FileAction: a}})
}

func (ft *FileTransfer) transferTimeout() time.Duration {
	if ft.conn.opts.Timeout > 0 {
		return ft.conn.opts.Timeout
	}
	return 10 * time.Second
}

func (ft *FileTransfer) sendTransferMessage(msg *pb.Message) error {
	if err := ft.conn.sc.SetWriteDeadline(time.Now().Add(ft.transferTimeout())); err != nil {
		return err
	}
	defer ft.conn.sc.SetWriteDeadline(time.Time{})
	return ft.conn.Send(msg)
}

func (ft *FileTransfer) sendBlock(b *pb.FileTransferBlock) error {
	return ft.sendTransferMessage(&pb.Message{Union: &pb.Message_FileResponse{FileResponse: &pb.FileResponse{
		Union: &pb.FileResponse_Block{Block: b},
	}}})
}

func (ft *FileTransfer) sendDone(id int32, fileNum int32) error {
	return ft.sendTransferMessage(&pb.Message{Union: &pb.Message_FileResponse{FileResponse: &pb.FileResponse{
		Union: &pb.FileResponse_Done{Done: &pb.FileTransferDone{Id: id, FileNum: fileNum}},
	}}})
}

// ReadDir lists a remote directory.
func (ft *FileTransfer) ReadDir(path string) (*pb.FileDirectory, error) {
	if err := ft.conn.sc.SetDeadline(time.Now().Add(ft.transferTimeout())); err != nil {
		return nil, err
	}
	defer ft.conn.sc.SetDeadline(time.Time{})
	if err := ft.conn.Send(&pb.Message{Union: &pb.Message_FileAction{FileAction: &pb.FileAction{
		Union: &pb.FileAction_ReadDir{ReadDir: &pb.ReadDir{Path: path}},
	}}}); err != nil {
		return nil, err
	}
	for {
		msg, err := ft.conn.Recv()
		if err != nil {
			return nil, fmt.Errorf("read directory response: %w", err)
		}
		if td := msg.GetTestDelay(); td != nil {
			if err := ft.conn.Send(&pb.Message{Union: &pb.Message_TestDelay{TestDelay: td}}); err != nil {
				return nil, err
			}
			continue
		}
		fr := msg.GetFileResponse()
		if fr == nil {
			continue
		}
		switch u := fr.GetUnion().(type) {
		case *pb.FileResponse_Dir:
			var budget transferListBudget
			if err := budget.addDirectory(u.Dir); err != nil {
				return nil, err
			}
			return u.Dir, nil
		case *pb.FileResponse_Error:
			return nil, errors.New(ft.conn.PeerText(u.Error.GetError()))
		default:
			return nil, fmt.Errorf("unexpected file response %T", u)
		}
	}
}

// CreateDir creates a remote directory.
func (ft *FileTransfer) CreateDir(path string) error {
	id := ft.nextJobID()
	if err := ft.conn.sc.SetDeadline(time.Now().Add(ft.transferTimeout())); err != nil {
		return err
	}
	defer ft.conn.sc.SetDeadline(time.Time{})
	if err := ft.conn.Send(&pb.Message{Union: &pb.Message_FileAction{FileAction: &pb.FileAction{
		Union: &pb.FileAction_Create{Create: &pb.FileDirCreate{Id: id, Path: path}},
	}}}); err != nil {
		return err
	}
	for {
		msg, err := ft.conn.Recv()
		if err != nil {
			return fmt.Errorf("create directory response: %w", err)
		}
		if td := msg.GetTestDelay(); td != nil {
			if err := ft.conn.Send(&pb.Message{Union: &pb.Message_TestDelay{TestDelay: td}}); err != nil {
				return err
			}
			continue
		}
		fr := msg.GetFileResponse()
		if fr == nil {
			continue
		}
		switch u := fr.GetUnion().(type) {
		case *pb.FileResponse_Done:
			if u.Done.GetId() == id {
				return nil
			}
		case *pb.FileResponse_Error:
			if u.Error.GetId() == id {
				return errors.New(ft.conn.PeerText(u.Error.GetError()))
			}
		}
	}
}

type job struct {
	localPath  string
	remotePath string
	files      []*pb.FileEntry
	identities []os.FileInfo
	source     *uploadSource
}

type uploadSource struct {
	root        *os.Root
	directories map[string]os.FileInfo
}

type uploadDirectory struct {
	path string
	info os.FileInfo
}

type transferListBudget struct {
	entries   int
	pathBytes int
}

func (b *transferListBudget) addPath(name string) error {
	if len(name) > maxTransferPathBytes {
		return errors.New("transfer list path exceeds length limit")
	}
	depth := 0
	inComponent := false
	for _, c := range name {
		if c == '/' || c == '\\' {
			inComponent = false
		} else if !inComponent {
			depth++
			inComponent = true
		}
	}
	if depth > maxTransferDepth {
		return errors.New("transfer list path exceeds depth limit")
	}
	if len(name) > maxTransferListPathBytes-b.pathBytes {
		return errors.New("transfer list exceeds path-byte limit")
	}
	b.pathBytes += len(name)
	return nil
}

func (b *transferListBudget) addEntry(name string) error {
	if b.entries >= maxTransferListEntries {
		return errors.New("transfer list exceeds entry limit")
	}
	if err := b.addPath(name); err != nil {
		return err
	}
	b.entries++
	return nil
}

func (b *transferListBudget) addDirectory(dir *pb.FileDirectory) error {
	if err := b.addPath(dir.GetPath()); err != nil {
		return err
	}
	if len(dir.GetEntries()) > maxTransferListEntries-b.entries {
		return errors.New("transfer list exceeds entry limit")
	}
	for _, entry := range dir.GetEntries() {
		if err := b.addEntry(entry.GetName()); err != nil {
			return err
		}
	}
	return nil
}

// Send uploads localPath to remoteDir. Directories are walked recursively
// when recursive is true; each remote directory is created first and files
// are pushed with one receive job per directory.
func (ft *FileTransfer) Send(localPath, remoteDir string, recursive bool, progress ProgressFunc) error {
	if ft.policy.Resume {
		return errors.New("verified resume is supported for downloads only")
	}
	if ft.policy.Existing == "skip" || ft.policy.Existing == "error" {
		return errors.New("uploads require overwrite permission: peer protocol cannot guarantee atomic no-clobber or skip")
	}
	jobs, err := ft.buildSendJobs(localPath, remoteDir, recursive)
	if err != nil {
		return err
	}
	if len(jobs) > 0 {
		defer jobs[0].source.root.Close()
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
			if f.GetSize() > uint64(^uint64(0)>>1)-uint64(totalBytes) {
				return errors.New("upload size exceeds supported limit")
			}
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

// Returned jobs share one root handle, which the caller closes after the last job.
func (ft *FileTransfer) buildSendJobs(localPath, remoteDir string, recursive bool) ([]job, error) {
	var budget transferListBudget
	if err := budget.addPath(localPath); err != nil {
		return nil, err
	}
	if err := budget.addPath(remoteDir); err != nil {
		return nil, err
	}
	fi, err := os.Lstat(localPath)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", localPath, err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("upload source is a symlink; select its target explicitly: %s", localPath)
	}
	if !fi.IsDir() && !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("upload source is not a regular file: %s", localPath)
	}
	if fi.IsDir() && !recursive {
		return nil, fmt.Errorf("%s is a directory (use -r to copy recursively)", localPath)
	}
	rootPath := localPath
	if !fi.IsDir() {
		rootPath = filepath.Dir(localPath)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, fmt.Errorf("open upload root: %w", err)
	}
	success := false
	defer func() {
		if !success {
			root.Close()
		}
	}()
	rootInfo, err := root.Stat(".")
	if err != nil {
		return nil, err
	}
	source := &uploadSource{root: root, directories: map[string]os.FileInfo{".": rootInfo}}
	if !fi.IsDir() {
		name := filepath.Base(localPath)
		info, err := root.Lstat(name)
		if err != nil || !os.SameFile(fi, info) {
			return nil, fmt.Errorf("upload source changed while opening root: %s", localPath)
		}
		if err := budget.addEntry(name); err != nil {
			return nil, err
		}
		success = true
		return []job{{source: source, localPath: ".", remotePath: remoteDir, files: []*pb.FileEntry{localEntry(name, info)}, identities: []os.FileInfo{info}}}, nil
	}
	if !os.SameFile(fi, rootInfo) {
		return nil, errors.New("upload directory changed while opening root")
	}
	var jobs []job
	var walk func(string) error
	walk = func(rel string) error {
		remote := remoteDir
		if rel != "." {
			remote = filepath.Join(remoteDir, rel)
		}
		if err := budget.addPath(remote); err != nil {
			return err
		}
		files, identities, directories, err := source.readDirectory(rel, ft.policy.IncludeHidden, &budget)
		if err != nil {
			return err
		}
		jobs = append(jobs, job{source: source, localPath: rel, remotePath: remote, files: files, identities: identities})
		for _, dir := range directories {
			source.directories[dir.path] = dir.info
			if err := walk(dir.path); err != nil {
				return err
			}
		}
		return nil
	}
	if err := budget.addEntry("."); err != nil {
		return nil, err
	}
	if err := walk("."); err != nil {
		return nil, err
	}
	success = true
	return jobs, nil
}

func (s *uploadSource) checkDirectories(path string) error {
	for {
		expected := s.directories[path]
		info, err := s.root.Lstat(path)
		if err != nil {
			return fmt.Errorf("upload directory %s is no longer accessible within the selected root: %w", path, err)
		}
		if expected == nil || !info.IsDir() || !os.SameFile(expected, info) {
			return fmt.Errorf("upload directory changed since enumeration: %s", path)
		}
		if path == "." {
			return nil
		}
		path = filepath.Dir(path)
	}
}

func (s *uploadSource) readDirectory(path string, includeHidden bool, budget *transferListBudget) ([]*pb.FileEntry, []os.FileInfo, []uploadDirectory, error) {
	if err := s.checkDirectories(path); err != nil {
		return nil, nil, nil, err
	}
	dir, err := s.root.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open upload directory %s within selected root: %w", path, err)
	}
	defer dir.Close()
	info, err := dir.Stat()
	if err != nil || !os.SameFile(s.directories[path], info) {
		return nil, nil, nil, fmt.Errorf("upload directory changed since enumeration: %s", path)
	}
	var files []*pb.FileEntry
	var identities []os.FileInfo
	var directories []uploadDirectory
	for {
		entries, readErr := dir.ReadDir(128)
		if readErr != nil && readErr != io.EOF {
			return nil, nil, nil, readErr
		}
		for _, entry := range entries {
			name := filepath.Join(path, entry.Name())
			if err := budget.addEntry(name); err != nil {
				return nil, nil, nil, err
			}
			if !includeHidden && strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			if err := s.checkDirectories(path); err != nil {
				return nil, nil, nil, err
			}
			// DirEntry.Info may resolve the old absolute directory name after a rename.
			info, err := s.root.Lstat(name)
			if err != nil {
				return nil, nil, nil, err
			}
			if info.IsDir() {
				directories = append(directories, uploadDirectory{path: name, info: info})
			} else if info.Mode().IsRegular() {
				files = append(files, localEntry(name, info))
				identities = append(identities, info)
			}
		}
		if readErr == io.EOF {
			break
		}
	}
	order := make([]int, len(files))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(i, j int) bool { return files[order[i]].GetName() < files[order[j]].GetName() })
	sortedFiles := make([]*pb.FileEntry, len(files))
	sortedIdentities := make([]os.FileInfo, len(files))
	for i, index := range order {
		sortedFiles[i], sortedIdentities[i] = files[index], identities[index]
	}
	sort.Slice(directories, func(i, j int) bool { return directories[i].path < directories[j].path })
	return sortedFiles, sortedIdentities, directories, nil
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
		if ft.overwriteEnabled {
			if err := ft.sendTransferMessage(&pb.Message{Union: &pb.Message_FileResponse{FileResponse: &pb.FileResponse{Union: &pb.FileResponse_Digest{Digest: &pb.FileTransferDigest{
				Id: id, FileNum: int32(fileIdx), FileSize: entry.GetSize(), LastModified: entry.GetModifiedTime(),
			}}}}}); err != nil {
				return 0, 0, err
			}
			d, err := ft.waitUploadDigest(id, fileIdx)
			if err != nil {
				return 0, 0, err
			}
			if d == nil {
				filesDone++
				doneBytes += int64(entry.GetSize())
				sent += int64(entry.GetSize())
				if progress != nil && fileIdx+1 < len(j.files) {
					progress(filesDone, totalFiles, doneBytes, totalBytes)
				}
				fileIdx++
				continue
			}
			if d.GetIsUpload() {
				// The peer cannot prove the contents of an upload prefix, so always restart.
				confirm := &pb.FileTransferSendConfirmRequest{Id: id, FileNum: int32(fileIdx), Union: &pb.FileTransferSendConfirmRequest_OffsetBlk{OffsetBlk: 0}}
				if err := ft.sendAction(&pb.FileAction{Union: &pb.FileAction_SendConfirm{SendConfirm: confirm}}); err != nil {
					return 0, 0, err
				}
			}
		}
		n, err := ft.streamFile(id, int32(fileIdx), j.source, filepath.Join(j.localPath, entry.GetName()), j.identities[fileIdx], entry, func(n int64) {
			if progress != nil {
				progress(filesDone, totalFiles, doneBytes+n, totalBytes)
			}
		})
		if err != nil {
			return 0, 0, err
		}
		fileIdx++
		filesDone++
		doneBytes += n
		sent += n
		if progress != nil && fileIdx < len(j.files) {
			progress(filesDone, totalFiles, doneBytes, totalBytes)
		}
	}
	if len(j.files) > 0 {
		if err := ft.sendDone(id, int32(len(j.files))); err != nil {
			return 0, 0, err
		}
		if err := ft.waitUploadDone(id, int32(len(j.files))); err != nil {
			return 0, 0, err
		}
	}
	return len(j.files), sent, nil
}

func (ft *FileTransfer) waitUploadDone(id, fileCount int32) error {
	if err := ft.conn.sc.SetReadDeadline(time.Now().Add(ft.transferTimeout())); err != nil {
		return err
	}
	defer ft.conn.sc.SetReadDeadline(time.Time{})
	for {
		msg, err := ft.conn.Recv()
		if err != nil {
			return fmt.Errorf("upload completion not acknowledged: %w", err)
		}
		if td := msg.GetTestDelay(); td != nil {
			if err := ft.sendTransferMessage(&pb.Message{Union: &pb.Message_TestDelay{TestDelay: td}}); err != nil {
				return err
			}
		}
		fr := msg.GetFileResponse()
		if fr == nil {
			continue
		}
		if e := fr.GetError(); e != nil && e.GetId() == id {
			return errors.New(ft.conn.PeerText(e.GetError()))
		}
		if d := fr.GetDone(); d != nil && d.GetId() == id {
			if d.GetFileNum() != fileCount {
				return fmt.Errorf("unexpected upload completion file number %d", d.GetFileNum())
			}
			return nil
		}
	}
}

// waitUploadDigest waits for the peer's overwrite-detection digest for the
// given file of an upload job. It returns nil when the peer asks us to skip
// the file, or the digest when the transfer should proceed. The caller sends
// the SendConfirm with the chosen offset.
func (ft *FileTransfer) waitUploadDigest(id int32, fileIdx int) (*pb.FileTransferDigest, error) {
	if err := ft.conn.sc.SetReadDeadline(time.Now().Add(ft.transferTimeout())); err != nil {
		return nil, err
	}
	defer ft.conn.sc.SetReadDeadline(time.Time{})
	for {
		msg, err := ft.conn.Recv()
		if err != nil {
			return nil, fmt.Errorf("upload digest not received: %w", err)
		}
		if td := msg.GetTestDelay(); td != nil {
			if err := ft.sendTransferMessage(&pb.Message{Union: &pb.Message_TestDelay{TestDelay: td}}); err != nil {
				return nil, err
			}
		}
		if fa := msg.GetFileAction(); fa != nil {
			if sc := fa.GetSendConfirm(); sc != nil && sc.GetId() == id && sc.GetFileNum() == int32(fileIdx) {
				if sc.GetSkip() {
					return nil, nil
				}
				if sc.GetOffsetBlk() != 0 {
					return nil, errors.New("unsolicited upload resume offset")
				}
				return &pb.FileTransferDigest{}, nil
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
			if !d.GetIsUpload() {
				return nil, errors.New("unexpected download digest during upload")
			}
			return d, nil
		case *pb.FileResponse_Error:
			if u.Error.GetId() == id {
				return nil, errors.New(ft.conn.PeerText(u.Error.GetError()))
			}
		}
	}
}

func (ft *FileTransfer) streamFile(id, fileNum int32, source *uploadSource, path string, expected os.FileInfo, entry *pb.FileEntry, progress func(int64)) (int64, error) {
	if err := source.checkDirectories(filepath.Dir(path)); err != nil {
		return 0, err
	}
	before, err := source.root.Lstat(path)
	if err != nil {
		return 0, fmt.Errorf("upload source %s is no longer accessible within the selected root: %w", path, err)
	}
	if !before.Mode().IsRegular() || !os.SameFile(expected, before) {
		return 0, errors.New("upload source changed since enumeration")
	}
	// A FIFO substituted after Lstat must not block before the descriptor check.
	f, err := source.root.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return 0, fmt.Errorf("open upload source %s within selected root: %w", path, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() || !os.SameFile(expected, info) || !info.ModTime().Equal(expected.ModTime()) || uint64(info.Size()) != entry.GetSize() || uint64(info.ModTime().Unix()) != entry.GetModifiedTime() {
		return 0, errors.New("upload source changed before transfer")
	}
	buf := make([]byte, fileBlockSize)
	reader := io.LimitReader(f, int64(entry.GetSize()))
	var sent int64
	for {
		n, rErr := io.ReadFull(reader, buf)
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
		if progress != nil {
			progress(sent)
		}
		if rErr != nil {
			break
		}
	}
	after, err := f.Stat()
	if err != nil {
		return 0, err
	}
	if uint64(sent) != entry.GetSize() || !after.ModTime().Equal(info.ModTime()) || after.Size() != info.Size() {
		return 0, errors.New("upload source changed during transfer")
	}
	if sent == 0 {
		if err := ft.sendBlock(&pb.FileTransferBlock{Id: id, FileNum: fileNum}); err != nil {
			return 0, err
		}
	}
	return sent, nil
}

// Receive downloads remotePath to localPath (file or recursive tree).
func (ft *FileTransfer) Receive(remotePath, localPath string, progress ProgressFunc) error {
	return ft.receiveFiles(remotePath, localPath, progress)
}

func closeFile(f *os.File) {
	if f != nil {
		_ = f.Close()
	}
}

func readLocalDirEntries(dir string) ([]*pb.FileEntry, error) {
	return readLocalDirEntriesPolicy(dir, false)
}

func readLocalDirEntriesPolicy(dir string, includeHidden bool) ([]*pb.FileEntry, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	info, err := root.Stat(".")
	if err != nil {
		return nil, err
	}
	source := &uploadSource{root: root, directories: map[string]os.FileInfo{".": info}}
	entries, _, _, err := source.readDirectory(".", includeHidden, &transferListBudget{})
	return entries, err
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
	return ft.readAllFilesList()
}

func (ft *FileTransfer) readAllFilesList() ([]*pb.FileEntry, error) {
	var entries []*pb.FileEntry
	var budget transferListBudget
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
			if err := budget.addDirectory(u.Dir); err != nil {
				return nil, err
			}
			entries = append(entries, u.Dir.GetEntries()...)
		case *pb.FileResponse_Done:
			return entries, nil
		case *pb.FileResponse_Error:
			return nil, errors.New(ft.conn.PeerText(u.Error.GetError()))
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

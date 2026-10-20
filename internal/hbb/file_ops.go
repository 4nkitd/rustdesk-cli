package hbb

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	pb "github.com/4nkitd/rustdesk-cli/internal/hbb/proto"
)

func NewFileOperations(opts DialOpts) (*FileTransfer, error) {
	return NewFileTransfer(opts, "")
}

func (ft *FileTransfer) beginFileOperation() error {
	timeout := ft.conn.opts.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return ft.conn.sc.SetDeadline(time.Now().Add(timeout))
}

func (ft *FileTransfer) recvFileOperation() (*pb.FileResponse, error) {
	msg, err := ft.conn.Recv()
	if err != nil {
		return nil, err
	}
	if td := msg.GetTestDelay(); td != nil && !td.GetFromClient() {
		if err := ft.conn.Send(msg); err != nil {
			return nil, err
		}
	}
	if reason := msg.GetMisc().GetCloseReason(); reason != "" {
		return nil, fmt.Errorf("peer closed: %s", ft.conn.PeerText(reason))
	}
	return msg.GetFileResponse(), nil
}

func (ft *FileTransfer) waitFileDirectory() (*pb.FileDirectory, error) {
	for {
		fr, err := ft.recvFileOperation()
		if err != nil {
			return nil, err
		}
		if dir := fr.GetDir(); dir != nil && dir.GetId() == 0 {
			return dir, nil
		}
		if e := fr.GetError(); e != nil && e.GetId() == 0 {
			return nil, errors.New(ft.conn.PeerText(e.GetError()))
		}
	}
}

// ReadDir replies have no request ID. Use the target-specific login listing
// in a dedicated session so no earlier listing can be mistaken for it.
func ReadDirWithHidden(opts DialOpts, remotePath string, includeHidden bool) (*pb.FileDirectory, error) {
	opts.ConnType = pb.ConnType_FILE_TRANSFER
	c, err := Connect(opts)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	if _, err := c.Login(LoginFileTransfer, LoginSpec{Dir: remotePath, ShowHidden: includeHidden}); err != nil {
		return nil, err
	}
	ft := &FileTransfer{conn: c}
	if err := ft.beginFileOperation(); err != nil {
		return nil, err
	}
	dir, err := ft.waitFileDirectory()
	if err != nil {
		return nil, err
	}
	// RustDesk can fall back to home when the login directory is invalid.
	if remotePath != "" && !sameRemoteDirectory(remotePath, dir.GetPath(), c.PeerInfo().GetPlatform()) {
		return nil, fmt.Errorf("requested directory %q, but peer returned %q", remotePath, c.PeerText(dir.GetPath()))
	}
	return dir, nil
}

func sameRemoteDirectory(requested, returned, platform string) bool {
	if requested == returned {
		return true
	}
	if !strings.EqualFold(platform, "Windows") {
		return false
	}
	requested = strings.ReplaceAll(requested, `\`, "/")
	returned = strings.ReplaceAll(returned, `\`, "/")
	for _, p := range []string{requested, returned} {
		if strings.HasPrefix(p, "//?/") || strings.HasPrefix(p, "//./") {
			return false
		}
	}
	// Windows drive letters are case-insensitive, but directory names may not
	// be. Keep all components, including .., to detect invalid-path fallback.
	if len(requested) >= 2 && requested[1] == ':' && len(returned) >= 2 && returned[1] == ':' {
		return strings.EqualFold(requested[:1], returned[:1]) && requested[1:] == returned[1:]
	}
	return requested == returned
}

func (ft *FileTransfer) runFileOperation(id int32, action *pb.FileAction) error {
	if err := ft.beginFileOperation(); err != nil {
		return err
	}
	defer ft.conn.sc.SetDeadline(time.Time{})
	// Transfer sends clear their write deadline. Keep the original operation
	// deadline on both this action and subsequent TestDelay replies instead.
	if err := ft.conn.Send(&pb.Message{Union: &pb.Message_FileAction{FileAction: action}}); err != nil {
		return err
	}
	for {
		fr, err := ft.recvFileOperation()
		if err != nil {
			return err
		}
		if done := fr.GetDone(); done != nil && done.GetId() == id && done.GetFileNum() == 0 {
			return nil
		}
		if e := fr.GetError(); e != nil && e.GetId() == id && e.GetFileNum() == 0 {
			return errors.New(ft.conn.PeerText(e.GetError()))
		}
	}
}

func (ft *FileTransfer) Mkdir(remotePath string) error {
	if err := ValidateRemoteMutationPath(remotePath); err != nil {
		return err
	}
	id := ft.nextJobID()
	return ft.runFileOperation(id, &pb.FileAction{Union: &pb.FileAction_Create{Create: &pb.FileDirCreate{Id: id, Path: remotePath}}})
}

func (ft *FileTransfer) RemoveFile(remotePath string) error {
	if err := ValidateRemoteMutationPath(remotePath); err != nil {
		return err
	}
	id := ft.nextJobID()
	return ft.runFileOperation(id, &pb.FileAction{Union: &pb.FileAction_RemoveFile{RemoveFile: &pb.FileRemoveFile{Id: id, Path: remotePath}}})
}

func (ft *FileTransfer) RemoveEmptyDir(remotePath string) error {
	if err := ValidateRemoteMutationPath(remotePath); err != nil {
		return err
	}
	id := ft.nextJobID()
	return ft.runFileOperation(id, &pb.FileAction{Union: &pb.FileAction_RemoveDir{RemoveDir: &pb.FileRemoveDir{Id: id, Path: remotePath, Recursive: false}}})
}

func (ft *FileTransfer) Rename(remotePath, newName string) error {
	if err := ValidateRemoteMutationPath(remotePath); err != nil {
		return err
	}
	if err := ValidateRemoteNewName(newName); err != nil {
		return err
	}
	version := ft.conn.PeerInfo().GetVersion()
	if !versionAtLeast(version, "1.3.0") {
		return fmt.Errorf("rename requires RustDesk 1.3.0 or newer (peer version %q)", ft.conn.PeerText(version))
	}
	id := ft.nextJobID()
	return ft.runFileOperation(id, &pb.FileAction{Union: &pb.FileAction_Rename{Rename: &pb.FileRename{Id: id, Path: remotePath, NewName: newName}}})
}

func ValidateRemoteMutationPath(remotePath string) error {
	p := strings.ReplaceAll(remotePath, `\`, "/")
	if strings.TrimSpace(p) == "" || strings.ContainsRune(p, 0) {
		return errors.New("remote path must not be empty or contain NUL")
	}
	// Reject both POSIX and Windows roots independently of the client OS.
	if strings.HasPrefix(p, "//") {
		parts := strings.Split(strings.TrimPrefix(path.Clean(p), "/"), "/")
		if len(parts) <= 2 || parts[0] == "?" || parts[0] == "." {
			return errors.New("refusing remote root or device path")
		}
	}
	trimmed := strings.TrimPrefix(p, "/")
	if len(trimmed) >= 2 && trimmed[1] == ':' {
		p = trimmed[2:]
	}
	clean := path.Clean(p)
	base := path.Base(strings.TrimRight(p, "/"))
	if clean == "/" || clean == "." || clean == ".." || strings.Trim(base, " .") == "" || strings.HasPrefix(clean, "../") {
		return errors.New("refusing remote root, current directory, or parent target")
	}
	return nil
}

func ValidateRemoteNewName(name string) error {
	if strings.TrimSpace(name) == "" || strings.Trim(name, " .") == "" || strings.ContainsAny(name, "/\\:\x00") {
		return errors.New("new name must be a single basename, not a path")
	}
	return nil
}

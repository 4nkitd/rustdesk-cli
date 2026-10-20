package integration

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/4nkitd/rustdesk-cli/internal/hbb"
	pb "github.com/4nkitd/rustdesk-cli/internal/hbb/proto"
	"github.com/klauspost/compress/zstd"
)

const (
	transferBlockSize = 64 * 1024
	transferSize      = 3*transferBlockSize + 12345
	transferModified  = 1700000000
	transferRemoteDir = "/integration-upload"
	transferName      = "fixture.bin"
)

func runTransferCases(t *testing.T, h cliHarness) {
	t.Helper()
	content := make([]byte, transferSize)
	for offset := 0; offset < len(content); offset += sha256.Size {
		block := sha256.Sum256([]byte(fmt.Sprintf("fixture block %d", offset/sha256.Size)))
		copy(content[offset:], block[:])
	}
	for _, version := range []string{"1.4.1", "1.4.9"} {
		for _, acknowledge := range []bool{true, false} {
			t.Run(fmt.Sprintf("cp upload %s ack=%t", version, acknowledge), func(t *testing.T) {
				local := filepath.Join(t.TempDir(), transferName)
				if err := os.WriteFile(local, content, 0o600); err != nil {
					t.Fatal(err)
				}
				modified := time.Unix(transferModified, 0)
				if err := os.Chtimes(local, modified, modified); err != nil {
					t.Fatal(err)
				}
				fixture := newPeerFixtureVersion(t, version, pb.ConnType_FILE_TRANSFER, "", uploadResponse(content, acknowledge))
				start := time.Now()
				result := fixture.run(t, h, "cp", fixtureID, "--direction", "up", "--overwrite", local, transferRemoteDir)
				if elapsed := time.Since(start); elapsed > 5*time.Second {
					t.Fatalf("transfer exceeded its bounded timeout: %s", elapsed)
				}
				if result.stdout != "" {
					t.Fatalf("unexpected transfer stdout: %q", result.stdout)
				}
				if acknowledge {
					result.requireCode(t, 0)
					if !strings.Contains(result.stderr, "copied in ") {
						t.Fatalf("missing acknowledged success: %s", result.stderr)
					}
				} else {
					result.requireCode(t, 1)
					if strings.Contains(result.stderr, "copied in ") || strings.Contains(result.stderr, "1/1 files") {
						t.Fatalf("unacknowledged upload reported success: %s", result.stderr)
					}
					if !strings.Contains(result.stderr, "not acknowledged") {
						t.Fatalf("missing upload ACK failure diagnostic: %s", result.stderr)
					}
				}
			})
		}
		t.Run("cp download "+version, func(t *testing.T) {
			fixture := newPeerFixtureVersion(t, version, pb.ConnType_FILE_TRANSFER, "", downloadResponse(content))
			destination := t.TempDir()
			result := fixture.run(t, h, "cp", fixtureID, "--direction", "down", transferRemoteDir+"/"+transferName, destination)
			result.requireCode(t, 0)
			if result.stdout != "" || !strings.Contains(result.stderr, "copied in ") {
				t.Fatalf("unexpected download output: stdout=%q stderr=%q", result.stdout, result.stderr)
			}
			actual, err := os.ReadFile(filepath.Join(destination, transferName))
			if err != nil {
				t.Fatal(err)
			}
			if len(actual) != len(content) || sha256.Sum256(actual) != sha256.Sum256(content) {
				t.Fatal("download content hash or length mismatch")
			}
			entries, err := os.ReadDir(destination)
			if err != nil || len(entries) != 1 || entries[0].Name() != transferName {
				t.Fatalf("download left unexpected artifacts: %v, %v", entries, err)
			}
		})
	}
}

func uploadResponse(content []byte, acknowledge bool) func(*hbb.SecureConn) error {
	return func(sc *hbb.SecureConn) error {
		message := &pb.Message{}
		if err := readProto(sc, message); err != nil {
			return err
		}
		create := message.GetFileAction().GetCreate()
		if create == nil || create.GetPath() != transferRemoteDir || create.GetId() <= 0 {
			return errors.New("expected destination directory creation")
		}
		if err := writeFileResponse(sc, &pb.FileResponse{Union: &pb.FileResponse_Done{Done: &pb.FileTransferDone{Id: create.GetId()}}}); err != nil {
			return err
		}
		if err := readProto(sc, message); err != nil {
			return err
		}
		request := message.GetFileAction().GetReceive()
		if request == nil || request.GetId() <= 0 || request.GetId() == create.GetId() || request.GetPath() != transferRemoteDir ||
			request.GetFileNum() != 0 || request.GetTotalSize() != uint64(len(content)) || len(request.GetFiles()) != 1 {
			return errors.New("unexpected upload receive request")
		}
		id := request.GetId()
		entry := request.GetFiles()[0]
		if entry.GetName() != transferName || entry.GetSize() != uint64(len(content)) || entry.GetModifiedTime() != transferModified || entry.GetEntryType() != pb.FileType_File {
			return errors.New("unexpected upload entry metadata")
		}
		if err := readProto(sc, message); err != nil {
			return err
		}
		digest := message.GetFileResponse().GetDigest()
		if digest == nil || digest.GetId() != id || digest.GetFileNum() != 0 || digest.GetFileSize() != uint64(len(content)) ||
			digest.GetLastModified() != transferModified || digest.GetIsUpload() || digest.GetIsResume() {
			return errors.New("expected exact source metadata digest before upload blocks")
		}
		if err := writeFileResponse(sc, &pb.FileResponse{Union: &pb.FileResponse_Digest{Digest: &pb.FileTransferDigest{
			Id: id, FileNum: 0, IsUpload: true, FileSize: 17, LastModified: transferModified - 1,
		}}}); err != nil {
			return err
		}
		if err := expectFreshConfirm(sc, id); err != nil {
			return err
		}
		decoder, err := zstd.NewReader(nil, zstd.WithDecoderMaxMemory(1<<20), zstd.WithDecoderMaxWindow(1<<20))
		if err != nil {
			return err
		}
		defer decoder.Close()
		received := make([]byte, 0, len(content))
		blocks := 0
		for {
			if err := readProto(sc, message); err != nil {
				return err
			}
			if block := message.GetFileResponse().GetBlock(); block != nil {
				if block.GetId() != id || block.GetFileNum() != 0 {
					return errors.New("upload block has wrong job or file number")
				}
				data := block.GetData()
				if block.GetCompressed() {
					data, err = decoder.DecodeAll(data, nil)
					if err != nil {
						return err
					}
				}
				if len(data) == 0 || len(data) > 128*1024 || len(data) > len(content)-len(received) {
					return errors.New("invalid upload block size")
				}
				received = append(received, data...)
				blocks++
				continue
			}
			done := message.GetFileResponse().GetDone()
			if done == nil || done.GetId() != id || done.GetFileNum() != 1 {
				return errors.New("expected one job-level Done with file_num equal to file count")
			}
			if blocks < 2 || len(received) != len(content) || sha256.Sum256(received) != sha256.Sum256(content) {
				return errors.New("upload content hash, length, or block count mismatch")
			}
			if acknowledge {
				if err := writeFileResponse(sc, &pb.FileResponse{Union: &pb.FileResponse_Done{Done: &pb.FileTransferDone{Id: id, FileNum: 1}}}); err != nil {
					return err
				}
			}
			// Withholding ACK keeps the connection open until the CLI's own transfer deadline closes it.
			return expectPeerEOF(sc)
		}
	}
}

func downloadResponse(content []byte) func(*hbb.SecureConn) error {
	return func(sc *hbb.SecureConn) error {
		message := &pb.Message{}
		if err := readProto(sc, message); err != nil {
			return err
		}
		request := message.GetFileAction().GetSend()
		if request == nil || request.GetId() <= 0 || request.GetPath() != transferRemoteDir+"/"+transferName || request.GetFileType() != pb.FileTransferSendRequest_Generic {
			return errors.New("unexpected download send request")
		}
		id := request.GetId()
		if err := writeFileResponse(sc, &pb.FileResponse{Union: &pb.FileResponse_Dir{Dir: &pb.FileDirectory{Id: id, Entries: []*pb.FileEntry{
			{EntryType: pb.FileType_File, Size: uint64(len(content)), ModifiedTime: transferModified},
		}}}}); err != nil {
			return err
		}
		if err := writeFileResponse(sc, &pb.FileResponse{Union: &pb.FileResponse_Digest{Digest: &pb.FileTransferDigest{
			Id: id, FileNum: 0, FileSize: uint64(len(content)), LastModified: transferModified,
		}}}); err != nil {
			return err
		}
		if err := expectFreshConfirm(sc, id); err != nil {
			return err
		}
		for offset := 0; offset < len(content); offset += transferBlockSize {
			data := content[offset:min(offset+transferBlockSize, len(content))]
			if err := writeFileResponse(sc, &pb.FileResponse{Union: &pb.FileResponse_Block{Block: &pb.FileTransferBlock{Id: id, FileNum: 0, Data: data}}}); err != nil {
				return err
			}
		}
		if err := writeFileResponse(sc, &pb.FileResponse{Union: &pb.FileResponse_Done{Done: &pb.FileTransferDone{Id: id, FileNum: 1}}}); err != nil {
			return err
		}
		return expectPeerEOF(sc)
	}
}

func expectFreshConfirm(sc *hbb.SecureConn, id int32) error {
	message := &pb.Message{}
	if err := readProto(sc, message); err != nil {
		return err
	}
	confirm := message.GetFileAction().GetSendConfirm()
	if confirm == nil || confirm.GetId() != id || confirm.GetFileNum() != 0 || confirm.GetOffsetBlk() != 0 {
		return errors.New("expected exact fresh-transfer confirmation")
	}
	if _, ok := confirm.GetUnion().(*pb.FileTransferSendConfirmRequest_OffsetBlk); !ok {
		return errors.New("expected explicit zero-offset confirmation")
	}
	return nil
}

func expectPeerEOF(sc *hbb.SecureConn) error {
	message := &pb.Message{}
	err := readProto(sc, message)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("extra message after transfer completion, including duplicate Done: %T", message.GetUnion())
}

func writeFileResponse(sc *hbb.SecureConn, response *pb.FileResponse) error {
	return writeProto(sc, &pb.Message{Union: &pb.Message_FileResponse{FileResponse: response}})
}

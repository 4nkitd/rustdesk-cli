package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"

	"github.com/4nkitd/rustdesk-cli/internal/hbb"
	pb "github.com/4nkitd/rustdesk-cli/internal/hbb/proto"
)

func fileOpsTestCommand(t *testing.T, cmd *cobra.Command, args ...string) (string, error) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("RDC_CONFIG", filepath.Join(home, "rdcli.toml"))
	root := &cobra.Command{Use: "rdcli", SilenceUsage: true, SilenceErrors: true}
	registerCommonFlags(root)
	root.PersistentFlags().StringP("connect", "c", "", "target")
	root.AddCommand(cmd)
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetArgs(append([]string{cmd.Name()}, args...))
	err := root.Execute()
	return output.String(), err
}

func TestFileOperationsCLIValidation(t *testing.T) {
	tests := []struct {
		name string
		cmd  func() *cobra.Command
		args []string
		want string
	}{
		{"yes is not delete confirmation", newRmCmd, []string{"123456789", "/work/file", "--yes"}, "--confirm-delete"},
		{"false confirmation", newRmCmd, []string{"123456789", "/work/file", "--confirm-delete=false"}, "--confirm-delete"},
		{"root deletion", newRmCmd, []string{"123456789", "/", "--force"}, "root"},
		{"empty deletion", newRmCmd, []string{"123456789", "", "--confirm-delete"}, "empty"},
		{"Windows root", newRmCmd, []string{"123456789", `C:\`, "--force"}, "root"},
		{"empty mkdir", newMkdirCmd, []string{"123456789", ""}, "empty"},
		{"move rejected", newRenameCmd, []string{"123456789", "/work/file", "/other/new"}, "basename"},
		{"rename traversal", newRenameCmd, []string{"123456789", "/work/file", ".."}, "basename"},
		{"rename requires consent", newRenameCmd, []string{"123456789", "/work/file", "new"}, "--overwrite"},
		{"yes is not overwrite consent", newRenameCmd, []string{"123456789", "/work/file", "new", "--yes"}, "--overwrite"},
		{"false overwrite consent", newRenameCmd, []string{"123456789", "/work/file", "new", "--overwrite=false", "--yes"}, "--overwrite"},
		{"extra ls args", newLsCmd, []string{"123456789", "/work", "extra", "--all"}, "usage"},
		{"connect resolution", newRmCmd, []string{"-c", "123456789", "/work/file", "--yes"}, "--confirm-delete"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output, err := fileOpsTestCommand(t, tt.cmd(), append(tt.args, "--json")...)
			var usage *UsageError
			if !errors.As(err, &usage) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v, want usage error containing %q", err, tt.want)
			}
			var result fileOperationJSON
			if err := json.Unmarshal([]byte(output), &result); err != nil {
				t.Fatalf("invalid JSON %q: %v", output, err)
			}
			if result.Version != 1 || result.Success || result.Operation != tt.cmd().Name() || !strings.Contains(result.Error, tt.want) {
				t.Fatalf("unexpected result: %+v", result)
			}
		})
	}
}

func TestFileOperationsNoRecursiveDelete(t *testing.T) {
	output, err := fileOpsTestCommand(t, newRmCmd(), "123456789", "/work", "--force", "-r")
	if err == nil || !strings.Contains(err.Error(), "unknown shorthand flag") {
		t.Fatalf("recursive deletion accepted: %q, %v", output, err)
	}
	cmd := newLsCmd()
	if err := cmd.ParseFlags([]string{"-a"}); err != nil {
		t.Fatal(err)
	}
	if all, err := cmd.Flags().GetBool("all"); err != nil || !all {
		t.Fatalf("-a not recognized: %v, %v", all, err)
	}
}

func TestFileOperationsRenameYesDoesNotDial(t *testing.T) {
	listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	output, err := fileOpsTestCommand(t, newRenameCmd(), listener.Addr().String(), "/work/file", "new", "--yes", "--json", "--timeout=50ms")
	var usage *UsageError
	if !errors.As(err, &usage) || !strings.Contains(err.Error(), "--overwrite") || !strings.Contains(output, "may replace") {
		t.Fatalf("missing overwrite consent error: %q, %v", output, err)
	}
	if err := listener.SetDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	conn, err := listener.Accept()
	if conn != nil {
		conn.Close()
		t.Fatal("rename dialed without overwrite consent")
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("accept: %v", err)
	}
}

func TestFileOperationsRenameOverwriteMockRequest(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan error, 1)
	go func() {
		done <- func() error {
			conn, err := listener.Accept()
			if err != nil {
				return err
			}
			defer conn.Close()
			if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
				return err
			}
			sc := hbb.NewSecureConn(conn)
			read := func() (*pb.Message, error) {
				data, err := sc.ReadMessage()
				if err != nil {
					return nil, err
				}
				msg := &pb.Message{}
				return msg, proto.Unmarshal(data, msg)
			}
			write := func(msg *pb.Message) error {
				data, err := proto.Marshal(msg)
				if err != nil {
					return err
				}
				_, err = sc.Write(data)
				return err
			}
			if _, err := read(); err != nil {
				return err
			}
			if err := write(&pb.Message{Union: &pb.Message_Hash{Hash: &pb.Hash{Salt: "mock", Challenge: "rename"}}}); err != nil {
				return err
			}
			login, err := read()
			if err != nil {
				return err
			}
			if login.GetLoginRequest().GetFileTransfer() == nil {
				return fmt.Errorf("expected file-transfer login: %v", login)
			}
			if err := write(&pb.Message{Union: &pb.Message_LoginResponse{LoginResponse: &pb.LoginResponse{Union: &pb.LoginResponse_PeerInfo{PeerInfo: &pb.PeerInfo{Version: "1.4.1", Platform: "Linux"}}}}}); err != nil {
				return err
			}
			if err := write(&pb.Message{Union: &pb.Message_FileResponse{FileResponse: &pb.FileResponse{Union: &pb.FileResponse_Dir{Dir: &pb.FileDirectory{Path: "/home/mock"}}}}}); err != nil {
				return err
			}
			request, err := read()
			if err != nil {
				return err
			}
			rename := request.GetFileAction().GetRename()
			if rename == nil || rename.GetPath() != "/work/file" || rename.GetNewName() != "new" || rename.GetId() <= 0 {
				return fmt.Errorf("unexpected rename request: %v", request)
			}
			return write(&pb.Message{Union: &pb.Message_FileResponse{FileResponse: &pb.FileResponse{Union: &pb.FileResponse_Done{Done: &pb.FileTransferDone{Id: rename.GetId()}}}}})
		}()
	}()
	output, err := fileOpsTestCommand(t, newRenameCmd(), listener.Addr().String(), "/work/file", "new", "--yes", "--overwrite", "--json", "--timeout=2s")
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var result fileOperationJSON
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Success || result.Operation != "rename" || result.Path != "/work/file" || result.NewName != "new" {
		t.Fatalf("unexpected rename result: %+v", result)
	}
}

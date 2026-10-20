package cli

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/4nkitd/rustdesk-cli/internal/hbb"
	pb "github.com/4nkitd/rustdesk-cli/internal/hbb/proto"
	"google.golang.org/protobuf/proto"
)

const shPipelineInput = "sleep 1; echo expected; exit\n"

func shPipelineSend(sc *hbb.SecureConn, message proto.Message) error {
	data, err := proto.Marshal(message)
	if err != nil {
		return err
	}
	_, err = sc.Write(data)
	return err
}

func shPipelineRecv(sc *hbb.SecureConn, message proto.Message) error {
	data, err := sc.ReadMessage()
	if err != nil {
		return err
	}
	return proto.Unmarshal(data, message)
}

func shPipelineIdentity(t *testing.T, private ed25519.PrivateKey, public []byte) []byte {
	t.Helper()
	data, err := proto.Marshal(&pb.IdPk{Id: "pipeline-peer", Pk: public})
	if err != nil {
		t.Fatal(err)
	}
	return append(ed25519.Sign(private, data), data...)
}

func shPipelineMock(t *testing.T, outcome string, cancel context.CancelFunc) (hbb.DialOpts, <-chan error) {
	t.Helper()
	listen := func() net.Listener {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { listener.Close() })
		return listener
	}
	rendezvous, peer := listen(), listen()
	serverPublic, serverPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	peerPublic, peerPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPair, err := hbb.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	serverIdentity := shPipelineIdentity(t, serverPrivate, peerPublic)
	peerIdentity := shPipelineIdentity(t, peerPrivate, keyPair.Pub)
	done := make(chan error, 2)
	go func() {
		done <- func() error {
			conn, err := rendezvous.Accept()
			if err != nil {
				return err
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(5 * time.Second))
			sc := hbb.NewSecureConn(conn)
			request := &pb.RendezvousMessage{}
			if err := shPipelineRecv(sc, request); err != nil {
				return err
			}
			if request.GetPunchHoleRequest() == nil {
				return errors.New("expected punch request")
			}
			return shPipelineSend(sc, &pb.RendezvousMessage{Union: &pb.RendezvousMessage_RelayResponse{RelayResponse: &pb.RelayResponse{
				RelayServer: peer.Addr().String(), Uuid: "pipeline-session", Union: &pb.RelayResponse_Pk{Pk: serverIdentity},
			}}})
		}()
	}()
	go func() {
		done <- func() error {
			conn, err := peer.Accept()
			if err != nil {
				return err
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(5 * time.Second))
			sc := hbb.NewSecureConn(conn)
			relay := &pb.RendezvousMessage{}
			if err := shPipelineRecv(sc, relay); err != nil {
				return err
			}
			if relay.GetRequestRelay().GetUuid() != "pipeline-session" {
				return errors.New("unexpected relay session")
			}
			if err := shPipelineSend(sc, &pb.Message{Union: &pb.Message_SignedId{SignedId: &pb.SignedId{Id: peerIdentity}}}); err != nil {
				return err
			}
			message := &pb.Message{}
			if err := shPipelineRecv(sc, message); err != nil {
				return err
			}
			public := message.GetPublicKey()
			key, err := hbb.DecryptSymmetricKey(public.GetSymmetricValue(), public.GetAsymmetricValue(), keyPair.Priv)
			if err != nil {
				return err
			}
			sc.SetKey(key)
			if err := shPipelineSend(sc, &pb.Message{Union: &pb.Message_Hash{Hash: &pb.Hash{Salt: "pipeline-salt", Challenge: "pipeline-challenge"}}}); err != nil {
				return err
			}
			if err := shPipelineRecv(sc, message); err != nil {
				return err
			}
			if message.GetLoginRequest().GetTerminal() == nil {
				return errors.New("expected terminal login")
			}
			if err := shPipelineSend(sc, &pb.Message{Union: &pb.Message_LoginResponse{LoginResponse: &pb.LoginResponse{
				Union: &pb.LoginResponse_PeerInfo{PeerInfo: &pb.PeerInfo{Platform: "Linux", Version: "1.5.0", Features: &pb.Features{Terminal: true}}},
			}}}); err != nil {
				return err
			}
			if err := shPipelineRecv(sc, message); err != nil {
				return err
			}
			open := message.GetTerminalAction().GetOpen()
			if open == nil {
				return errors.New("expected terminal open")
			}
			id := open.GetTerminalId()
			send := func(response *pb.TerminalResponse) error {
				return shPipelineSend(sc, &pb.Message{Union: &pb.Message_TerminalResponse{TerminalResponse: response}})
			}
			if err := send(&pb.TerminalResponse{Union: &pb.TerminalResponse_Opened{Opened: &pb.TerminalOpened{TerminalId: id, Success: true}}}); err != nil {
				return err
			}
			if err := shPipelineRecv(sc, message); err != nil {
				return err
			}
			if data := message.GetTerminalAction().GetData(); data.GetTerminalId() != id || string(data.GetData()) != shPipelineInput {
				return fmt.Errorf("unexpected pipeline input: %v", data)
			}
			next := &pb.Message{}
			received := make(chan error, 1)
			go func() { received <- shPipelineRecv(sc, next) }()
			select {
			case err := <-received:
				return fmt.Errorf("client ended terminal on stdin EOF before delayed output: message=%v error=%v", next, err)
			case <-time.After(150 * time.Millisecond):
			}
			if outcome == "cancel" {
				cancel()
				if err := <-received; err != nil {
					return err
				}
				if next.GetTerminalAction().GetClose() == nil {
					return errors.New("cancellation did not close terminal")
				}
				if err := shPipelineRecv(sc, next); err != io.EOF {
					return fmt.Errorf("transport not closed after cancellation: %v", err)
				}
				return nil
			}
			if err := send(&pb.TerminalResponse{Union: &pb.TerminalResponse_Data{Data: &pb.TerminalData{TerminalId: id, Data: []byte("expected\r\n")}}}); err != nil {
				return err
			}
			response := &pb.TerminalResponse{Union: &pb.TerminalResponse_Closed{Closed: &pb.TerminalClosed{TerminalId: id}}}
			if outcome == "peer error" {
				response.Union = &pb.TerminalResponse_Error{Error: &pb.TerminalError{TerminalId: id, Message: "pipeline peer failed"}}
			} else if outcome == "nonzero exit" {
				response.GetClosed().ExitCode = 29
			}
			if err := send(response); err != nil {
				return err
			}
			if err := <-received; err != io.EOF {
				return fmt.Errorf("transport not closed after terminal completion: message=%v error=%v", next, err)
			}
			return nil
		}()
	}()
	return hbb.DialOpts{Server: rendezvous.Addr().String(), PeerID: "pipeline-peer", Key: base64.StdEncoding.EncodeToString(serverPublic), ForceRelay: true, Timeout: 3 * time.Second}, done
}

func TestShPipelineEOFEncryptedDelayedOutput(t *testing.T) {
	for _, outcome := range []string{"success", "peer error", "nonzero exit", "cancel"} {
		t.Run(outcome, func(t *testing.T) {
			input, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			if _, err := io.WriteString(writer, shPipelineInput); err != nil {
				t.Fatal(err)
			}
			writer.Close()
			output, err := os.CreateTemp(t.TempDir(), "pipeline-output")
			if err != nil {
				t.Fatal(err)
			}
			defer output.Close()
			oldInput, oldOutput := os.Stdin, os.Stdout
			os.Stdin, os.Stdout = input, output
			defer func() { os.Stdin, os.Stdout = oldInput, oldOutput }()
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			opts, done := shPipelineMock(t, outcome, cancel)
			err = runShInteractiveContext(ctx, opts)
			switch outcome {
			case "success":
				if err != nil {
					t.Fatal(err)
				}
			case "peer error":
				if err == nil || !strings.Contains(err.Error(), "pipeline peer failed") {
					t.Fatalf("peer error not propagated: %v", err)
				}
			case "nonzero exit":
				var status interface{ ExitCode() int }
				if !errors.As(err, &status) || status.ExitCode() != 29 {
					t.Fatalf("exit status not propagated: %v", err)
				}
			case "cancel":
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation not propagated: %v", err)
				}
			}
			for i := 0; i < 2; i++ {
				select {
				case err := <-done:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("encrypted mock did not finish cleanup")
				}
			}
			data, err := os.ReadFile(output.Name())
			if err != nil {
				t.Fatal(err)
			}
			want := "expected\r\n"
			if outcome == "cancel" {
				want = ""
			}
			if string(data) != want {
				t.Fatalf("pipeline output = %q, want %q", data, want)
			}
		})
	}
}

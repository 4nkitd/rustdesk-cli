package integration

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/4nkitd/rustdesk-cli/internal/hbb"
	pb "github.com/4nkitd/rustdesk-cli/internal/hbb/proto"
	"google.golang.org/protobuf/proto"
)

const (
	fixtureID        = "987654321"
	fixturePassword  = "integration-fixture-password"
	fixtureToken     = "integration-fixture-token"
	fixtureSalt      = "integration-salt"
	fixtureChallenge = "integration-challenge"
	fixtureUUID      = "integration-relay-session"
)

type cliHarness struct{ binary string }

type commandResult struct {
	stdout, stderr string
	code           int
}

func buildCLI(t *testing.T) cliHarness {
	t.Helper()
	dir := t.TempDir()
	name := "rdcli"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	binary := filepath.Join(dir, name)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cache := exec.CommandContext(ctx, "go", "env", "-json", "GOCACHE", "GOMODCACHE")
	cacheData, err := cache.Output()
	if err != nil {
		t.Fatalf("locate Go caches: %v", err)
	}
	var caches map[string]string
	if err := json.Unmarshal(cacheData, &caches); err != nil {
		t.Fatal(err)
	}
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "../../cmd/rdcli")
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		switch strings.ToUpper(key) {
		case "HOME", "USERPROFILE", "GOCACHE", "GOMODCACHE":
			continue
		}
		if !strings.HasPrefix(strings.ToUpper(key), "RDC_") {
			build.Env = append(build.Env, value)
		}
	}
	build.Env = append(build.Env, "HOME="+dir, "USERPROFILE="+dir, "RDC_CONFIG="+filepath.Join(dir, "build-config.toml"))
	for key, value := range caches {
		build.Env = append(build.Env, key+"="+value)
	}
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build rdcli: %v\n%s", err, output)
	}
	return cliHarness{binary: binary}
}

func (h cliHarness) run(t *testing.T, args ...string) commandResult {
	t.Helper()
	home := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, h.binary, args...)
	cmd.Dir = home
	// An allowlist prevents inherited RDC_* values and GUI configuration paths from entering the CLI.
	cmd.Env = []string{
		"HOME=" + home, "USERPROFILE=" + home,
		"RDC_CONFIG=" + filepath.Join(home, "config.toml"),
		"XDG_CONFIG_HOME=" + home, "APPDATA=" + home, "LOCALAPPDATA=" + home,
		"TMPDIR=" + home, "TEMP=" + home, "TMP=" + home,
		"PATH=" + filepath.Dir(h.binary),
	}
	if runtime.GOOS == "windows" {
		cmd.Env = append(cmd.Env, "SYSTEMROOT="+os.Getenv("SYSTEMROOT"))
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("rdcli exceeded subprocess deadline: %s", stderr.String())
	}
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("start rdcli: %v", err)
		}
		code = exit.ExitCode()
	}
	result := commandResult{stdout.String(), stderr.String(), code}
	for _, secret := range []string{fixturePassword, fixtureToken} {
		if strings.Contains(result.stdout+result.stderr, secret) {
			t.Fatal("fixture credential leaked into command output")
		}
	}
	return result
}

func (r commandResult) requireCode(t *testing.T, want int) {
	t.Helper()
	if r.code != want {
		t.Fatalf("exit = %d, want %d\nstdout: %s\nstderr: %s", r.code, want, r.stdout, r.stderr)
	}
}

type fixtureServer struct {
	addr string
	done chan struct{}
	err  error
}

func serveOnce(t *testing.T, handler func(*hbb.SecureConn) error) *fixtureServer {
	t.Helper()
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	if err := listener.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		listener.Close()
		t.Fatal(err)
	}
	server := &fixtureServer{addr: listener.Addr().String(), done: make(chan struct{})}
	var mu sync.Mutex
	var active net.Conn
	closing := false
	go func() {
		defer close(server.done)
		defer listener.Close()
		conn, err := listener.Accept()
		if err != nil {
			server.err = err
			return
		}
		defer conn.Close()
		mu.Lock()
		active = conn
		if closing {
			conn.Close()
		}
		mu.Unlock()
		if err := conn.SetDeadline(time.Now().Add(8 * time.Second)); err != nil {
			server.err = err
			return
		}
		server.err = handler(hbb.NewSecureConn(conn))
	}()
	t.Cleanup(func() {
		listener.Close()
		mu.Lock()
		closing = true
		if active != nil {
			active.Close()
		}
		mu.Unlock()
		select {
		case <-server.done:
		case <-time.After(time.Second):
			t.Error("fixture server did not stop")
		}
	})
	return server
}

func (s *fixtureServer) check(t *testing.T) {
	t.Helper()
	select {
	case <-s.done:
		if s.err != nil {
			t.Fatalf("fixture %s: %v", s.addr, s.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("fixture %s did not complete", s.addr)
	}
}

func writeProto(sc *hbb.SecureConn, message proto.Message) error {
	data, err := proto.Marshal(message)
	if err != nil {
		return err
	}
	_, err = sc.Write(data)
	return err
}

func readProto(sc *hbb.SecureConn, message proto.Message) error {
	data, err := sc.ReadMessage()
	if err != nil {
		return err
	}
	return proto.Unmarshal(data, message)
}

func signedID(key ed25519.PrivateKey, public []byte) ([]byte, error) {
	data, err := proto.Marshal(&pb.IdPk{Id: fixtureID, Pk: public})
	if err != nil {
		return nil, err
	}
	return append(ed25519.Sign(key, data), data...), nil
}

type peerFixture struct {
	args             []string
	rendezvous, peer *fixtureServer
}

func newPeerFixture(t *testing.T, connType pb.ConnType, loginError string, handler func(*hbb.SecureConn) error) peerFixture {
	t.Helper()
	return newPeerFixtureVersion(t, "1.4.1", connType, loginError, handler)
}

func newPeerFixtureVersion(t *testing.T, version string, connType pb.ConnType, loginError string, handler func(*hbb.SecureConn) error) peerFixture {
	t.Helper()
	serverPublic, serverPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	peerPublic, peerPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serverKey := base64.StdEncoding.EncodeToString(serverPublic)
	peerIdentity, err := signedID(serverPrivate, peerPublic)
	if err != nil {
		t.Fatal(err)
	}
	peer := serveOnce(t, func(sc *hbb.SecureConn) error {
		relay := &pb.RendezvousMessage{}
		if err := readProto(sc, relay); err != nil {
			return err
		}
		rr := relay.GetRequestRelay()
		if rr == nil || rr.GetId() != fixtureID || rr.GetUuid() != fixtureUUID || rr.GetLicenceKey() != serverKey || rr.GetConnType() != connType {
			return errors.New("unexpected relay request")
		}
		keyPair, err := hbb.GenerateKeyPair()
		if err != nil {
			return err
		}
		identity, err := signedID(peerPrivate, keyPair.Pub)
		if err != nil {
			return err
		}
		if err := writeProto(sc, &pb.Message{Union: &pb.Message_SignedId{SignedId: &pb.SignedId{Id: identity}}}); err != nil {
			return err
		}
		message := &pb.Message{}
		if err := readProto(sc, message); err != nil {
			return err
		}
		pk := message.GetPublicKey()
		if pk == nil {
			return errors.New("missing encrypted peer key exchange")
		}
		key, err := hbb.DecryptSymmetricKey(pk.GetSymmetricValue(), pk.GetAsymmetricValue(), keyPair.Priv)
		if err != nil {
			return err
		}
		sc.SetKey(key)
		if err := writeProto(sc, &pb.Message{Union: &pb.Message_Hash{Hash: &pb.Hash{Salt: fixtureSalt, Challenge: fixtureChallenge}}}); err != nil {
			return err
		}
		if err := readProto(sc, message); err != nil {
			return err
		}
		login := message.GetLoginRequest()
		first := sha256.Sum256([]byte(fixturePassword + fixtureSalt))
		proof := sha256.Sum256(append(first[:], []byte(fixtureChallenge)...))
		if login == nil || !bytes.Equal(login.GetPassword(), proof[:]) {
			return errors.New("missing fixture password proof")
		}
		if connType == pb.ConnType_TERMINAL {
			if login.GetTerminal() == nil {
				return errors.New("expected terminal login")
			}
		} else if connType == pb.ConnType_FILE_TRANSFER {
			if login.GetFileTransfer() == nil || login.GetFileTransfer().GetDir() == "" {
				return errors.New("expected file-transfer login with remote directory")
			}
		} else {
			if login.GetUnion() != nil {
				return errors.New("expected default login")
			}
			option := login.GetOption()
			if option.GetDisableAudio() != pb.OptionMessage_Yes || option.GetDisableClipboard() != pb.OptionMessage_Yes ||
				option.GetDisableKeyboard() != pb.OptionMessage_Yes || option.GetDisableCamera() != pb.OptionMessage_Yes ||
				option.GetEnableFileTransfer() != pb.OptionMessage_No || option.GetLockAfterSessionEnd() != pb.OptionMessage_No {
				return errors.New("default login enabled unrelated peer services")
			}
		}
		response := &pb.LoginResponse{Union: &pb.LoginResponse_PeerInfo{PeerInfo: &pb.PeerInfo{
			Version: version, Platform: "Linux", Hostname: "integration-fixture",
			Features: &pb.Features{Terminal: true}, Displays: []*pb.DisplayInfo{{Width: 3, Height: 2}},
		}}}
		if loginError != "" {
			response.Union = &pb.LoginResponse_Error{Error: loginError}
		}
		if err := writeProto(sc, &pb.Message{Union: &pb.Message_LoginResponse{LoginResponse: response}}); err != nil {
			return err
		}
		if loginError != "" {
			return nil
		}
		return handler(sc)
	})
	rendezvous := serveOnce(t, func(sc *hbb.SecureConn) error {
		keyPair, err := hbb.GenerateKeyPair()
		if err != nil {
			return err
		}
		signed := append(ed25519.Sign(serverPrivate, keyPair.Pub), keyPair.Pub...)
		if err := writeProto(sc, &pb.RendezvousMessage{Union: &pb.RendezvousMessage_KeyExchange{KeyExchange: &pb.KeyExchange{Keys: [][]byte{signed}}}}); err != nil {
			return err
		}
		message := &pb.RendezvousMessage{}
		if err := readProto(sc, message); err != nil {
			return err
		}
		keys := message.GetKeyExchange().GetKeys()
		if len(keys) != 2 {
			return errors.New("missing encrypted rendezvous key exchange")
		}
		key, err := hbb.DecryptSymmetricKey(keys[1], keys[0], keyPair.Priv)
		if err != nil {
			return err
		}
		sc.SetKey(key)
		if err := readProto(sc, message); err != nil {
			return err
		}
		request := message.GetPunchHoleRequest()
		if request == nil || request.GetId() != fixtureID || request.GetToken() != fixtureToken || request.GetLicenceKey() != serverKey || !request.GetForceRelay() || request.GetConnType() != connType {
			return errors.New("unexpected authenticated rendezvous request")
		}
		return writeProto(sc, &pb.RendezvousMessage{Union: &pb.RendezvousMessage_RelayResponse{RelayResponse: &pb.RelayResponse{
			RelayServer: peer.addr, Uuid: fixtureUUID, Union: &pb.RelayResponse_Pk{Pk: peerIdentity},
		}}})
	})
	return peerFixture{
		args:       []string{"--server", rendezvous.addr, "--key", serverKey, "--token", fixtureToken, "--password", fixturePassword, "--relay", "--timeout", "2s"},
		rendezvous: rendezvous, peer: peer,
	}
}

func (f peerFixture) run(t *testing.T, h cliHarness, args ...string) commandResult {
	t.Helper()
	result := h.run(t, append(f.args, args...)...)
	f.rendezvous.check(t)
	f.peer.check(t)
	return result
}

func requireJSON(t *testing.T, output string, target any) {
	t.Helper()
	if err := json.Unmarshal([]byte(output), target); err != nil {
		t.Fatalf("expected one JSON document: %v\n%s", err, output)
	}
}

func unexpectedMessage(message *pb.Message) error {
	return fmt.Errorf("unexpected peer message %T", message.GetUnion())
}

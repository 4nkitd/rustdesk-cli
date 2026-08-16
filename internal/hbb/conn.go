package hbb

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/nacl/secretbox"
	"google.golang.org/protobuf/proto"

	pb "rdcli/internal/hbb/proto"
)

const (
	Version     = "1.4.1"
	RS_PUB_KEY  = "OeVuKk5nlHiXp+APNn0Y3pC1Iwpwn44JGqrQCsWqmBw="
	relayPort   = 21117
	punchMaxTry = 3
)

type LoginKind int

const (
	LoginDefault LoginKind = iota
	LoginFileTransfer
	LoginTunnel
	LoginTerminal
)

type DialOpts struct {
	Server     string
	PeerID     string
	Password   string
	Key        string
	Token      string
	ForceRelay bool
	Yes        bool
	ConnType   pb.ConnType
	Timeout    time.Duration
}

type punchResult struct {
	peerAddr    netip.AddrPort
	signedIDPk  []byte
	relayServer string
	natType     pb.NatType
	isLocal     bool
	uuid        string
	localAddr   netip.AddrPort
}

type Conn struct {
	sc       *SecureConn
	opts     DialOpts
	peerID   string
	direct   bool
	peerInfo *pb.PeerInfo
}

func normalizeRendezvous(server string) string {
	if server == "" {
		return DefaultRendezvousServer
	}
	if _, _, err := net.SplitHostPort(server); err != nil {
		return net.JoinHostPort(server, "21116")
	}
	return server
}

func normalizeRelay(server string) string {
	if _, _, err := net.SplitHostPort(server); err != nil {
		return net.JoinHostPort(server, "21117")
	}
	return server
}

// secureTcp performs the hbbs KeyExchange handshake required when a login
// token is present: the server sends sig(64)||x25519_pub(32) signed by its
// ed25519 key, the client replies with its ephemeral pubkey and a box-sealed
// session key, and the stream switches to encrypted framing. Mirrors
// secure_tcp_impl in rustdesk common.rs.
func secureTcp(conn net.Conn, serverKey string) (*SecureConn, error) {
	rsPk, err := decodeRSKey(serverKey)
	if err != nil {
		return nil, err
	}
	msg, err := readRendezvousMessage(conn)
	if err != nil {
		return nil, fmt.Errorf("hbb: read key exchange: %w", err)
	}
	ke := msg.GetKeyExchange()
	if ke == nil || len(ke.GetKeys()) != 1 {
		return nil, errors.New("hbb: expected key exchange from rendezvous")
	}
	pk, err := VerifySignedBlob(ke.GetKeys()[0], rsPk)
	if err != nil {
		return nil, err
	}
	if len(pk) != 32 {
		return nil, fmt.Errorf("hbb: invalid key exchange key length %d", len(pk))
	}
	asym, sealed, key32, err := CreateSymmetricKeyMsg(pk)
	if err != nil {
		return nil, err
	}
	resp := &pb.RendezvousMessage{
		Union: &pb.RendezvousMessage_KeyExchange{
			KeyExchange: &pb.KeyExchange{Keys: [][]byte{asym, sealed}},
		},
	}
	if err := writeRendezvousMessage(conn, resp); err != nil {
		return nil, err
	}
	sc := NewSecureConn(conn)
	sc.SetKey(key32)
	return sc, nil
}

func writeRendezvousSecure(sc *SecureConn, msg *pb.RendezvousMessage) error {
	data, err := proto.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = sc.Write(data)
	return err
}

func readRendezvousSecure(sc *SecureConn) (*pb.RendezvousMessage, error) {
	raw, err := sc.ReadMessage()
	if err != nil {
		return nil, err
	}
	msg := &pb.RendezvousMessage{}
	if err := proto.Unmarshal(raw, msg); err != nil {
		return nil, err
	}
	return msg, nil
}

// punch connects to the rendezvous server and performs the hole-punch request.
// It may return a RelayResponse in which case uuid and relayServer are set and
// peerAddr is invalid.
func punch(server, peerID, key, token string, forceRelay bool, connType pb.ConnType, timeout time.Duration) (punchResult, error) {
	var res punchResult
	if forceRelay {
		return res, nil
	}

	addr := normalizeRendezvous(server)
	var lastErr error

	natType := pb.NatType_UNKNOWN_NAT
	if !forceRelay {
		if nt, ntErr := TestNATType(server, minDuration(timeout, time.Second)); ntErr == nil {
			natType = nt
		}
	}

	for i := 0; i < punchMaxTry; i++ {
		conn, err := net.DialTimeout("tcp", addr, timeout)
		if err != nil {
			lastErr = fmt.Errorf("hbb: connect rendezvous: %w", err)
			continue
		}
		if timeout > 0 {
			_ = conn.SetDeadline(time.Now().Add(timeout))
		}

		local := conn.LocalAddr().(*net.TCPAddr)
		res.localAddr = local.AddrPort()

		req := &pb.RendezvousMessage{
			Union: &pb.RendezvousMessage_PunchHoleRequest{
				PunchHoleRequest: &pb.PunchHoleRequest{
					Id:         peerID,
					NatType:    natType,
					LicenceKey: key,
					Token:      token,
					ConnType:   connType,
					ForceRelay: forceRelay,
					Version:    Version,
				},
			},
		}

		readMsg := func() (*pb.RendezvousMessage, error) { return readRendezvousMessage(conn) }
		writeMsg := func(m *pb.RendezvousMessage) error { return writeRendezvousMessage(conn, m) }
		if token != "" {
			sc, scErr := secureTcp(conn, key)
			if scErr != nil {
				lastErr = fmt.Errorf("hbb: secure rendezvous: %w", scErr)
				conn.Close()
				continue
			}
			readMsg = func() (*pb.RendezvousMessage, error) { return readRendezvousSecure(sc) }
			writeMsg = func(m *pb.RendezvousMessage) error { return writeRendezvousSecure(sc, m) }
		}

		if err := writeMsg(req); err != nil {
			lastErr = fmt.Errorf("hbb: send punch request: %w", err)
			conn.Close()
			continue
		}

		for j := 0; j < 5; j++ {
			msg, err := readMsg()
			if err != nil {
				lastErr = fmt.Errorf("hbb: read punch response: %w", err)
				break
			}

			switch u := msg.GetUnion().(type) {
			case *pb.RendezvousMessage_PunchHoleResponse:
				conn.Close()
				ph := u.PunchHoleResponse
				if os.Getenv("RDC_DEBUG") != "" {
					fmt.Fprintf(os.Stderr, "hbb debug: punch response socket=%d failure=%v other=%q relay=%q\n", len(ph.GetSocketAddr()), ph.GetFailure(), ph.GetOtherFailure(), ph.GetRelayServer())
				}
				if len(ph.GetSocketAddr()) > 0 {
					res.peerAddr = DecodeAddr(ph.GetSocketAddr())
					res.signedIDPk = ph.GetPk()
					res.relayServer = ph.GetRelayServer()
					res.natType = ph.GetNatType()
					res.isLocal = ph.GetIsLocal()
					return res, nil
				}
				if ph.GetOtherFailure() != "" {
					return res, errors.New(ph.GetOtherFailure())
				}
				switch ph.GetFailure() {
				case pb.PunchHoleResponse_ID_NOT_EXIST:
					return res, errors.New("id does not exist")
				case pb.PunchHoleResponse_OFFLINE:
					return res, errors.New("remote desktop is offline")
				case pb.PunchHoleResponse_LICENSE_MISMATCH:
					return res, errors.New("key mismatch")
				case pb.PunchHoleResponse_LICENSE_OVERUSE:
					return res, errors.New("key overuse")
				}
				lastErr = errors.New("punch hole failed")
			case *pb.RendezvousMessage_RelayResponse:
				conn.Close()
				rr := u.RelayResponse
				res.relayServer = rr.GetRelayServer()
				res.signedIDPk = rr.GetPk()
				res.uuid = rr.GetUuid()
				if rr.GetRefuseReason() != "" {
					return res, errors.New(rr.GetRefuseReason())
				}
				return res, nil
			case *pb.RendezvousMessage_KeyExchange:
				continue
			default:
				lastErr = fmt.Errorf("hbb: unexpected rendezvous message %T", u)
			}
		}
	}
	if lastErr == nil {
		lastErr = errors.New("no response from rendezvous server")
	}
	return res, lastErr
}

// requestRelayViaRendezvous asks hbbs to assign a relay server for force-relay
// connections.
func requestRelayViaRendezvous(server, peerID, key, token string, timeout time.Duration) (string, string, []byte, error) {
	addr := normalizeRendezvous(server)
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return "", "", nil, fmt.Errorf("hbb: connect rendezvous: %w", err)
	}
	defer conn.Close()
	if timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(timeout))
	}
	uuid := newUUID()
	req := &pb.RendezvousMessage{
		Union: &pb.RendezvousMessage_RequestRelay{
			RequestRelay: &pb.RequestRelay{
				Id:          peerID,
				Uuid:        uuid,
				RelayServer: "",
				LicenceKey:  key,
				Token:       token,
				ConnType:    pb.ConnType_DEFAULT_CONN,
			},
		},
	}
	readMsg := func() (*pb.RendezvousMessage, error) { return readRendezvousMessage(conn) }
	writeMsg := func(m *pb.RendezvousMessage) error { return writeRendezvousMessage(conn, m) }
	if token != "" {
		sc, scErr := secureTcp(conn, key)
		if scErr != nil {
			return "", "", nil, fmt.Errorf("hbb: secure rendezvous: %w", scErr)
		}
		readMsg = func() (*pb.RendezvousMessage, error) { return readRendezvousSecure(sc) }
		writeMsg = func(m *pb.RendezvousMessage) error { return writeRendezvousSecure(sc, m) }
	}
	if err := writeMsg(req); err != nil {
		return "", "", nil, err
	}
	for i := 0; i < 12; i++ {
		msg, err := readMsg()
		if err != nil {
			break
		}
		if rr := msg.GetRelayResponse(); rr != nil {
			if rr.GetRefuseReason() != "" {
				return "", "", nil, errors.New(rr.GetRefuseReason())
			}
			return rr.GetRelayServer(), rr.GetUuid(), rr.GetPk(), nil
		}
	}
	return "", "", nil, errors.New("timeout waiting for relay response")
}

func connectRelay(relayServer, peerID, uuid, key string, connType pb.ConnType, timeout time.Duration) (*SecureConn, error) {
	addr := normalizeRelay(relayServer)
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, fmt.Errorf("hbb: connect relay server: %w", err)
	}
	if timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(timeout))
	}
	req := &pb.RendezvousMessage{
		Union: &pb.RendezvousMessage_RequestRelay{
			RequestRelay: &pb.RequestRelay{
				Id:         peerID,
				Uuid:       uuid,
				LicenceKey: key,
				ConnType:   connType,
			},
		},
	}
	if err := writeRendezvousMessage(conn, req); err != nil {
		conn.Close()
		return nil, err
	}
	return NewSecureConn(conn), nil
}

func newUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func decodeRSKey(key string) ([]byte, error) {
	if key == "" {
		key = RS_PUB_KEY
	}
	pk, err := base64.StdEncoding.DecodeString(key)
	if err != nil {
		return nil, fmt.Errorf("hbb: invalid server public key: %w", err)
	}
	if len(pk) != 32 {
		return nil, fmt.Errorf("hbb: invalid server public key length %d", len(pk))
	}
	return pk, nil
}

// Connect establishes a connection to the peer via hole punch or relay and
// performs the secure handshake. Login must still be called.
func Connect(opts DialOpts) (*Conn, error) {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	if isDirectIP(opts.PeerID) {
		return connectDirect(opts, timeout)
	}

	pr, err := punch(opts.Server, opts.PeerID, opts.Key, opts.Token, opts.ForceRelay, opts.ConnType, timeout)
	if err != nil {
		return nil, err
	}

	var sc *SecureConn
	var direct bool

	if os.Getenv("RDC_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, "hbb debug: punch result peerAddr=%v relay=%q uuid=%q signedPkLen=%d\n", pr.peerAddr, pr.relayServer, pr.uuid, len(pr.signedIDPk))
	}

	if !opts.ForceRelay && pr.peerAddr.IsValid() && pr.peerAddr.Port() > 0 {
		conn, dErr := dialPunch(pr.localAddr, pr.peerAddr, connectTimeout(pr, timeout))
		if dErr == nil {
			sc = NewSecureConn(conn)
			direct = true
		} else if os.Getenv("RDC_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "hbb debug: direct punch failed: %v\n", dErr)
		}
	}

	if sc == nil {
		relayServer := pr.relayServer
		uuid := pr.uuid
		signedIDPk := pr.signedIDPk
		if uuid == "" {
			rs, u, pk, rErr := requestRelayViaRendezvous(opts.Server, opts.PeerID, opts.Key, opts.Token, timeout)
			if rErr != nil {
				return nil, rErr
			}
			relayServer = rs
			uuid = u
			if len(pk) > 0 {
				signedIDPk = pk
			}
		}
		sc, err = connectRelay(relayServer, opts.PeerID, uuid, opts.Key, opts.ConnType, timeout)
		if err != nil {
			return nil, err
		}
		pr.signedIDPk = signedIDPk
	}

	c := &Conn{sc: sc, opts: opts, peerID: opts.PeerID, direct: direct}
	if err := c.secureHandshake(pr.signedIDPk, opts.Key); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

func connectTimeout(pr punchResult, timeout time.Duration) time.Duration {
	if pr.isLocal || pr.natType == pb.NatType_SYMMETRIC {
		return minDuration(3*time.Second, timeout)
	}
	return minDuration(12*time.Second, timeout)
}

func isDirectIP(peer string) bool {
	if h, p, err := net.SplitHostPort(peer); err == nil {
		if _, err := strconv.Atoi(p); err != nil {
			return false
		}
		return net.ParseIP(h) != nil
	}
	if ip := net.ParseIP(peer); ip != nil {
		return true
	}
	return false
}

// connectDirect dials a peer by IP/host directly, skipping the rendezvous
// server. These connections are unencrypted (no SignedId channel), so the
// caller must pass --yes to accept the insecure connection.
func connectDirect(opts DialOpts, timeout time.Duration) (*Conn, error) {
	if !opts.Yes {
		return nil, errors.New("insecure direct connection to " + opts.PeerID + " requires --yes")
	}
	addr := opts.PeerID
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(addr, "21116")
	}
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, fmt.Errorf("hbb: direct connect: %w", err)
	}
	if timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(timeout))
	}
	sc := NewSecureConn(conn)
	c := &Conn{sc: sc, opts: opts, peerID: opts.PeerID, direct: true}
	if err := c.secureHandshake(nil, opts.Key); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// secureHandshake implements the SignedId/PublicKey exchange. If the peer
// identity cannot be verified, it falls back to an insecure plaintext
// connection (matching the reference client behaviour).
func (c *Conn) secureHandshake(signedIDPk []byte, key string) error {
	rsPk, err := decodeRSKey(key)
	if err != nil {
		return err
	}

	var peerSignPk []byte
	if len(signedIDPk) > 0 {
		if _, pk, vErr := VerifySignedID(signedIDPk, rsPk); vErr == nil {
			peerSignPk = pk
		} else if os.Getenv("RDC_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "hbb debug: server blob verify failed: %v\n", vErr)
		}
	}

	if len(peerSignPk) == 0 {
		if os.Getenv("RDC_DEBUG") != "" {
			fmt.Fprintln(os.Stderr, "hbb debug: no peer sign key, insecure fallback")
		}
		// send an empty message out in case server is setting up secure and
		// waiting for first message
		return c.sendMessage(&pb.Message{})
	}

	raw, err := c.sc.ReadMessage()
	if err != nil {
		if os.Getenv("RDC_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "hbb debug: read signed id: %v\n", err)
		}
		return fmt.Errorf("hbb: read signed id: %w", err)
	}
	msg := &pb.Message{}
	if err := proto.Unmarshal(raw, msg); err != nil {
		return fmt.Errorf("hbb: parse message: %w", err)
	}
	sid := msg.GetSignedId()
	if sid == nil {
		return c.sendMessage(&pb.Message{})
	}
	id, theirPk, err := VerifySignedID(sid.GetId(), peerSignPk)
	if err != nil || id != c.peerID {
		return c.sendMessage(&pb.Message{})
	}

	asym, sym, key32, err := CreateSymmetricKeyMsg(theirPk)
	if err != nil {
		return err
	}
	out := &pb.Message{
		Union: &pb.Message_PublicKey{
			PublicKey: &pb.PublicKey{
				AsymmetricValue: asym,
				SymmetricValue:  sym,
			},
		},
	}
	if err := c.sendMessage(out); err != nil {
		return err
	}
	c.sc.SetKey(key32)
	if os.Getenv("RDC_DEBUG") != "" {
		fmt.Fprintln(os.Stderr, "hbb debug: public key sent, session key set")
	}
	return nil
}

func (c *Conn) sendMessage(msg *pb.Message) error {
	data, err := proto.Marshal(msg)
	if err != nil {
		return fmt.Errorf("hbb: marshal message: %w", err)
	}
	if _, err := c.sc.Write(data); err != nil {
		return fmt.Errorf("hbb: send message: %w", err)
	}
	return nil
}

// Send writes a message on the connection.
func (c *Conn) Send(msg *pb.Message) error {
	return c.sendMessage(msg)
}

// Recv reads the next message on the connection.
func (c *Conn) Recv() (*pb.Message, error) {
	raw, err := c.sc.ReadMessage()
	if err != nil {
		return nil, err
	}
	msg := &pb.Message{}
	if err := proto.Unmarshal(raw, msg); err != nil {
		return nil, fmt.Errorf("hbb: parse message: %w", err)
	}
	if os.Getenv("RDC_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, "hbb debug: recv msg %T\n", msg.GetUnion())
	}
	return msg, nil
}

func hashPassword(password, salt, challenge string) []byte {
	h1 := h1ForPassword(password, salt)
	h := sha256.New()
	h.Write(h1)
	h.Write([]byte(challenge))
	return h.Sum(nil)
}

// h1ForPassword returns the intermediate h1 secret used for login hashing.
// RustDesk GUI stores remembered passwords as "00" + base64(secretbox(h1))
// keyed by the machine UUID; when the password looks like such a blob we
// decrypt it. Otherwise hash plaintext+salt.
func h1ForPassword(password, salt string) []byte {
	if strings.HasPrefix(password, "00") {
		if h1, err := decryptH1Blob(password, machineUUID()); err == nil && len(h1) == 32 {
			return h1
		}
	}
	first := sha256.Sum256([]byte(password + salt))
	return first[:]
}

// decryptH1Blob decrypts a "00"-prefixed encrypted h1 blob using the given
// machine identifier as the secretbox key (password_security.rs symmetric_crypt).
func decryptH1Blob(blob string, machineID []byte) ([]byte, error) {
	if len(blob) <= 2 || len(machineID) == 0 {
		return nil, errors.New("invalid h1 blob")
	}
	payload, err := base64.StdEncoding.DecodeString(blob[2:])
	if err != nil {
		return nil, err
	}
	var key [32]byte
	copy(key[:], machineID)
	if len(payload) < 1+24+16 {
		return nil, errors.New("h1 blob too short")
	}
	if payload[0] != 1 {
		return nil, errors.New("unsupported h1 blob format")
	}
	var nonce [24]byte
	copy(nonce[:], payload[1:25])
	open, ok := secretbox.Open(nil, payload[25:], &nonce, &key)
	if !ok {
		return nil, errors.New("h1 blob decryption failed")
	}
	return open, nil
}

// machineUUID returns the platform machine identifier RustDesk uses to key
// local secretbox encryption (hbb_common get_uuid), or nil if unavailable.
func machineUUID() []byte {
	if out, err := exec.Command("ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, `"IOPlatformUUID"`) {
				if idx := strings.Index(line, "="); idx >= 0 {
					u := strings.TrimSpace(line[idx+1:])
					u = strings.Trim(u, `"`)
					if u != "" {
						return []byte(u)
					}
				}
			}
		}
	}
	if id, err := os.ReadFile("/etc/machine-id"); err == nil {
		if s := strings.TrimSpace(string(id)); s != "" {
			return []byte(s)
		}
	}
	return nil
}

func randomPeerID() string {
	const digits = "0123456789"
	b := make([]byte, 9)
	if _, err := rand.Read(b); err != nil {
		return "000000000"
	}
	for i := range b {
		b[i] = digits[int(b[i])%len(digits)]
	}
	return string(b)
}

// LoginSpec carries per-kind login details.
type LoginSpec struct {
	Host string // tunnel remote host
	Port int32  // tunnel remote port
	Dir  string // file transfer remote dir
}

// Login sends the LoginRequest for the given kind and waits for the peer's
// LoginResponse, returning the peer info on success.
func (c *Conn) Login(kind LoginKind, spec LoginSpec) (*pb.PeerInfo, error) {
	// The peer sends Hash{challenge, salt} immediately after the secure
	// handshake, before we send our login request.
	for {
		msg, err := c.Recv()
		if err != nil {
			return nil, err
		}
		switch u := msg.GetUnion().(type) {
		case *pb.Message_Hash:
			hash := u.Hash
			lr := &pb.LoginRequest{
				Username:   c.peerID,
				MyId:       randomPeerID(),
				MyName:     "rdcli",
				MyPlatform: "Linux",
				Version:    Version,
			}
			if c.opts.Password != "" {
				lr.Password = hashPassword(c.opts.Password, hash.GetSalt(), hash.GetChallenge())
			}
			switch kind {
			case LoginFileTransfer:
				lr.Union = &pb.LoginRequest_FileTransfer{
					FileTransfer: &pb.FileTransfer{Dir: spec.Dir},
				}
			case LoginTunnel:
				lr.Union = &pb.LoginRequest_PortForward{
					PortForward: &pb.PortForward{Host: spec.Host, Port: spec.Port},
				}
			case LoginTerminal:
				lr.Union = &pb.LoginRequest_Terminal{Terminal: &pb.Terminal{}}
			}
			if err := c.sendMessage(&pb.Message{
				Union: &pb.Message_LoginRequest{LoginRequest: lr},
			}); err != nil {
				return nil, err
			}
		case *pb.Message_LoginResponse:
			lr := u.LoginResponse
			if errMsg := lr.GetError(); errMsg != "" {
				return nil, fmt.Errorf("%s", errMsg)
			}
			pi := lr.GetPeerInfo()
			if pi == nil {
				return nil, errors.New("empty peer info in login response")
			}
			c.peerInfo = pi
			return pi, nil
		case *pb.Message_TestDelay:
			td := u.TestDelay
			if err := c.sendMessage(&pb.Message{
				Union: &pb.Message_TestDelay{TestDelay: td},
			}); err != nil {
				return nil, err
			}
		case *pb.Message_Misc:
			misc := u.Misc
			if cr := misc.GetCloseReason(); cr != "" {
				return nil, fmt.Errorf("peer closed: %s", cr)
			}
		}
	}
}

func (c *Conn) PeerInfo() *pb.PeerInfo {
	return c.peerInfo
}

func (c *Conn) Direct() bool {
	return c.direct
}

func (c *Conn) SetRaw() {
	c.sc.SetRaw()
}

func (c *Conn) ReadRaw(p []byte) (int, error) {
	return c.sc.Read(p)
}

func (c *Conn) WriteRaw(p []byte) (int, error) {
	return c.sc.Write(p)
}

func (c *Conn) Close() error {
	return c.sc.Close()
}

package hbb

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"net"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	pb "github.com/4nkitd/rustdesk-cli/internal/hbb/proto"
)

func peerTestBytes(number protowire.Number, value []byte) []byte {
	return protowire.AppendBytes(protowire.AppendTag(nil, number, protowire.BytesType), value)
}

func peerTestDirectory(entries []byte) []byte {
	return peerTestBytes(18, peerTestBytes(1, entries))
}

func TestDecodePeerMessageRejectsEmptyEntryAmplification(t *testing.T) {
	raw := peerTestDirectory(bytes.Repeat([]byte{0x1a, 0}, 16384))
	msg := &pb.Message{Union: &pb.Message_Hash{Hash: &pb.Hash{Challenge: "unchanged"}}}
	err := decodePeerMessage(raw, msg)
	if err == nil || !strings.Contains(err.Error(), "repeated field") {
		t.Fatalf("expected repeated-field rejection, got %v", err)
	}
	if msg.GetHash().GetChallenge() != "unchanged" {
		t.Fatal("destination changed before resource validation completed")
	}
	if allocs := testing.AllocsPerRun(3, func() {
		_ = decodePeerMessage(raw, msg)
	}); allocs > 100 {
		t.Fatalf("pre-scan allocated per entry: %.0f allocations", allocs)
	}
}

func TestDecodePeerMessageManifestAtLimit(t *testing.T) {
	dir := &pb.FileDirectory{Id: 1, Path: "/remote"}
	for i := 0; i < maxPeerRepeatedField; i++ {
		dir.Entries = append(dir.Entries, &pb.FileEntry{
			EntryType: pb.FileType_File, Name: fmt.Sprintf("file-%d", i), Size: 1024, ModifiedTime: 123,
		})
	}
	want := &pb.Message{Union: &pb.Message_FileResponse{FileResponse: &pb.FileResponse{Union: &pb.FileResponse_Dir{Dir: dir}}}}
	peerTestRoundTrip(t, want)
}

func peerTestRoundTrip(t *testing.T, want *pb.Message) {
	t.Helper()
	raw, err := proto.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got pb.Message
	if err := decodePeerMessage(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(want, &got) {
		t.Fatal("decoded message differs")
	}
}

func TestDecodePeerMessageNormalAndOpaquePayloads(t *testing.T) {
	var screenshot bytes.Buffer
	if err := png.Encode(&screenshot, image.NewNRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	opaque := append(bytes.Repeat([]byte{0x1a, 0}, 16384), 0xff)
	for name, msg := range map[string]*pb.Message{
		"empty": {},
		"hash":  {Union: &pb.Message_Hash{Hash: &pb.Hash{Salt: "salt", Challenge: "challenge"}}},
		"screenshot": {Union: &pb.Message_ScreenshotResponse{ScreenshotResponse: &pb.ScreenshotResponse{
			Sid: "request", Data: screenshot.Bytes(),
		}}},
		"compressed clipboard opaque": {Union: &pb.Message_Clipboard{Clipboard: &pb.Clipboard{Compress: true, Content: opaque}}},
		"terminal opaque": {Union: &pb.Message_TerminalResponse{TerminalResponse: &pb.TerminalResponse{Union: &pb.TerminalResponse_Data{
			Data: &pb.TerminalData{TerminalId: 1, Compressed: true, Data: opaque},
		}}}},
		"packed sessions": {Union: &pb.Message_TerminalResponse{TerminalResponse: &pb.TerminalResponse{Union: &pb.TerminalResponse_Opened{
			Opened: &pb.TerminalOpened{Success: true, PersistentSessions: []int32{1, 2, 3}},
		}}}},
	} {
		t.Run(name, func(t *testing.T) { peerTestRoundTrip(t, msg) })
	}
}

func TestDecodePeerMessageAggregatesMergedRepeatedFields(t *testing.T) {
	entries := bytes.Repeat([]byte{0x1a, 0}, maxPeerRepeatedField/2+1)
	raw := append(peerTestDirectory(entries), peerTestDirectory(entries)...)
	if err := decodePeerMessage(raw, &pb.Message{}); err == nil || !strings.Contains(err.Error(), "repeated field") {
		t.Fatalf("merged directory fields escaped limit: %v", err)
	}
	packed := peerTestBytes(6, bytes.Repeat([]byte{0}, maxPeerRepeatedField))
	packed = append(packed, protowire.AppendVarint(protowire.AppendTag(nil, 6, protowire.VarintType), 1)...)
	raw = peerTestBytes(32, peerTestBytes(1, packed))
	if err := decodePeerMessage(raw, &pb.Message{}); err == nil || !strings.Contains(err.Error(), "repeated field") {
		t.Fatalf("mixed packed/unpacked field escaped limit: %v", err)
	}
}

func TestDecodePeerMessageAggregateElementBudget(t *testing.T) {
	values := make([]byte, maxPeerRepeatedField)
	raw := peerTestBytes(15, peerTestBytes(8, values))
	raw = append(raw, peerTestBytes(10, peerTestBytes(4, values))...)
	displays := append(peerTestBytes(1, values), peerTestBytes(2, values)...)
	raw = append(raw, peerTestBytes(19, peerTestBytes(30, displays))...)
	if err := decodePeerMessage(raw, &pb.Message{}); err != nil {
		t.Fatalf("aggregate boundary rejected: %v", err)
	}
	raw = append(raw, peerTestBytes(19, peerTestBytes(30, peerTestBytes(3, []byte{0})))...)
	if err := decodePeerMessage(raw, &pb.Message{}); err == nil || !strings.Contains(err.Error(), "repeated element budget") {
		t.Fatalf("expected aggregate element rejection, got %v", err)
	}
}

func TestDecodePeerMessageClipboardElementBudget(t *testing.T) {
	raw := peerTestBytes(28, bytes.Repeat([]byte{0x0a, 0}, 16384))
	if err := decodePeerMessage(raw, &pb.Message{}); err == nil || !strings.Contains(err.Error(), "repeated field") {
		t.Fatalf("expected clipboard element rejection, got %v", err)
	}
}

func TestDecodePeerMessageObjectAndFieldBudgets(t *testing.T) {
	for name, test := range map[string]struct {
		raw  []byte
		want string
	}{
		"objects": {bytes.Repeat(peerTestBytes(9, nil), maxPeerMessageObjects), "object budget"},
		"fields":  {bytes.Repeat([]byte{0x70, 0}, maxPeerWireFields+1), "field budget"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := decodePeerMessage(test.raw, &pb.Message{}); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %s, got %v", test.want, err)
			}
		})
	}
}

func TestDecodePeerMessageUnknownFields(t *testing.T) {
	unknown := peerTestBytes(100, []byte{0xff, 0xff, 0xff})
	unknown = protowire.AppendTag(unknown, 101, protowire.StartGroupType)
	unknown = append(unknown, peerTestBytes(1, []byte{0xff})...)
	unknown = protowire.AppendTag(unknown, 101, protowire.EndGroupType)
	var msg pb.Message
	if err := decodePeerMessage(unknown, &msg); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(msg.ProtoReflect().GetUnknown(), unknown) {
		t.Fatal("unknown fields were not preserved")
	}
	oversized := peerTestBytes(100, make([]byte, maxPeerUnknownBytes))
	if err := decodePeerMessage(oversized, &msg); err == nil || !strings.Contains(err.Error(), "unknown field byte budget") {
		t.Fatalf("expected unknown-byte rejection, got %v", err)
	}
	wrongType := peerTestBytes(14, make([]byte, maxPeerUnknownBytes))
	if err := decodePeerMessage(wrongType, &msg); err == nil || !strings.Contains(err.Error(), "unknown field byte budget") {
		t.Fatalf("wrong wire type escaped unknown budget: %v", err)
	}
	part := peerTestBytes(100, make([]byte, maxPeerUnknownBytes/2))
	combined := append(peerTestBytes(9, part), peerTestBytes(9, part)...)
	if err := decodePeerMessage(combined, &msg); err == nil || !strings.Contains(err.Error(), "unknown field byte budget") {
		t.Fatalf("nested unknown fields escaped aggregate budget: %v", err)
	}
}

func TestDecodePeerMessageUnknownGroupDepth(t *testing.T) {
	for _, depth := range []int{maxPeerMessageDepth - 1, maxPeerMessageDepth} {
		var raw []byte
		for i := 0; i < depth; i++ {
			raw = protowire.AppendTag(raw, 100, protowire.StartGroupType)
		}
		for i := 0; i < depth; i++ {
			raw = protowire.AppendTag(raw, 100, protowire.EndGroupType)
		}
		err := decodePeerMessage(raw, &pb.Message{})
		if depth < maxPeerMessageDepth && err != nil {
			t.Fatalf("valid group depth rejected: %v", err)
		}
		if depth == maxPeerMessageDepth && (err == nil || !strings.Contains(err.Error(), "depth limit")) {
			t.Fatalf("expected depth rejection, got %v", err)
		}
	}
}

func TestDecodePeerMessageMalformedWire(t *testing.T) {
	for name, raw := range map[string][]byte{
		"zero field":           {0},
		"truncated tag":        {0x80},
		"truncated bytes":      {0x4a, 2, 0},
		"invalid child":        peerTestBytes(9, []byte{0xff}),
		"bad packed varint":    peerTestBytes(32, peerTestBytes(1, peerTestBytes(6, []byte{0x80}))),
		"unexpected group end": protowire.AppendTag(nil, 100, protowire.EndGroupType),
		"missing group end":    protowire.AppendTag(nil, 100, protowire.StartGroupType),
		"mismatched group end": protowire.AppendTag(protowire.AppendTag(nil, 100, protowire.StartGroupType), 101, protowire.EndGroupType),
		"reserved wire type":   {0x4e},
	} {
		t.Run(name, func(t *testing.T) {
			if err := decodePeerMessage(raw, &pb.Message{}); err == nil {
				t.Fatal("accepted malformed message")
			}
		})
	}
	if err := decodePeerMessage(nil, nil); err == nil {
		t.Fatal("accepted nil destination")
	}
}

func TestDecodeRendezvousMessageBudgets(t *testing.T) {
	for name, raw := range map[string][]byte{
		"keys":    peerTestBytes(25, bytes.Repeat([]byte{0x0a, 0}, 16384)),
		"servers": peerTestBytes(14, bytes.Repeat([]byte{0x12, 0}, 16384)),
		"headers": peerTestBytes(28, bytes.Repeat([]byte{0x12, 0}, 16384)),
	} {
		t.Run(name, func(t *testing.T) {
			msg := &pb.RendezvousMessage{Union: &pb.RendezvousMessage_OnlineResponse{OnlineResponse: &pb.OnlineResponse{States: []byte{1}}}}
			if err := decodeRendezvousMessage(raw, msg); err == nil || !strings.Contains(err.Error(), "repeated field") {
				t.Fatalf("expected repeated-field rejection, got %v", err)
			}
			if !bytes.Equal(msg.GetOnlineResponse().GetStates(), []byte{1}) {
				t.Fatal("destination changed before resource validation completed")
			}
			if allocs := testing.AllocsPerRun(3, func() {
				_ = decodeRendezvousMessage(raw, msg)
			}); allocs > 100 {
				t.Fatalf("pre-scan allocated per element: %.0f allocations", allocs)
			}
		})
	}
	if err := decodeRendezvousMessage(nil, nil); err == nil {
		t.Fatal("accepted nil rendezvous destination")
	}
}

func TestDecodeRendezvousMessageNormalAndOpaquePayloads(t *testing.T) {
	opaque := append(bytes.Repeat([]byte{0x0a, 0}, 16384), 0xff)
	for name, want := range map[string]*pb.RendezvousMessage{
		"keys":    {Union: &pb.RendezvousMessage_KeyExchange{KeyExchange: &pb.KeyExchange{Keys: [][]byte{opaque, {1, 2, 3}}}}},
		"servers": {Union: &pb.RendezvousMessage_ConfigureUpdate{ConfigureUpdate: &pb.ConfigUpdate{Serial: 1, RendezvousServers: []string{"one.example", "two.example"}}}},
		"proxy": {Union: &pb.RendezvousMessage_HttpProxyResponse{HttpProxyResponse: &pb.HttpProxyResponse{
			Status: 200, Body: opaque, Headers: []*pb.HeaderEntry{{Name: "Content-Type", Value: "application/octet-stream"}},
		}}},
	} {
		t.Run(name, func(t *testing.T) {
			raw, err := proto.Marshal(want)
			if err != nil {
				t.Fatal(err)
			}
			var got pb.RendezvousMessage
			if err := decodeRendezvousMessage(raw, &got); err != nil {
				t.Fatal(err)
			}
			if !proto.Equal(want, &got) {
				t.Fatal("decoded rendezvous message differs")
			}
		})
	}
}

func TestConnRecvRejectsEntryAmplificationLoopback(t *testing.T) {
	listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	deadline := time.Now().Add(2 * time.Second)
	if err := listener.SetDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	sender, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()
	receiver, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	for _, conn := range []net.Conn{sender, receiver} {
		if err := conn.SetDeadline(deadline); err != nil {
			t.Fatal(err)
		}
	}
	sendStream, recvStream := NewSecureConn(sender), NewSecureConn(receiver)
	key := [32]byte{1, 2, 3}
	sendStream.SetKey(key)
	recvStream.SetKey(key)
	want := &pb.Message{Union: &pb.Message_Hash{Hash: &pb.Hash{Challenge: "loopback"}}}
	normal, err := proto.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	hostile := peerTestDirectory(bytes.Repeat([]byte{0x1a, 0}, 16384))
	written := make(chan error, 1)
	go func() {
		for _, raw := range [][]byte{normal, hostile} {
			if _, err := sendStream.Write(raw); err != nil {
				written <- err
				return
			}
		}
		written <- nil
	}()
	conn := &Conn{sc: recvStream}
	got, err := conn.Recv()
	if err != nil || !proto.Equal(want, got) {
		t.Fatalf("normal encrypted frame failed: %v", err)
	}
	got, err = conn.Recv()
	if got != nil || err == nil || !strings.Contains(err.Error(), "repeated field hbb.FileDirectory.entries") {
		t.Fatalf("hostile encrypted frame was not rejected by decoder budget: %v", err)
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
}

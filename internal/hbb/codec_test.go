package hbb

import (
	"bytes"
	"testing"
)

func TestCodec1(t *testing.T) {
	codec := NewBytesCodec()
	data := bytes.Repeat([]byte{1}, 0x3F)

	buf := codec.Encode(data)
	if len(buf) != 0x3F+1 {
		t.Fatalf("expected length %d, got %d", 0x3F+1, len(buf))
	}

	bufSaved := make([]byte, len(buf))
	copy(bufSaved, buf)

	res, ok := codec.DecodeFrame(&buf)
	if !ok || len(res) != 0x3F || res[0] != 1 {
		t.Fatalf("decode failed: ok=%v, len=%d", ok, len(res))
	}

	codec2 := NewBytesCodec()
	var buf2 []byte
	if _, ok := codec2.DecodeFrame(&buf2); ok {
		t.Fatal("expected false on empty buffer")
	}

	buf2 = append(buf2, bufSaved[0:1]...)
	if _, ok := codec2.DecodeFrame(&buf2); ok {
		t.Fatal("expected false on incomplete frame header")
	}

	buf2 = append(buf2, bufSaved[1:]...)
	res2, ok := codec2.DecodeFrame(&buf2)
	if !ok || len(res2) != 0x3F || res2[0] != 1 {
		t.Fatalf("incremental decode failed: ok=%v, len=%d", ok, len(res2))
	}
}

func TestCodec2(t *testing.T) {
	codec := NewBytesCodec()
	buf := codec.Encode([]byte(""))
	if len(buf) != 1 {
		t.Fatalf("expected empty payload encoded length 1, got %d", len(buf))
	}

	data2 := bytes.Repeat([]byte{2}, 0x3F+1)
	buf = append(buf, codec.Encode(data2)...)
	if len(buf) != 0x3F+2+2 {
		t.Fatalf("expected total length %d, got %d", 0x3F+2+2, len(buf))
	}

	res1, ok1 := codec.DecodeFrame(&buf)
	if !ok1 || len(res1) != 0 {
		t.Fatalf("frame 1 decode failed: ok=%v, len=%d", ok1, len(res1))
	}

	res2, ok2 := codec.DecodeFrame(&buf)
	if !ok2 || len(res2) != 0x3F+1 || res2[0] != 2 {
		t.Fatalf("frame 2 decode failed: ok=%v, len=%d", ok2, len(res2))
	}
}

func TestCodec3(t *testing.T) {
	codec := NewBytesCodec()
	data := bytes.Repeat([]byte{3}, 0x3F-1)

	buf := codec.Encode(data)
	if len(buf) != 0x3F+1-1 {
		t.Fatalf("expected length %d, got %d", 0x3F+1-1, len(buf))
	}

	res, ok := codec.DecodeFrame(&buf)
	if !ok || len(res) != 0x3F-1 || res[0] != 3 {
		t.Fatalf("decode failed: ok=%v, len=%d", ok, len(res))
	}
}

func TestCodec4(t *testing.T) {
	codec := NewBytesCodec()
	data := bytes.Repeat([]byte{4}, 0x3FFF)

	buf := codec.Encode(data)
	if len(buf) != 0x3FFF+2 {
		t.Fatalf("expected length %d, got %d", 0x3FFF+2, len(buf))
	}

	res, ok := codec.DecodeFrame(&buf)
	if !ok || len(res) != 0x3FFF || res[0] != 4 {
		t.Fatalf("decode failed: ok=%v, len=%d", ok, len(res))
	}
}

func TestCodec5(t *testing.T) {
	codec := NewBytesCodec()
	data := bytes.Repeat([]byte{5}, 0x3FFFFF)

	buf := codec.Encode(data)
	if len(buf) != 0x3FFFFF+3 {
		t.Fatalf("expected length %d, got %d", 0x3FFFFF+3, len(buf))
	}

	res, ok := codec.DecodeFrame(&buf)
	if !ok || len(res) != 0x3FFFFF || res[0] != 5 {
		t.Fatalf("decode failed: ok=%v, len=%d", ok, len(res))
	}
}

func TestCodec6(t *testing.T) {
	codec := NewBytesCodec()
	data := bytes.Repeat([]byte{6}, 0x3FFFFF+1)

	buf := codec.Encode(data)
	if len(buf) != 0x3FFFFF+4+1 {
		t.Fatalf("expected length %d, got %d", 0x3FFFFF+4+1, len(buf))
	}

	bufSaved := make([]byte, len(buf))
	copy(bufSaved, buf)

	res, ok := codec.DecodeFrame(&buf)
	if !ok || len(res) != 0x3FFFFF+1 || res[0] != 6 {
		t.Fatalf("decode failed: ok=%v, len=%d", ok, len(res))
	}

	codec2 := NewBytesCodec()
	var buf2 []byte

	buf2 = append(buf2, bufSaved[0:1]...)
	if _, ok := codec2.DecodeFrame(&buf2); ok {
		t.Fatal("expected false on incomplete header")
	}

	buf2 = append(buf2, bufSaved[1:6]...)
	if _, ok := codec2.DecodeFrame(&buf2); ok {
		t.Fatal("expected false on incomplete payload")
	}

	buf2 = append(buf2, bufSaved[6:]...)
	res2, ok := codec2.DecodeFrame(&buf2)
	if !ok || len(res2) != 0x3FFFFF+1 || res2[0] != 6 {
		t.Fatalf("incremental decode failed: ok=%v, len=%d", ok, len(res2))
	}
}

func TestCodecIncrementalOneByteAtATime(t *testing.T) {
	codec := NewBytesCodec()
	data := []byte("hello, rustdesk framing protocol!")

	encoded := codec.Encode(data)
	var stream []byte

	for i := 0; i < len(encoded)-1; i++ {
		stream = append(stream, encoded[i])
		if _, ok := codec.DecodeFrame(&stream); ok {
			t.Fatalf("unexpected frame at byte offset %d", i)
		}
	}

	stream = append(stream, encoded[len(encoded)-1])
	res, ok := codec.DecodeFrame(&stream)
	if !ok {
		t.Fatal("expected frame decode upon full stream arrival")
	}
	if !bytes.Equal(res, data) {
		t.Fatalf("decoded data %q != expected %q", res, data)
	}
	if len(stream) != 0 {
		t.Fatalf("expected empty stream, got len %d", len(stream))
	}
}

func TestCodecRawMode(t *testing.T) {
	codec := NewBytesCodec()
	codec.SetRaw(true)

	data := []byte("raw passthrough data")
	enc := codec.Encode(data)
	if !bytes.Equal(enc, data) {
		t.Fatalf("raw encode mismatch: got %q, want %q", enc, data)
	}

	buf := append([]byte(nil), data...)
	res, ok := codec.DecodeFrame(&buf)
	if !ok || !bytes.Equal(res, data) {
		t.Fatalf("raw decode mismatch: ok=%v, res=%q", ok, res)
	}
	if len(buf) != 0 {
		t.Fatalf("expected buffer to be emptied in raw mode, got len %d", len(buf))
	}
}

func TestCodecRoundtrip(t *testing.T) {
	sizes := []int{0, 1, 63, 64, 100, 16383, 16384, 100000, 4194303, 4194304}

	for _, size := range sizes {
		payload := make([]byte, size)
		for i := 0; i < size; i++ {
			payload[i] = byte(i % 251)
		}

		encoded := Encode(payload)
		buf := append([]byte(nil), encoded...)

		decoded, ok := DecodeFrame(&buf)
		if !ok {
			t.Fatalf("failed to decode size %d", size)
		}
		if !bytes.Equal(decoded, payload) {
			t.Fatalf("payload mismatch for size %d", size)
		}
		if len(buf) != 0 {
			t.Fatalf("expected empty buffer for size %d, remaining %d", size, len(buf))
		}
	}
}

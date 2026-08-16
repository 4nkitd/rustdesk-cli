package hbb

import (
	"encoding/binary"
	"fmt"
)

type BytesCodec struct {
	raw             bool
	maxPacketLength int
}

func NewBytesCodec() *BytesCodec {
	return &BytesCodec{}
}

func (c *BytesCodec) SetRaw(raw bool) {
	c.raw = raw
}

func (c *BytesCodec) SetMaxPacketLength(maxLen int) {
	c.maxPacketLength = maxLen
}

func (c *BytesCodec) Encode(data []byte) []byte {
	if c.raw {
		out := make([]byte, len(data))
		copy(out, data)
		return out
	}

	length := len(data)
	var header []byte

	if length <= 0x3F {
		header = []byte{byte(length << 2)}
	} else if length <= 0x3FFF {
		h := uint16((length << 2) | 0x1)
		header = make([]byte, 2)
		binary.LittleEndian.PutUint16(header, h)
	} else if length <= 0x3FFFFF {
		h := uint32((length << 2) | 0x2)
		header = []byte{
			byte(h & 0xFF),
			byte((h >> 8) & 0xFF),
			byte((h >> 16) & 0xFF),
		}
	} else if length <= 0x3FFFFFFF {
		h := uint32((length << 2) | 0x3)
		header = make([]byte, 4)
		binary.LittleEndian.PutUint32(header, h)
	} else {
		panic(fmt.Sprintf("hbb: payload length %d exceeds maximum limit (0x3FFFFFFF)", length))
	}

	out := make([]byte, len(header)+length)
	copy(out, header)
	copy(out[len(header):], data)
	return out
}

func (c *BytesCodec) DecodeFrame(src *[]byte) ([]byte, bool) {
	if src == nil || len(*src) == 0 {
		return nil, false
	}

	if c.raw {
		data := *src
		*src = nil
		return data, true
	}

	headLen := int(((*src)[0] & 0x3) + 1)
	if len(*src) < headLen {
		return nil, false
	}

	var n uint32
	switch headLen {
	case 1:
		n = uint32((*src)[0])
	case 2:
		n = uint32(binary.LittleEndian.Uint16((*src)[0:2]))
	case 3:
		n = uint32((*src)[0]) | (uint32((*src)[1]) << 8) | (uint32((*src)[2]) << 16)
	case 4:
		n = binary.LittleEndian.Uint32((*src)[0:4])
	}

	payloadLen := int(n >> 2)
	if c.maxPacketLength > 0 && payloadLen > c.maxPacketLength {
		return nil, false
	}

	totalLen := headLen + payloadLen
	if len(*src) < totalLen {
		return nil, false
	}

	frame := (*src)[headLen:totalLen]
	*src = (*src)[totalLen:]
	return frame, true
}

var defaultCodec = NewBytesCodec()

func Encode(data []byte) []byte {
	return defaultCodec.Encode(data)
}

func DecodeFrame(src *[]byte) ([]byte, bool) {
	return defaultCodec.DecodeFrame(src)
}

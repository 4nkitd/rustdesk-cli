package hbb

import (
	"encoding/binary"
	"math/big"
	"net/netip"
	"time"
)

func EncodeAddr(addr netip.AddrPort) []byte {
	unmapped := addr.Addr().Unmap()
	if unmapped.Is4() {
		tm := uint64(uint32(time.Now().UnixMicro()))
		ipBytes := unmapped.As4()
		ip := uint64(binary.LittleEndian.Uint32(ipBytes[:]))
		port := uint64(addr.Port())

		v := new(big.Int)
		part1 := new(big.Int).SetUint64(ip + tm)
		part1.Lsh(part1, 49)

		part2 := new(big.Int).SetUint64(tm)
		part2.Lsh(part2, 17)

		part3 := new(big.Int).SetUint64(port + (tm & 0xffff))

		v.Or(part1, part2)
		v.Or(v, part3)

		var padded [16]byte
		v.FillBytes(padded[:])
		for i := 0; i < 8; i++ {
			padded[i], padded[15-i] = padded[15-i], padded[i]
		}

		nPadding := 0
		for i := 15; i >= 0; i-- {
			if padded[i] == 0 {
				nPadding++
			} else {
				break
			}
		}
		return padded[:16-nPadding]
	}

	ip16 := addr.Addr().As16()
	res := make([]byte, 18)
	copy(res[:16], ip16[:])
	binary.LittleEndian.PutUint16(res[16:18], addr.Port())
	return res
}

func DecodeAddr(b []byte) netip.AddrPort {
	if len(b) > 16 {
		if len(b) != 18 {
			return netip.AddrPortFrom(netip.AddrFrom4([4]byte{}), 0)
		}
		var ipBytes [16]byte
		copy(ipBytes[:], b[:16])
		ip := netip.AddrFrom16(ipBytes)
		port := binary.LittleEndian.Uint16(b[16:18])
		return netip.AddrPortFrom(ip, port)
	}

	var padded [16]byte
	copy(padded[:len(b)], b)

	var bePadded [16]byte
	for i := 0; i < 16; i++ {
		bePadded[i] = padded[15-i]
	}

	number := new(big.Int).SetBytes(bePadded[:])

	tmBig := new(big.Int).Rsh(number, 17)
	tmBig.And(tmBig, big.NewInt(0xffffffff))
	tm := tmBig.Uint64()

	ipBig := new(big.Int).Rsh(number, 49)
	ipBig.Sub(ipBig, new(big.Int).SetUint64(tm))
	ipUint32 := uint32(ipBig.Uint64())

	var ipBytes [4]byte
	binary.LittleEndian.PutUint32(ipBytes[:], ipUint32)
	ip := netip.AddrFrom4(ipBytes)

	mask24 := new(big.Int).SetUint64(0xffffff)
	numLow := new(big.Int).And(number, mask24)
	portUint64 := numLow.Uint64() - (tm & 0xffff)
	port := uint16(portUint64)

	return netip.AddrPortFrom(ip, port)
}

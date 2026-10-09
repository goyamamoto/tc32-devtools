// The LE Secure Connections functions of Bluetooth Core Vol 3 Part H 2.2
// for the scripted central, the Go port of emulator/ble_sc.py and p256.py:
// P-256 on crypto/elliptic, AES-CMAC (RFC 4493) on aes128.Encrypt, f4, f5,
// f6 and g2 in the octet order the SMP PDUs carry their values (least
// significant octet first). sc_test.go checks them against the
// specification's vectors and a recorded pairing.
//
// SPDX-License-Identifier: Apache-2.0
package ble

import (
	"crypto/elliptic"
	"encoding/binary"
	"math/big"

	"github.com/goyamamoto/tc32-devtools/go/ble/aes128"
)

var (
	p256      = elliptic.P256()
	P256Order = p256.Params().N
	scSalt    = []byte{0x6c, 0x88, 0x83, 0x91, 0xaa, 0xf5, 0xa5, 0x38, 0x60, 0x37, 0x0b, 0xdb, 0x5a, 0x60, 0x83, 0xbe}
)

func reversed(b []byte) []byte {
	out := make([]byte, len(b))
	for i, v := range b {
		out[len(b)-1-i] = v
	}
	return out
}

// LEToBig is an integer from its on-air octets (least significant first).
func LEToBig(b []byte) *big.Int { return new(big.Int).SetBytes(reversed(b)) }

// BigToLE is n on-air octets of x.
func BigToLE(x *big.Int, n int) []byte { return reversed(x.FillBytes(make([]byte, n))) }

// PublicKey is priv * G, the coordinates as the Pairing Public Key PDU carries them (x then y, 64 octets).
func PublicKey(priv *big.Int) []byte {
	x, y := p256.ScalarBaseMult(priv.FillBytes(make([]byte, 32)))
	return append(BigToLE(x, 32), BigToLE(y, 32)...)
}

// OnCurve reports whether a public key in PDU form is a point of the curve.
func OnCurve(pk []byte) bool {
	if len(pk) != 64 {
		return false
	}
	return p256.IsOnCurve(LEToBig(pk[:32]), LEToBig(pk[32:]))
}

// DHKey is the x coordinate of priv * pk, 32 on-air octets.
func DHKey(priv *big.Int, pk []byte) []byte {
	x, _ := p256.ScalarMult(LEToBig(pk[:32]), LEToBig(pk[32:]), priv.FillBytes(make([]byte, 32)))
	return BigToLE(x, 32)
}

func cmacShift(k []byte) []byte {
	out := make([]byte, 16)
	carry := byte(0)
	for i := 15; i >= 0; i-- {
		out[i] = k[i]<<1 | carry
		carry = k[i] >> 7
	}
	if carry != 0 {
		out[15] ^= 0x87
	}
	return out
}

// AESCMAC is RFC 4493: the key and the message as written, the MAC most significant octet first.
func AESCMAC(key, m []byte) []byte {
	k1 := cmacShift(aes128.Encrypt(key, make([]byte, 16)))
	k2 := cmacShift(k1)
	blocks := (len(m) + 15) / 16
	if blocks == 0 {
		blocks = 1
	}
	whole := len(m) > 0 && len(m)%16 == 0
	last := make([]byte, 16)
	copy(last, m[(blocks-1)*16:])
	sub := k1
	if !whole {
		last[len(m)-(blocks-1)*16] = 0x80
		sub = k2
	}
	x := make([]byte, 16)
	for i := 0; i < blocks-1; i++ {
		for j := range x {
			x[j] ^= m[i*16+j]
		}
		x = aes128.Encrypt(key, x)
	}
	for j := range x {
		x[j] ^= last[j] ^ sub[j]
	}
	return aes128.Encrypt(key, x)
}

// F4 is the confirm value: u, v the 32-octet x coordinates, x the 16-octet random, z one octet (on-air order).
func F4(u, v, x []byte, z byte) []byte {
	m := append(append(reversed(u), reversed(v)...), z)
	return reversed(AESCMAC(reversed(x), m))
}

// Addr7 is A1 or A2 of f5 and f6: the address type, then the 6 on-air octets most significant first.
func Addr7(addrType int, addr []byte) []byte {
	return append([]byte{byte(addrType)}, reversed(addr)...)
}

// F5 gives MacKey and LTK (on-air order) from the DHKey w, the randoms and Addr7 of both sides.
func F5(w, n1, n2, a1, a2 []byte) (mackey, ltk []byte) {
	t := AESCMAC(scSalt, reversed(w))
	body := append([]byte("btle"), reversed(n1)...)
	body = append(body, reversed(n2)...)
	body = append(body, a1...)
	body = append(body, a2...)
	body = append(body, 1, 0)
	return reversed(AESCMAC(t, append([]byte{0}, body...))), reversed(AESCMAC(t, append([]byte{1}, body...)))
}

// F6 is a DHKey check (on-air order): w the MacKey, iocap the octets AuthReq, OOB flag, IO capability.
func F6(w, n1, n2, r, iocap, a1, a2 []byte) []byte {
	m := append(reversed(n1), reversed(n2)...)
	m = append(m, reversed(r)...)
	m = append(m, iocap...)
	m = append(m, a1...)
	m = append(m, a2...)
	return reversed(AESCMAC(reversed(w), m))
}

// G2 is the numeric comparison value, below 1000000.
func G2(u, v, x, y []byte) uint32 {
	m := append(append(reversed(u), reversed(v)...), reversed(y)...)
	return binary.BigEndian.Uint32(AESCMAC(reversed(x), m)[12:]) % 1000000
}

// IOCapOf is AuthReq, OOB flag, IO capability of a Pairing Request or Response as sent.
func IOCapOf(pdu []byte) []byte { return []byte{pdu[3], pdu[2], pdu[1]} }

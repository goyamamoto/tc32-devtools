// Package aes128 is the Go port of ../../../emulator/aes128.py: AES-128
// (FIPS-197), CCM (RFC 3610) and the Bluetooth LE security functions a
// scripted central needs: e, c1, s1 (Core Vol 3 Part H 2.2) and the link
// layer's AES-CCM with a 4-octet MIC (Core Vol 6 Part E 2.1). Byte slices are
// in the standard AES orientation (first byte = most significant octet)
// unless a name says LE; 128-bit integers are *big.Int, as the Python ints.
//
// SPDX-License-Identifier: Apache-2.0
package aes128

import (
	"math/big"
)

var sbox, inv [256]byte

func init() {
	p, q := 1, 1
	for {
		if p&0x80 != 0 {
			p = p ^ ((p << 1) & 0xFF) ^ 0x1B
		} else {
			p = p ^ ((p << 1) & 0xFF)
		}
		q ^= q << 1
		q ^= q << 2
		q ^= q << 4
		q &= 0xFF
		if q&0x80 != 0 {
			q ^= 0x09
		}
		x := q ^ ((q << 1) | (q >> 7)) ^ ((q << 2) | (q >> 6)) ^ ((q << 3) | (q >> 5)) ^ ((q << 4) | (q >> 4))
		x = (x ^ 0x63) & 0xFF
		sbox[p] = byte(x)
		inv[x] = byte(p)
		if p == 1 {
			break
		}
	}
	sbox[0] = 0x63
	inv[0x63] = 0
}

func xt(a byte) byte {
	if a&0x80 != 0 {
		return (a << 1) ^ 0x1B
	}
	return a << 1
}

func mul(a, b byte) byte {
	var r byte
	for b != 0 {
		if b&1 != 0 {
			r ^= a
		}
		a = xt(a)
		b >>= 1
	}
	return r
}

func expand(key []byte) [11][16]byte {
	var w [44][4]byte
	for i := 0; i < 4; i++ {
		copy(w[i][:], key[4*i:4*i+4])
	}
	rcon := byte(1)
	for i := 4; i < 44; i++ {
		t := w[i-1]
		if i%4 == 0 {
			t = [4]byte{sbox[t[1]], sbox[t[2]], sbox[t[3]], sbox[t[0]]}
			t[0] ^= rcon
			rcon = xt(rcon)
		}
		for j := 0; j < 4; j++ {
			w[i][j] = w[i-4][j] ^ t[j]
		}
	}
	var rk [11][16]byte
	for r := 0; r < 11; r++ {
		for c := 0; c < 4; c++ {
			copy(rk[r][4*c:4*c+4], w[4*r+c][:])
		}
	}
	return rk
}

// Encrypt is the AES-128 block encryption.
func Encrypt(key, block []byte) []byte {
	rk := expand(key)
	var s [16]byte
	for i := 0; i < 16; i++ {
		s[i] = block[i] ^ rk[0][i]
	}
	for rnd := 1; rnd <= 10; rnd++ {
		for i := range s {
			s[i] = sbox[s[i]]
		}
		var t [16]byte
		for i := 0; i < 16; i++ { // ShiftRows (column-major state)
			t[i] = s[(i+4*(i%4))%16]
		}
		s = t
		if rnd != 10 {
			var n [16]byte
			for c := 0; c < 4; c++ {
				a := s[4*c : 4*c+4]
				n[4*c] = mul(a[0], 2) ^ mul(a[1], 3) ^ a[2] ^ a[3]
				n[4*c+1] = a[0] ^ mul(a[1], 2) ^ mul(a[2], 3) ^ a[3]
				n[4*c+2] = a[0] ^ a[1] ^ mul(a[2], 2) ^ mul(a[3], 3)
				n[4*c+3] = mul(a[0], 3) ^ a[1] ^ a[2] ^ mul(a[3], 2)
			}
			s = n
		}
		for i := 0; i < 16; i++ {
			s[i] ^= rk[rnd][i]
		}
	}
	return s[:]
}

// Decrypt is the AES-128 block decryption.
func Decrypt(key, block []byte) []byte {
	rk := expand(key)
	var s [16]byte
	for i := 0; i < 16; i++ {
		s[i] = block[i] ^ rk[10][i]
	}
	for rnd := 9; rnd >= 0; rnd-- {
		var t [16]byte
		for i := 0; i < 16; i++ { // InvShiftRows
			t[i] = s[((i-4*(i%4))%16+16)%16]
		}
		s = t
		for i := range s {
			s[i] = inv[s[i]]
		}
		for i := 0; i < 16; i++ {
			s[i] ^= rk[rnd][i]
		}
		if rnd != 0 {
			var n [16]byte
			for c := 0; c < 4; c++ {
				a := s[4*c : 4*c+4]
				n[4*c] = mul(a[0], 14) ^ mul(a[1], 11) ^ mul(a[2], 13) ^ mul(a[3], 9)
				n[4*c+1] = mul(a[0], 9) ^ mul(a[1], 14) ^ mul(a[2], 11) ^ mul(a[3], 13)
				n[4*c+2] = mul(a[0], 13) ^ mul(a[1], 9) ^ mul(a[2], 14) ^ mul(a[3], 11)
				n[4*c+3] = mul(a[0], 11) ^ mul(a[1], 13) ^ mul(a[2], 9) ^ mul(a[3], 14)
			}
			s = n
		}
	}
	return s[:]
}

// Big128 makes a 128-bit big.Int from 16 big-endian bytes.
func Big128(b []byte) *big.Int { return new(big.Int).SetBytes(b) }

// Bytes128 writes a big.Int as 16 big-endian bytes (Python int.to_bytes(16, "big")).
func Bytes128(x *big.Int) []byte { return x.FillBytes(make([]byte, 16)) }

// BytesLE returns n little-endian bytes of x (Python int.to_bytes(n, "little")).
func BytesLE(x *big.Int, n int) []byte {
	b := x.FillBytes(make([]byte, n))
	for i, j := 0, n-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	return b
}

// FromLE reads little-endian bytes as an integer (Python int.from_bytes(b, "little")).
func FromLE(b []byte) *big.Int {
	r := make([]byte, len(b))
	for i := range b {
		r[len(b)-1-i] = b[i]
	}
	return new(big.Int).SetBytes(r)
}

// E is the security function e with 128-bit integers.
func E(k, p *big.Int) *big.Int { return Big128(Encrypt(Bytes128(k), Bytes128(p))) }

// C1 is the legacy confirm value. preq/pres: the 7-octet SMP commands as
// sent; ia/ra: 6-octet addresses as sent (LSO first).
func C1(k, r *big.Int, preq, pres []byte, iat int, ia []byte, rat int, ra []byte) *big.Int {
	p1 := new(big.Int).Lsh(FromLE(pres), 72)
	p1.Or(p1, new(big.Int).Lsh(FromLE(preq), 16))
	p1.Or(p1, big.NewInt(int64(rat<<8|iat)))
	p2 := new(big.Int).Lsh(FromLE(ia), 48)
	p2.Or(p2, FromLE(ra))
	x := new(big.Int).Xor(r, p1)
	x = E(k, x)
	x.Xor(x, p2)
	return E(k, x)
}

// S1 is the key generation function s1.
func S1(k, r1, r2 *big.Int) *big.Int {
	mask := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 64), big.NewInt(1))
	x := new(big.Int).Lsh(new(big.Int).And(r1, mask), 64)
	x.Or(x, new(big.Int).And(r2, mask))
	return E(k, x)
}

// Ah is the random address hash ah (Core Vol 3 Part H 2.2.2): the low 24
// bits of e(k, r) for a 24-bit r.
func Ah(k *big.Int, r uint32) uint32 {
	x := E(k, big.NewInt(int64(r&0xFFFFFF)))
	return uint32(new(big.Int).And(x, big.NewInt(0xFFFFFF)).Int64())
}

// RPA is a resolvable private address, 6 octets LSO first: the hash in the
// low three octets, prand (bits 23:22 = 01) in the high three. irk: 16 octets
// LSO first, as SMP's Identity Information carries it.
func RPA(irk []byte, prand uint32) []byte {
	prand = prand&0x3FFFFF | 0x400000
	h := Ah(FromLE(irk), prand)
	return []byte{byte(h), byte(h >> 8), byte(h >> 16), byte(prand), byte(prand >> 8), byte(prand >> 16)}
}

func xorInto(dst, a, b []byte) {
	for i := range dst {
		dst[i] = a[i] ^ b[i]
	}
}

// CCM is CCM encryption (RFC 3610): data' || MIC. The length field has
// 15 - len(nonce) octets; aad shorter than 0xff00 octets.
func CCM(key, nonce, aad, data []byte, micLen int) []byte {
	n, lf := len(data), 15-len(nonce)
	flags := byte(((micLen-2)/2)<<3 | (lf - 1))
	if len(aad) > 0 {
		flags |= 0x40
	}
	b0 := append(append([]byte{flags}, nonce...), lenBytes(n, lf)...)
	x := Encrypt(key, b0)
	var blocks []byte
	if len(aad) > 0 {
		a := append([]byte{byte(len(aad) >> 8), byte(len(aad))}, aad...)
		blocks = append(blocks, a...)
		blocks = append(blocks, make([]byte, (16-len(a)%16)%16)...)
	}
	blocks = append(blocks, data...)
	blocks = append(blocks, make([]byte, (16-n%16)%16)...)
	for i := 0; i < len(blocks); i += 16 {
		var t [16]byte
		xorInto(t[:], x, blocks[i:i+16])
		x = Encrypt(key, t[:])
	}
	ctr := func(i int) []byte {
		return Encrypt(key, append(append([]byte{byte(lf - 1)}, nonce...), lenBytes(i, lf)...))
	}
	out := make([]byte, 0, n+micLen)
	for i := 0; i < n; i += 16 {
		s := ctr(i/16 + 1)
		end := i + 16
		if end > n {
			end = n
		}
		for j := i; j < end; j++ {
			out = append(out, data[j]^s[j-i])
		}
	}
	s0 := ctr(0)
	for i := 0; i < micLen; i++ {
		out = append(out, x[i]^s0[i])
	}
	return out
}

func lenBytes(n, lf int) []byte {
	b := make([]byte, lf)
	for i := lf - 1; i >= 0; i-- {
		b[i] = byte(n)
		n >>= 8
	}
	return b
}

// CCMEncrypt is the BLE LL AES-CCM: payload' || MIC (4 octets). sk: 16 key
// octets (standard orientation); nonce: 13 octets; AAD = hdr0 with NESN, SN,
// MD masked (& 0xe3).
func CCMEncrypt(sk, nonce []byte, hdr0 byte, payload []byte) []byte {
	return CCM(sk, nonce, []byte{hdr0 & 0xE3}, payload, 4)
}

// CCMDecrypt is the inverse of CCMEncrypt: (payload, MIC ok). Fewer than 4
// octets cannot hold the MIC: (empty, false).
func CCMDecrypt(sk, nonce []byte, hdr0 byte, data []byte) ([]byte, bool) {
	if len(data) < 4 {
		return []byte{}, false
	}
	n := len(data) - 4
	lf := 15 - len(nonce)
	out := make([]byte, 0, n)
	for i := 0; i < n; i += 16 {
		s := Encrypt(sk, append(append([]byte{byte(lf - 1)}, nonce...), lenBytes(i/16+1, lf)...))
		end := i + 16
		if end > n {
			end = n
		}
		for j := i; j < end; j++ {
			out = append(out, data[j]^s[j-i])
		}
	}
	mic := CCMEncrypt(sk, nonce, hdr0, out)[n:]
	ok := true
	for i := range mic {
		if mic[i] != data[n+i] {
			ok = false
		}
	}
	return out, ok
}

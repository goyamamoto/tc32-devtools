// SPDX-License-Identifier: Apache-2.0
package aes128

import (
	"bytes"
	"encoding/hex"
	"math/big"
	"testing"
)

func hx(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

func bigHex(s string) *big.Int {
	x, ok := new(big.Int).SetString(s, 16)
	if !ok {
		panic(s)
	}
	return x
}

// The known answers of checks/ble_models_check.py's crypto() and the
// module-level asserts of aes128.py.
func TestFIPS197(t *testing.T) {
	k := make([]byte, 16)
	for i := range k {
		k[i] = byte(i)
	}
	p := hx("00112233445566778899aabbccddeeff")
	c := Encrypt(k, p)
	if hex.EncodeToString(c) != "69c4e0d86a7b0430d8cdb78070b4c55a" {
		t.Fatalf("FIPS-197 C.1: %x", c)
	}
	if !bytes.Equal(Decrypt(k, c), p) {
		t.Fatal("decrypt")
	}
	if sbox[0x53] != 0xED || inv[0xED] != 0x53 {
		t.Fatal("S-box")
	}
}

func TestRFC3610(t *testing.T) {
	key := hx("c0c1c2c3c4c5c6c7c8c9cacbcccdcecf")
	nonce := hx("00000003020100a0a1a2a3a4a5")
	pkt := make([]byte, 31)
	for i := range pkt {
		pkt[i] = byte(i)
	}
	out := CCM(key, nonce, pkt[:8], pkt[8:], 8)
	if hex.EncodeToString(out) != "588c979a61c663d2f066d0c2c0f989806d5f6b61dac38417e8d12cfdf926e0" {
		t.Fatalf("RFC 3610 packet vector #1: %x", out)
	}
}

func TestSMP(t *testing.T) {
	c1 := C1(big.NewInt(0), bigHex("5783D52156AD6F0E6388274EC6702EE0"), hx("01010000100707"), hx("02030000080005"),
		1, hx("a6a5a4a3a2a1"), 0, hx("b6b5b4b3b2b1"))
	if c1.Cmp(bigHex("1E1E3FEF878988EAD2A74DC5BEF13B86")) != 0 {
		t.Fatalf("c1: %x", c1)
	}
	s1 := S1(big.NewInt(0), bigHex("000F0E0D0C0B0A091122334455667788"), bigHex("010203040506070899AABBCCDDEEFF00"))
	if s1.Cmp(bigHex("9A1FE1F0E8B0F49B5B4216AE796DA062")) != 0 {
		t.Fatalf("s1: %x", s1)
	}
}

// Core Vol 3 Part H D.7 sample data, and the RPA made from it.
func TestAh(t *testing.T) {
	if h := Ah(bigHex("EC0234A357C8AD05341010A60A397D9B"), 0x708194); h != 0x0DFBAA {
		t.Fatalf("ah: %06x", h)
	}
	irk := hx("9b7d390aa610103405adc857a33402ec")
	if a := RPA(irk, 0x708194); hex.EncodeToString(a) != "aafb0d948170" {
		t.Fatalf("rpa: %x", a)
	}
}

func TestLLEncryption(t *testing.T) {
	sk := E(bigHex("4C68384139F574D836BCF34E9DFB01BF"), bigHex("0213243546576879ACBDCEDFE0F10213"))
	if sk.Cmp(bigHex("99AD1B5226A37E3E058E3B8E27C2C666")) != 0 {
		t.Fatalf("SK: %x", sk)
	}
	nonce := append(append([]byte{0, 0, 0, 0}, 0x80), hx("24abdcbabebaafde")...)
	enc := CCMEncrypt(Bytes128(sk), nonce, 0x0F, []byte{0x06})
	if hex.EncodeToString(enc) != "9fcda7f448" {
		t.Fatalf("packet: %x", enc)
	}
	dec, ok := CCMDecrypt(Bytes128(sk), nonce, 0x0F, enc)
	if !ok || !bytes.Equal(dec, []byte{6}) {
		t.Fatalf("decrypt: %x %v", dec, ok)
	}
	tail := CCMEncrypt(Bytes128(sk), nonce, 0x0F, nil)
	short, ok := CCMDecrypt(Bytes128(sk), nonce, 0x0F, append([]byte{0, 0}, tail[len(tail)-1]))
	if ok || len(short) != 0 {
		t.Fatalf("3 octets must not pass: %x %v", short, ok)
	}
}

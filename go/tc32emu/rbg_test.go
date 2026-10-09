// SPDX-License-Identifier: Apache-2.0
package tc32emu

// The random number generator's sources and the 32 kHz jitter: the Go port
// of checks/rbg_check.py, the same cases and the same digests.

import (
	"crypto/sha256"
	"encoding/binary"
	hexenc "encoding/hex"
	"math/bits"
	"testing"
)

var rbgDigests = map[string]string{
	"lcg":           "21fec69d8d40f43d3db8cc0aa5b50187ee93f5753249756e287297ec53dcfef3",
	"healthy:7":     "5f3e4c48b587b6a658ddb749fca054f558a383328ba57710b6861bec4e12d041",
	"stuck:0":       "8bfe96b7ab7217459a0d2f0b4b020a21e5976fec991eba4803711536093ca1b2",
	"biased:243":    "03e6ae5ee4cfd96a59b7ff5d6bdb3bc0f9fdcb94017963cd02219c6fc50f58c4",
	"cycle:8":       "6401d64776273163028f7e7a56f521db75ebc3e786e39498a3b263ef3d9c0f1a",
	"attacker:4660": "7fe500de9c002c813d5f8d993debc20c3be1c1a42995a6b30f56bb7f202abfdf",
}

const k32Digest = "7b2833aff6ee20cb0b0d95f91a7da645d29e9ef052bbf5fe729553c0b5127307"

func rbgMachine(t *testing.T) *Machine {
	fl := NewFlash(FlashMem)
	fl.Mem[8] = 0x4B
	m, err := NewMachine(fl, -1, 1)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func rbgRead(t *testing.T, m *Machine, o, size int) uint32 {
	v, err := m.RegRead(o, size)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func rbgWords(t *testing.T, m *Machine, n int) []uint32 {
	w := make([]uint32, n)
	for i := range w {
		w[i] = rbgRead(t, m, 0x440C, 4)
	}
	return w
}

func digestU32(ws []uint32) string {
	h := sha256.New()
	var b [4]byte
	for _, w := range ws {
		binary.LittleEndian.PutUint32(b[:], w)
		h.Write(b[:])
	}
	return hexenc.EncodeToString(h.Sum(nil))
}

func TestRBGSources(t *testing.T) {
	m := rbgMachine(t)
	if rbgRead(t, m, 0x4408, 1)&1 != 1 {
		t.Error("lcg: SR bit 0 reads 0 with the block off")
	}
	ws := rbgWords(t, m, 64)
	if ws[0] != 0x0B719151 {
		t.Errorf("lcg: first word %#x", ws[0])
	}
	if d := digestU32(ws); d != rbgDigests["lcg"] {
		t.Errorf("lcg: digest %s", d)
	}
	cases := []struct {
		name        string
		mode, param uint32
	}{{"healthy:7", 1, 7}, {"stuck:0", 2, 0}, {"biased:243", 3, 243}, {"cycle:8", 4, 8}, {"attacker:4660", 5, 4660}}
	for _, c := range cases {
		m := rbgMachine(t)
		if err := m.RegWrite(0xFFF4, 4, c.mode|c.param<<8); err != nil {
			t.Fatal(err)
		}
		if rbgRead(t, m, 0x4408, 1)&1 != 0 || rbgRead(t, m, 0x440C, 4) != 0 {
			t.Errorf("%s: ready or DR not 0 with the block off", c.name)
		}
		m.Regs[0x65] |= 0x08
		m.Regs[0x4400] |= 0x01
		if rbgRead(t, m, 0x4408, 1)&1 != 1 {
			t.Errorf("%s: not ready when clocked and enabled", c.name)
		}
		m.Regs[0x62] |= 0x08
		if rbgRead(t, m, 0x4408, 1)&1 != 0 {
			t.Errorf("%s: ready in reset", c.name)
		}
		m.Regs[0x62] &^= 0x08
		ws := rbgWords(t, m, 64)
		if d := digestU32(ws); d != rbgDigests[c.name] {
			t.Errorf("%s: digest %s, the Python check's is %s", c.name, d, rbgDigests[c.name])
		}
		if c.mode == 3 {
			ones := 0
			for _, w := range rbgWords(t, m, 1024) {
				ones += bits.OnesCount32(w)
			}
			if f := float64(ones) / (1024 * 32); f < 243.0/256-0.01 || f > 243.0/256+0.01 {
				t.Errorf("biased: %.4f of the bits are 1", f)
			}
		}
		if err := m.RegWrite(0xFFF4, 4, c.mode|c.param<<8); err != nil {
			t.Fatal(err)
		}
		if d := digestU32(rbgWords(t, m, 64)); d != rbgDigests[c.name] {
			t.Errorf("%s: setting the source again does not start afresh", c.name)
		}
	}
	m = rbgMachine(t)
	m.Regs[0x65] |= 0x08
	m.Regs[0x4400] |= 0x01
	if err := m.RegWrite(0xFFF4, 4, 6); err != nil {
		t.Fatal(err)
	}
	if rbgRead(t, m, 0x4408, 1)&1 != 0 {
		t.Error("never: SR bit 0 reads 1")
	}
	if err := m.RegWrite(0xFFF4, 4, 7); err == nil {
		t.Error("an unknown source is taken")
	}
}

func TestK32Jitter(t *testing.T) {
	m := rbgMachine(t)
	if m.k32Now() != 0 {
		t.Error("power-on: the count does not start at 0")
	}
	if err := m.RegWrite(0xFFFC, 4, 7); err != nil {
		t.Fatal(err)
	}
	if err := m.RegWrite(0xFFF8, 4, 200); err != nil {
		t.Fatal(err)
	}
	var counts []uint32
	worst, moved := int64(0), 0
	for i := 0; i < 4000; i++ {
		m.Cycles += 977
		c := m.k32Now()
		e := int64(m.Ms() * 32.768)
		counts = append(counts, c)
		d := int64(c) - e
		if d < 0 {
			d = -d
		}
		if d > worst {
			worst = d
		}
		if int64(c) != e {
			moved++
		}
	}
	if worst > 3 {
		t.Errorf("jitter 200 ns: the count %d ticks from the exact one", worst)
	}
	if moved == 0 {
		t.Error("jitter 200 ns: the count never differs from the exact one")
	}
	for i := 1; i < len(counts); i++ {
		if counts[i] < counts[i-1] {
			t.Fatal("jitter: the count goes back")
		}
	}
	m.Cycles += 24_000_000
	c := m.k32Now()
	if d := int64(c) - int64(m.Ms()*32.768); d > 3 || d < -3 {
		t.Errorf("jitter: after a jump of 1 s the count is %d off", d)
	}
	if d := digestU32(append(counts, c)); d != k32Digest {
		t.Errorf("k32 jitter: digest %s, the Python check's is %s", d, k32Digest)
	}
	if err := m.RegWrite(0xFFF8, 4, 0); err != nil {
		t.Fatal(err)
	}
	if m.k32Now() != uint32(int64(m.Ms()*32.768)) {
		t.Error("jitter 0: not the exact count")
	}
	if err := m.RegWrite(0xFFF8, 4, 0x80000000); err != nil {
		t.Fatal(err)
	}
	c0, b0 := m.k32Now(), rbgRead(t, m, 0x74B, 1)
	m.Cycles += 48_000
	if m.k32Now() != c0 || rbgRead(t, m, 0x750, 4) != c0 || rbgRead(t, m, 0x74B, 1) != b0 {
		t.Error("0xfff8 bit 31: the 32 kHz count moves")
	}
	if err := m.RegWrite(0xFFF8, 4, 0); err != nil {
		t.Fatal(err)
	}
	if m.k32Now() != uint32(int64(m.Ms()*32.768)) {
		t.Error("0xfff8 bit 31 clear: the count does not go on")
	}
}

// The AES block: FIPS-197 Appendix B, both ways (as checks/rbg_check.py).
func TestAESBlock(t *testing.T) {
	m := rbgMachine(t)
	key, _ := hexenc.DecodeString("2b7e151628aed2a6abf7158809cf4f3c")
	pt, _ := hexenc.DecodeString("3243f6a8885a308d313198a2e0370734")
	ct, _ := hexenc.DecodeString("3925841d02dc09fbdc118597196a0b32")
	for _, dec := range []uint32{0, 1} {
		in, want := pt, ct
		if dec == 1 {
			in, want = ct, pt
		}
		if err := m.RegWrite(0x540, 1, dec); err != nil {
			t.Fatal(err)
		}
		copy(m.Regs[0x550:0x560], key)
		for i := 0; i < 16; i += 4 {
			if err := m.RegWrite(0x548, 4, binary.LittleEndian.Uint32(in[i:])); err != nil {
				t.Fatal(err)
			}
		}
		if rbgRead(t, m, 0x540, 1)&0x04 == 0 {
			t.Error("AES block: bit 2 not set after the fourth word")
		}
		var got []byte
		for i := 0; i < 4; i++ {
			got = binary.LittleEndian.AppendUint32(got, rbgRead(t, m, 0x548, 4))
		}
		if hexenc.EncodeToString(got) != hexenc.EncodeToString(want) {
			t.Errorf("AES block (decrypt %d): %x", dec, got)
		}
	}
}

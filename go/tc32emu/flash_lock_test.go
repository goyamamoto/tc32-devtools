// SPDX-License-Identifier: Apache-2.0
package tc32emu

// The flash model's status register and block protection: the Go port of
// checks/flash_lock_check.py, the same cases.

import (
	"strings"
	"testing"
)

func TestFlashLock(t *testing.T) {
	check := func(cond bool, text string) {
		t.Helper()
		if !cond {
			t.Errorf("FAIL %s", text)
		}
	}
	spi := func(fl *Flash, data []byte, read int) ([]byte, error) {
		var got []byte
		if err := fl.Select(true); err != nil {
			return got, err
		}
		for _, b := range data {
			if _, err := fl.Clock(b); err != nil {
				return got, err
			}
		}
		for i := 0; i < read; i++ {
			v, err := fl.Clock(0)
			if err != nil {
				return got, err
			}
			got = append(got, v)
		}
		return got, fl.Select(false)
	}
	status := func(fl *Flash) int {
		lo, _ := spi(fl, []byte{0x05}, 1)
		hi, _ := spi(fl, []byte{0x35}, 1)
		return int(lo[0]) | int(hi[0])<<8
	}
	program := func(fl *Flash, addr int, data ...byte) error {
		spi(fl, []byte{0x06}, 0)
		_, err := spi(fl, append([]byte{0x02, byte(addr >> 16), byte(addr >> 8), byte(addr)}, data...), 0)
		return err
	}
	erase := func(fl *Flash, addr int, cmd byte) error {
		spi(fl, []byte{0x06}, 0)
		_, err := spi(fl, []byte{cmd, byte(addr >> 16), byte(addr >> 8), byte(addr)}, 0)
		return err
	}
	writeStatus := func(fl *Flash, cmd byte, data ...byte) error {
		spi(fl, []byte{0x06}, 0)
		_, err := spi(fl, append([]byte{cmd}, data...), 0)
		return err
	}
	rangeIs := func(fl *Flash, first, last int) bool {
		r, p, l := fl.ProtectedRange()
		return p && l && r.First == first && r.Last == last
	}
	none := func(fl *Flash) bool {
		_, p, l := fl.ProtectedRange()
		return !p && l
	}
	last := func(fl *Flash) FlashOp { return fl.Log[len(fl.Log)-1] }
	opIs := func(op FlashOp, name string, addr, n int) bool {
		return op.Op == name && op.Addr == addr && op.Len == n
	}
	newFlash := NewFlashJedec

	// GigaDevice GD25LD40C (mid 0x1360c8, 512 KB).
	fl := newFlash(0x80000, [3]byte{0xC8, 0x60, 0x14})
	check(fl.MID == 0x1360C8, "512 KB GD mid")
	check(status(fl) == 0 && none(fl), "status 0 after power-on")
	spi(fl, []byte{0x06}, 0)
	v, _ := spi(fl, []byte{0x05}, 1)
	check(v[0] == 0x02, "WEL in bit 1")
	spi(fl, []byte{0x04}, 0)
	err := writeStatus(fl, 0x01, 0x18)
	check(err == nil && status(fl) == 0x18 && rangeIs(fl, 0, 0x3FFFF), "0x18: the low 256 KB protected")
	check(last(fl).Op == "status" && last(fl).Addr == 0x01 && last(fl).Data == "18", "status logged")
	err = program(fl, 0x3F000, 0x00)
	check(err == nil && fl.Mem[0x3F000] == 0xFF && opIs(last(fl), "protected", 0x3F000, 1), "program at 0x3f000 ignored")
	err = program(fl, 0x8, 0xFF, 0x00)
	check(err == nil && fl.Mem[0x9] == 0xFF && opIs(last(fl), "protected", 0x8, 2), "boot flag program ignored")
	err = erase(fl, 0x20000, 0x20)
	check(err == nil && opIs(last(fl), "protected", 0x20000, 0x1000), "erase at 0x20000 ignored")
	err = erase(fl, 0x3F000, 0xD8)
	check(err == nil && opIs(last(fl), "protected", 0x30000, 0x10000), "64 KB erase ignored")
	err = program(fl, 0x40000, 0x5A)
	check(err == nil && fl.Mem[0x40000] == 0x5A && opIs(last(fl), "program", 0x40000, 1), "program at 0x40000 written")
	err = erase(fl, 0x68000, 0x20)
	check(err == nil && opIs(last(fl), "erase", 0x68000, 0x1000), "erase at 0x68000 erased")
	fl.Mem[0x3F000] = 0x00
	err = erase(fl, 0x3F000, 0x52)
	check(err == nil && fl.Mem[0x3F000] == 0x00 && opIs(last(fl), "protected", 0x38000, 0x8000), "32 KB erase ignored")
	v, _ = spi(fl, []byte{0x05}, 1)
	check(v[0] == 0x18, "WEL clear after an ignored program")
	err = writeStatus(fl, 0x01, 0x1C)
	check(err == nil && rangeIs(fl, 0, 0x7FFFF), "0x1c: all protected")
	err = program(fl, 0x40000, 0x00)
	check(err == nil && fl.Mem[0x40000] == 0x5A, "program at 0x40000 under 0x1c ignored")
	err = writeStatus(fl, 0x01, 0x00)
	check(err == nil && status(fl) == 0 && none(fl), "0x00: the unlock")
	err = program(fl, 0x3F000, 0x00)
	check(err == nil && fl.Mem[0x3F000] == 0x00 && opIs(last(fl), "program", 0x3F000, 1), "program after the unlock written")
	writeStatus(fl, 0x01, 0x18)
	check(status(fl) == 0x18, "locked again")
	_, err = spi(fl, []byte{0x01, 0x00}, 0)
	check(err == nil && status(fl) == 0x18, "0x01 without write enable ignored")
	writeStatus(fl, 0x01, 0x18|0x02|0x01)
	check(status(fl) == 0x18, "WIP and WEL not stored")
	err = writeStatus(fl, 0x01, 0x18, 0x02)
	check(err == nil && status(fl) == 0x0218 && last(fl).Data == "1802", "two bytes with 0x01")
	err = writeStatus(fl, 0x31, 0x00)
	check(err == nil && status(fl) == 0x0018, "0x31 writes the high byte")

	// GD25LD80C (mid 0x1460c8, 1 MB).
	fl = newFlash(FlashMem, [3]byte{0xC8, 0x60, 0x14})
	check(fl.MID == 0x1460C8, "1 MB GD mid")
	writeStatus(fl, 0x01, 0x18)
	check(rangeIs(fl, 0, 0xBFFFF), "0x18 on 1 MB: the low 768 KB")
	program(fl, 0x40000, 0x00)
	check(fl.Mem[0x40000] == 0xFF && opIs(last(fl), "protected", 0x40000, 1), "program at 0x40000 ignored on 1 MB")
	writeStatus(fl, 0x01, 0x14)
	check(rangeIs(fl, 0, 0xDFFFF), "0x14 on 1 MB: the low 896 KB")

	// Zbit ZB25WD40B (mid 0x13325e).
	fl = newFlash(0x80000, [3]byte{0x5E, 0x32, 0x14})
	writeStatus(fl, 0x01, 0x18)
	check(fl.MID == 0x13325E && rangeIs(fl, 0, 0x3FFFF), "Zbit 512 KB: 0x18 the low 256 KB")

	// Zbit ZB25WD40C: the B part's ID, told by SFDP.
	fl = newFlash(0x80000, [3]byte{0x5E, 0x32, 0x14})
	v, _ = spi(fl, []byte{0x5A, 0, 0, 0, 0}, 4)
	check(string(v) == "\xff\xff\xff\xff" && fl.Part == 0x13325E, "Zbit B: no SFDP")
	fl.SetSFDP([]byte("SFDP"))
	v, _ = spi(fl, []byte{0x5A, 0, 0, 0, 0}, 4)
	check(string(v) == "SFDP" && fl.Part == 0x0113325E, "Zbit C: SFDP signature, part 0x0113325e")
	v, _ = spi(fl, []byte{0x5A, 0, 0, 2, 0}, 3)
	check(string(v) == "DP\xff", "SFDP from address 2")
	err = writeStatus(fl, 0x01, 0x2C, 0x00)
	check(err == nil && status(fl) == 0x002C && rangeIs(fl, 0, 0x3FFFF), "C 0x002c: the low 256 KB")
	err = writeStatus(fl, 0x01, 0x10)
	check(err == nil && rangeIs(fl, 0, 0x7FFFF), "C 0x0010: all")
	err = writeStatus(fl, 0x01, 0x18)
	check(err != nil && strings.Contains(err.Error(), "113325e"), "C 0x0018: not listed")
	err = writeStatus(fl, 0x01, 0x04, 0x40)
	check(err == nil && rangeIs(fl, 0, 0x6FFFF), "C 0x4004: the low 448 KB")
	fl = newFlash(FlashMem, [3]byte{0x5E, 0x32, 0x14})
	fl.SetSFDP([]byte("SFDP"))
	writeStatus(fl, 0x01, 0x1C)
	check(fl.Part == 0x0114325E && rangeIs(fl, 0, 0xFFFFF), "ZB25WD80C 0x001c: all")
	writeStatus(fl, 0x01, 0x10)
	check(rangeIs(fl, 0x80000, 0xFFFFF), "ZB25WD80C 0x0010: the upper 512 KB")

	// Puya P25Q80 (mid 0x146085, the default ID).
	fl = NewFlash(FlashMem)
	check(fl.MID == 0x146085, "default mid")
	err = writeStatus(fl, 0x01, 0x2C)
	check(err == nil && rangeIs(fl, 0, 0x3FFFF), "0x002c: the low 256 KB")
	err = writeStatus(fl, 0x01, 0x0C, 0x40)
	check(err == nil && status(fl) == 0x400C && rangeIs(fl, 0, 0xBFFFF), "0x400c: the low 768 KB")
	err = writeStatus(fl, 0x01, 0x18)
	check(err != nil && strings.Contains(err.Error(), "does not list"), "0x18 on Puya refused")
	err = writeStatus(fl, 0x01, 0x00)
	check(err == nil && status(fl) == 0x4000 && rangeIs(fl, 0, 0xFFFFF), "one byte 0x00 leaves 0x4000: all")
	err = writeStatus(fl, 0x01, 0x00, 0x00)
	check(err == nil && none(fl), "0x0000 two bytes: nothing protected")
	fl = NewFlash(0x80000)
	err = writeStatus(fl, 0x01, 0x04, 0x40)
	check(fl.MID == 0x136085 && err == nil && rangeIs(fl, 0, 0x6FFFF), "P25Q40 0x4004: the low 448 KB")

	// A part without a table.
	fl = newFlash(FlashMem, [3]byte{0xEF, 0x40, 0x14})
	err = writeStatus(fl, 0x01, 0x1C)
	check(err == nil && status(fl) == 0x1C && none(fl), "unknown mid: status kept, nothing protected")
	program(fl, 0x1000, 0x00)
	check(fl.Mem[0x1000] == 0x00, "program there written")
}

func TestFlashOnWrite(t *testing.T) {
	fl := NewFlashJedec(0x80000, [3]byte{0xC8, 0x60, 0x14})
	var seen []FlashOp
	fl.OnWrite = func(op string, addr, n int) { seen = append(seen, FlashOp{op, addr, n, ""}) }
	cmd := func(b ...byte) {
		fl.Select(true)
		for _, x := range b {
			if _, err := fl.Clock(x); err != nil {
				t.Fatal(err)
			}
		}
		if err := fl.Select(false); err != nil {
			t.Fatal(err)
		}
	}
	cmd(0x06)
	cmd(0x02, 0x04, 0x00, 0x00, 0x12, 0x34)
	cmd(0x06)
	cmd(0x20, 0x04, 0x10, 0x00)
	cmd(0x06)
	cmd(0x01, 0x18)
	cmd(0x06)
	cmd(0x02, 0x00, 0x10, 0x00, 0x00)
	cmd(0x06)
	cmd(0xD8, 0x03, 0xF0, 0x00)
	want := []FlashOp{{"program", 0x40000, 2, ""}, {"erase", 0x41000, 0x1000, ""}, {"protected", 0x1000, 1, ""}, {"protected", 0x30000, 0x10000, ""}}
	if len(seen) != len(want) {
		t.Fatalf("OnWrite calls %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("OnWrite call %d: %v, want %v", i, seen[i], want[i])
		}
	}
}

func TestXIPWhileChipSelectLow(t *testing.T) {
	fl := NewFlashJedec(FlashMem, [3]byte{0xC8, 0x60, 0x14})
	fl.Mem[8] = 0x4B
	copy(fl.Mem[0x10000:], []byte{0x11, 0x22, 0x33, 0x44})
	m, err := NewMachine(fl, -1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if v, err := m.Read(0x10000, 4); err != nil || v != 0x44332211 {
		t.Fatalf("XIP read with the chip select high: %v %#x", err, v)
	}
	_ = m.Write(RegBase+0x0D, 1, 0x00)
	if v, err := m.Read(0x10000, 4); err != nil || v != 0x44332211 {
		t.Fatalf("XIP read with the chip select low and nothing sent: %v %#x", err, v)
	}
	_ = m.Write(RegBase+0x0C, 1, 0x02)
	if _, err := m.Read(0x10000, 4); err == nil || !strings.Contains(err.Error(), "chip select low") || !strings.Contains(err.Error(), "0x02") {
		t.Fatalf("XIP read with the chip select low: %v", err)
	}
	_ = m.Write(RegBase+0x0D, 1, 0x01)
	if v, err := m.Read(0x10000, 4); err != nil || v != 0x44332211 {
		t.Fatalf("served again: %v %#x", err, v)
	}
	_ = m.Write(RegBase+0x0D, 1, 0x00)
	_ = m.Write(RegBase+0x0C, 1, 0xAB)
	if v, err := m.Read(0x10000, 4); err != nil || v != 0x44332211 {
		t.Fatalf("XIP read after a bare one-byte command: %v %#x", err, v)
	}
}

func TestSRAMFill(t *testing.T) {
	// The Python sram_fill(1, 8): 2101c54fd1d01ab2.
	want := []byte{0x21, 0x01, 0xc5, 0x4f, 0xd1, 0xd0, 0x1a, 0xb2}
	got := SRAMFill(1, 8)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("SRAMFill(1, 8) = %x, want %x", got, want)
		}
	}
}

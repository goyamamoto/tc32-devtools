// SPDX-License-Identifier: Apache-2.0
package tc32emu

// The optional flash cache model: the Go port of checks/icache_check.py, the
// same cases.

import "testing"

func TestICache(t *testing.T) {
	fl := NewFlash(FlashMem)
	fl.Mem[8] = 0x4B
	copy(fl.Mem[0x0C:], []byte{0x10, 0, 0, 0}) // the boot ROM copies 0x100 bytes: the RAM mirror
	m, err := NewMachine(fl, -1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if m.sramCodeEnd() != 0x100 {
		t.Fatalf("RAM mirror up to %#x", m.sramCodeEnd())
	}
	cost := func(addr uint32, size int) [2]int64 {
		c, n := m.Cycles, m.ICacheMisses
		if _, err := m.Read(addr, size); err != nil {
			t.Fatal(err)
		}
		return [2]int64{m.Cycles - c, m.ICacheMisses - n}
	}
	want := func(got [2]int64, cycles, misses int64, text string) {
		t.Helper()
		if got != [2]int64{cycles, misses} {
			t.Errorf("%s: %v, want %d cycles %d misses", text, got, cycles, misses)
		}
	}
	m.ICacheMiss = 0
	want(cost(0x4000, 2), 0, 0, "model off")
	m.ICacheMiss = 288
	want(cost(0x4000, 2), 288, 1, "first access to a line")
	want(cost(0x4002, 2), 0, 0, "the same line again")
	want(cost(0x401E, 2), 0, 0, "the same line's end")
	want(cost(0x4020, 2), 288, 1, "the next line")
	want(cost(0x4000+64*32, 2), 288, 1, "a line 64 lines further")
	want(cost(0x4000, 2), 288, 1, "the first line again")
	want(cost(0x403E, 4), 288, 1, "a read across two lines, the first held")
	want(cost(0x80, 2), 0, 0, "the RAM mirror")
	if err := m.Write(SRAMBase+0x1000, 4, 1); err != nil {
		t.Fatal(err)
	}
	want(cost(SRAMBase+0x1000, 4), 0, 0, "an SRAM address")
	if err := m.Reset(-1, false); err != nil {
		t.Fatal(err)
	}
	m.ICacheMiss = 288
	want(cost(0x4020, 2), 288, 1, "after a reset")
}

// SPDX-License-Identifier: Apache-2.0
package tc32emu

import "testing"

// The hooks the Python checks get by replacing a machine's sysclk() or
// feed(), or by reading irq_latch and bank directly (board and sleep checks
// of a whole-keyboard runner): SysclkHook, OnFeed, LatchIrq, Banked and
// SramCodeEndHook.
func TestCheckHooks(t *testing.T) {
	fl := NewFlash(FlashSize)
	fl.Mem[8] = 0x4B
	m, err := NewMachine(fl, -1, 1)
	if err != nil {
		t.Fatal(err)
	}
	// Clock select 0x20 is 48 MHz; the hook runs it at 16 MHz instead.
	m.SysclkHook = func() int64 { return 16_000_000 }
	if err := m.RegWrite(0x66, 1, 0x20); err != nil {
		t.Fatal(err)
	}
	if m.CPUHz != 16_000_000 {
		t.Fatalf("CPU clock with the hook: %d", m.CPUHz)
	}
	m.SysclkHook = nil
	if err := m.RegWrite(0x66, 1, 0x20); err != nil {
		t.Fatal(err)
	}
	if m.CPUHz != 48_000_000 {
		t.Fatalf("CPU clock without the hook: %d", m.CPUHz)
	}
	// Both feeds: 0x623 bit 3, and bit 27 of a 32-bit write to 0x620.
	feeds := 0
	m.OnFeed = func() { feeds++ }
	if err := m.RegWrite(0x623, 1, 0x08); err != nil {
		t.Fatal(err)
	}
	if err := m.RegWrite(0x620, 4, 1<<27); err != nil {
		t.Fatal(err)
	}
	if err := m.RegWrite(0x623, 1, 0x01); err != nil {
		t.Fatal(err)
	}
	if feeds != 2 {
		t.Fatalf("feeds seen: %d", feeds)
	}
	m.LatchIrq(IRQSTimer)
	if m.IrqSources()&(1<<IRQSTimer) == 0 {
		t.Fatalf("latched source %d not in the sources %#x", IRQSTimer, m.IrqSources())
	}
	m.bank[ModeSVC] = [2]uint32{0x848000, 0x1234}
	if b := m.Banked(ModeSVC); b != [2]uint32{0x848000, 0x1234} {
		t.Fatalf("banked SVC: %#x", b)
	}
	m.SramCodeEndHook = func() int { return 0x400 }
	if e := m.sramCodeEnd(); e != 0x400 {
		t.Fatalf("SRAM code end with the hook: %#x", e)
	}
}

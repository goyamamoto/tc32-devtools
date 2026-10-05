// SPDX-License-Identifier: Apache-2.0
package tc32emu

import "testing"

// The cases of checks/gpio_check.py: GPIO input reads from the pads, the
// floating hold, the input-enable mask, and the register alias.
func TestGPIOInputsAndAlias(t *testing.T) {
	fl := NewFlash(FlashSize)
	fl.Mem[8] = 0x4B
	m, err := NewMachine(fl, -1, 1)
	if err != nil {
		t.Fatal(err)
	}
	rd := func(o int) uint32 {
		v, err := m.RegRead(o, 1)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	m.Analog[0x0E] = 0x01 | 0x02<<2 | 0x03<<4
	m.Regs[0x586] |= 0x10
	m.Regs[0x582] &^= 0x10
	m.Regs[0x583] &^= 0x10
	m.Regs[0x581] = 0xFF
	if v := rd(0x580); v != 0xED {
		t.Fatalf("port A: %#02x", v)
	}
	m.Regs[0x581] = 0x0F
	if v := rd(0x580); v != 0x0D {
		t.Fatalf("port A through the input enable: %#02x", v)
	}
	m.Regs[0x599] = 0xFF
	var seq []uint32
	for _, pull := range []byte{2, 0, 1, 0} {
		m.Analog[0x14] = pull
		seq = append(seq, rd(0x598)&1)
	}
	if len(seq) != 4 || seq[0] != 0 || seq[1] != 0 || seq[2] != 1 || seq[3] != 1 {
		t.Fatalf("port D pin 0 hold: %v", seq)
	}
	if err := m.Reset(0, false); err != nil {
		t.Fatal(err)
	}
	m.Analog[0x14] = 0
	m.Regs[0x599] = 0xFF
	if rd(0x598)&1 != 1 {
		t.Fatal("after a reset a floating pad must read 1")
	}
	m.Analog[0x12], m.Analog[0x13] = 0xFF, 0xFF
	m.Analog[0xC0] = 0x03
	if v := rd(0x590); v != 0x03 {
		t.Fatalf("port C through analog 0xc0: %#02x", v)
	}
	m.Analog[0x11] = 0x03 << 6
	m.Regs[0x589] = 0x80
	if v := rd(0x588); v != 0x80 {
		t.Fatalf("port B: %#02x", v)
	}
	// A pad released with no read in between (0x5b5 bit 3 clear): PD1 driven
	// low then floating, PD2 pulled down (through the analog port) then
	// floating; both read 0.
	if err := m.Reset(0, false); err != nil {
		t.Fatal(err)
	}
	m.Regs[0x599] = 0xFF
	m.RegWrite(0x59E, 1, uint32(m.Regs[0x59E]|0x06))
	m.RegWrite(0x59B, 1, uint32(m.Regs[0x59B]&^0x02))
	m.RegWrite(0x59A, 1, uint32(m.Regs[0x59A]&^0x02))
	m.RegWrite(0x59A, 1, uint32(m.Regs[0x59A]|0x02))
	for _, val := range []uint32{0x02 << 4, 0x00} {
		m.RegWrite(0xB8, 1, 0x14)
		m.RegWrite(0xB9, 1, val)
		m.RegWrite(0xBA, 1, 0x60)
	}
	if v := rd(0x598); v&0x06 != 0 {
		t.Fatalf("port D after driving/pulling then floating with no read: %#02x", v)
	}
	if err := m.Write(0x1000ABC, 1, 0x5A); err != nil {
		t.Fatal(err)
	}
	a, _ := m.Read(0x800ABC, 1)
	b, _ := m.Read(0x1000ABC, 1)
	if m.Regs[0xABC] != 0x5A || a != 0x5A || b != 0x5A {
		t.Fatal("alias write/read")
	}
	m.Write(0x1000664, 4, 100)
	m.Write(0x1000668, 4, 7)
	m.Write(0x1000660, 1, 0)
	if q, _ := m.Read(0x1000664, 4); q != 14 {
		t.Fatalf("divider through the alias: %d", q)
	}
}

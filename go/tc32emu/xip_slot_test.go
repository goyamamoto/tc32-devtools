// SPDX-License-Identifier: Apache-2.0
package tc32emu

// What an XIP address reads when the chip has booted from slot B: the Go
// port of checks/xip_slot_check.py, the same cases.

import (
	"encoding/binary"
	"strings"
	"testing"
)

func TestXipSlot(t *testing.T) {
	addrs := []int{0x100, 0x20100, 0x30000, 0x40000, 0x77000}
	machine := func(slot int) *Machine {
		fl := NewFlash(FlashMem)
		fl.Mem[slot+8] = 0x4B
		copy(fl.Mem[slot+0x0C:], []byte{0, 0, 0, 0})
		for _, a := range addrs {
			binary.LittleEndian.PutUint32(fl.Mem[a:], uint32(a))
		}
		m, err := NewMachine(fl, -1, 1)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	m := machine(0)
	if m.BootSlot != 0 {
		t.Fatalf("boot slot %#x", m.BootSlot)
	}
	for _, a := range addrs {
		if v, err := m.Read(uint32(a), 4); err != nil || int(v) != a {
			t.Errorf("slot A: %#x reads %#x, %v", a, v, err)
		}
	}
	m = machine(0x20000)
	if m.BootSlot != 0x20000 {
		t.Fatalf("boot slot %#x", m.BootSlot)
	}
	if v, err := m.Read(0x100, 4); err != nil || v != 0x20100 {
		t.Errorf("slot B: 0x100 reads %#x, %v", v, err)
	}
	for _, a := range []uint32{0x20100, 0x30000} {
		if _, err := m.Read(a, 4); err == nil || !strings.Contains(err.Error(), "not documented") {
			t.Errorf("slot B: %#x: %v", a, err)
		}
	}
	for _, a := range []uint32{0x40000, 0x77000} {
		if v, err := m.Read(a, 4); err != nil || v != a {
			t.Errorf("slot B: %#x reads %#x, %v", a, v, err)
		}
	}
	if err := m.Write(RegBase+0x608, 4, (1<<24)|0x100); err != nil {
		t.Fatal(err)
	}
	if v := binary.LittleEndian.Uint32(m.SRAM[0x100:]); v != 0x20100 {
		t.Errorf("slot B: the loader loaded %#x", v)
	}
}

// SPDX-License-Identifier: Apache-2.0
package tc32emu

// The analog registers a reset keeps: the Go port of checks/power_on_check.py.

import "testing"

func TestPowerOnAnalog(t *testing.T) {
	fl := NewFlash(FlashMem)
	fl.Mem[8] = 0x4B
	m, err := NewMachine(fl, -1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if m.Analog[0x3A] != 0 || m.Analog[0x3B] != 0 || m.Analog[0x3C] != 0x0F {
		t.Errorf("power-on: %x", m.Analog[0x3A:0x3D])
	}
	defaults := string([]byte{0x20, 0, 0, 0, 0xFF})
	if string(m.Analog[0x35:0x3A]) != defaults {
		t.Errorf("power-on: 0x35-0x39 %x", m.Analog[0x35:0x3A])
	}
	copy(m.Analog[0x3A:0x3D], []byte{0x12, 0x34, 0x56})
	copy(m.Analog[0x35:0x3A], []byte{0x71, 0x72, 0x73, 0x74, 0x75})
	if err := m.Reset(-1, false); err != nil {
		t.Fatal(err)
	}
	if m.Analog[0x3A] != 0x12 || m.Analog[0x3B] != 0x34 || m.Analog[0x3C] != 0x56 {
		t.Errorf("after a reset: %x", m.Analog[0x3A:0x3D])
	}
	if string(m.Analog[0x35:0x3A]) != defaults {
		t.Errorf("after a reset: 0x35-0x39 %x", m.Analog[0x35:0x3A])
	}
	m, err = NewMachine(fl, -1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if m.Analog[0x3C] != 0x0F || m.Analog[0x3A] != 0 {
		t.Errorf("a new power-on: %x", m.Analog[0x3A:0x3D])
	}
}

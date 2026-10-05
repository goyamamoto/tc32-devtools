// SPDX-License-Identifier: Apache-2.0
package tc32emu

// The USB core wake source as a level or as an edge: the Go port of
// checks/usb_core_wake_check.py, the same cases.

import "testing"

func TestUsbCoreWake(t *testing.T) {
	fl := NewFlash(FlashMem)
	fl.Mem[8] = 0x4B
	m, err := NewMachine(fl, -1, 1)
	if err != nil {
		t.Fatal(err)
	}
	u := NewUsbModel(m, false)
	if u.CoreWake != "level" && u.CoreWake != "edge" {
		t.Fatalf("CoreWake %q", u.CoreWake)
	}
	m.Analog[0x26] |= 0x10
	k := func(on bool) { u.kState, u.kEdge = on, on }
	for _, c := range []struct {
		mode string
		want [3]byte
	}{{"level", [3]byte{4, 4, 4}}, {"edge", [3]byte{4, 0, 0}}} {
		u.CoreWake = c.mode
		m.Regs[0x6E] |= 0x04
		k(true)
		var got [3]byte
		for i := range got {
			got[i] = m.wakeStatus() & 0x04
		}
		if got != c.want {
			t.Errorf("%s: three sleeps during one K period wake %v, want %v", c.mode, got, c.want)
		}
		k(false)
		if m.wakeStatus()&0x04 != 0 {
			t.Errorf("%s: a wake after K has ended", c.mode)
		}
		k(true)
		if m.wakeStatus()&0x04 != 4 {
			t.Errorf("%s: the next K period does not wake", c.mode)
		}
		k(false)
		m.Regs[0x6E] &^= 0x04
		k(true)
		if m.wakeStatus()&0x04 != 0 {
			t.Errorf("%s: a wake with 0x6e bit 2 clear", c.mode)
		}
		k(false)
	}
}

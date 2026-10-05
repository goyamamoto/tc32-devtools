// SPDX-License-Identifier: Apache-2.0
package tc32emu

// The 32 kHz timer wake from suspend: the Go port of
// checks/timer_wake_check.py, the same cases.

import "testing"

func TestTimerWake(t *testing.T) {
	fl := NewFlash(FlashMem)
	fl.Mem[8] = 0x4B
	m, err := NewMachine(fl, -1, 1)
	if err != nil {
		t.Fatal(err)
	}
	tickMs := 1 / 32.768
	for _, ticks := range []uint32{1, 7, 33, 100, 491} {
		m.Cycles += 1234
		wake := m.k32Now() + ticks
		if err := m.RegWrite(0x74C, 4, wake); err != nil {
			t.Fatal(err)
		}
		if err := m.RegWrite(0x74B, 1, 0x08); err != nil {
			t.Fatal(err)
		}
		m.Analog[0x26] = 0x20
		start := m.Ms()
		if err := m.RegWrite(0x6F, 1, 0x81); err != nil {
			t.Fatal(err)
		}
		end := m.Cycles + int64(50*float64(m.CPUHz)/1000)
		for m.Asleep != nil && m.Cycles < end {
			m.SleepUntil(end)
		}
		if m.Asleep != nil {
			t.Fatalf("%d ticks: no wake within 50 ms", ticks)
		}
		slept := m.Ms() - start
		if (m.k32Now()-wake)&m32 >= 0x80000000 || m.Analog[0x44]&0x02 == 0 {
			t.Errorf("%d ticks: awake before the wake value", ticks)
		}
		if slept > float64(ticks)*tickMs+1e-6 {
			t.Errorf("%d ticks: slept %.4f ms, more than %d ticks", ticks, slept, ticks)
		}
		m.Analog[0x44] = 0
	}
}

// SPDX-License-Identifier: Apache-2.0
package tc32emu

// Timer0/1's compare on equality: the Go port of checks/timer_capture_check.py,
// the same cases.

import (
	"strings"
	"testing"
)

func TestTimerCapture(t *testing.T) {
	const slot = 24000 // 500 us at 48 MHz
	check := func(cond bool, text string) {
		t.Helper()
		if !cond {
			t.Errorf("FAIL %s", text)
		}
	}
	machine := func() *Machine {
		fl := NewFlash(FlashMem)
		fl.Mem[8] = 0x4B
		m, err := NewMachine(fl, -1, 1)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	write := func(m *Machine, o, size int, val uint32) string {
		if err := m.RegWrite(o, size, val); err != nil {
			return err.Error()
		}
		return ""
	}
	startT1 := func(m *Machine, capture, tick uint32) string {
		write(m, 0x620, 1, uint32(m.Regs[0x620])&^0x38)
		write(m, 0x634, 4, tick)
		write(m, 0x628, 4, capture)
		return write(m, 0x620, 1, uint32(m.Regs[0x620])&^0x30|0x08)
	}
	startT0 := func(m *Machine, capture, tick uint32) string {
		write(m, 0x620, 1, uint32(m.Regs[0x620])&^0x07)
		write(m, 0x630, 4, tick)
		write(m, 0x624, 4, capture)
		return write(m, 0x620, 1, uint32(m.Regs[0x620])&^0x06|0x01)
	}
	run := func(m *Machine, cycles int64) {
		m.Cycles += cycles
		if err := m.UpdateTime(); err != nil {
			t.Fatal(err)
		}
	}
	matchedN := func(m *Machine, n uint) bool {
		hit := m.Regs[0x623]&(1<<n) != 0
		write(m, 0x623, 1, 1<<n)
		return hit
	}
	matched := func(m *Machine) bool { return matchedN(m, 1) }
	read := func(m *Machine, o, size int) uint32 {
		v, err := m.RegRead(o, size)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	saved := TimerCaptureBelow
	defer func() { TimerCaptureBelow = saved }()
	TimerCaptureBelow = "stop"

	m := machine()
	check(startT1(m, slot, 0) == "", "Timer1 started with capture 24000")
	run(m, slot-1)
	check(!matched(m), "no match one cycle before the capture")
	run(m, 1)
	check(matched(m) && m.tmrTick(1) == 0, "a match at the capture, and the count starts again from 0")
	run(m, 3*slot)
	check(matched(m) && m.tmrTick(1) == 0, "periodic: matches go on, the count at 0 after three periods")

	m = machine()
	startT1(m, slot, 0)
	run(m, 10000)
	check(write(m, 0x628, 4, 30000) == "", "a capture raised above the running count (10000 -> 30000) is accepted")
	run(m, 19999)
	check(!matched(m), "no match before the count reaches the raised capture")
	run(m, 1)
	check(matched(m), "a match at the raised capture")

	m = machine()
	startT1(m, slot, 0)
	run(m, 20000)
	err := write(m, 0x628, 4, 10000)
	check(err != "" && strings.Contains(err, "wrapped"), "a capture written below the running count stops the run: "+err)

	m = machine()
	startT1(m, slot, 0)
	run(m, 10000)
	err = write(m, 0x634, 4, 30000)
	check(err != "" && strings.Contains(err, "count write"), "a count written past the capture while running stops the run")

	m = machine()
	err = startT1(m, slot, 30000)
	check(err != "" && strings.Contains(err, "a start"), "a start with the count past the capture stops the run")

	m = machine()
	write(m, 0x620, 1, 0)
	check(write(m, 0x628, 4, 5) == "", "a capture write while the timer is stopped is not checked")

	// The fault seen on hardware: a period stretched through the capture and set back
	// after a flash write held interrupts off for more than a period.
	m = machine()
	startT1(m, slot, 0)
	run(m, 10000)
	check(write(m, 0x628, 4, slot+10000+960) == "", "stretched: capture 34960 while the count is 10000")
	run(m, slot+960)
	check(matched(m) && m.tmrTick(1) == 0, "the stretched period matches at 34960")
	run(m, 30000)
	check(write(m, 0x628, 4, slot) != "", "set back to 24000 when the count is 30000: stops the run")

	// A match between two updates (Run brings the timers up to date every 32
	// instructions): the count passes the capture, and the capture is
	// written, or the timer stopped and started, before the next update.
	m = machine()
	startT1(m, slot, 0)
	run(m, slot-5)
	m.Cycles += 10
	check(write(m, 0x628, 4, slot) == "" && matched(m) && m.tmrTick(1) == 5,
		"a capture write 5 cycles after a match no update has seen: the match first, the count 5, no stop")
	m = machine()
	startT1(m, slot, 0)
	run(m, slot-5)
	m.Cycles += 10
	write(m, 0x620, 1, uint32(m.Regs[0x620])&^0x38)
	check(matched(m) && m.tmrTick(1) == 5, "a stop 5 cycles after a match no update has seen: the match set, the count kept 5")
	check(write(m, 0x620, 1, uint32(m.Regs[0x620])&^0x30|0x08) == "", "and a start from that count does not stop the run")
	m = machine()
	startT1(m, slot, 0)
	run(m, slot-5)
	m.Cycles += 10
	check(write(m, 0x634, 4, 1000) == "" && matched(m) && m.tmrTick(1) == 1000,
		"a count write 5 cycles after a match no update has seen: the match is taken, the count 1000")
	m = machine()
	startT1(m, slot, 0)
	run(m, slot-5)
	m.Cycles += 10
	check(write(m, 0x628, 4, 30000) == "" && matched(m) && m.tmrTick(1) == 5,
		"a capture raised to 30000 5 cycles after a match no update has seen: that match is reported, the count 5")
	m = machine()
	startT1(m, slot, 0)
	run(m, slot-5)
	m.Cycles += 10
	write(m, 0x620, 4, uint32(m.Regs[0x620])|uint32(m.Regs[0x621])<<8|uint32(m.Regs[0x622])<<16|0x02<<24)
	run(m, 0)
	check(!matched(m) && m.tmrTick(1) == 5,
		"a 32-bit 0x620 write clearing Timer1's status 5 cycles after a match no update has seen clears that match")

	// Reads catch up as writes do.
	m = machine()
	startT1(m, slot, 0)
	run(m, slot-5)
	m.Cycles += 10
	check(read(m, 0x634, 4) == 5, "a count read 5 cycles after a match no update has seen returns 5, not the capture + 5")
	m = machine()
	startT1(m, slot, 0)
	run(m, slot-5)
	m.Cycles += 10
	check(read(m, 0x623, 1)&0x02 != 0, "a status read 5 cycles after a match no update has seen shows the match")

	// Timer0 as Timer1.
	m = machine()
	check(startT0(m, slot, 0) == "", "Timer0 started with capture 24000")
	run(m, slot-1)
	check(!matchedN(m, 0), "Timer0: no match one cycle before the capture")
	run(m, 1)
	check(matchedN(m, 0) && m.tmrTick(0) == 0 && !matched(m), "Timer0: a match at the capture, its own status bit")
	run(m, 20000)
	err = write(m, 0x624, 4, 10000)
	check(err != "" && strings.Contains(err, "timer 0"), "Timer0: a capture written below the running count stops the run: "+err)

	// The count is 32 bits: a timer that ran past 2^32 cycles with capture 0.
	m = machine()
	startT1(m, 0, 0)
	run(m, (1<<32)+5)
	check(write(m, 0x628, 4, 100) == "" && m.tmrTick(1) == 5,
		"capture 0, 2^32 + 5 cycles on: a capture of 100 is taken with the count at 5")
	run(m, 94)
	check(!matched(m), "no match before the count reaches 100")
	run(m, 1)
	check(matched(m) && m.tmrTick(1) == 0, "the match at 100")

	TimerCaptureBelow = "wrap"
	m = machine()
	var events []string
	m.Event = func(text string) { events = append(events, text) }
	startT1(m, slot, 0)
	run(m, 20000)
	ok := write(m, 0x628, 4, 10000) == ""
	logged := false
	for _, e := range events {
		if strings.Contains(e, "wrapped") {
			logged = true
		}
	}
	check(ok && logged, "wrap: the write is taken and logged as an event")
	run(m, (1<<32)-20000+10000-1)
	check(!matched(m), "wrap: no match until the count has wrapped round (2^32 - 10001 cycles on)")
	run(m, 1)
	check(matched(m) && m.tmrTick(1) == 0, "wrap: the match when the count reaches the capture after the wrap")
	run(m, 10000)
	check(matched(m), "wrap: periodic again after it")
	m = machine()
	startT1(m, slot, 0)
	run(m, 20000)
	write(m, 0x628, 4, 10000)
	run(m, (1<<32)-20000+5)
	check(write(m, 0x628, 4, 100) == "" && m.tmrTick(1) == 5,
		"wrap: a capture of 100 written when the wrapped count is 5 is taken")
	run(m, 94)
	check(!matched(m), "wrap: no match before the count reaches 100")
	run(m, 1)
	check(matched(m) && m.tmrTick(1) == 0, "wrap: the match at 100")
}

// SPDX-License-Identifier: Apache-2.0
package tc32emu

// The optional pad capacitance model: the Go port of checks/pad_rc_check.py,
// the same cases.

import "testing"

func TestPadRC(t *testing.T) {
	const port, bit = 2, 2 // PC2, a matrix row
	outside := -1          // the level something outside drives PC2 to, or -1
	machine := func(pull byte, cpf, rpct int64) *Machine {
		fl := NewFlash(FlashMem)
		fl.Mem[8] = 0x4B
		copy(fl.Mem[0x0C:], []byte{0, 0, 0, 0})
		m, err := NewMachine(fl, -1, 1)
		if err != nil {
			t.Fatal(err)
		}
		m.PadCpF, m.PadRPct = cpf, rpct
		m.Analog[0x12] = pull << 4
		m.Analog[0xC0] = 0xFF
		m.Regs[0x590+6] = 0xFF
		m.Regs[0x590+2] = 0xFF
		outside = -1
		m.PadLevelsHook = func(p int, lvl, flt byte) (byte, byte) {
			if p == port && outside >= 0 {
				lvl = lvl&^(1<<bit) | byte(outside)<<bit
				flt &^= 1 << bit
			}
			return lvl, flt
		}
		return m
	}
	read := func(m *Machine) int {
		v, err := m.Read(RegBase+0x590, 1)
		if err != nil {
			t.Fatal(err)
		}
		return int(v>>bit) & 1
	}
	release := func(m *Machine, level int, cycles int64) int {
		outside = level
		m.holdPads(port)
		outside = -1
		m.holdPads(port)
		m.Cycles += cycles
		return read(m)
	}
	check := func(cond bool, text string) {
		t.Helper()
		if !cond {
			t.Errorf("FAIL %s", text)
		}
	}
	hz := machine(2, 0, 100).CPUHz
	cyclesFor := func(ns int64) int64 {
		for c := int64(0); ; c++ {
			if c*1_000_000_000/hz >= ns {
				return c
			}
		}
	}
	n := cyclesFor(1204 * 100_000 * 40 / 1_000_000)

	check(release(machine(2, 0, 100), 1, 0) == 0, "model off: low at once")
	check(release(machine(2, 40, 100), 1, 0) == 1, "still high right after the release")
	check(release(machine(2, 40, 100), 1, n-1) == 1, "still high one cycle before the time")
	m := machine(2, 40, 100)
	check(release(m, 1, n) == 0 && read(m) == 0, "low at the time, and it stays low")
	m = machine(2, 40, 100)
	release(m, 1, 10)
	m.Cycles += n - 10
	check(read(m) == 0, "the time runs from the release")
	n2 := cyclesFor(1204 * 100_000 * 60 * 150 / 100 / 1_000_000)
	m = machine(2, 60, 150)
	check(release(m, 1, n2-1) == 1, "60 pF at 150 %: high before the time")
	m.Cycles++
	check(read(m) == 0, "60 pF at 150 %: low at the time")
	m = machine(2, 40, 100)
	release(m, 1, 5)
	outside = 1
	check(read(m) == 1, "driven high again: high")
	outside = -1
	m.holdPads(port)
	m.Cycles += n - 1
	check(read(m) == 1, "the time starts again")
	m.Cycles++
	check(read(m) == 0, "low once it has passed")
	n3 := cyclesFor(1204 * 10_000 * 40 / 1_000_000)
	check(release(machine(3, 40, 100), 0, n3-1) == 0, "10 kOhm up: still low before the time")
	check(release(machine(3, 40, 100), 0, n3) == 1, "10 kOhm up: high at the time")
	m = machine(2, 40, 100)
	release(m, 1, n)
	check(release(m, 0, 0) == 0, "at the pull's own level already")
	m = machine(2, 40, 100)
	outside = 1
	m.holdPads(port)
	outside = 0
	m.holdPads(port)
	check(read(m) == 1, "driven from outside to the pull's own level: taken as released")
	m = machine(2, 40, 100)
	outside = 1
	m.holdPads(port)
	outside = -1
	if err := m.Write(RegBase+0x590+3, 1, 0x00); err != nil {
		t.Fatal(err)
	}
	if err := m.Write(RegBase+0x590+2, 1, 0xFF&^(1<<bit)); err != nil {
		t.Fatal(err)
	}
	check(read(m) == 0, "the pad's own output low: low at once")
	if err := m.Write(RegBase+0x590+2, 1, 0xFF); err != nil {
		t.Fatal(err)
	}
	check(read(m) == 0, "released from its own output: already at the pull's level")

	// The GPIO interrupt comes when the pad's read level changes, not when the contact does.
	m = machine(3, 40, 100)
	m.Regs[0x5B5] |= 0x08
	m.Regs[0x590+7] = 1 << bit
	m.Regs[0x590+4] = 0
	outside = 0
	m.holdPads(port)
	m.GpioIrqUpdate()
	m.irqLatch &^= 1 << 18
	outside = -1
	m.GpioIrqUpdate()
	check(read(m) == 0 && m.irqLatch&(1<<18) == 0, "contact open, pad still low: no interrupt yet")
	m.Cycles += n3 - 1
	m.padRCService()
	check(m.irqLatch&(1<<18) == 0, "one cycle before the time: none yet")
	due, ok := m.padRCDue()
	check(ok && due >= 0 && due <= 1, "the pad is due")
	m.Cycles++
	m.padRCService()
	check(read(m) == 1 && m.irqLatch&(1<<18) != 0, "at the time the pad reads high and the interrupt is latched")
	_, ok = m.padRCDue()
	check(!ok, "no pad on its way any more")
}

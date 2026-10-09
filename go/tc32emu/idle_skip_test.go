// SPDX-License-Identifier: Apache-2.0
package tc32emu

// Run's idle skip only while an interrupt could be taken: the Go port of
// checks/idle_skip_check.py, the same cases.

import (
	"encoding/binary"
	"testing"

	"github.com/goyamamoto/tc32-devtools/go/tc32isa"
)

func TestIdleSkip(t *testing.T) {
	const gpioSrc = 18 // a latched source, unmasked through 0x642 bit 2
	idle := []Range{{Lo: 0x100, Hi: 0x120}}
	machine := func(code []uint16, words ...uint32) *Machine {
		t.Helper()
		fl := NewFlash(FlashMem)
		fl.Mem[8] = 0x4B
		a := 0x100
		for _, hw := range code {
			binary.LittleEndian.PutUint16(fl.Mem[a:], tc32isa.ToTC32(hw))
			a += 2
		}
		for _, w := range words {
			binary.LittleEndian.PutUint32(fl.Mem[a:], w)
			a += 4
		}
		binary.LittleEndian.PutUint16(fl.Mem[0x10:], tc32isa.ToTC32(0xE7FE))
		m, err := NewMachine(fl, -1, 1)
		if err != nil {
			t.Fatal(err)
		}
		m.R[15] = 0x100
		if err := m.setCPSR(ModeSVC); err != nil { // I bit clear
			t.Fatal(err)
		}
		for _, w := range [][3]uint32{{0x74A, 1, stimerEn}, {0x748, 1, stimerIRQ}} {
			if err := m.RegWrite(int(w[0]), int(w[1]), w[2]); err != nil {
				t.Fatal(err)
			}
		}
		return m
	}
	regWrite := func(m *Machine, o, size int, val uint32) {
		t.Helper()
		if err := m.RegWrite(o, size, val); err != nil {
			t.Fatal(err)
		}
	}
	compareIn := func(m *Machine, seconds float64) {
		regWrite(m, 0x744, 4, m.StimerNow()+uint32(seconds*STimerHz))
	}
	runToIRQ := func(m *Machine, limit int64) (int64, bool) {
		if m.Hooks == nil {
			m.Hooks = map[uint32]func(*Machine) error{}
		}
		m.Hooks[0x10] = func(*Machine) error { return &StopError{Reason: "irq"} }
		if err := m.Run(limit, idle, 32); err != nil {
			if _, ok := err.(*StopError); ok {
				return m.Cycles, true
			}
			t.Fatal(err)
		}
		return 0, false
	}

	// 0x100 movs r0, #200; subs r0, #1; bne .-2; ldr r1, =0x800643; movs r2, #1;
	// strb r2, [r1]; b .; (pad); .word 0x800643
	m := machine([]uint16{0x20C8, 0x3801, 0xD1FD, 0x4902, 0x2201, 0x700A, 0xE7FE, 0x46C0}, 0x00800643)
	regWrite(m, 0x643, 1, 0)
	regWrite(m, 0x642, 1, 1<<(gpioSrc-16))
	m.LatchIrq(gpioSrc)
	compareIn(m, 1.0)
	if at, ok := runToIRQ(m, 3*CPUHz); !ok || at >= 20_000 {
		t.Errorf("FAIL 0x800643 = 0, an interrupt pending: taken at cycle %d (ok %v), want right after the code sets 0x800643", at, ok)
	}

	// 0x100 movs r0, #200; subs r0, #1; bne .-2; tmcsr r3; b .
	m = machine([]uint16{0x20C8, 0x3801, 0xD1FD, 0xBBC3, 0xE7FE})
	if err := m.setCPSR(ModeSVC | 0x80); err != nil { // I bit set
		t.Fatal(err)
	}
	m.R[3] = ModeSVC // what tmcsr writes: I clear
	regWrite(m, 0x643, 1, 1)
	regWrite(m, 0x642, 1, 1<<(gpioSrc-16))
	m.LatchIrq(gpioSrc)
	compareIn(m, 1.0)
	if at, ok := runToIRQ(m, 3*CPUHz); !ok || at >= 20_000 {
		t.Errorf("FAIL CPSR I set, an interrupt pending: taken at cycle %d (ok %v), want right after the code clears I", at, ok)
	}

	// 0x100 adds r0, #1; b .-2
	m = machine([]uint16{0x3001, 0xE7FD})
	regWrite(m, 0x643, 1, 1)
	regWrite(m, 0x642, 1, 1<<(IRQSTimer-16))
	compareIn(m, 0.010)
	at, ok := runToIRQ(m, CPUHz)
	if !ok || at < CPUHz*9/1000 || m.R[0] >= 1000 {
		t.Errorf("FAIL interrupts on, none pending: taken at cycle %d (ok %v) after %d loop iterations, want a skip to the compare 10 ms on", at, ok, m.R[0])
	}
	if !ok || m.IdleSkipped*10 < at*9 || m.IdleSkipped > at {
		t.Errorf("FAIL the cycles skipped there are counted as the idle loop's: IdleSkipped %d of %d", m.IdleSkipped, at)
	}

	// Timer1 (mode 0, the system clock) with a 500 us capture, its interrupt
	// unmasked through 0x640 bit 1; the compare 10 ms away.
	cap := int64(CPUHz / 2000)
	timer1 := func(m *Machine, irqMask uint32) {
		regWrite(m, 0x643, 1, 1)
		regWrite(m, 0x640, 1, irqMask)
		regWrite(m, 0x642, 1, 1<<(IRQSTimer-16))
		regWrite(m, 0x628, 4, uint32(cap))
		regWrite(m, 0x634, 4, 0)
		regWrite(m, 0x620, 1, 0x08) // Timer1 enabled, mode 0
	}
	m = machine([]uint16{0x3001, 0xE7FD})
	timer1(m, 1<<1)
	start := m.Cycles
	compareIn(m, 0.010)
	at, ok = runToIRQ(m, CPUHz)
	if !ok || at-start < cap || at-start >= cap+100 || m.R[0] >= 1000 {
		t.Errorf("FAIL Timer1 running, its interrupt unmasked: taken at cycle %d (ok %v) after %d loop iterations, want the match %d cycles on, not the compare", at-start, ok, m.R[0], cap)
	}

	// The same, with a handler that clears the status and goes back to the
	// loop: the interrupts keep the capture's spacing.
	m = machine([]uint16{0x3001, 0xE7FD})
	timer1(m, 1<<1)
	var taken []int64
	m.Hooks = map[uint32]func(*Machine) error{0x10: func(mm *Machine) error {
		taken = append(taken, mm.Cycles)
		if err := mm.RegWrite(0x623, 1, 0x02); err != nil { // the status cleared
			return err
		}
		if err := mm.setCPSR(ModeSVC); err != nil { // back to the loop, I clear
			return err
		}
		mm.R[15] = 0x100
		return nil
	}}
	compareIn(m, 0.020)
	if err := m.Run(m.Cycles+10*cap+cap/2, idle, 32); err != nil {
		t.Fatal(err)
	}
	spaced := len(taken) == 10
	for i := 1; i < len(taken); i++ {
		if g := taken[i] - taken[i-1]; g < cap-100 || g > cap+100 {
			spaced = false
		}
	}
	if !spaced {
		t.Errorf("FAIL Timer1's interrupts through the idle loop: %d in 10.5 periods at cycles %v, want 10 spaced by the capture %d", len(taken), taken, cap)
	}

	// The same timer with its interrupt masked: the loop skips to the compare.
	m = machine([]uint16{0x3001, 0xE7FD})
	timer1(m, 0)
	start = m.Cycles
	compareIn(m, 0.010)
	at, ok = runToIRQ(m, CPUHz)
	if !ok || at-start < CPUHz*9/1000 || m.R[0] >= 1000 || m.Regs[0x623]&0x02 == 0 {
		t.Errorf("FAIL Timer1 running, its interrupt masked: taken at cycle %d (ok %v) after %d loop iterations, status 0x%02x; want the compare 10 ms on with the timer's status set", at-start, ok, m.R[0], m.Regs[0x623])
	}
}

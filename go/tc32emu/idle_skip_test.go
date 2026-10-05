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
}

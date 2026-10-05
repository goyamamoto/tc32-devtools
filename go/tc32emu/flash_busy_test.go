// SPDX-License-Identifier: Apache-2.0
package tc32emu

// The flash's busy time: the Go port of checks/flash_busy_check.py, the same
// cases.

import (
	"fmt"
	"strings"
	"testing"
)

func TestFlashBusy(t *testing.T) {
	check := func(cond bool, text string) {
		t.Helper()
		if !cond {
			t.Errorf("FAIL %s", text)
		}
	}
	// spi clocks the bytes in, then `read` more; the bytes read and the first
	// error (the chip select is left low when a byte is refused, as in the
	// Python).
	spi := func(fl *Flash, data []byte, read int) ([]byte, error) {
		var got []byte
		if err := fl.Select(true); err != nil {
			return got, err
		}
		for _, b := range data {
			if _, err := fl.Clock(b); err != nil {
				return got, err
			}
		}
		for i := 0; i < read; i++ {
			v, err := fl.Clock(0)
			if err != nil {
				return got, err
			}
			got = append(got, v)
		}
		return got, fl.Select(false)
	}
	must := func(fl *Flash, data []byte, read int) []byte {
		t.Helper()
		got, err := spi(fl, data, read)
		if err != nil {
			t.Errorf("FAIL unexpected: %v", err)
			for len(got) < read {
				got = append(got, 0xFF)
			}
		}
		return got
	}
	errOf := func(err error) string {
		if err != nil {
			return err.Error()
		}
		return ""
	}
	wip := func(fl *Flash) bool { return must(fl, []byte{0x05}, 1)[0]&0x01 != 0 }
	program := func(fl *Flash, addr int, data ...byte) {
		must(fl, []byte{0x06}, 0)
		must(fl, append([]byte{0x02, byte(addr >> 16), byte(addr >> 8), byte(addr)}, data...), 0)
	}
	erase := func(fl *Flash, addr int, cmd byte) {
		must(fl, []byte{0x06}, 0)
		must(fl, []byte{cmd, byte(addr >> 16), byte(addr >> 8), byte(addr)}, 0)
	}
	savedP, savedS, saved32, saved64 := FlashTppUs, FlashTseUs, FlashTbe32Us, FlashTbe64Us
	defer func() { FlashTppUs, FlashTseUs, FlashTbe32Us, FlashTbe64Us = savedP, savedS, saved32, saved64 }()
	FlashTppUs, FlashTseUs, FlashTbe32Us, FlashTbe64Us = 0, 0, 0, 0

	fl := NewFlashJedec(0x80000, [3]byte{0xC8, 0x60, 0x14})
	fl.Mem[8] = 0x4B
	m, err := NewMachine(fl, -1, 1)
	if err != nil {
		t.Fatal(err)
	}
	advanceUs := func(us int64) { m.Cycles += us * m.CPUHz / 1_000_000 }
	xipErr := func(addr uint32) string {
		_, err := m.Read(addr, 4)
		return errOf(err)
	}

	program(fl, 0x40000, 0x12)
	check(fl.Mem[0x40000] == 0x12 && !wip(fl), "no busy times: a program is done at once, WIP clear")

	FlashTppUs, FlashTseUs, FlashTbe32Us, FlashTbe64Us = 1600, 150000, 500000, 800000
	program(fl, 0x40001, 0x34)
	check(wip(fl), "TPP 1600 us: WIP set right after a page program")
	advanceUs(1590)
	check(wip(fl), "WIP still set 1590 us on")
	advanceUs(20)
	check(!wip(fl), "WIP clear 1610 us on")

	erase(fl, 0x41000, 0x20)
	advanceUs(149000)
	check(wip(fl), "TSE 150 ms: WIP set 149 ms after a sector erase")
	advanceUs(2000)
	check(!wip(fl), "and clear at 151 ms")

	for _, c := range []struct {
		cmd  byte
		name string
		ms   int64
	}{{0xD8, "64 KB", 800}, {0x52, "32 KB", 500}} {
		erase(fl, 0x50000, c.cmd)
		advanceUs((c.ms - 1) * 1000)
		busyThen := wip(fl)
		advanceUs(2000)
		check(busyThen && !wip(fl), fmt.Sprintf("TBE %d ms: a %s block erase busy %d ms on, clear at %d ms", c.ms, c.name, c.ms-1, c.ms+1))
	}

	must(fl, []byte{0x06}, 0)
	must(fl, []byte{0x01, 0x1C}, 0) // the whole 512 KB protected (the SDK's table for 0x1360c8)
	program(fl, 0x60000, 0x00)
	lastOp := fl.Log[len(fl.Log)-1]
	check(lastOp.Op == "protected" && lastOp.Addr == 0x60000 && !wip(fl), "a program the protection ignores leaves WIP clear")
	must(fl, []byte{0x06}, 0)
	must(fl, []byte{0x01, 0x00}, 0)

	program(fl, 0x40002, 0x56)
	e := xipErr(0x10000)
	check(e != "" && strings.Contains(e, "busy"), "an XIP read while the part is busy stops the run: "+e)
	fl.Abort()
	check(wip(fl), "a chip reset (abort) leaves the part busy")
	advanceUs(2000)
	check(xipErr(0x10000) == "" && !wip(fl), "the XIP read goes through once the part is done")

	// Reset starts the clock again at 0; the part goes on.
	program(fl, 0x40003, 0x78)
	advanceUs(1000)
	if err := m.Reset(-1, false); err != nil {
		t.Fatal(err)
	}
	check(m.Ms() == 0 && wip(fl), "a reset 1000 us into a page program: the clock at 0, WIP still set")
	advanceUs(590)
	check(wip(fl), "WIP still set 590 us after the reset")
	advanceUs(20)
	check(!wip(fl), "WIP clear 610 us after the reset (1610 us after the program)")

	// A clock change: the time left is converted, not the cycles kept.
	advanceUs(2000)
	program(fl, 0x40005, 0xBC)
	advanceUs(800)
	if err := m.RegWrite(0x66, 1, 0x20); err != nil { // 24 -> 48 MHz
		t.Fatal(err)
	}
	check(m.CPUHz == 48_000_000, "the CPU clock at 48 MHz, 800 us into a page program")
	advanceUs(790)
	check(wip(fl), "WIP still set 790 us after the clock change (1590 us after the program)")
	advanceUs(20)
	check(!wip(fl), "WIP clear 810 us after the clock change (1610 us after the program)")

	// A reset at 48 MHz: the clock starts again at 24 MHz; the time left is kept.
	program(fl, 0x40006, 0xDE)
	advanceUs(1000)
	if err := m.Reset(-1, false); err != nil {
		t.Fatal(err)
	}
	check(m.CPUHz == 24_000_000 && wip(fl), "a reset at 48 MHz 1000 us into a page program: 24 MHz, WIP still set")
	advanceUs(590)
	check(wip(fl), "WIP still set 590 us after the reset")
	advanceUs(20)
	check(!wip(fl), "WIP clear 610 us after the reset")

	// Sleep: the part goes on while the chip is suspended.
	program(fl, 0x40007, 0xF0)
	if err := m.enterSuspend(0x81); err != nil { // no wake source enabled: it sleeps until Run's limit
		t.Fatal(err)
	}
	if err := m.Run(m.Cycles+1590*m.CPUHz/1_000_000, nil, 32); err != nil {
		t.Fatal(err)
	}
	check(m.Asleep != nil && wip(fl), "WIP still set after 1590 us of sleep")
	if err := m.Run(m.Cycles+20*m.CPUHz/1_000_000, nil, 32); err != nil {
		t.Fatal(err)
	}
	m.Asleep = nil
	check(!wip(fl), "WIP clear after 1610 us of sleep")

	// While busy the part takes status reads only.
	program(fl, 0x40008, 0x11)
	_, err = spi(fl, []byte{0x35}, 1)
	check(wip(fl) && err == nil, "while busy: 0x05 and 0x35 are taken")
	_, err = spi(fl, []byte{0x02, 0x04, 0x00, 0x09, 0x22}, 0)
	e = errOf(err)
	fl.Select(false)
	check(strings.Contains(e, "command 0x02") && strings.Contains(e, "us left"), "while busy: a page program stops the run: "+e)
	if err := m.RegWrite(0x0D, 1, 0x00); err != nil { // the MSPI's chip select low
		t.Fatal(err)
	}
	if err := m.RegWrite(0x0C, 1, 0x03); err != nil {
		t.Fatal(err)
	}
	e = errOf(m.RegWrite(0x0C, 1, 0x00))
	m.RegWrite(0x0D, 1, 0x01)
	check(strings.Contains(e, "command 0x03"), "while busy: a read (0x03) through the MSPI stops the run: "+e)
	_, err = spi(fl, []byte{0x9F}, 3)
	e = errOf(err)
	fl.Select(false)
	check(strings.Contains(e, "command 0x9f"), "while busy: a JEDEC ID read stops the run: "+e)
	_, err = spi(fl, []byte{0x06}, 0)
	e = errOf(err)
	check(strings.Contains(e, "command 0x06"), "while busy: a write enable stops the run: "+e)
	advanceUs(2000)
	_, err1 := spi(fl, []byte{0x06}, 0)
	_, err2 := spi(fl, []byte{0x04}, 0)
	check(err1 == nil && err2 == nil, "the write enable is taken once the part is done")

	program(fl, 0x40004, 0x9A)
	if _, err := NewMachine(fl, -1, 1); err != nil {
		t.Fatal(err)
	}
	check(!wip(fl), "a power-on (a new Machine on the flash) finds the part idle")
}

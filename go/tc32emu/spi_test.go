// SPDX-License-Identifier: Apache-2.0
package tc32emu

// The SPI master model and its device, and the ADC's pin input codes: the
// Go port of checks/spi_check.py, the same register accesses and the same
// trace digest.

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"
	"testing"
)

const spiDigest = "f42f855764c2c592b83c8f7e062ddf52850259c7116b35c7ec5f5a38f22d7ae4"

func spiScenario(t *testing.T, m *Machine) []uint32 {
	var trace []uint32
	w := func(o, size int, v uint32) {
		if err := m.RegWrite(o, size, v); err != nil {
			t.Fatal(err)
		}
	}
	rd := func(o, size int) uint32 {
		v, err := m.RegRead(o, size)
		if err != nil {
			t.Fatal(err)
		}
		trace = append(trace, v)
		return v
	}
	setbits := func(o int, mask, v byte) {
		old, err := m.RegRead(o, 1)
		if err != nil {
			t.Fatal(err)
		}
		w(o, 1, uint32(byte(old)&^mask|v&mask))
	}
	w(0x60, 1, 0x00)
	w(0x63, 1, 0x01)
	w(0xFFD8, 4, 1<<24|0x1D<<16|0)
	setbits(0x59E, 0x20, 0x20)
	setbits(0x59A, 0x20, 0x00)
	setbits(0x59B, 0x20, 0x00)
	setbits(0x5A8, 0xF0, 0x00)
	setbits(0x5A9, 0x03, 0x00)
	setbits(0x5B6, 0x20, 0x20)
	setbits(0x5B7, 0x11, 0x01)
	setbits(0x581, 0x08, 0x08)
	setbits(0x586, 0x1C, 0x00)
	w(0x0A, 1, 0x85)
	w(0x09, 1, 0x02)
	setbits(0x59B, 0x20, 0x20)
	w(0x08, 1, 0x20)
	rd(0x09, 1)
	m.Cycles += 95
	rd(0x09, 1)
	m.Cycles++
	rd(0x09, 1)
	rd(0x08, 1)
	w(0x08, 1, 0x05)
	busyError := uint32(0)
	if err := m.RegWrite(0x08, 1, 0x06); err != nil {
		busyError = 1
	}
	trace = append(trace, busyError)
	m.Cycles += 96
	rd(0x08, 1)
	setbits(0x59B, 0x20, 0x00)
	setbits(0x59B, 0x20, 0x20)
	w(0x08, 1, 0xA5)
	m.Cycles += 96
	rd(0x08, 1)
	w(0x09, 1, 0x02|0x08|0x04)
	rd(0x08, 1)
	m.Cycles += 96
	rd(0x08, 1)
	m.Cycles += 96
	w(0x09, 1, 0x02)
	setbits(0x586, 0x10, 0x10)
	w(0x08, 1, 0x77)
	m.Cycles += 96
	rd(0x08, 1)
	rd(0xFFD8, 4)
	for i := 0; i < 4; i++ {
		rd(0xFFDC, 1)
	}
	// The ADC's pin codes.
	w(0xFFE4, 4, 1<<24|4<<16|0x1234)
	m.Analog[0xEB], m.Analog[0xFC] = 0x4F, 0x00
	const buf = 0x848000
	m.Regs[0xB08], m.Regs[0xB09], m.Regs[0xB0A] = buf&0xFF, (buf>>8)&0xFF, 1
	m.Regs[0xB10] |= 0x04
	slot := func() uint32 {
		if err := m.UpdateTime(); err != nil {
			t.Fatal(err)
		}
		o := buf - SRAMBase
		return binary.LittleEndian.Uint32(m.SRAM[o : o+4])
	}
	trace = append(trace, slot())
	m.Analog[0xEB] = 0x5F
	trace = append(trace, slot())
	w(0xFFE4, 4, 0)
	m.Analog[0xEB] = 0x4F
	trace = append(trace, slot())
	bad := uint32(0)
	if err := m.RegWrite(0xFFE4, 4, 1<<24|11<<16); err != nil {
		bad = 1
	}
	trace = append(trace, bad)
	return trace
}

func TestSpiModel(t *testing.T) {
	m := rbgMachine(t)
	tr := spiScenario(t, m)
	parts := make([]string, len(tr))
	for i, v := range tr {
		parts[i] = fmt.Sprintf("%x", v)
	}
	text := strings.Join(parts, " ")
	sum := sha256.Sum256([]byte(text))
	if got := fmt.Sprintf("%x", sum[:]); got != spiDigest {
		t.Fatalf("trace %s: digest %s, want %s (checks/spi_check.py)", text, got, spiDigest)
	}
	if tr[10] != 2<<16|3 || tr[11] != 0x20 || tr[12] != 0x05 || tr[13] != 0xA5 || tr[15] != 0x1234 {
		t.Fatalf("trace %s", text)
	}
}

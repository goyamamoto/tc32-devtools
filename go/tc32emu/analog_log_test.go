// SPDX-License-Identifier: Apache-2.0
package tc32emu

// The record of the analog port's accesses (LogAnalog): what the Python
// machine keeps as analog_log and analog_writes.

import "testing"

func TestAnalogLog(t *testing.T) {
	fl := NewFlash(FlashMem)
	fl.Mem[8] = 0x4B
	m, err := NewMachine(fl, -1, 1)
	if err != nil {
		t.Fatal(err)
	}
	write := func(addr, val byte) {
		_ = m.Write(RegBase+0xB8, 1, uint32(addr))
		_ = m.Write(RegBase+0xB9, 1, uint32(val))
		_ = m.Write(RegBase+0xBA, 1, 0x60)
		_ = m.Write(RegBase+0xBA, 1, 0x00)
	}
	read := func(addr byte) byte {
		_ = m.Write(RegBase+0xB8, 1, uint32(addr))
		_ = m.Write(RegBase+0xBA, 1, 0x40)
		v, _ := m.Read(RegBase+0xB9, 1)
		_ = m.Write(RegBase+0xBA, 1, 0x00)
		return byte(v)
	}
	write(0x12, 0x68)
	if len(m.AnalogLog) != 0 || len(m.AnalogWrites) != 0 {
		t.Fatalf("recorded with LogAnalog off: %v %v", m.AnalogLog, m.AnalogWrites)
	}
	m.LogAnalog = true
	m.SetSymbols(map[string]uint32{"start": 0})
	write(0x12, 0x28)
	if v := read(0x12); v != 0x28 {
		t.Errorf("read back %#x", v)
	}
	m.XtalReady = true
	v88 := read(0x88)
	want := []AnalogAccess{{'w', 0x12, 0x28}, {'r', 0x12, 0x28}, {'r', 0x88, v88}}
	if len(m.AnalogLog) != len(want) {
		t.Fatalf("AnalogLog %v", m.AnalogLog)
	}
	for i := range want {
		if m.AnalogLog[i] != want[i] {
			t.Errorf("AnalogLog[%d] = %v, want %v", i, m.AnalogLog[i], want[i])
		}
	}
	if v88&0x80 == 0 {
		t.Errorf("0x88 without the crystal-ready bit: %#x", v88)
	}
	if len(m.AnalogWrites) != 1 || m.AnalogWrites[0].Addr != 0x12 || m.AnalogWrites[0].Val != 0x28 || m.AnalogWrites[0].Who != m.Symbolize(m.R[15]) {
		t.Errorf("AnalogWrites %v", m.AnalogWrites)
	}
	if err := m.Reset(-1, false); err != nil {
		t.Fatal(err)
	}
	if len(m.AnalogLog) != 0 || len(m.AnalogWrites) != 0 {
		t.Errorf("a reset keeps the record: %v", m.AnalogLog)
	}
}

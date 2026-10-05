// SPDX-License-Identifier: Apache-2.0
package tc32emu

// Symbolize with symbols set after construction: the Go port of
// checks/symbolize_check.py, the same cases.

import "testing"

func TestSymbolize(t *testing.T) {
	fl := NewFlash(FlashMem)
	fl.Mem[8] = 0x4B
	m, err := NewMachine(fl, -1, 1)
	if err != nil {
		t.Fatal(err)
	}
	check := func(got, want, text string) {
		t.Helper()
		if got != want {
			t.Errorf("%s: %q, want %q", text, got, want)
		}
	}
	check(m.Symbolize(0x1BAA), "0x1baa", "no symbols")
	m.SetSymbols(map[string]uint32{"trim_analog_write": 0x1BA0, "other": 0x1000, "alias": 0x1000})
	check(m.Symbolize(0x1BAA), "trim_analog_write+0xa", "set after construction")
	check(m.Symbolize(0x1004), "other+0x4", "two names at one address: the later by name")
	check(m.Symbolize(0x10), "0x10", "below every symbol")
	m.SetSymbols(nil)
	check(m.Symbolize(0x1BAA), "0x1baa", "cleared")
}

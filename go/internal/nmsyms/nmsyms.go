// Package nmsyms reads code symbols from an ELF through llvm-nm, as
// emulator/run_boot.py's symbols() and idle_ranges() do.
//
// SPDX-License-Identifier: Apache-2.0
package nmsyms

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/goyamamoto/tc32-devtools/go/internal/llvmtool"
	"github.com/goyamamoto/tc32-devtools/go/tc32emu"
)

// Lines runs llvm-nm -S on elf.
func Lines(elf string) ([]string, error) {
	out, err := exec.Command(llvmtool.Tool("llvm-nm"), "-S", elf).Output()
	if err != nil {
		return nil, fmt.Errorf("llvm-nm: %v", err)
	}
	return strings.Split(string(out), "\n"), nil
}

// Symbols returns the code symbols' addresses (bit 0 cleared) and sizes.
func Symbols(elf string) (map[string]uint32, map[string]uint32, error) {
	lines, err := Lines(elf)
	if err != nil {
		return nil, nil, err
	}
	addr, size := map[string]uint32{}, map[string]uint32{}
	for _, line := range lines {
		p := strings.Fields(line)
		if len(p) == 4 && len(p[2]) == 1 && strings.Contains("TtWw", p[2]) {
			a, _ := strconv.ParseUint(p[0], 16, 32)
			s, _ := strconv.ParseUint(p[1], 16, 32)
			addr[p[3]], size[p[3]] = uint32(a)&^1, uint32(s)
		} else if len(p) == 3 && len(p[1]) == 1 && strings.Contains("TtWw", p[1]) {
			a, _ := strconv.ParseUint(p[0], 16, 32)
			addr[p[2]] = uint32(a) &^ 1
		}
	}
	return addr, size, nil
}

// IdleRanges returns the idle functions' address ranges, from which
// Machine.Run skips ahead to the next system timer compare while an
// interrupt could be taken.
func IdleRanges(addr, size map[string]uint32) []tc32emu.Range {
	var idle []tc32emu.Range
	for _, n := range []string{"arch_cpu_idle", "arch_cpu_atomic_idle", "idle"} {
		if a, ok := addr[n]; ok {
			idle = append(idle, tc32emu.Range{Lo: a, Hi: a + size[n]})
		}
	}
	return idle
}

// tc32emu-run: run a freestanding test image in the Go emulator until it
// sets its done word, the way checks/ccdiff/ccdiff.py's emulate() and
// checks/sem_check.py step the Python emulator (instruction by instruction,
// no peripheral time, no interrupts), and print what they read back. Those
// scripts call it with --engine go; their default is the Python emulator.
//
// Usage: tc32emu-run <image.bin> --done <addr> [--limit N]
//
//	(--checksum <addr> --printed <addr> | --results <addr> --count N)
//
// Output, one line:
//
//	ok <checksum hex> <cycles>          the checksum mode, done and printed once
//	ok <r0> <r1> ... <cycles>           the results mode (count words, hex)
//	DIFF checksum printed <n> times
//	SLOW over <limit> instructions
//	EMU <message>
//
// SPDX-License-Identifier: Apache-2.0
package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/goyamamoto/tc32-devtools/go/tc32emu"
)

const doneValue = 0x600DC0DE

func parse(s string) uint32 {
	v, err := strconv.ParseUint(s, 0, 32)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tc32emu-run: bad number", s)
		os.Exit(2)
	}
	return uint32(v)
}

func main() {
	image := ""
	var done, checksum, printed, results uint32
	count, limit := 0, int64(100_000_000)
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--done":
			i++
			done = parse(args[i])
		case "--checksum":
			i++
			checksum = parse(args[i])
		case "--printed":
			i++
			printed = parse(args[i])
		case "--results":
			i++
			results = parse(args[i])
		case "--count":
			i++
			count = int(parse(args[i]))
		case "--limit":
			i++
			limit = int64(parse(args[i]))
		default:
			image = args[i]
		}
	}
	if image == "" || done == 0 {
		fmt.Fprintln(os.Stderr, "usage: tc32emu-run <image.bin> --done <addr> [--limit N] (--checksum <addr> --printed <addr> | --results <addr> --count N)")
		os.Exit(2)
	}
	data, err := os.ReadFile(image)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tc32emu-run:", err)
		os.Exit(1)
	}
	fl := tc32emu.NewFlash(tc32emu.FlashSize)
	copy(fl.Mem, data)
	m, err := tc32emu.NewMachine(fl, 0, 1)
	if err != nil {
		fmt.Println("EMU", err)
		return
	}
	word := func(addr uint32) uint32 { return binary.LittleEndian.Uint32(m.SRAM[addr-tc32emu.SRAMBase:]) }
	d := done - tc32emu.SRAMBase
	// The boot copy puts flash bytes in SRAM; the flag must start clear.
	copy(m.SRAM[d:d+4], []byte{0, 0, 0, 0})
	for word(done) != doneValue {
		for i := 0; i < 20000; i++ {
			if _, _, _, err := m.Step(); err != nil {
				fmt.Printf("EMU %s at pc 0x%x\n", err, m.R[15])
				return
			}
		}
		if m.Cycles > limit {
			fmt.Printf("SLOW over %d instructions\n", limit)
			return
		}
	}
	if results != 0 {
		var parts []string
		for i := 0; i < count; i++ {
			parts = append(parts, fmt.Sprintf("%08x", word(results+4*uint32(i))))
		}
		fmt.Printf("ok %s %d\n", strings.Join(parts, " "), m.Cycles)
		return
	}
	if n := word(printed); n != 1 {
		fmt.Printf("DIFF checksum printed %d times\n", n)
		return
	}
	fmt.Printf("ok %08X %d\n", word(checksum), m.Cycles)
}

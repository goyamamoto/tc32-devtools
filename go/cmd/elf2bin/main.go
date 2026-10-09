// elf2bin: write the flash image of a linked TC32 ELF (the direct path).
//
// Usage: elf2bin <in.elf> <out.bin>
//
// The Go build of compiler/elf2bin.py: the same image, byte for byte
// (checks/run_checks.sh compares them), as a single static binary.
//
// SPDX-License-Identifier: Apache-2.0
package main

import (
	"fmt"
	"os"

	"github.com/goyamamoto/tc32-devtools/go/elf2bin"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: elf2bin <in.elf> <out.bin>")
		os.Exit(2)
	}
	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "elf2bin:", err)
		os.Exit(1)
	}
	image, n, err := elf2bin.Convert(data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "elf2bin: %s: %v\n", os.Args[1], err)
		os.Exit(1)
	}
	if err := os.WriteFile(os.Args[2], image, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "elf2bin:", err)
		os.Exit(1)
	}
	fmt.Printf("%s: %d bytes from %d PT_LOAD segment(s), TC32 code as linked\n", os.Args[2], len(image), n)
}

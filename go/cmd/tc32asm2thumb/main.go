// tc32asm2thumb: translate TC32 assembly (Telink's syntax) into ARMv4T Thumb
// assembly for a mainstream assembler; thumb2tc32 turns the linked result
// back into TC32 machine code. The Go build of compiler/tc32asm2thumb.py,
// with the same output byte for byte.
//
// Usage: tc32asm2thumb <in.S> <out.S>
//
// SPDX-License-Identifier: Apache-2.0
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/goyamamoto/tc32-devtools/go/tc32asm"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: tc32asm2thumb <in.S> <out.S>")
		os.Exit(2)
	}
	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "tc32asm2thumb:", err)
		os.Exit(1)
	}
	out, err := tc32asm.Translate(tc32asm.SplitLines(string(data)))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	text := tc32asm.Header(filepath.Base(os.Args[1])) + strings.Join(out, "")
	if err := os.WriteFile(os.Args[2], []byte(text), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "tc32asm2thumb:", err)
		os.Exit(1)
	}
}

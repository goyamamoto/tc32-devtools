// thumb2tc32: re-encode a Thumb-1 (ARMv4T) ELF as a TC32 image.
//
// Usage: thumb2tc32 <in.elf> <out.bin>
//
// The Go build of compiler/thumb2tc32.py: the same image, byte for byte
// (checks/run_checks.sh compares them), as a single static binary.
//
// SPDX-License-Identifier: Apache-2.0
package main

import (
	"fmt"
	"os"

	"github.com/goyamamoto/tc32-devtools/go/thumb2tc32"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: thumb2tc32 <in.elf> <out.bin>")
		os.Exit(2)
	}
	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "thumb2tc32:", err)
		os.Exit(1)
	}
	image, c, err := thumb2tc32.Convert(data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "thumb2tc32: %s: %v\n", os.Args[1], err)
		os.Exit(1)
	}
	if err := os.WriteFile(os.Args[2], image, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "thumb2tc32:", err)
		os.Exit(1)
	}
	fmt.Printf("%s: %d bytes, %d instructions re-encoded (%d TC32-only, %d lsls #0 as adds #0, %d udf as b ., "+
		"%d zero fill kept), %d bytes of data in code sections left as they are\n",
		os.Args[2], len(image), c.Code, c.TC32Only, c.Movs, c.Udf, c.Zero, c.Data)
}

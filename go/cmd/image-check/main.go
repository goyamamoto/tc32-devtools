// image-check: check the TC32 image thumb2tc32 wrote against the Thumb ELF
// it came from (package imagecheck).
//
// Usage: image-check <firmware.elf> <firmware.bin> [--objcopy <llvm-objcopy>] [--readelf <llvm-readelf>]
//
//	[--blob <manifest.json>] [--startup <manifest.json>]
//
// SPDX-License-Identifier: Apache-2.0
package main

import (
	"os"

	"github.com/goyamamoto/tc32-devtools/go/imagecheck"
)

func main() {
	os.Exit(imagecheck.Main(os.Args[1:], os.Stdout, os.Stderr))
}

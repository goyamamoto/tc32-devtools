// tc32-cc: one command from C and assembly to a checked TC32 image, on a
// mainstream clang and ld.lld (package tc32cc; tc32-cc --help for the usage).
//
// SPDX-License-Identifier: Apache-2.0
package main

import (
	"os"

	"github.com/goyamamoto/tc32-devtools/go/tc32cc"
)

func main() {
	os.Exit(tc32cc.Main(os.Args[1:], os.Stdout, os.Stderr))
}

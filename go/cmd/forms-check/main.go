// forms-check: instruction forms in an image that Telink's own code never
// uses (package formscheck).
//
// Usage: forms-check check <elf> [--thumb] [--evidence vendor_forms.txt]
//
// SPDX-License-Identifier: Apache-2.0
package main

import (
	"os"

	"github.com/goyamamoto/tc32-devtools/go/formscheck"
)

func main() {
	os.Exit(formscheck.Main(os.Args[1:], os.Stdout, os.Stderr))
}

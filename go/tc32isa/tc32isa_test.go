// SPDX-License-Identifier: Apache-2.0
package tc32isa

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

// TestPermutation: TOP is a permutation of 0..31 and INV inverts it.
func TestPermutation(t *testing.T) {
	seen := [32]bool{}
	for tc, th := range TOP {
		if th > 31 || seen[th] {
			t.Fatalf("TOP[%d] = %#x: not a permutation", tc, th)
		}
		seen[th] = true
		if INV[th] != uint16(tc) {
			t.Fatalf("INV[TOP[%d]] = %d", tc, INV[th])
		}
	}
	for hw := 0; hw < 0x10000; hw++ {
		if ToTC32(ToThumb(uint16(hw))) != uint16(hw) {
			t.Fatalf("%#04x does not round-trip", hw)
		}
	}
}

// TestMatchesPython: the table equals the one in the reference
// implementation, ../../tc32isa.py.
func TestMatchesPython(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "tc32isa.py"))
	if err != nil {
		t.Skip("reference tc32isa.py not found:", err)
	}
	m := regexp.MustCompile(`(?s)\nTOP = \[(.*?)\]`).FindSubmatch(src)
	if m == nil {
		t.Fatal("no TOP table in tc32isa.py")
	}
	vals := regexp.MustCompile(`0x[0-9a-f]+`).FindAll(m[1], -1)
	if len(vals) != 32 {
		t.Fatalf("tc32isa.py TOP has %d entries", len(vals))
	}
	for i, v := range vals {
		n, err := strconv.ParseUint(string(v), 0, 16)
		if err != nil {
			t.Fatal(err)
		}
		if uint16(n) != TOP[i] {
			t.Fatalf("TOP[%d]: Go %#x, Python %#x", i, TOP[i], n)
		}
	}
}

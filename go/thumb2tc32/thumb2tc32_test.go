// SPDX-License-Identifier: Apache-2.0
package thumb2tc32

import (
	"testing"

	"github.com/goyamamoto/tc32-devtools/go/tc32isa"
)

// TestRewrite: the two rewrites and the zero fill, as thumb2tc32.py's
// rewrite() docstring states them.
func TestRewrite(t *testing.T) {
	cases := []struct {
		in, out uint16
		kind    Kind
	}{
		{0x0000, 0x0000, KindZero}, // .org/.space fill, not lsls r0, r0, #0
		{0x0002, 0x1C02, KindMovs}, // lsls r2, r0, #0 -> adds r2, r0, #0
		{0x003F, 0x1C3F, KindMovs}, // lsls r7, r7, #0
		{0x0040, 0x0040, KindNone}, // lsls r0, r0, #1: kept
		{0xDEFE, 0xE7FE, KindUdf},  // udf #0xfe -> b .
		{0xDE00, 0xE7FE, KindUdf},  // udf #0
		{0xDF00, 0xDF00, KindNone}, // svc: not a udf
		{0x4770, 0x4770, KindNone}, // bx lr
		{0xB500, 0xB500, KindNone}, // push {lr}
	}
	for _, c := range cases {
		out, kind := Rewrite(c.in)
		if out != c.out || kind != c.kind {
			t.Errorf("Rewrite(%#04x) = %#04x, %v; want %#04x, %v", c.in, out, kind, c.out, c.kind)
		}
	}
}

// TestEncode: zero fill stays zero, everything else goes through the
// permutation after the rewrite.
func TestEncode(t *testing.T) {
	if Encode(0x0000) != 0x0000 {
		t.Error("zero fill must stay 0x0000")
	}
	if Encode(0x0002) != tc32isa.ToTC32(0x1C02) {
		t.Error("lsls #0 must be encoded as adds #0")
	}
	if Encode(0xDEFE) != tc32isa.ToTC32(0xE7FE) {
		t.Error("udf must be encoded as b .")
	}
	if Encode(0x4770) != tc32isa.ToTC32(0x4770) {
		t.Error("bx lr must be re-encoded as it is")
	}
	// The BL halves are never rewritten (Convert re-encodes them as a pair).
	for _, hw := range []uint16{0xF000, 0xF800, 0xF7FF, 0xFFFF} {
		if Encode(hw) != tc32isa.ToTC32(hw) {
			t.Errorf("BL half %#04x must only be re-encoded", hw)
		}
	}
}

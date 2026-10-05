// SPDX-License-Identifier: Apache-2.0
package ble

import (
	"encoding/hex"
	"testing"
)

// The first values of Python's random.Random(1): getrandbits(32) x 8,
// randrange(256) x 24, and bytes(randrange(256) for 16).
func TestPyRandomMatchesPython(t *testing.T) {
	r := newPyRandom(1)
	want32 := []uint32{577090037, 2444712010, 3639700191, 3445702192, 3280387012, 271041745, 1095513148, 506456969}
	for i, w := range want32 {
		if got := r.getrandbits(32); got != w {
			t.Fatalf("getrandbits(32) #%d: %d, want %d", i, got, w)
		}
	}
	r = newPyRandom(1)
	want := []uint32{68, 32, 130, 60, 253, 230, 241, 194, 107, 48, 249, 14, 199, 221, 1, 228, 136, 117, 52, 162, 15, 11, 13, 4}
	for i, w := range want {
		if got := r.randrange(256); got != w {
			t.Fatalf("randrange(256) #%d: %d, want %d", i, got, w)
		}
	}
	r = newPyRandom(1)
	if got := hex.EncodeToString(r.bytes(16)); got != "4420823cfde6f1c26b30f90ec7dd01e4" {
		t.Fatalf("16 bytes: %s", got)
	}
}

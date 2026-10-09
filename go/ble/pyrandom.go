// SPDX-License-Identifier: Apache-2.0
package ble

import "math/big"

// pyRandom reproduces Python's random.Random(seed) for a small int seed:
// MT19937 seeded through init_by_array([seed]), getrandbits(k) as the top k
// bits of one 32-bit output (k <= 32), and randrange(n) as
// _randbelow_with_getrandbits (rejection sampling over n.bit_length() bits).
// The central's mrand, SKDm and IVm bytes must equal the Python central's
// (random.Random(1)) for the canonical logs to agree.
type pyRandom struct {
	mt  [624]uint32
	idx int
}

func newPyRandom(seed uint32) *pyRandom {
	r := &pyRandom{}
	r.initGenrand(19650218)
	key := []uint32{seed}
	i, j := 1, 0
	n := 624
	if len(key) > n {
		n = len(key)
	}
	for k := n; k > 0; k-- {
		r.mt[i] = (r.mt[i] ^ ((r.mt[i-1] ^ (r.mt[i-1] >> 30)) * 1664525)) + key[j] + uint32(j)
		i++
		j++
		if i >= 624 {
			r.mt[0] = r.mt[623]
			i = 1
		}
		if j >= len(key) {
			j = 0
		}
	}
	for k := 623; k > 0; k-- {
		r.mt[i] = (r.mt[i] ^ ((r.mt[i-1] ^ (r.mt[i-1] >> 30)) * 1566083941)) - uint32(i)
		i++
		if i >= 624 {
			r.mt[0] = r.mt[623]
			i = 1
		}
	}
	r.mt[0] = 0x80000000
	r.idx = 624
	return r
}

func (r *pyRandom) initGenrand(s uint32) {
	r.mt[0] = s
	for i := 1; i < 624; i++ {
		r.mt[i] = 1812433253*(r.mt[i-1]^(r.mt[i-1]>>30)) + uint32(i)
	}
	r.idx = 624
}

func (r *pyRandom) genrand() uint32 {
	if r.idx >= 624 {
		for i := 0; i < 624; i++ {
			y := (r.mt[i] & 0x80000000) | (r.mt[(i+1)%624] & 0x7fffffff)
			v := r.mt[(i+397)%624] ^ (y >> 1)
			if y&1 != 0 {
				v ^= 0x9908b0df
			}
			r.mt[i] = v
		}
		r.idx = 0
	}
	y := r.mt[r.idx]
	r.idx++
	y ^= y >> 11
	y ^= (y << 7) & 0x9d2c5680
	y ^= (y << 15) & 0xefc60000
	y ^= y >> 18
	return y
}

// getrandbits for k <= 32.
func (r *pyRandom) getrandbits(k uint) uint32 { return r.genrand() >> (32 - k) }

// randrange(n) for n > 0, as Python's _randbelow_with_getrandbits.
func (r *pyRandom) randrange(n uint32) uint32 {
	k := uint(0)
	for x := n; x > 0; x >>= 1 {
		k++
	}
	v := r.getrandbits(k)
	for v >= n {
		v = r.getrandbits(k)
	}
	return v
}

func (r *pyRandom) bytes(n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = byte(r.randrange(256))
	}
	return out
}

// getrandbitsBig is Python's getrandbits(k) for any k: 32-bit words, the first generated the least
// significant, the last one shifted right when k is not a multiple of 32.
func (r *pyRandom) getrandbitsBig(k uint) *big.Int {
	words := (k-1)/32 + 1
	out := new(big.Int)
	for i := uint(0); i < words; i++ {
		w := r.genrand()
		if k-32*i < 32 {
			w >>= 32 - (k - 32*i)
		}
		out.Or(out, new(big.Int).Lsh(big.NewInt(int64(w)), 32*i))
	}
	return out
}

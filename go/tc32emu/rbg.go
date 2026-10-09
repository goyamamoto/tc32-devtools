// SPDX-License-Identifier: Apache-2.0
package tc32emu

// The random number generator's sources and the 32 kHz timer's jitter: the
// Go port of the Python RBG_MODES, rbg_set(), rbg_ready(), rbg_word(),
// k32_now() with jitter, k32_jitter_ms() and k32_set_jitter(). The same
// splitmix64 sequences, so both engines give the same words and counts
// (checks/rbg_check.py and rbg_test.go hold the same digests).

import (
	"os"
	"strconv"
	"strings"
)

// RBGModes are the sources TC32EMU_RBG names and register 0xfff4 numbers.
var RBGModes = []string{"lcg", "healthy", "stuck", "biased", "cycle", "attacker", "never"}

// RBGMode and RBGParam are TC32EMU_RBG (name[:parameter]).
var RBGMode, RBGParam = rbgFromEnv()

// K32JitterNs (TC32EMU_K32_JITTER_NS) and K32JitterSeed
// (TC32EMU_K32_JITTER_SEED, default 1).
var K32JitterNs = int64(sizeFromEnv("TC32EMU_K32_JITTER_NS", 0))
var K32JitterSeed = k32SeedFromEnv()

// K32Frozen (TC32EMU_K32_FROZEN not 0): 0x750 and 0x74b bit 5 read as at
// power-on, a 32 kHz count that stands still for the code that reads it
// (the timer wake keeps its own count).
var K32Frozen = os.Getenv("TC32EMU_K32_FROZEN") != "" && os.Getenv("TC32EMU_K32_FROZEN") != "0"

const (
	k32PeriodMs   = 1000.0 / 32768 // exact in binary
	k32JitterSpan = 4096           // periods given jitter at most after a jump of the time
	k32Sum4SD     = 37837.2264     // standard deviation of a sum of four uniform 16-bit values
)

func rbgFromEnv() (int, uint32) {
	v := os.Getenv("TC32EMU_RBG")
	if v == "" {
		return 0, 0
	}
	name, param, _ := strings.Cut(v, ":")
	mode := -1
	for i, n := range RBGModes {
		if n == name {
			mode = i
		}
	}
	if mode < 0 {
		panic("TC32EMU_RBG: " + name)
	}
	if param == "" {
		return mode, 0
	}
	p, err := strconv.ParseInt(param, 0, 64)
	if err != nil {
		panic("TC32EMU_RBG: " + v)
	}
	return mode, uint32(p) & 0xFFFFFF
}

func k32SeedFromEnv() uint64 {
	s := os.Getenv("TC32EMU_K32_JITTER_SEED")
	if s == "" {
		return 1
	}
	v, err := strconv.ParseUint(s, 0, 64)
	if err != nil {
		panic("TC32EMU_K32_JITTER_SEED: " + s)
	}
	return v
}

// splitmix64 is one step: the next state and the 64-bit output.
func splitmix64(state uint64) (uint64, uint64) {
	state += 0x9E3779B97F4A7C15
	z := state
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return state, z ^ (z >> 31)
}

func (m *Machine) rbgSet(mode int, param uint32) {
	m.rbgMode, m.rbgParam, m.rbgState, m.rbgIndex = mode, param, uint64(param), 0
	m.rbgCycle = nil
	if mode == 4 {
		n := int(param)
		if n == 0 {
			n = 8
		}
		var s, z uint64
		for i := 0; i < n; i++ {
			s, z = splitmix64(s)
			m.rbgCycle = append(m.rbgCycle, uint32(z>>32))
		}
	}
}

func (m *Machine) rbgReady() bool {
	r := m.Regs
	if m.rbgMode == 0 {
		return true
	}
	return m.rbgMode != 6 && r[0x62]&0x08 == 0 && r[0x65]&0x08 != 0 && r[0x4400]&0x01 != 0
}

func (m *Machine) rbgWord() uint32 {
	switch {
	case m.rbgMode == 0:
		m.rng = m.rng*1103515245 + 12345
		return m.rng
	case !m.rbgReady():
		return 0
	case m.rbgMode == 2:
		return 0x5A5A5A5A ^ m.rbgParam
	case m.rbgMode == 3:
		p := uint64(m.rbgParam)
		if p == 0 {
			p = 243
		}
		var w uint32
		for i := 0; i < 4; i++ {
			var z uint64
			m.rbgState, z = splitmix64(m.rbgState)
			for j := 0; j < 8; j++ {
				if (z>>(8*uint(j)))&0xFF < p {
					w |= 1 << uint(8*i+j)
				}
			}
		}
		return w
	case m.rbgMode == 4:
		w := m.rbgCycle[m.rbgIndex%len(m.rbgCycle)]
		m.rbgIndex++
		return w
	}
	var z uint64
	m.rbgState, z = splitmix64(m.rbgState)
	return uint32(z >> 32)
}

func (m *Machine) k32JitterMs() float64 {
	var z uint64
	m.k32Rng, z = splitmix64(m.k32Rng)
	s := int64(z&0xFFFF) + int64((z>>16)&0xFFFF) + int64((z>>32)&0xFFFF) + int64(z>>48) - 131070
	return float64(s) * float64(m.k32SigmaNs) / k32Sum4SD * 1e-6
}

// k32SetJitter turns the 32 kHz jitter on (or changes it) from the exact count now.
func (m *Machine) k32SetJitter(sigmaNs int64) {
	if sigmaNs != 0 && m.k32SigmaNs == 0 {
		m.k32Count = int64(m.Ms() * 32.768)
		m.k32Next = float64(m.k32Count+1) * k32PeriodMs
	}
	m.k32SigmaNs = sigmaNs
}

// k32Jittered is k32Now with the jitter on.
func (m *Machine) k32Jittered() uint32 {
	t := m.Ms()
	if t >= m.k32Next {
		n := int64((t - m.k32Next) / k32PeriodMs)
		if n > k32JitterSpan {
			skip := n - k32JitterSpan
			m.k32Count += skip
			m.k32Next += float64(skip) * k32PeriodMs
		}
		for t >= m.k32Next {
			m.k32Count++
			m.k32Next += k32PeriodMs + m.k32JitterMs()
		}
	}
	return uint32(m.k32Count & m32)
}

// rbgControl handles the emulator-only registers 0xfff4, 0xfff8 (bits 30:0
// the jitter in ns, bit 31 stops the 32 kHz count) and 0xfffc.
func (m *Machine) rbgControl(o int, val uint32) error {
	switch o {
	case 0xFFF4:
		if int(val&0xFF) >= len(RBGModes) {
			return emuErr("0xfff4: no random number source %d", val&0xFF)
		}
		m.rbgSet(int(val&0xFF), val>>8)
		m.event("random number source: " + RBGModes[val&0xFF] + ", parameter " + strconv.FormatUint(uint64(val>>8), 10))
	case 0xFFF8:
		m.k32SetJitter(int64(val & 0x7FFFFFFF))
		m.k32Stopped = nil
		text := "32 kHz jitter: " + strconv.FormatUint(uint64(val&0x7FFFFFFF), 10) + " ns"
		if val>>31 != 0 {
			c := m.k32Now()
			m.k32Stopped = &c
			text += ", count stopped"
		}
		m.event(text)
	default:
		m.k32Rng = uint64(val)
	}
	m.store(o, 4, val)
	return nil
}

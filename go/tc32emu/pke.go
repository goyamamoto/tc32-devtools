// SPDX-License-Identifier: Apache-2.0
package tc32emu

// The public key engine (DS-TLSR8278 16): the Go port of the Python
// PKE_US, pke_go(), pke_stop(), pke_done() and the ec_* arithmetic. The same
// integer arithmetic on the same operand RAM, so both engines give the same
// results (checks/pke_check.py and pke_test.go hold the same values).

import (
	"encoding/binary"
	"math"
	"math/big"
	"strconv"
)

// PKEUs is the time one of the engine's operations takes, in us
// (TC32EMU_PKE_US, or register 0xffec). The chip's figure is not in the
// datasheet; the default is a guess.
var PKEUs = int64(sizeFromEnv("TC32EMU_PKE_US", 30000)) // a point multiplication on the chip: 30 ms

// PKEPVerUs is the time of a check that a point is on the curve (TC32EMU_PKE_PVER_US).
var PKEPVerUs = int64(sizeFromEnv("TC32EMU_PKE_PVER_US", 200))

// The engine's registers (Table 16-2) and operand RAM (Table 16-1, 256 bits:
// a slot every 0x24 bytes) at their offsets in the register space.
const (
	pkeCtrl, pkeConf, pkeMcPtr, pkeStat, pkeRtCode, pkeExeConf = 0x2000, 0x2004, 0x2010, 0x2020, 0x2024, 0x2050
	pkeRAMA, pkeRAMB, pkeStep, pkeRAMEnd                       = 0x2400, 0x3000, 0x24, 0x3200
	pkePMUL, pkePVER, pkeCalPreMon                             = 0x10, 0x0C, 0x28
	pkeRtStopped, pkeRtNoInverse, pkeRtNotOnCurve              = 1, 2, 3
)

type pkeWrite struct {
	off   int
	value *big.Int
	words int
}

type ecPoint struct{ x, y *big.Int }

// ecAdd is the Python ec_add: affine addition on y^2 = x^3 + a x + b over
// the prime field of p; nil is the point at infinity.
func ecAdd(p, a *big.Int, P, Q *ecPoint) *ecPoint {
	if P == nil {
		return Q
	}
	if Q == nil {
		return P
	}
	lam := new(big.Int)
	if P.x.Cmp(Q.x) == 0 {
		if s := new(big.Int).Add(P.y, Q.y); s.Mod(s, p).Sign() == 0 {
			return nil
		}
		// (3 x1^2 + a) / (2 y1)
		lam.Mul(P.x, P.x)
		lam.Mul(lam, big.NewInt(3))
		lam.Add(lam, a)
		d := new(big.Int).Lsh(P.y, 1)
		lam.Mul(lam, d.ModInverse(d, p))
	} else {
		d := new(big.Int).Sub(Q.x, P.x)
		d.Mod(d, p)
		lam.Sub(Q.y, P.y)
		lam.Mul(lam, d.ModInverse(d, p))
	}
	lam.Mod(lam, p)
	x3 := new(big.Int).Mul(lam, lam)
	x3.Sub(x3, P.x)
	x3.Sub(x3, Q.x)
	x3.Mod(x3, p)
	y3 := new(big.Int).Sub(P.x, x3)
	y3.Mul(y3, lam)
	y3.Sub(y3, P.y)
	y3.Mod(y3, p)
	return &ecPoint{x3, y3}
}

func ecMul(p, a, k *big.Int, P *ecPoint) *ecPoint {
	var R *ecPoint
	for i := 0; i < k.BitLen(); i++ {
		if k.Bit(i) != 0 {
			R = ecAdd(p, a, R, P)
		}
		P = ecAdd(p, a, P, P)
	}
	return R
}

func ecOnCurve(p, a, b *big.Int, P *ecPoint) bool {
	if P.x.Cmp(p) >= 0 || P.y.Cmp(p) >= 0 {
		return false
	}
	l := new(big.Int).Mul(P.y, P.y)
	r := new(big.Int).Mul(P.x, P.x)
	r.Mul(r, P.x)
	r.Add(r, new(big.Int).Mul(a, P.x))
	r.Add(r, b)
	l.Sub(l, r)
	return l.Mod(l, p).Sign() == 0
}

// pkeOperand is words little-endian words of the operand RAM at off.
func (m *Machine) pkeOperand(off, words int) *big.Int {
	b := make([]byte, 4*words)
	for i := 0; i < words; i++ {
		binary.BigEndian.PutUint32(b[4*(words-1-i):], binary.LittleEndian.Uint32(m.Regs[off+4*i:]))
	}
	return new(big.Int).SetBytes(b)
}

func (m *Machine) pkeStore(off int, value *big.Int, words int) {
	b := value.FillBytes(make([]byte, 4*words))
	for i := 0; i < words; i++ {
		binary.LittleEndian.PutUint32(m.Regs[off+4*i:], binary.BigEndian.Uint32(b[4*(words-1-i):]))
	}
}

// pkeMontgomery is R^2 mod p and -p^-1 mod 2^32 for R = 2^256.
func pkeMontgomery(p *big.Int) (*big.Int, *big.Int) {
	r := new(big.Int).Lsh(big.NewInt(1), 256)
	r2 := new(big.Int).Exp(r, big.NewInt(2), p)
	w := new(big.Int).Lsh(big.NewInt(1), 32)
	n1 := new(big.Int).ModInverse(p, w)
	n1.Neg(n1)
	n1.Mod(n1, w)
	return r2, n1
}

// pkeDone is whether Done reads 1; a finished operation's results reach the
// operand RAM then.
func (m *Machine) pkeDone() bool {
	if m.pkeDoneAt < 0 || m.Cycles < m.pkeDoneAt {
		return false
	}
	for _, w := range m.pkePending {
		m.pkeStore(w.off, w.value, w.words)
	}
	m.pkePending = nil
	return true
}

func (m *Machine) pkeStop() {
	if m.pkeDoneAt >= 0 && !m.pkeDone() {
		m.pkeRT, m.pkePending, m.pkeDoneAt = pkeRtStopped, nil, m.Cycles
		m.event("PKE: stopped")
	}
}

// pkeGo is CTRL.Go: the microcode MC_PTR names, on the operand RAM (the
// Python pke_go has the slots).
func (m *Machine) pkeGo() error {
	r := m.Regs
	here := m.Symbolize(m.R[15])
	if r[0x61]&0x80 != 0 {
		return emuErr("PKE started while in reset (0x61 bit 7) at %s", here)
	}
	if r[0x64]&0x80 == 0 {
		return emuErr("PKE started without its clock (0x64 bit 7) at %s", here)
	}
	if m.pkeDoneAt >= 0 && !m.pkeDone() {
		return emuErr("PKE started while running at %s", here)
	}
	conf := binary.LittleEndian.Uint32(r[pkeConf:])
	if (conf>>24)&7 != 2 || (conf>>16)&0xFF != 8 {
		return emuErr("PKE radix 0x%08x not modelled (256-bit operands: base 2, partial 8) at %s", conf, here)
	}
	exe := binary.LittleEndian.Uint32(r[pkeExeConf:])
	if exe&0x3F != 0x15 {
		return emuErr("PKE operand form 0x%02x not modelled (affine, not Montgomery: 0x15) at %s", exe, here)
	}
	mc := binary.LittleEndian.Uint32(r[pkeMcPtr:]) & 0xFF
	aSlot := func(i int) int { return pkeRAMA + i*pkeStep }
	bSlot := func(i int) int { return pkeRAMB + i*pkeStep }
	p := m.pkeOperand(bSlot(3), 8)
	rt, out, text := uint32(0), []pkeWrite(nil), ""
	switch {
	case mc == pkeCalPreMon:
		if p.Cmp(big.NewInt(3)) < 0 || p.Bit(0) == 0 {
			return emuErr("PKE CAL_PRE_MON of 0x%x at %s", p, here)
		}
		r2, n1 := pkeMontgomery(p)
		out = []pkeWrite{{aSlot(3), r2, 9}, {bSlot(4), n1, 9}}
		text = "Montgomery constants"
	case mc == pkePMUL || mc == pkePVER:
		if p.Cmp(big.NewInt(3)) < 0 || p.Bit(0) == 0 {
			return emuErr("PKE modulus 0x%x at %s", p, here)
		}
		r2, n1 := pkeMontgomery(p)
		if m.pkeOperand(aSlot(3), 8).Cmp(r2) != 0 {
			return emuErr("PKE A3 is not R^2 mod p at %s", here)
		}
		if m.pkeOperand(bSlot(4), 1).Cmp(n1) != 0 {
			return emuErr("PKE B4 is not -p^-1 mod 2^32 at %s", here)
		}
		P := &ecPoint{m.pkeOperand(bSlot(0), 8), m.pkeOperand(bSlot(1), 8)}
		a := m.pkeOperand(aSlot(5), 8)
		if mc == pkePVER {
			b := m.pkeOperand(aSlot(4), 8)
			text = "point on the curve"
			if !ecOnCurve(p, a, b, P) {
				rt, text = pkeRtNotOnCurve, "point not on the curve"
			}
		} else {
			k := m.pkeOperand(aSlot(4), 8)
			if k.Sign() == 0 {
				// The chip never finishes a multiplication by 0: Done stays clear until Stop.
				m.pkeRT, m.pkePending, m.pkeDoneAt = 0, nil, math.MaxInt64
				m.event("PKE: point multiplication by 0, never done")
				return nil
			}
			var Q *ecPoint
			if P.x.Cmp(p) < 0 && P.y.Cmp(p) < 0 {
				Q = ecMul(p, a, k, P)
			}
			if Q == nil {
				rt, text = pkeRtNoInverse, "point multiplication: no valid modulo inverse"
			} else {
				out = []pkeWrite{{aSlot(0), Q.x, 9}, {aSlot(1), Q.y, 9}}
				text = "point multiplication"
			}
		}
	default:
		return emuErr("PKE microcode 0x%02x not modelled at %s", mc, here)
	}
	us := m.pkeUs
	if mc == pkePVER {
		us = PKEPVerUs
	}
	m.pkeRT, m.pkePending = rt, out
	m.pkeDoneAt = m.Cycles + (us*m.CPUHz+999999)/1000000
	m.event("PKE: " + text + ", done in " + strconv.FormatInt(us, 10) + " us")
	return nil
}

// pkeRead handles the engine's registers that are not plain storage: Done,
// STOP_LOG, and the operand RAM once an operation has ended.
func (m *Machine) pkeRead(o, size int) (uint32, bool) {
	switch {
	case o == pkeStat && (size == 1 || size == 4):
		if m.pkeDone() {
			return 1, true
		}
		return 0, true
	case o == pkeRtCode && (size == 1 || size == 4):
		if m.pkeDone() {
			return m.pkeRT, true
		}
		return 0, true
	case pkeRAMA <= o && o < pkeRAMEnd:
		m.pkeDone()
	}
	return 0, false
}

// pkeWriteReg handles CTRL (bit 0 Go, bit 16 Stop), STAT (a write clears Done
// once the operation has ended, whatever bit 0 holds; DS-TLSR8278 Table 16-2
// names a write of 1) and the emulator-only 0xffec.
func (m *Machine) pkeWriteReg(o, size int, val uint32) (bool, error) {
	switch {
	case o == pkeCtrl && (size == 1 || size == 4):
		if val&0x01 != 0 {
			if err := m.pkeGo(); err != nil {
				return true, err
			}
		}
		if size == 4 && val&0x10000 != 0 {
			m.pkeStop()
		}
		return true, nil
	case o == pkeStat && (size == 1 || size == 4):
		if m.pkeDone() {
			m.pkeDoneAt = -1
		}
		return true, nil
	case o == 0xFFEC && size == 4:
		m.pkeUs = int64(val)
		m.event("PKE: " + strconv.FormatInt(m.pkeUs, 10) + " us per operation")
		m.store(o, 4, val)
		return true, nil
	}
	return false, nil
}

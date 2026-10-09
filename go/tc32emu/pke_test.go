// SPDX-License-Identifier: Apache-2.0
package tc32emu

// The public key engine's model: the Go port of checks/pke_check.py, the
// same operations and the same values.

import (
	"math/big"
	"strings"
	"testing"
)

func pkeInt(t *testing.T, hex string) *big.Int {
	v, ok := new(big.Int).SetString(hex, 16)
	if !ok {
		t.Fatal("not hex: " + hex)
	}
	return v
}

func pkeMachine(t *testing.T) *Machine {
	m := rbgMachine(t)
	m.Regs[0x61] &^= 0x80 // reset released
	m.Regs[0x64] |= 0x80  // clock on
	if err := m.RegWrite(pkeConf, 4, 2<<24|8<<16); err != nil {
		t.Fatal(err)
	}
	if err := m.RegWrite(pkeExeConf, 4, 0x15); err != nil {
		t.Fatal(err)
	}
	return m
}

// pkeRun starts the microcode, advances the clock to its end and gives the stop reason.
func pkeRun(t *testing.T, m *Machine, mc uint32) (uint32, error) {
	for _, w := range []struct {
		o   int
		val uint32
	}{{pkeMcPtr, mc}, {pkeStat, 0}} {
		if err := m.RegWrite(w.o, 4, w.val); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.RegWrite(pkeCtrl, 4, 1); err != nil {
		return 0, err
	}
	m.Cycles = m.pkeDoneAt
	return rbgRead(t, m, pkeRtCode, 4), nil
}

func TestPKE(t *testing.T) {
	p := pkeInt(t, "ffffffff00000001000000000000000000000000ffffffffffffffffffffffff")
	a := new(big.Int).Sub(p, big.NewInt(3))
	b := pkeInt(t, "5ac635d8aa3a93e7b3ebbd55769886bc651d06b0cc53b0f63bce3c3e27d2604b")
	gx := pkeInt(t, "6b17d1f2e12c4247f8bce6e563a440f277037d812deb33a0f4a13945d898c296")
	gy := pkeInt(t, "4fe342e2fe1a7f9b8ee7eb4a7c0f9e162bce33576b315ececbb6406837bf51f5")
	r2, n1 := pkeMontgomery(p)
	// The Core specification's debug key pair, a second pair and their shared secret.
	dA := pkeInt(t, "3f49f6d4a3c55f3874c9b3e3d2103f504aff607beb40b7995899b8a6cd3c1abd")
	ax := pkeInt(t, "20b003d2f297be2c5e2c83a7e9f9a5b9eff49111acf4fddbcc0301480e359de6")
	ay := pkeInt(t, "dc809c49652aeb6d63329abf5a52155c766345c28fed3024741c8ed01589d28b")
	dB := pkeInt(t, "55188b3d32f6bb9a900afcfbeed4e72a59cb9ac2f19d7cfb6b4fdd49f47fc5fd")
	bx := pkeInt(t, "1ea1f0f01faf1d9609592284f19e4c0047b58afd8615a69f559077b22faaa190")
	by := pkeInt(t, "4c55f33e429dad377356703a9ab85160472d1130e28e36765f89aff915b1214a")
	dhkey := pkeInt(t, "ec0234a357c8ad05341010a60a397d9b99796b13b4f866f1868d34f373bfa698")
	aSlot := func(i int) int { return pkeRAMA + i*pkeStep }
	bSlot := func(i int) int { return pkeRAMB + i*pkeStep }
	m := pkeMachine(t)
	curve := func() {
		m.pkeStore(bSlot(3), p, 9)
		m.pkeStore(aSlot(3), r2, 9)
		m.pkeStore(bSlot(4), n1, 9)
		m.pkeStore(aSlot(5), a, 9)
	}
	mul := func(k, x, y *big.Int) (uint32, *big.Int, *big.Int) {
		curve()
		m.pkeStore(bSlot(0), x, 9)
		m.pkeStore(bSlot(1), y, 9)
		m.pkeStore(aSlot(4), k, 9)
		rt, err := pkeRun(t, m, pkePMUL)
		if err != nil {
			t.Fatal(err)
		}
		return rt, m.pkeOperand(aSlot(0), 9), m.pkeOperand(aSlot(1), 9)
	}
	verify := func(x, y *big.Int) uint32 {
		curve()
		m.pkeStore(bSlot(0), x, 9)
		m.pkeStore(bSlot(1), y, 9)
		m.pkeStore(aSlot(4), b, 9)
		rt, err := pkeRun(t, m, pkePVER)
		if err != nil {
			t.Fatal(err)
		}
		return rt
	}
	if rt, x, y := mul(dA, gx, gy); rt != 0 || x.Cmp(ax) != 0 || y.Cmp(ay) != 0 {
		t.Errorf("PMUL dA*G: %d %x %x", rt, x, y)
	}
	if rt, x, y := mul(dB, gx, gy); rt != 0 || x.Cmp(bx) != 0 || y.Cmp(by) != 0 {
		t.Errorf("PMUL dB*G: %d %x %x", rt, x, y)
	}
	rt, x, y := mul(dA, bx, by)
	if rt != 0 || x.Cmp(dhkey) != 0 {
		t.Errorf("PMUL dA*B: %d %x", rt, x)
	}
	if rt2, x2, y2 := mul(dB, ax, ay); rt2 != 0 || x2.Cmp(dhkey) != 0 || y2.Cmp(y) != 0 {
		t.Errorf("PMUL dB*A: %d %x %x", rt2, x2, y2)
	}
	// A multiplication by 0 never finishes on the chip: Done stays 0 however long, until Stop.
	curve()
	m.pkeStore(bSlot(0), gx, 9)
	m.pkeStore(bSlot(1), gy, 9)
	m.pkeStore(aSlot(4), big.NewInt(0), 9)
	for _, w := range []struct{ off, v int }{{pkeMcPtr, pkePMUL}, {pkeStat, 0}, {pkeCtrl, 1}} {
		if err := m.RegWrite(w.off, 4, uint32(w.v)); err != nil {
			t.Fatal(err)
		}
	}
	m.Cycles += 100 * PKEUs * m.CPUHz / 1000000
	if rbgRead(t, m, pkeStat, 4) != 0 {
		t.Error("PMUL 0*G: Done after 100 times the operation's time")
	}
	if err := m.RegWrite(pkeCtrl, 4, 1<<16); err != nil {
		t.Fatal(err)
	}
	if rbgRead(t, m, pkeStat, 4) != 1 || rbgRead(t, m, pkeRtCode, 4) != pkeRtStopped {
		t.Error("PMUL 0*G: Stop does not end it with STOP_LOG 1")
	}
	c0 := m.Cycles
	if rt := verify(ax, ay); rt != 0 {
		t.Errorf("PVER A: %d", rt)
	}
	if m.Cycles-c0 != (PKEPVerUs*m.CPUHz+999999)/1000000 {
		t.Errorf("PVER took %d cycles, not PKE_PVER_US (%d us)", m.Cycles-c0, PKEPVerUs)
	}
	if rt := verify(ax, new(big.Int).Add(ay, big.NewInt(1))); rt != pkeRtNotOnCurve {
		t.Errorf("PVER off the curve: %d", rt)
	}
	if rt := verify(ax, p); rt != pkeRtNotOnCurve {
		t.Errorf("PVER y = p: %d", rt)
	}
	m.pkeStore(aSlot(3), big.NewInt(0), 9)
	m.pkeStore(bSlot(4), big.NewInt(0), 9)
	if rt, err := pkeRun(t, m, pkeCalPreMon); err != nil || rt != 0 ||
		m.pkeOperand(aSlot(3), 8).Cmp(r2) != 0 || m.pkeOperand(bSlot(4), 1).Cmp(n1) != 0 {
		t.Errorf("CAL_PRE_MON: %v %d %x %x", err, rt, m.pkeOperand(aSlot(3), 8), m.pkeOperand(bSlot(4), 1))
	}

	// Done, its clearing, the time and Stop.
	m = pkeMachine(t)
	curve()
	m.pkeStore(bSlot(0), gx, 9)
	m.pkeStore(bSlot(1), gy, 9)
	m.pkeStore(aSlot(4), dA, 9)
	for _, w := range []struct {
		o   int
		val uint32
	}{{0xFFEC, 1234}, {pkeMcPtr, pkePMUL}, {pkeCtrl, 1}} {
		if err := m.RegWrite(w.o, 4, w.val); err != nil {
			t.Fatal(err)
		}
	}
	t0 := m.Cycles
	if rbgRead(t, m, pkeStat, 4) != 0 || rbgRead(t, m, pkeRtCode, 4) != 0 || m.pkeOperand(aSlot(0), 8).Cmp(ax) == 0 {
		t.Error("Done, STOP_LOG or the result seen while the engine runs")
	}
	m.Cycles = t0 + 1234*m.CPUHz/1000000 - 1
	if rbgRead(t, m, pkeStat, 4) != 0 {
		t.Error("Done one cycle early")
	}
	m.Cycles++
	if rbgRead(t, m, pkeStat, 1) != 1 || m.pkeOperand(aSlot(0), 8).Cmp(ax) != 0 {
		t.Error("Done or the result missing at the time asked for")
	}
	if err := m.RegWrite(pkeStat, 4, 0); err != nil || rbgRead(t, m, pkeStat, 4) != 0 {
		t.Errorf("a write to STAT does not clear Done: %v", err)
	}
	if err := m.RegWrite(pkeCtrl, 4, 1); err != nil {
		t.Fatal(err)
	}
	m.Cycles += 10
	if err := m.RegWrite(pkeCtrl, 4, 0x10000); err != nil {
		t.Fatal(err)
	}
	if rbgRead(t, m, pkeStat, 4) != 1 || rbgRead(t, m, pkeRtCode, 4) != pkeRtStopped {
		t.Error("Stop does not end the operation with STOP_LOG 1")
	}

	// What is not modelled stops the emulation.
	m = pkeMachine(t)
	curve()
	m.pkeStore(bSlot(0), gx, 9)
	m.pkeStore(bSlot(1), gy, 9)
	m.pkeStore(aSlot(4), dA, 9)
	expectErr := func(what, text string, fn func() error) {
		err := fn()
		if err == nil || !strings.Contains(err.Error(), text) {
			t.Errorf("%s: %v", what, err)
		}
	}
	run := func(mc uint32) func() error {
		return func() error { _, err := pkeRun(t, m, mc); return err }
	}
	m.Regs[0x64] &^= 0x80
	expectErr("without the clock", "without its clock", run(pkePMUL))
	m.Regs[0x64] |= 0x80
	m.Regs[0x61] |= 0x80
	expectErr("in reset", "in reset", run(pkePMUL))
	m.Regs[0x61] &^= 0x80
	_ = m.RegWrite(pkeConf, 4, 2<<24|6<<16)
	expectErr("192-bit operands", "radix", run(pkePMUL))
	_ = m.RegWrite(pkeConf, 4, 2<<24|8<<16)
	_ = m.RegWrite(pkeExeConf, 4, 0x2A)
	expectErr("Montgomery operands", "operand form", run(pkePMUL))
	_ = m.RegWrite(pkeExeConf, 4, 0x15)
	expectErr("MODMUL", "microcode 0x18", run(0x18))
	m.pkeStore(aSlot(3), new(big.Int).Xor(r2, big.NewInt(1)), 9)
	expectErr("wrong R^2", "R^2 mod p", run(pkePMUL))
	m.pkeStore(aSlot(3), r2, 9)
	_ = m.RegWrite(pkeMcPtr, 4, pkePMUL)
	if err := m.RegWrite(pkeCtrl, 4, 1); err != nil {
		t.Fatal(err)
	}
	expectErr("while running", "while running", func() error { return m.RegWrite(pkeCtrl, 4, 1) })
}

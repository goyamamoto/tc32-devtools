// SPDX-License-Identifier: Apache-2.0
package tc32emu

// The ADC model: the Go port of checks/adc_check.py, the same cases and the
// same digests.

import (
	"encoding/binary"
	"math"
	"testing"
)

var adcDigests = map[string]string{
	"noise:0":       "f39668f34e48954261e54f075e63acdd58c586ad3614b641fb3ebea7258247b8",
	"noise:0x0705":  "49ddb0545586e3801bb99555ddd93e03df8f3787a57a03902297b4e16255610e",
	"off:0":         "07b257c68af081ace0683be5705c4a6aa8334c38652412184434348c22c37f64",
	"stuck:0":       "78459bb79475b8bdad8f407c1c9b428aa63b3f2a812470e31f90200d295ecf26",
	"biased:0":      "4062d5b703c991556d2663c3efa46fa12b3d0e2a454759c779bd2a26ccf8c654",
	"ramp:4":        "29dd47c52318dd68f273e99af8290af437f88e55947b8553bdc4ca65d225dfbd",
	"two:3":         "3a3bed0fd0e3e7174d2a8dcfa515c4e295dff4d5c97a60e91e8413e1b17bceee",
	"attacker:4660": "ba3c7835e44eb5e04df37807ab3a4179f4e38c7152ae448448d081e7f0c43a74",
	"level:0x800":   "32628d239eccabaa2186c444bcdbf4a4da58816be3767565321202fcfb58c772",
}

const (
	adcBuf    = 0x848000 // the DMA buffer: 32 bytes, 8 slots
	adcSlots  = 8
	adcPeriod = 500 // cycles at the 24 MHz boot clock: r_max_mc 240 + r_max_s 10 at 12 MHz
)

func adcMachine(t *testing.T, mode int, param uint32) *Machine {
	m := rbgMachine(t)
	if mode >= 0 {
		if err := m.RegWrite(0xFFE8, 4, uint32(mode)|param<<8); err != nil {
			t.Fatal(err)
		}
	}
	return m
}

func adcSlot(m *Machine, i int) uint32 {
	o := adcBuf - SRAMBase + 4*i
	return binary.LittleEndian.Uint32(m.SRAM[o : o+4])
}

func adcClear(m *Machine) {
	o := adcBuf - SRAMBase
	for i := 0; i < 4*adcSlots; i++ {
		m.SRAM[o+i] = 0
	}
}

func adcUpdate(t *testing.T, m *Machine) {
	if err := m.UpdateTime(); err != nil {
		t.Fatal(err)
	}
}

// adcStart is the SDK's VBAT channel set-up (or a pin), the DMA on adcBuf, enabled.
func adcStart(t *testing.T, m *Machine, vbat bool) {
	m.Analog[0xEB] = 0x4F
	if vbat {
		m.Analog[0xEB] = 0xFF
	}
	m.Analog[0xEF], m.Analog[0xF1], m.Analog[0xFC] = 0xF0, 0x0A, 0x00
	adcClear(m)
	m.Regs[0xB08], m.Regs[0xB09], m.Regs[0xB0A] = adcBuf&0xFF, (adcBuf>>8)&0xFF, 1
	m.Regs[0xB10] |= 0x04
	adcUpdate(t, m)
}

// adcCodes is n conversions, one period at a time: the slot each lands in.
func adcCodes(t *testing.T, m *Machine, n int) []uint32 {
	out := make([]uint32, n)
	for i := range out {
		m.Cycles += adcPeriod
		adcUpdate(t, m)
		out[i] = adcSlot(m, int((m.adcDone-1)%adcSlots))
	}
	return out
}

func TestADCTiming(t *testing.T) {
	code := uint32(ADCVBATCode)
	m := adcMachine(t, -1, 0)
	adcStart(t, m, false)
	for i := 0; i < adcSlots; i++ {
		if adcSlot(m, i) != m.ADCCode {
			t.Fatalf("pin input: slot %d is %#x, not adc_code", i, adcSlot(m, i))
		}
	}
	m = adcMachine(t, 1, 0)
	adcStart(t, m, true)
	if adcSlot(m, 0) != 0 {
		t.Fatal("VBAT: a conversion at once")
	}
	m.Cycles += adcPeriod - 1
	adcUpdate(t, m)
	if adcSlot(m, 0) != 0 {
		t.Fatal("VBAT: a conversion before the first period")
	}
	m.Cycles++
	adcUpdate(t, m)
	if adcSlot(m, 0) != code || adcSlot(m, 1) != 0 {
		t.Fatal("VBAT: the first conversion is not in slot 0 after one period")
	}
	adcClear(m)
	m.Cycles += adcPeriod * (adcSlots + 2)
	adcUpdate(t, m)
	if m.adcDone != adcSlots+3 {
		t.Fatalf("VBAT: %d conversions after 11 periods", m.adcDone)
	}
	for i := 0; i < adcSlots; i++ {
		if adcSlot(m, i) != code {
			t.Fatalf("VBAT: the ring did not go round (slot %d)", i)
		}
	}
	m.Cycles += adcPeriod * 1000
	adcUpdate(t, m)
	if m.adcDone != adcSlots+1003 {
		t.Fatalf("VBAT: %d conversions after a jump of 1000 periods", m.adcDone)
	}
	m.Regs[0xB10] &^= 0x04
	adcUpdate(t, m)
	if m.adcOn {
		t.Fatal("VBAT: the DMA off does not end the run")
	}
	adcClear(m)
	m.Regs[0xB10] |= 0x04
	adcUpdate(t, m)
	m.Cycles += adcPeriod
	adcUpdate(t, m)
	if adcSlot(m, 0) != code || adcSlot(m, 1) != 0 {
		t.Fatal("VBAT: enabled again, the next conversion is not in slot 0")
	}
	m = adcMachine(t, 1, 0)
	adcStart(t, m, true)
	m.Analog[0xFC] = 0x20
	m.Cycles += adcPeriod * 4
	adcUpdate(t, m)
	if adcSlot(m, 0) != 0 {
		t.Fatal("VBAT: a conversion while powered down")
	}
	m = adcMachine(t, 7, 0)
	adcStart(t, m, true)
	m.Cycles += adcPeriod * 4
	adcUpdate(t, m)
	if adcSlot(m, 0) != 0 {
		t.Fatal("never: a conversion arrived")
	}
	m = adcMachine(t, 1, 0)
	m.setCPUHz(48_000_000)
	adcStart(t, m, true)
	m.Cycles += 2*adcPeriod - 1
	adcUpdate(t, m)
	if adcSlot(m, 0) != 0 {
		t.Fatal("VBAT at 48 MHz: a conversion before 1000 cycles")
	}
	m.Cycles++
	adcUpdate(t, m)
	if adcSlot(m, 0) != code {
		t.Fatal("VBAT at 48 MHz: no conversion after 1000 cycles")
	}
}

func adcStats(cs []uint32) (mean, sd float64) {
	for _, c := range cs {
		mean += float64(c)
	}
	mean /= float64(len(cs))
	for _, c := range cs {
		sd += (float64(c) - mean) * (float64(c) - mean)
	}
	return mean, math.Sqrt(sd / float64(len(cs)))
}

func TestADCModes(t *testing.T) {
	code := uint32(ADCVBATCode)
	for _, c := range []struct {
		name  string
		mode  int
		param uint32
	}{{"noise:0", 0, 0}, {"noise:0x0705", 0, 0x0705}, {"off:0", 1, 0}, {"stuck:0", 2, 0},
		{"biased:0", 3, 0}, {"ramp:4", 4, 4}, {"two:3", 5, 3}, {"attacker:4660", 6, 4660},
		{"level:0x800", 8, 0x800}} {
		m := adcMachine(t, c.mode, c.param)
		adcStart(t, m, true)
		cs := adcCodes(t, m, 64)
		if d := digestU32(cs); d != adcDigests[c.name] {
			t.Errorf("%s: digest %s, the Python check's is %s", c.name, d, adcDigests[c.name])
		}
		more := append(append([]uint32{}, cs...), adcCodes(t, m, 4096-64)...)
		mean, sd := adcStats(more)
		switch c.name {
		case "noise:0":
			if math.Abs(sd-2.0) >= 0.1 || math.Abs(mean-float64(code)) >= 0.15 {
				t.Errorf("noise 2.0 LSB: %.3f LSB rms around %.2f", sd, mean)
			}
		case "noise:0x0705":
			if sd <= 0.45 || sd >= 0.65 {
				t.Errorf("noise 0.5 LSB: %.3f LSB rms", sd)
			}
		case "level:0x800":
			if math.Abs(sd-2.0) >= 0.1 || math.Abs(mean-0x800) >= 0.15 {
				t.Errorf("level 0x800: %.3f LSB rms around %.2f", sd, mean)
			}
		case "off:0", "stuck:0":
			want := code
			if c.mode == 2 {
				want = 0x1555
			}
			for _, v := range more {
				if v != want {
					t.Fatalf("%s: %#x", c.name, v)
				}
			}
		case "biased:0":
			same := 0
			for _, v := range more {
				if v == code {
					same++
				}
			}
			if f := float64(same) / float64(len(more)); f <= 0.93 || f >= 0.99 {
				t.Errorf("biased: %.3f of the codes are the code alone", f)
			}
		case "ramp:4":
			for i := 1; i < 2048; i++ {
				if d := int64(more[i]) - int64(more[i-1]); d != 0 && d != 1 {
					t.Fatalf("ramp: %d after %d", more[i], more[i-1])
				}
			}
			m2 := adcMachine(t, 4, 1)
			adcStart(t, m2, true)
			wrap := adcCodes(t, m2, 0x1FFF-int(code)+2)
			if wrap[len(wrap)-2] != 0x1FFF || wrap[len(wrap)-1] != 1 {
				t.Errorf("ramp: %v at the wrap, not 0x1fff then 1", wrap[len(wrap)-2:])
			}
		case "two:3":
			for _, v := range more {
				if v != code && v != code+3 {
					t.Fatalf("two: %#x", v)
				}
			}
		case "attacker:4660":
			seen := map[uint32]bool{}
			for _, v := range cs {
				seen[v] = true
			}
			for _, v := range more {
				if v < code-32 || v > code+31 {
					t.Fatalf("attacker: %#x out of range", v)
				}
			}
			if len(seen) <= 30 {
				t.Errorf("attacker: %d different codes in 64", len(seen))
			}
		}
		if err := m.RegWrite(0xFFE8, 4, uint32(c.mode)|c.param<<8); err != nil {
			t.Fatal(err)
		}
		m.Regs[0xB10] &^= 0x04
		adcUpdate(t, m)
		adcStart(t, m, true)
		again := adcCodes(t, m, 64)
		for i := range again {
			if again[i] != cs[i] {
				t.Fatalf("%s: setting it again does not start its sequence afresh", c.name)
			}
		}
	}
	if err := adcMachine(t, -1, 0).RegWrite(0xFFE8, 4, 9); err == nil {
		t.Error("an unknown mode is not refused")
	}
}

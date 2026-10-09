// SPDX-License-Identifier: Apache-2.0
package tc32emu

// The ADC on the VBAT input and its DMA (DFIFO2): the Go port of the Python
// ADC_MODES, adc_set(), adc_noise(), adc_next() and dfifo2_fill(). The same
// splitmix64 sequences and integer arithmetic, so both engines give the same
// codes at the same cycles (checks/adc_check.py and adc_test.go hold the same
// digests).

import (
	"encoding/binary"
	"os"
	"strconv"
	"strings"
)

// ADCModes are the VBAT conversions TC32EMU_ADC names and register 0xffe8 numbers.
var ADCModes = []string{"noise", "off", "stuck", "biased", "ramp", "two", "attacker", "never", "level"}

// ADCMode and ADCParam are TC32EMU_ADC (name[:parameter]; default noise).
var ADCMode, ADCParam = adcFromEnv()

// ADCStateHz is the clock the ADC's states count (TC32EMU_ADC_STATE_HZ):
// 12 MHz. DS-TLSR8278 12.3 gives 24 MHz, but on a TLSR8278 with r_max_mc 240 +
// r_max_s 10 the codes of VBAT showed a keyboard backlight's 500 us column slots 24
// codes apart: a conversion every 20.8 us (48 kHz), not 10.4 us. 24000000
// gives the datasheet's rate.
var ADCStateHz = adcStateHzFromEnv()

func adcStateHzFromEnv() int64 {
	v := os.Getenv("TC32EMU_ADC_STATE_HZ")
	if v == "" {
		return 12_000_000
	}
	hz, err := strconv.ParseInt(v, 0, 64)
	if err != nil || hz <= 0 {
		panic("TC32EMU_ADC_STATE_HZ: " + v)
	}
	return hz
}

const (
	// ADCVBATCode is 3.3 V through the 1/3 divider against the 1175 mV
	// reference: 3300 / 3 / 1175 * 8192.
	ADCVBATCode = 0x1DF5
	adcSDx10    = 378372 // the sum of four uniforms' standard deviation, in tenths
)

func adcFromEnv() (int, uint32) {
	v := os.Getenv("TC32EMU_ADC")
	if v == "" {
		return 0, 0
	}
	name, param, _ := strings.Cut(v, ":")
	mode := -1
	for i, n := range ADCModes {
		if n == name {
			mode = i
		}
	}
	if mode < 0 {
		panic("TC32EMU_ADC: " + name)
	}
	if param == "" {
		return mode, 0
	}
	p, err := strconv.ParseInt(param, 0, 64)
	if err != nil {
		panic("TC32EMU_ADC: " + v)
	}
	return mode, uint32(p) & 0xFFFFFF
}

// floorDiv is Python's // for a positive divisor.
func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && a < 0 {
		q--
	}
	return q
}

func (m *Machine) adcSet(mode int, param uint32) {
	m.adcMode, m.adcParam, m.adcState, m.adcIndex = mode, param, uint64(param), 0
}

// adcNoise is noise of sigmaX10 / 10 LSB rms: a sum of four uniform 16-bit
// values, rounded.
func (m *Machine) adcNoise(sigmaX10 int64) int64 {
	var z uint64
	m.adcState, z = splitmix64(m.adcState)
	s := int64(z&0xFFFF) + int64((z>>16)&0xFFFF) + int64((z>>32)&0xFFFF) + int64(z>>48) - 131070
	return floorDiv(2*s*sigmaX10+adcSDx10, 2*adcSDx10)
}

// adcNext is the next VBAT conversion's code (13 bits; ADCModes).
func (m *Machine) adcNext() uint32 {
	p, code := int64(m.adcParam), int64(ADCVBATCode)
	k := m.adcIndex
	m.adcIndex++
	or := func(v, d int64) int64 {
		if v == 0 {
			return d
		}
		return v
	}
	switch m.adcMode {
	case 1:
		return uint32(code)
	case 2:
		return uint32(or(p&0x1FFF, 0x1555))
	case 4:
		return uint32((code+k/or(p, 1)-1)%0x1FFF + 1)
	case 0, 8:
		var v int64
		if m.adcMode == 8 {
			v = or(p&0x1FFF, code) + m.adcNoise(20)
		} else {
			v = code + m.adcNoise(or(p&0xFF, 20))
		}
		if v < 0 {
			v = 0
		}
		if v > 0x1FFF {
			v = 0x1FFF
		}
		return uint32(v)
	}
	var z uint64
	m.adcState, z = splitmix64(m.adcState)
	switch m.adcMode {
	case 3:
		if int64(z>>56) < or(p, 16) {
			return uint32(code + m.adcNoise(20))
		}
		return uint32(code)
	case 5:
		return uint32((code + or(p, 1)*int64(z>>63)) & 0x1FFF)
	}
	return uint32(code + int64(z&63) - 32)
}

// dfifo2Fill is the ADC's DMA (DFIFO2, the misc channel) while enabled
// (0xb10 bit 2): its buffer is 0xb08 (address), 0xb0b (high byte) and 0xb0a
// (size in 16-byte units - 1). A pin input keeps it full of ADCCode at once;
// the VBAT input (analog 0xeb bits 7:4 = 0xf) puts each conversion into the
// next 32-bit slot as its time comes (adcNext).
func (m *Machine) dfifo2Fill() {
	a := &m.Analog
	hi := int(m.Regs[0xB0B])
	if hi == 0 {
		hi = 0x04
	}
	addr := 0x800000 | hi<<16 | int(m.Regs[0xB08]) | int(m.Regs[0xB09])<<8
	size := (int(m.Regs[0xB0A]) + 1) * 16
	o := addr - SRAMBase
	if o < 0 || o+size > SRAMSize {
		return
	}
	if a[0xEB]>>4 != 0xF {
		m.adcOn = false
		var sample [4]byte
		code, ok := m.adcPinCodes[int(a[0xEB]>>4)]
		if !ok {
			code = m.ADCCode
		}
		binary.LittleEndian.PutUint32(sample[:], code)
		for i := 0; i+4 <= size; i += 4 {
			copy(m.SRAM[o+i:o+i+4], sample[:])
		}
		return
	}
	if !m.adcOn {
		states := (int64(a[0xEF]) | int64(a[0xF1]>>6)<<8) + int64(a[0xF1]&0x0F)
		if states == 0 {
			states = 250
		}
		m.adcOn, m.adcT0, m.adcDone = true, m.Cycles, 0
		m.adcPeriod = states * m.CPUHz / ADCStateHz
		if m.adcPeriod < 1 {
			m.adcPeriod = 1
		}
	}
	due := (m.Cycles - m.adcT0) / m.adcPeriod
	if m.adcMode == 7 || a[0xFC]&0x20 != 0 {
		m.adcDone = due
		return
	}
	slots := int64(size / 4)
	if due-m.adcDone > slots {
		m.adcDone = due - slots
	}
	for m.adcDone < due {
		i := o + 4*int(m.adcDone%slots)
		binary.LittleEndian.PutUint32(m.SRAM[i:i+4], m.adcNext())
		m.adcDone++
	}
}

// adcPinControl handles the emulator-only register 0xffe4 (ADC_PIN_CODE in
// emulator/tc32emu.py): bits 23:16 a pin input (1-10), bits 15:0 its code;
// bit 24 clear puts every pin input back to ADCCode.
func (m *Machine) adcPinControl(o int, val uint32) error {
	input := int(val>>16) & 0xFF
	switch {
	case val>>24 == 0:
		m.adcPinCodes = nil
	case input >= 1 && input <= 10:
		if m.adcPinCodes == nil {
			m.adcPinCodes = map[int]uint32{}
		}
		m.adcPinCodes[input] = val & 0xFFFF
	default:
		return emuErr("0xffe4: no ADC pin input %d", input)
	}
	m.store(o, 4, val)
	return nil
}

// adcControl handles the emulator-only register 0xffe8 (mode in bits 7:0,
// parameter in bits 31:8).
func (m *Machine) adcControl(o int, val uint32) error {
	if int(val&0xFF) >= len(ADCModes) {
		return emuErr("0xffe8: no ADC mode %d", val&0xFF)
	}
	m.adcSet(int(val&0xFF), val>>8)
	m.event("ADC on VBAT: " + ADCModes[val&0xFF] + ", parameter " + strconv.FormatUint(uint64(val>>8), 10))
	m.store(o, 4, val)
	return nil
}

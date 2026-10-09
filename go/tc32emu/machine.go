// SPDX-License-Identifier: Apache-2.0
package tc32emu

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
)

// SRAMSize is the SRAM's size: 64 KB on the TLSR8278; TC32EMU_SRAM_SIZE in the
// environment gives a part with less (the TLSR8271 has 32 KB, 0x8000), as the
// Python emulator reads it. An access above it is an EmuError, as any access
// outside the memory map is.
var SRAMSize = sramSizeFromEnv()

// FlashMem is the flash model's array: 1 MB, the TLSR8278's. FlashSize is the
// part's size: TC32EMU_FLASH_SIZE in the environment gives a part with less
// (0x80000: 512 KB),
// as the Python emulator reads it. What lies at or above the part's size is
// refused: an XIP access, an SPI read whose byte the firmware takes, a page
// program and an erase there are EmuErrors. The array keeps FlashMem bytes,
// so a check that seeds a byte above the part's size still runs.
const FlashMem = 0x100000

// The flash cache model's geometry: 64 lines of 32 bytes (the Python
// ICACHE_LINES, ICACHE_LINE_SHIFT).
const (
	icacheLines     = 64
	icacheLineShift = 5
)

var FlashSize = sizeFromEnv("TC32EMU_FLASH_SIZE", FlashMem)

// SRAMSeed (TC32EMU_SRAM_SEED, not 0) fills the SRAM at power-on (NewMachine)
// with SRAMFill's bytes, as a real chip's SRAM holds something at power-on
// rather than zeros; a reset keeps the SRAM's contents either way. Unset or 0,
// the SRAM is zeros at power-on. The same as the Python SRAM_SEED.
var SRAMSeed = uint32(sizeFromEnv("TC32EMU_SRAM_SEED", 0))

// SRAMFill is the Python sram_fill(): the power-on contents of an SRAM of
// size bytes for a seed, xorshift32 (Marsaglia's 13, 17, 5), one byte (the
// low one) per step.
func SRAMFill(seed uint32, size int) []byte {
	out := make([]byte, size)
	x := seed
	for i := range out {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		out[i] = byte(x)
	}
	return out
}

func sizeFromEnv(name string, def int) int {
	s := os.Getenv(name)
	if s == "" {
		return def
	}
	v, err := strconv.ParseInt(s, 0, 64)
	if err != nil || v < 0 || (v == 0 && def != 0) {
		panic(name + ": " + s)
	}
	return int(v)
}

// TimerCaptureBelow is the Python TIMER_CAPTURE_BELOW
// (TC32EMU_TIMER_CAPTURE_BELOW): "stop" (the default) makes a capture or count
// that leaves a running Timer0/1's count at or past its capture an error;
// "wrap" models the chip's wait for the 32-bit count to wrap round. A count
// equal to the capture counts as past: an assumption, as in the Python.
var TimerCaptureBelow = timerCaptureBelowFromEnv()

func timerCaptureBelowFromEnv() string {
	v := os.Getenv("TC32EMU_TIMER_CAPTURE_BELOW")
	if v == "" {
		return "stop"
	}
	if v != "stop" && v != "wrap" {
		panic("TC32EMU_TIMER_CAPTURE_BELOW: " + v)
	}
	return v
}

func sramSizeFromEnv() int {
	s := os.Getenv("TC32EMU_SRAM_SIZE")
	if s == "" {
		return 0x10000
	}
	v, err := strconv.ParseInt(s, 0, 64)
	if err != nil || v <= 0 {
		panic("TC32EMU_SRAM_SIZE: " + s)
	}
	return int(v)
}

const (
	SRAMBase        = 0x840000
	RegBase         = 0x800000
	RegSize         = 0x10000
	RegAlias        = 0x1000000 // the register block again (Telink's older SDK writes RF registers there)
	ModeIRQ         = 0x12
	ModeSVC         = 0x13
	CPUHz           = 24_000_000 // the boot clock
	STimerHz        = 16_000_000
	IRQSTimer       = 20
	stimerCtrlReset = 0xC1 // 0x74a: 32 kHz calibration mode 0xc, write mode; timer stopped
	stimerEn        = 0x02 // 0x74a bit 1
	stimerIRQ       = 0x04 // 0x748 bit 2
	m32             = 0xFFFFFFFF
)

// AnalogAccess is one entry of Machine.AnalogLog: Kind 'r' or 'w'.
type AnalogAccess struct {
	Kind      byte
	Addr, Val byte
}

// AnalogWrite is one entry of Machine.AnalogWrites; Who is the pc's symbol.
type AnalogWrite struct {
	Addr, Val byte
	Who       string
}

// EmuError is what the Python emulator raises as EmuError: the run stops.
type EmuError struct{ Msg string }

func (e *EmuError) Error() string { return e.Msg }

func emuErr(format string, a ...any) error { return &EmuError{fmt.Sprintf(format, a...)} }

// ErrWatchdogReset and ErrSoftwareReset are the Python WatchdogReset and
// SoftwareReset exceptions: the caller resets the machine.
var (
	ErrWatchdogReset = errors.New("watchdog reset")
	ErrSoftwareReset = errors.New("software reset")
)

// Machine is the CPU, its memory and the TLSR8278 peripherals.
type Machine struct {
	Flash *Flash
	CPI   int64

	R      [16]uint32
	bank   map[uint32][2]uint32
	SPSR   map[uint32]uint32
	Mode   uint32
	IBit   uint32
	N, Z   uint32
	C, V   uint32
	SRAM   []byte
	Regs   []byte
	Analog [256]byte
	// PadHold is a floating pad's last level per port (GPIO input reads); 0xff after reset.
	PadHold [4]byte

	BootSlot   int
	BootCopy   int
	Cycles     int64
	CPUHz      int64
	msBase     float64
	msBaseCyc  int64
	tickBase   uint32
	tickBaseCy int64
	stimerOn   bool
	StimerCmp  uint32
	irqLatch   uint32
	gpioReq    bool
	lastTick   uint32
	tick2Base  int64
	tmrBase    [2]int64
	tmrOn      [2]bool
	tmrWrap    [2]bool // the next match only after the count wraps (TimerCaptureBelow "wrap")
	wdFed      int64
	mspiAuto   bool
	rng        uint32
	// The random number generator's source and the 32 kHz jitter (rbg.go).
	rbgMode    int
	rbgParam   uint32
	rbgState   uint64
	rbgIndex   int
	rbgCycle   []uint32
	k32SigmaNs int64
	k32Rng     uint64
	k32Count   int64
	k32Next    float64
	k32Stopped *uint32 // the frozen 32 kHz count (0xfff8 bit 31), or nil
	// The public key engine (pke.go): the time per operation, the cycle the
	// operation under way is done at (-1: idle, Done clear), the stop reason
	// it ends with, and the operand RAM words it writes then.
	pkeUs      int64
	pkeDoneAt  int64
	pkeRT      uint32
	pkePending []pkeWrite
	// The ADC's VBAT conversions (adc.go): the setting and its sequence, and
	// the DMA's run (from adcT0, one conversion per adcPeriod cycles, adcDone
	// of them written).
	adcMode   int
	adcParam  uint32
	adcState  uint64
	adcIndex  int64
	adcOn     bool
	adcT0     int64
	adcPeriod int64
	adcDone   int64

	adcPinCodes map[int]uint32 // pin input (1-10) -> its code (0xffe4)

	// The SPI master and the device on its bus (spi.go).
	SpiDev       *SpiDevice
	spiBusyUntil int64
	spiRx        byte
	// The AES block's input and output (aes.go).
	aesIn  []byte
	aesOut []byte
	DivOps int
	// ICacheMiss is the cycles a flash cache miss costs (TC32EMU_ICACHE_MISS;
	// 0: no cache model), ICacheMisses their count; see the Python ICACHE_MISS.
	// LogAnalog turns on the record of the analog port's accesses, which the
	// Python machine always keeps: AnalogLog is its analog_log (every read and
	// write in order; a read's value is what the firmware gets, after the
	// crystal-ready bit of 0x88) and AnalogWrites its analog_writes (with the
	// pc's symbol). Off by default, since a long run makes many. A reset
	// empties both, as the Python's does.
	LogAnalog    bool
	AnalogLog    []AnalogAccess
	AnalogWrites []AnalogWrite
	// PadCpF (TC32EMU_PAD_C_PF, pF; 0: off) gives every pad a capacitance and
	// PadRPct (TC32EMU_PAD_R_PCT, percent) scales the pulls' nominal
	// resistance; see the Python PAD_C_PF.
	PadCpF, PadRPct int64
	padRCSince      map[[2]int]int64
	ICacheMiss      int64
	ICacheMisses    int64
	icacheTags      [icacheLines]int
	ResetCount      int

	Asleep     *uint32 // the system timer value when suspend began, or nil
	sleepSince float64
	k32Wake    *uint32

	XtalReady    bool // analog 0x88 bit 7
	StimerStuck  bool // a system timer that never counts (a fault to test against)
	ADCCode      uint32
	GPIOOenReset byte
	// LevelSources return the interrupt source bits their peripheral's
	// status holds (sources 0-15 are level-triggered).
	LevelSources []func() uint32
	// CoreWakeSources report a core (digital) wake event now, e.g. USB resume.
	CoreWakeSources []func() bool
	Wakes           []Wake
	FloatWakes      map[[2]int]bool
	OnTreti         func(*Machine)

	// Hooks for models on the register bus (the Python usb_model patches
	// reg_read/reg_write/pad_levels and appends to reset_hooks) and for tracing.
	RegReadHook   func(o, size int) (v uint32, handled bool, err error)
	RegWriteHook  func(o, size int, val uint32) (handled bool, err error)
	PadLevelsHook func(port int, lvl, flt byte) (byte, byte)
	ResetHooks    []func()
	TraceStep     func(pc uint32, hw uint16) // after every executed instruction
	OnIrq         func()                     // after TakeIrq
	OnWake        func(status byte)          // when SleepUntil wakes the machine
	// Hooks run before the instruction at their address (the Python hooks
	// dict): a hook may change pc (the instruction is then not run) or return
	// an error (a *StopError ends the run without being a fault).
	Hooks map[uint32]func(*Machine) error
	// Event receives the Python event() texts (reset, clock, timer, DP pull-up, suspend, wake).
	Event func(text string)
	// MemReadHook and MemWriteHook see every Read/Write before the address
	// map (a Python subclass overriding read()/write(), e.g. a register alias
	// at 0x1000000 + offset): handled true takes the access.
	MemReadHook  func(a uint32, size int) (v uint32, handled bool, err error)
	MemWriteHook func(a uint32, size int, val uint32) (handled bool, err error)
	// UnalignedReadHook is a MemReadHook that acts on unaligned reads alone:
	// it sees only a Read whose address is not a multiple of its size, so
	// the aligned ones (every instruction fetch among them) do not pay for
	// the call.
	UnalignedReadHook func(a uint32, size int) (v uint32, handled bool, err error)
	// ChkHook replaces the unaligned-access stop (the Python m.chk): for an
	// access at a with a % size != 0 it returns the address to use instead,
	// or ok false to stop as usual.
	ChkHook func(a uint32, size int) (addr uint32, ok bool)
	// UpdateTimeHook runs after a successful UpdateTime (a Python wrapper of
	// update_time, e.g. the radio's tick); its error stops the run.
	UpdateTimeHook func() error
	// IdleSkipHook adjusts the idle skip Run computes from CyclesToStimer (a
	// Python wrapper of cycles_to_stimer).
	IdleSkipHook func(n int64) int64
	// SysclkHook replaces Sysclk() where a clock select write sets the CPU
	// clock (a Python machine whose sysclk is replaced, e.g. to run the
	// image's clock select at another frequency).
	SysclkHook func() int64
	// OnFeed runs at each watchdog feed (a Python wrapper of feed()).
	OnFeed func()
	// SramCodeEndHook replaces the end of the SRAM code region (a Python
	// machine whose sram_code_end is replaced, e.g. a negative control).
	SramCodeEndHook func() int
	// Symbols (name -> address) serve Symbolize(); set with SetSymbols.
	symAddrs []uint32
	symNames []string

	// IdleSkipped: of Cycles, those Run's idle skip jumped over (the idle
	// loop's share; the Python idle_skipped).
	IdleSkipped int64
}

// StopError is the Python Stop exception: a hook ends the run.
type StopError struct{ Reason string }

func (e *StopError) Error() string { return "stop " + e.Reason }

// Banked returns a mode's banked r13 and r14 (the Python bank[mode]).
func (m *Machine) Banked(mode uint32) [2]uint32 { return m.bank[mode] }

// LatchIrq latches an edge-triggered interrupt source (the Python
// irq_latch |= 1 << src).
func (m *Machine) LatchIrq(src int) { m.irqLatch |= 1 << uint(src) }

// BankedSP returns the saved r13 of the banked modes (the Python bank values).
func (m *Machine) BankedSP() []uint32 {
	return []uint32{m.bank[ModeIRQ][0], m.bank[ModeSVC][0]}
}

// SetSymbols gives Symbolize() its table (name -> address).
func (m *Machine) SetSymbols(syms map[string]uint32) {
	type pair struct {
		a uint32
		n string
	}
	ps := make([]pair, 0, len(syms))
	for n, a := range syms {
		ps = append(ps, pair{a, n})
	}
	sort.Slice(ps, func(i, j int) bool {
		if ps[i].a != ps[j].a {
			return ps[i].a < ps[j].a
		}
		return ps[i].n < ps[j].n
	})
	m.symAddrs, m.symNames = make([]uint32, len(ps)), make([]string, len(ps))
	for i, p := range ps {
		m.symAddrs[i], m.symNames[i] = p.a, p.n
	}
}

// Symbolize is the Python symbolize(): name+0xoff of the last symbol at or
// before addr, or the hex address.
func (m *Machine) Symbolize(addr uint32) string {
	i := sort.Search(len(m.symAddrs), func(k int) bool { return m.symAddrs[k] > addr }) - 1
	if i < 0 {
		return fmt.Sprintf("0x%x", addr)
	}
	return fmt.Sprintf("%s+0x%x", m.symNames[i], addr-m.symAddrs[i])
}

func (m *Machine) event(text string) {
	if m.Event != nil {
		m.Event(text)
	}
}

// Wake records a wake from suspend: the time and the analog 0x44 status.
type Wake struct {
	Ms     float64
	Status byte
}

// NewMachine makes a machine on flash, booting from bootSlot (-1: the first
// bootable slot). It mirrors Machine.__init__ and reset() of the Python
// emulator.
func NewMachine(flash *Flash, bootSlot int, cpi int64) (*Machine, error) {
	m := &Machine{
		Flash: flash, CPI: cpi, XtalReady: true, ADCCode: 0x0D47, GPIOOenReset: 0xFF,
		FloatWakes: map[[2]int]bool{}, rng: 0x12345678,
		ICacheMiss: int64(sizeFromEnv("TC32EMU_ICACHE_MISS", 0)),
		PadCpF:     int64(sizeFromEnv("TC32EMU_PAD_C_PF", 0)), PadRPct: int64(sizeFromEnv("TC32EMU_PAD_R_PCT", 100)),
		padRCSince: map[[2]int]int64{},
	}
	m.LevelSources = []func() uint32{func() uint32 { return uint32(m.Regs[0x623] & 0x07) }}
	flash.machine = m
	// Power-on: analog 0x3a-0x3c, which a reset keeps, are 0x00, 0x00 and 0x0f
	// (DS-TLSR8278 Table 2-2; the B87 SDK's pm.h, DEEP_ANA_REG2).
	m.Analog[0x3C] = 0x0F
	// Power-on: the SRAM's contents (SRAMSeed); a reset keeps them.
	if SRAMSeed != 0 {
		m.SRAM = SRAMFill(SRAMSeed, SRAMSize)
	} else {
		m.SRAM = make([]byte, SRAMSize)
	}
	// The random number generator's source and the 32 kHz jitter: emulator
	// settings, kept across resets.
	m.rbgSet(RBGMode, RBGParam)
	m.adcSet(ADCMode, ADCParam)
	m.pkeUs = PKEUs
	m.k32SigmaNs = K32JitterNs
	m.k32Rng = K32JitterSeed
	if err := m.Reset(bootSlot, false); err != nil {
		return nil, err
	}
	return m, nil
}

// Reset is the Python reset(): the boot ROM's slot choice and copy, the
// register and analog reset values, and a cold CPU. bootSlot -1 picks the
// first slot whose byte 8 is 0x4b.
func (m *Machine) Reset(bootSlot int, causeWd bool) error {
	f := m.Flash.Mem
	if bootSlot < 0 {
		bootSlot = -1
		for _, s := range []int{0x0, 0x20000, 0x40000, 0x80000} {
			if s < m.Flash.Size && f[s+8] == 0x4B {
				bootSlot = s
				break
			}
		}
		if bootSlot < 0 {
			return emuErr("no bootable slot (byte 8 = 0x4b)")
		}
	}
	m.BootSlot = bootSlot
	m.Flash.Abort()
	// A program or erase goes on through a reset (the part does not see it);
	// the clock starts again at 0 and at CPUHz below, so the cycles left are
	// kept, converted to the boot clock. At power-on (NewMachine) the part is
	// idle.
	busyLeft, busyHz := int64(0), int64(CPUHz)
	if m.ResetCount > 0 {
		if m.Flash.Busy() {
			busyLeft = m.Flash.BusyUntil - m.Cycles
		}
		busyHz = m.CPUHz
	}
	m.AnalogLog, m.AnalogWrites = nil, nil
	for i := range m.icacheTags {
		m.icacheTags[i] = -1
	}
	copyLen := int(binary.LittleEndian.Uint32(f[bootSlot+0x0C:])&0xFFFF) * 16
	if copyLen > SRAMSize { // an erased header word asks for 1 MB: no more than the SRAM
		copyLen = SRAMSize
	}
	copy(m.SRAM[:copyLen], f[bootSlot:bootSlot+copyLen])
	var keep [3]byte
	copy(keep[:], m.Analog[0x3A:0x3D])
	m.Regs = make([]byte, RegSize)
	m.Analog = [256]byte{}
	m.adcOn = false
	m.pkeDoneAt, m.pkeRT, m.pkePending = -1, 0, nil
	// 0x35-0x39 are kept in deep sleep only: every reset puts them at their
	// defaults, 0x20, 0, 0, 0 and 0xff (DS-TLSR8278 Table 2-2).
	m.Analog[0x35], m.Analog[0x39] = 0x20, 0xFF
	copy(m.Analog[0x3A:0x3D], keep[:]) // kept across watchdog/software reset
	m.PadHold = [4]byte{0xFF, 0xFF, 0xFF, 0xFF}
	m.Analog[0x7F] = 0x01
	if causeWd {
		m.Regs[0x72] = 1
	}
	m.Regs[0x74A] = stimerCtrlReset
	for _, oen := range []int{0x582, 0x58A, 0x592, 0x59A, 0x5A2} {
		m.Regs[oen] = m.GPIOOenReset
	}
	m.Regs[0x586], m.Regs[0x58E] = 0x1F, 0x3F
	m.Regs[0x596], m.Regs[0x59E] = 0xFF, 0x7B
	if bootSlot != 0 {
		m.Regs[0x63E] = 0x02
	}
	m.R = [16]uint32{}
	m.bank = map[uint32][2]uint32{ModeIRQ: {}, ModeSVC: {}}
	m.SPSR = map[uint32]uint32{ModeIRQ: 0, ModeSVC: 0}
	m.Mode = ModeSVC
	m.IBit = 1
	m.N, m.Z, m.C, m.V = 0, 0, 0, 0
	m.Cycles = 0
	m.IdleSkipped = 0
	m.CPUHz = CPUHz
	m.msBase, m.msBaseCyc = 0, 0
	m.Flash.BusyUntil = m.flashBusyUntil(busyLeft, busyHz)
	m.tickBase, m.tickBaseCy = 0, 0
	m.stimerOn = false
	m.StimerCmp = 0
	m.irqLatch = 0
	m.gpioReq = false
	m.lastTick = 0
	m.tick2Base = 0
	m.tmrOn = [2]bool{}
	m.tmrBase = [2]int64{}
	m.tmrWrap = [2]bool{}
	m.wdFed = 0
	m.mspiAuto = false
	m.spiBusyUntil, m.spiRx = 0, 0
	if m.SpiDev != nil {
		m.SpiDev.selected = false
	}
	m.BootCopy = copyLen
	m.Asleep = nil
	m.sleepSince = 0
	m.k32Wake = nil
	m.k32Count, m.k32Next = 0, k32PeriodMs
	m.aesIn, m.aesOut = nil, nil
	m.ResetCount++
	for _, f := range m.ResetHooks {
		f()
	}
	wd := ""
	if causeWd {
		wd = " (watchdog)"
	}
	m.event(fmt.Sprintf("reset #%d: boot slot 0x%05x%s", m.ResetCount, bootSlot, wd))
	return nil
}

// ------------------------------------------------------------ time

// Ms is the simulated time in milliseconds.
func (m *Machine) Ms() float64 {
	return m.msBase + float64((m.Cycles-m.msBaseCyc)*1000)/float64(m.CPUHz)
}

// Sysclk decodes reg_clk_sel (0x66) and 0x70[0] (DS-TLSR8278 4.2).
func (m *Machine) Sysclk() int64 {
	sel := int64(m.Regs[0x66])
	fhsSel := (int64(m.Regs[0x70]&1) << 1) | (sel >> 7)
	fhs := int64(48_000_000)
	if fhsSel != 0 {
		fhs = 24_000_000
	}
	switch (sel >> 5) & 3 {
	case 0:
		return 24_000_000
	case 1:
		return fhs
	case 2:
		d := sel & 0x1F
		if d < 2 {
			d = 2
		}
		return fhs / d
	}
	return 32_000_000
}

func (m *Machine) setCPUHz(hz int64) {
	if hz == m.CPUHz {
		return
	}
	nowTick := m.stimerRaw()
	m.msBase, m.msBaseCyc = m.Ms(), m.Cycles
	m.tickBase, m.tickBaseCy = nowTick, m.Cycles
	busyLeft, oldHz := int64(0), m.CPUHz
	if m.Flash.Busy() {
		busyLeft = m.Flash.BusyUntil - m.Cycles
	}
	m.CPUHz = hz
	m.Flash.BusyUntil = m.flashBusyUntil(busyLeft, oldHz)
	m.event(fmt.Sprintf("CPU clock %d MHz", hz/1_000_000))
}

// flashBusyUntil is the Python flash_busy_until(): the flash's busy deadline
// (Flash.BusyUntil) for a busy time of left cycles at hz, on this clock: the
// same time in cycles of CPUHz, rounded up to a whole cycle; 0 when nothing
// is left. A clock change and a reset convert what is left this way; sleep
// and the idle skip advance the cycles, so the time goes on through them.
func (m *Machine) flashBusyUntil(left, hz int64) int64 {
	if left <= 0 {
		return 0
	}
	return m.Cycles + (left*m.CPUHz+hz-1)/hz
}

func (m *Machine) stimerRaw() uint32 {
	if !m.stimerOn || m.StimerStuck {
		return m.tickBase
	}
	return uint32((int64(m.tickBase) + (m.Cycles-m.tickBaseCy)*STimerHz/m.CPUHz) & m32)
}

// StimerNow is the system timer count as read (bits 2:0 read 0).
func (m *Machine) StimerNow() uint32 { return m.stimerRaw() &^ 7 }

func (m *Machine) k32Now() uint32 {
	if m.k32Stopped != nil {
		return *m.k32Stopped
	}
	if m.k32SigmaNs == 0 {
		return uint32(int64(m.Ms()*32.768) & m32)
	}
	return m.k32Jittered()
}

func (m *Machine) tick2Now() uint32 { return uint32((m.Cycles - m.tick2Base) & m32) }

// UpdateTime advances the peripherals to the current cycle: the ADC DMA,
// the system timer compare, Timer0/1 and the watchdog (ErrWatchdogReset).
func (m *Machine) UpdateTime() error {
	if m.Regs[0xB10]&0x04 != 0 {
		m.dfifo2Fill()
	} else {
		m.adcOn = false
	}
	now := m.StimerNow()
	if m.stimerOn && m.Regs[0x748]&stimerIRQ != 0 {
		// Compare reached between the last check and now (wrapping arithmetic).
		if (now-m.StimerCmp)&m32 < 0x80000000 && (m.lastTick-m.StimerCmp)&m32 >= 0x80000000 {
			m.irqLatch |= 1 << IRQSTimer
		}
	}
	m.lastTick = now
	m.timersUpdate()
	ctrl := binary.LittleEndian.Uint32(m.Regs[0x620:])
	if ctrl&(1<<23) != 0 && ctrl&(1<<6) != 0 {
		capture := (ctrl >> 9) & 0x3FFF
		if capture != 0 && (m.tick2Now()>>18) >= capture {
			return ErrWatchdogReset
		}
	}
	if m.UpdateTimeHook != nil {
		return m.UpdateTimeHook()
	}
	return nil
}

func (m *Machine) tmrEnabled(n int) bool {
	ctrl := m.Regs[0x620]
	enBit, sh := byte(0x01), uint(1)
	if n == 1 {
		enBit, sh = 0x08, 4
	}
	return ctrl&enBit != 0 && (ctrl>>sh)&3 == 0
}

func (m *Machine) tmrTick(n int) uint32 {
	if !m.tmrOn[n] {
		return binary.LittleEndian.Uint32(m.Regs[0x630+4*n:])
	}
	return uint32((m.Cycles - m.tmrBase[n]) & m32)
}

// timersUpdate: Timer0/1 in mode 0 (DS-TLSR8278 5.1.2) count the system
// clock; at the capture the status bit (0x623) is set and the tick starts
// again from 0, as the SDK's periodic use needs. A tick past the capture
// reaches it only after the 32-bit wrap (TimerCaptureBelow). A write to
// Timer0/1's control, capture or count, and a read of 0x620-0x637, call this
// first, so a match the count made since the last update is taken before the
// write is judged and is in what the read returns. Two things follow that
// the chip does as well: a 32-bit write of 0x620 that clears the status
// (write one to clear) a few cycles after a match no update had seen clears
// that match (the status was set at the match, the word write clears it);
// and a capture raised a few cycles after such a match reports that match.
func (m *Machine) timersUpdate() {
	for n := 0; n < 2; n++ {
		if !m.tmrOn[n] {
			continue
		}
		cap := int64(binary.LittleEndian.Uint32(m.Regs[0x624+4*n:]))
		if cap == 0 {
			continue
		}
		elapsed := m.Cycles - m.tmrBase[n]
		if m.tmrWrap[n] {
			// The count wraps round to 0 and counts up to the capture as from
			// a start; a capture written after the wrap is judged against the
			// wrapped count.
			if elapsed < 1<<32 {
				continue
			}
			m.tmrWrap[n] = false
			m.tmrBase[n] += 1 << 32
			elapsed -= 1 << 32
			m.event(fmt.Sprintf("timer %d: the count wrapped round to 0 (capture 0x%x)", n, cap))
		}
		if elapsed >= cap {
			m.Regs[0x623] |= 1 << uint(n)
			m.tmrBase[n] += (elapsed / cap) * cap
		}
	}
}

// tmrCheckPast is the Python tmr_check_past: after a capture or count write,
// or a start, a running count at or past a non-zero capture waits for the wrap.
func (m *Machine) tmrCheckPast(n int, what string) error {
	cap := binary.LittleEndian.Uint32(m.Regs[0x624+4*n:])
	if m.tmrOn[n] {
		// The count is 32 bits: fold the base to it, so a timer that ran 2^32
		// cycles or more with capture 0 (never updated) counts on from its
		// 32-bit count, not from the cycles since its start.
		m.tmrBase[n] = m.Cycles - int64(m.tmrTick(n))
	}
	if !m.tmrOn[n] || cap == 0 {
		m.tmrWrap[n] = false
		return nil
	}
	tick := m.tmrTick(n)
	if tick < cap {
		m.tmrWrap[n] = false
		return nil
	}
	waitS := float64((uint64(1)<<32)-uint64(tick)+uint64(cap)) / float64(m.CPUHz)
	text := fmt.Sprintf("timer %d: %s leaves the count 0x%x at or past the capture 0x%x: the chip matches on equality, so the next match comes when the count has wrapped, %.1f s on, at %s",
		n, what, tick, cap, waitS, m.Symbolize(m.R[15]))
	if TimerCaptureBelow == "stop" {
		return emuErr("%s", text)
	}
	m.tmrWrap[n] = true
	m.event(text)
	return nil
}

func (m *Machine) tmrCtrlWritten() error {
	for n := 0; n < 2; n++ {
		on := m.tmrEnabled(n)
		if on && !m.tmrOn[n] {
			start := int64(binary.LittleEndian.Uint32(m.Regs[0x630+4*n:]))
			m.tmrBase[n] = m.Cycles - start
			m.tmrOn[n] = true
			if err := m.tmrCheckPast(n, "a start"); err != nil {
				return err
			}
		} else if !on && m.tmrOn[n] {
			binary.LittleEndian.PutUint32(m.Regs[0x630+4*n:], m.tmrTick(n))
			m.tmrOn[n] = false
			m.tmrWrap[n] = false
		}
	}
	return nil
}

// CyclesToStimer is the number of cycles until the system timer reaches target.
func (m *Machine) CyclesToStimer(target uint32) int64 {
	if !m.stimerOn {
		return 0
	}
	delta := (target - m.StimerNow()) & m32
	if delta >= 0x80000000 {
		return 0
	}
	return int64(delta)*m.CPUHz/STimerHz + 1
}

// CyclesToTimerMatch is the Python cycles_to_timer_match: the cycles until
// the first match of a running Timer0/1 whose interrupt is unmasked (0x640
// bit 0/1), and false when there is none. A timer waiting for its wrap
// matches after the wrap.
func (m *Machine) CyclesToTimerMatch() (int64, bool) {
	var best int64
	found := false
	for n := 0; n < 2; n++ {
		if !m.tmrOn[n] || m.Regs[0x640]&(1<<uint(n)) == 0 {
			continue
		}
		cap := int64(binary.LittleEndian.Uint32(m.Regs[0x624+4*n:]))
		if cap == 0 {
			continue
		}
		left := m.tmrBase[n] + cap - m.Cycles
		if m.tmrWrap[n] {
			left += 1 << 32
		}
		if left < 0 {
			left = 0
		}
		if !found || left < best {
			best, found = left, true
		}
	}
	return best, found
}

// ------------------------------------------------------------ memory

func (m *Machine) sramCodeEnd() int {
	if m.SramCodeEndHook != nil {
		return m.SramCodeEndHook()
	}
	e := int(m.Regs[0x60C]) << 8
	if m.BootCopy > e {
		return m.BootCopy
	}
	return e
}

// xip is the Python xip(): the flash address an XIP address reads. Booted
// from a slot S other than 0, an address below S reads the running image
// (S + address), one at or above 2 x S the flash at that address (the
// calibration sector, read by pointer in every image), and what lies between
// is documented nowhere, so an access there is an error.
func (m *Machine) xip(a int) (int, error) {
	s := m.BootSlot
	if a < s {
		return a + s, nil
	}
	if a < 2*s {
		return 0, emuErr("XIP access 0x%x with boot slot 0x%x: what 0x%x-0x%x reads then is not documented, at %s", a, s, s, 2*s-1, m.Symbolize(m.R[15]))
	}
	return a, nil
}

func leInt(b []byte) uint32 {
	switch len(b) {
	case 4:
		return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
	case 2:
		return uint32(b[0]) | uint32(b[1])<<8
	case 1:
		return uint32(b[0])
	}
	var v uint32
	for i := len(b) - 1; i >= 0; i-- {
		v = v<<8 | uint32(b[i])
	}
	return v
}

func clip(b []byte, o, size int) []byte {
	if o >= len(b) {
		return nil
	}
	if o+size > len(b) {
		return b[o:]
	}
	return b[o : o+size]
}

// Read reads size (1, 2 or 4) bytes at address a.
func (m *Machine) Read(a uint32, size int) (uint32, error) {
	if m.MemReadHook != nil {
		if v, handled, err := m.MemReadHook(a, size); handled || err != nil {
			return v, err
		}
	}
	if m.UnalignedReadHook != nil && a&uint32(size-1) != 0 {
		if v, handled, err := m.UnalignedReadHook(a, size); handled || err != nil {
			return v, err
		}
	}
	if RegAlias <= a && a < RegAlias+RegSize {
		return m.RegRead(int(a-RegAlias), size)
	}
	switch {
	case int(a) < FlashSize:
		if m.Flash.DPD && int(a) >= m.sramCodeEnd() {
			return 0, emuErr("flash read 0x%x while the flash is in deep power-down (0xb9) at %s", a, m.Symbolize(m.R[15]))
		}
		if m.Flash.BusyUntil != 0 && int(a) >= m.sramCodeEnd() && m.Flash.Busy() {
			return 0, emuErr("flash read 0x%x while the flash is busy with a program or erase (WIP, %.0f us left) at %s",
				a, m.Flash.BusyLeftUs(), m.Symbolize(m.R[15]))
		}
		if m.Flash.csLow && m.Flash.SPIBusy() && int(a) >= m.sramCodeEnd() {
			// A command with an address or data phase is underway: the flash cannot serve XIP as well.
			return 0, emuErr("XIP read 0x%x while SPI command 0x%02x is underway (%d bytes sent, chip select low) at %s", a, m.Flash.tx[0], len(m.Flash.tx), m.Symbolize(m.R[15]))
		}
		p, err := m.xip(int(a))
		if err != nil {
			return 0, err
		}
		if p+size > m.Flash.Size {
			return 0, emuErr("XIP read 0x%x past the end of flash (boot slot 0x%x) at %s", a, m.BootSlot, m.Symbolize(m.R[15]))
		}
		if m.ICacheMiss != 0 && int(a) >= m.sramCodeEnd() {
			line, last := int(a)>>icacheLineShift, (int(a)+size-1)>>icacheLineShift
			for ; ; line++ {
				slot := line % icacheLines
				if m.icacheTags[slot] != line {
					m.icacheTags[slot] = line
					m.ICacheMisses++
					m.Cycles += m.ICacheMiss
				}
				if line == last {
					break
				}
			}
		}
		return leInt(m.Flash.Mem[p : p+size]), nil
	case SRAMBase <= a && a < SRAMBase+uint32(SRAMSize):
		o := int(a - SRAMBase)
		if o+size > SRAMSize {
			return 0, emuErr("read%d 0x%08x crosses the end of SRAM at %s", size*8, a, m.Symbolize(m.R[15]))
		}
		return leInt(m.SRAM[o : o+size]), nil
	case RegBase <= a && a < RegBase+RegSize:
		return m.RegRead(int(a-RegBase), size)
	}
	return 0, emuErr("read%d 0x%08x at %s", size*8, a, m.Symbolize(m.R[15]))
}

// Write writes size (1, 2 or 4) bytes at address a.
func (m *Machine) Write(a uint32, size int, val uint32) error {
	if m.MemWriteHook != nil {
		if handled, err := m.MemWriteHook(a, size, val&(1<<(8*uint(size))-1)); handled || err != nil {
			return err
		}
	}
	if RegAlias <= a && a < RegAlias+RegSize {
		return m.RegWrite(int(a-RegAlias), size, val&(1<<(8*uint(size))-1))
	}
	switch {
	case SRAMBase <= a && a < SRAMBase+uint32(SRAMSize):
		o := int(a - SRAMBase)
		if o+size > SRAMSize {
			return emuErr("write%d 0x%08x crosses the end of SRAM at %s", size*8, a, m.Symbolize(m.R[15]))
		}
		for i := 0; i < size; i++ {
			m.SRAM[o+i] = byte(val >> (8 * uint(i)))
		}
		return nil
	case RegBase <= a && a < RegBase+RegSize:
		return m.RegWrite(int(a-RegBase), size, val&(1<<(8*uint(size))-1))
	}
	return emuErr("write%d 0x%08x = 0x%x at %s", size*8, a, val, m.Symbolize(m.R[15]))
}

// ------------------------------------------------------------ registers

// RegRead reads a peripheral register at offset o from 0x800000.
func (m *Machine) RegRead(o, size int) (uint32, error) {
	if m.RegReadHook != nil {
		if v, handled, err := m.RegReadHook(o, size); handled || err != nil {
			return v, err
		}
	}
	r := m.Regs
	if o < 0x638 && o+size > 0x620 {
		// Timer0/1's control, status, captures and counts: the matches the
		// counts made since the last update are in what a read returns.
		m.timersUpdate()
	}
	switch {
	case o == 0x740 && size == 4:
		return m.StimerNow(), nil
	case o == 0x638 && size == 4:
		return m.tick2Now(), nil
	case (o == 0x630 || o == 0x634) && size == 4:
		return m.tmrTick((o - 0x630) / 4), nil
	case 0x648 <= o && o <= 0x64B:
		if err := m.UpdateTime(); err != nil {
			return 0, err
		}
		var val [4]byte
		binary.LittleEndian.PutUint32(val[:], m.IrqSources()&0xFFFFFF)
		return leInt(clip(val[:], o-0x648, size)), nil
	case o == 0x08 && size == 1 && m.SpiDev != nil:
		v := m.spiRx
		if m.spiRunning() && r[0x09]&0x08 != 0 {
			m.spiOctet(-1) // a read clocks the next octet in
		}
		return uint32(v), nil
	case o == 0x09 && size == 1 && m.SpiDev != nil:
		v := r[0x09] &^ 0x40
		if m.Cycles < m.spiBusyUntil {
			v |= 0x40
		}
		return uint32(v), nil
	case o == 0xFFD8 && size == 4:
		if m.SpiDev == nil {
			return 0, nil
		}
		return uint32(m.SpiDev.Transactions&0xFFFF)<<16 | uint32(m.SpiDev.Count&0xFFFF), nil
	case o == 0xFFDC && size == 1:
		if m.SpiDev == nil || len(m.SpiDev.Fifo) == 0 {
			return 0, nil
		}
		v := m.SpiDev.Fifo[0]
		m.SpiDev.Fifo = m.SpiDev.Fifo[1:]
		return uint32(v), nil
	case o == 0x0C && size == 1:
		if m.Flash.Beyond >= 0 {
			if m.Flash.BeyondMode == "stop" {
				return 0, emuErr("flash read of 0x%x: the part has 0x%x bytes at %s", m.Flash.Beyond, m.Flash.Size, m.Symbolize(m.R[15]))
			}
			m.Flash.Log = append(m.Flash.Log, FlashOp{"read beyond", m.Flash.Beyond, 1, ""})
		}
		if m.mspiAuto {
			v := m.Flash.Rx
			rx, err := m.Flash.Clock(0x00)
			if err != nil {
				return 0, err
			}
			m.Flash.Rx = rx
			return uint32(v), nil
		}
		return uint32(m.Flash.Rx), nil
	case o == 0x0D && size == 1:
		return uint32(r[0x0D] &^ 0x10), nil // never busy
	case o == 0xBA && size == 1:
		return uint32(r[0xBA] &^ 0x01), nil // analog port never busy
	case o == pkeStat || o == pkeRtCode || (pkeRAMA <= o && o < pkeRAMEnd):
		// The public key engine: Done, STOP_LOG, and the operand RAM once an
		// operation has ended (pke.go).
		if v, handled := m.pkeRead(o, size); handled {
			return v, nil
		}
	case o == 0x4408 && size == 1:
		if m.rbgReady() {
			return uint32(r[0x4408] | 0x01), nil // a number is ready
		}
		return uint32(r[0x4408] & 0xFE), nil
	case o == 0x440C && size == 4:
		return m.rbgWord(), nil
	case o == 0x548 && size == 4:
		// The AES block's output, a word per read.
		return m.aesRead(), nil
	case o == 0x74B && size == 1:
		if K32Frozen {
			return 0, nil
		}
		return (m.k32Now() & 1) << 5, nil
	case o == 0x750 && size == 4:
		if K32Frozen {
			return 0, nil
		}
		return m.k32Now(), nil
	case o == 0x754 && size == 4:
		// reg_system_32k_tick_cal: system timer ticks per 32 kHz tick, times 16
		// (the B87 SDK's cpu_sleep_wakeup scales by it >> 4); here the exact
		// 16 MHz / 32768 Hz, as the Python model returns.
		return STimerHz * 16 / 32768, nil
	case (o == 0x580 || o == 0x588 || o == 0x590 || o == 0x598) && size == 1:
		// GPIO inputs: the pads (padLevels, so board and USB models apply), a
		// floating pad at its last level, through the input enable (port C's
		// is analog 0xc0).
		port := (o - 0x580) / 8
		v := m.padInputs(port)
		ie := r[o+1]
		if port == 2 {
			ie = m.Analog[0xC0]
		}
		return uint32(v & ie), nil
	}
	return leInt(clip(r, o, size)), nil
}

func (m *Machine) store(o, size int, val uint32) {
	for i := 0; i < size && o+i < len(m.Regs); i++ {
		m.Regs[o+i] = byte(val >> (8 * uint(i)))
	}
}

// RegWrite writes a peripheral register at offset o from 0x800000.
func (m *Machine) RegWrite(o, size int, val uint32) error {
	if m.RegWriteHook != nil {
		if handled, err := m.RegWriteHook(o, size, val); handled || err != nil {
			return err
		}
	}
	r := m.Regs
	switch {
	case (o == 0x540 && size == 1) || (o == 0x548 && size == 4):
		// The AES block: 0x540 starts a block, 0x548 takes the input.
		m.aesWrite(o, val)
		return nil
	case o == 0xFFE4 && size == 4:
		// Emulator-only setting (adc.go): a pin input's code.
		return m.adcPinControl(o, val)
	case o == 0xFFD8 && size == 4:
		// Emulator-only setting (spi.go): a device on the SPI bus, afresh.
		return m.spiAttach(val)
	case o == 0x08 && size == 1 && m.spiRunning():
		if m.Cycles < m.spiBusyUntil {
			return emuErr("SPI: 0x08 written while an octet is going out at %s", m.Symbolize(m.R[15]))
		}
		r[0x08] = byte(val)
		out := int(val & 0xFF)
		if r[0x09]&0x04 != 0 {
			out = -1
		}
		m.spiOctet(out)
		return nil
	case o == 0x09 && size == 1 && m.SpiDev != nil:
		r[0x09] = byte(val) &^ 0x40
		m.spiCsUpdate()
		return nil
	case o == pkeCtrl || o == pkeStat || o == 0xFFEC:
		// The public key engine's control and status, and the emulator-only
		// time per operation (pke.go).
		if handled, err := m.pkeWriteReg(o, size, val); handled {
			return err
		}
	case o == 0xFFE8 && size == 4:
		// Emulator-only setting (adc.go): the ADC's VBAT conversions.
		return m.adcControl(o, val)
	case (o == 0xFFF4 || o == 0xFFF8 || o == 0xFFFC) && size == 4:
		// Emulator-only settings (rbg.go): the random number source, the
		// 32 kHz jitter and its seed.
		return m.rbgControl(o, val)
	case o == 0x740 && size == 4:
		m.tickBase, m.tickBaseCy = val, m.Cycles
		m.lastTick = val &^ 7
		return nil
	case o == 0x744 && size == 4:
		val &^= 7
		m.StimerCmp = val
		m.lastTick = m.StimerNow()
		binary.LittleEndian.PutUint32(r[o:], val)
		return nil
	case o == 0x660 && size == 1:
		// Hardware divider, as the Telink SDK's common/div_mod.S drives it.
		a := int64(binary.LittleEndian.Uint32(r[0x664:]))
		b := int64(binary.LittleEndian.Uint32(r[0x668:]))
		if val&1 != 0 {
			a, b = int64(int32(a)), int64(int32(b))
		}
		var q, rem int64
		if b == 0 {
			q, rem = -1, a
		} else {
			aa, bb := a, b
			if aa < 0 {
				aa = -aa
			}
			if bb < 0 {
				bb = -bb
			}
			q = aa / bb
			if (a < 0) != (b < 0) {
				q = -q
			}
			rem = a - q*b
		}
		binary.LittleEndian.PutUint32(r[0x664:], uint32(q&m32))
		binary.LittleEndian.PutUint32(r[0x668:], uint32(rem&m32))
		r[0x660] = 0
		m.DivOps++
		return nil
	case o == 0x608 && size == 4:
		// Load 16 bytes of flash into SRAM at the same offset (bit 24 starts it).
		if val&(1<<24) != 0 {
			off := int(val & 0xFFFFFF)
			src, err := m.xip(off)
			if err != nil {
				return err
			}
			if off+16 > SRAMSize {
				return emuErr("load of flash into SRAM offset 0x%x, past the end of SRAM at %s", off, m.Symbolize(m.R[15]))
			}
			if src+16 > m.Flash.Size {
				return emuErr("load of flash 0x%x into SRAM: the part has 0x%x bytes at %s", src, m.Flash.Size, m.Symbolize(m.R[15]))
			}
			copy(m.SRAM[off:off+16], m.Flash.Mem[src:src+16])
		}
		binary.LittleEndian.PutUint32(r[o:], val&0x00FFFFFF)
		return nil
	case o == 0x74A && size == 1:
		on := val&stimerEn != 0
		if on != m.stimerOn {
			m.tickBase, m.tickBaseCy = m.stimerRaw(), m.Cycles
			m.stimerOn = on
			m.lastTick = m.StimerNow()
			if on {
				m.event("system timer started")
			} else {
				m.event("system timer stopped")
			}
		}
		r[o] = byte(val)
		return nil
	case o == 0x638 && size == 4:
		m.tick2Base = (m.Cycles - int64(val)) & m32
		return nil
	case 0x648 <= o && o <= 0x64B:
		m.irqLatch &^= val << (8 * uint(o-0x648))
		return nil
	case (o == 0x630 || o == 0x634) && size == 4:
		n := (o - 0x630) / 4
		m.timersUpdate()
		binary.LittleEndian.PutUint32(r[o:], val)
		if m.tmrOn[n] {
			m.tmrBase[n] = m.Cycles - int64(val)
			return m.tmrCheckPast(n, "a count write")
		}
		return nil
	case 0x624 <= o && o < 0x62C:
		// Judged after each write: a capture written a byte at a time, low
		// byte first, can stop the run where the chip reaches the whole word
		// (the SDK and ZMK write it as one word).
		m.timersUpdate()
		m.store(o, size, val)
		first, last := (o-0x624)/4, (o+size-1-0x624)/4
		for n := first; n <= last && n < 2; n++ {
			if err := m.tmrCheckPast(n, "a capture write"); err != nil {
				return err
			}
		}
		return nil
	case o == 0x623 && size == 1:
		r[0x623] &^= byte(val & 0x07) // timer status, write one to clear
		if val&0x08 != 0 {
			m.feed()
		}
		return nil
	case o == 0x620 && (size == 1 || size == 2 || size == 4):
		m.timersUpdate()
		if size == 4 {
			r[0x620], r[0x621], r[0x622] = byte(val), byte(val>>8), byte(val>>16)
			r[0x623] &^= byte((val >> 24) & 0x07)
			if val&(1<<27) != 0 {
				m.feed()
			}
		} else {
			m.store(o, size, val)
		}
		return m.tmrCtrlWritten()
	case o == 0x72 && size == 1:
		r[0x72] &^= byte(val)
		return nil
	case o == 0x6F && size == 1 && val&0x20 != 0:
		return ErrSoftwareReset
	case o == 0x6F && size == 1 && val&0x80 != 0:
		r[o] = byte(val)
		return m.enterSuspend(val)
	}
	if o == 0x74B && size == 1 && val&0x08 != 0 {
		w := binary.LittleEndian.Uint32(r[0x74C:])
		m.k32Wake = &w
	}
	switch {
	case (o == 0x66 || o == 0x70) && size == 1:
		r[o] = byte(val)
		if m.SysclkHook != nil {
			m.setCPUHz(m.SysclkHook())
		} else {
			m.setCPUHz(m.Sysclk())
		}
		return nil
	case o == 0x0D && size == 1:
		if err := m.Flash.Select(val&0x01 == 0); err != nil {
			return err
		}
		m.mspiAuto = val&0x08 != 0
		r[0x0D] = byte(val)
		return nil
	case o == 0x0C && size == 1:
		rx, err := m.Flash.Clock(byte(val))
		if err != nil {
			return err
		}
		m.Flash.Rx = rx
		return nil
	case o == 0xBA && size == 1:
		if val&0x40 != 0 {
			addr := int(r[0xB8])
			if val&0x20 != 0 {
				old := m.Analog[addr]
				if addr == 0x44 {
					m.Analog[addr] = old &^ r[0xB9]
				} else {
					m.Analog[addr] = r[0xB9]
				}
				if addr == 0xC6 || addr == 0xC7 {
					// RC calibration: starting it (bit 0) completes at once;
					// done is analog 0xcf bit 6 (32 kHz) or bit 7 (24 MHz).
					done := byte(0x40)
					if addr == 0xC7 {
						done = 0x80
					}
					if r[0xB9]&1 != 0 {
						m.Analog[0xCF] |= done
					} else {
						m.Analog[0xCF] &^= done
					}
				}
				if 0x0E <= addr && addr <= 0x15 {
					m.holdPads((addr - 0x0E) / 2) // a pull changed
					m.spiCsUpdate()
				}
				if m.LogAnalog {
					m.AnalogWrites = append(m.AnalogWrites, AnalogWrite{byte(addr), r[0xB9], m.Symbolize(m.R[15])})
					m.AnalogLog = append(m.AnalogLog, AnalogAccess{'w', byte(addr), r[0xB9]})
				}
				if addr == 0x0B && (old^r[0xB9])&0x80 != 0 {
					m.event(fmt.Sprintf("analog 0x0b bit 7 (USB DP pull-up) -> %d", r[0xB9]>>7))
				}
				if addr == 0xC0 {
					m.GpioIrqUpdate() // PC input enable
				}
			} else {
				r[0xB9] = m.Analog[addr]
				if addr == 0x88 {
					if m.XtalReady {
						r[0xB9] |= 0x80
					} else {
						r[0xB9] &= 0x7F
					}
				}
				if m.LogAnalog {
					m.AnalogLog = append(m.AnalogLog, AnalogAccess{'r', byte(addr), r[0xB9]})
				}
			}
		}
		r[0xBA] = byte(val)
		return nil
	}
	m.store(o, size, val)
	if o < 0x5A0 && o+size > 0x580 {
		lo, hi := (o-0x580)/8, (o+size-1-0x580)/8
		if lo < 0 {
			lo = 0
		}
		if hi > 3 {
			hi = 3
		}
		for port := lo; port <= hi; port++ {
			m.holdPads(port)
		}
	}
	if (o < 0x5A0 && o+size > 0x580) || (o <= 0x5B5 && 0x5B5 < o+size) {
		m.GpioIrqUpdate()
	}
	if m.SpiDev != nil && o < 0x5B8 && o+size > 0x580 {
		m.spiCsUpdate()
	}
	return nil
}

// holdPads takes the levels of a port's driven or pulled pads into PadHold:
// a pad keeps the level it had when it stops being either.
func (m *Machine) holdPads(port int) {
	if m.PadCpF != 0 {
		// A pad of another port may have been released by this write.
		for p := 0; p < 4; p++ {
			m.padInputs(p)
		}
		return
	}
	m.padInputs(port)
}

var padROhm = [4]int64{0, 1_000_000, 100_000, 10_000}

// padInputs is the Python pad_inputs(): the levels of a port's pads as the
// input buffers see them, with the capacitance model when PadCpF is set.
func (m *Machine) padInputs(port int) byte {
	lvl, flt := m.padLevels(port)
	v := (lvl &^ flt) | (m.PadHold[port] & flt)
	if m.PadCpF != 0 {
		r, b := m.Regs, 0x580+8*port
		for bit := 0; bit < 8; bit++ {
			key, mask := [2]int{port, bit}, byte(1)<<uint(bit)
			pull := (m.Analog[0x0E+2*port+(bit>>2)] >> uint(2*(bit&3))) & 3
			own := (r[b+6]>>uint(bit))&1 != 0 && (r[b+2]>>uint(bit))&1 == 0
			var rest byte
			if pull == 1 || pull == 3 {
				rest = mask
			}
			if flt&mask != 0 || own || pull == 0 || v&mask != rest || m.PadHold[port]&mask == rest {
				delete(m.padRCSince, key)
				continue
			}
			since, ok := m.padRCSince[key]
			if !ok {
				since = m.Cycles
				m.padRCSince[key] = since
			}
			if m.Cycles-since >= m.padRCCycles(pull) {
				delete(m.padRCSince, key)
			} else {
				v = v&^mask | m.PadHold[port]&mask
			}
		}
	}
	m.PadHold[port] = v
	return v
}

// padRCCycles is the Python pad_rc_cycles(): the cycles a pad takes to reach
// its pull's level at the present clock.
func (m *Machine) padRCCycles(pull byte) int64 {
	needNs := 1204 * padROhm[pull] * m.PadCpF * m.PadRPct / 100 / 1_000_000
	return (needNs*m.CPUHz + 999_999_999) / 1_000_000_000
}

// padRCService is the Python pad_rc_service(): pads whose time has passed take
// their pull's level and the GPIO interrupt sees the change.
func (m *Machine) padRCService() {
	before := m.PadHold
	for port := 0; port < 4; port++ {
		m.padInputs(port)
	}
	if m.PadHold != before {
		m.GpioIrqUpdate()
	}
}

// padRCDue is the Python pad_rc_due(): cycles until the first pad on its way
// reaches its pull's level; ok false when none is on its way.
func (m *Machine) padRCDue() (int64, bool) {
	var due int64
	found := false
	for key, since := range m.padRCSince {
		port, bit := key[0], key[1]
		pull := (m.Analog[0x0E+2*port+(bit>>2)] >> uint(2*(bit&3))) & 3
		var left int64
		if pull != 0 {
			left = since + m.padRCCycles(pull) - m.Cycles
		}
		if !found || left < due {
			due, found = left, true
		}
	}
	return due, found
}

// HoldPads is the Python hold_pads(): a board model calls it when what it
// wires to the pads has changed (a key, a knob contact), so that the
// capacitance model's time runs from the change.
func (m *Machine) HoldPads(port int) { m.holdPads(port) }

// GpioIrqUpdate is the Python gpio_irq_update(): the GPIO interrupt request
// is |((input ^ polarity) & irq) over the four ports, enabled by 0x5b5 bit 3;
// a rising request latches source 18. Board models call it after a pin change.
func (m *Machine) GpioIrqUpdate() {
	r := m.Regs
	var req byte
	if r[0x5B5]&0x08 != 0 {
		for p := 0; p < 4; p++ {
			b := 0x580 + 8*p
			in, _ := m.RegRead(b, 1)
			req |= (byte(in) ^ r[b+4]) & r[b+7]
		}
	}
	if req != 0 && !m.gpioReq {
		m.irqLatch |= 1 << 18
	}
	m.gpioReq = req != 0
}

func (m *Machine) feed() {
	if m.OnFeed != nil {
		m.OnFeed()
	}
	// Assumed: clearing the watchdog status (0x623 bit 3) restarts the count.
	m.tick2Base = m.Cycles
	m.wdFed = m.Cycles
}

// ------------------------------------------------------------ IRQ

// IrqSources is the interrupt source word: the latched edge sources and
// the level sources' current status.
func (m *Machine) IrqSources() uint32 {
	var level uint32
	for _, f := range m.LevelSources {
		level |= f()
	}
	return m.irqLatch | (level & 0xFFFF)
}

// IrqPending says whether an interrupt would be taken now.
func (m *Machine) IrqPending() bool {
	r := m.Regs
	if r[0x643]&1 == 0 || m.IBit != 0 {
		return false
	}
	mask := uint32(r[0x640]) | uint32(r[0x641])<<8 | uint32(r[0x642])<<16
	return m.IrqSources()&mask != 0
}

// TakeIrq enters IRQ mode as the TC32 does: SPSR_irq = CPSR, I set, r14 =
// the address of the next instruction, pc = 0x10.
func (m *Machine) TakeIrq() {
	ret := m.R[15]
	cpsr := m.CPSR()
	m.setCPSR((cpsr &^ 0x9F) | 0x80 | ModeIRQ)
	m.SPSR[ModeIRQ] = cpsr
	m.R[14] = ret
	m.R[15] = 0x10
	if m.OnIrq != nil {
		m.OnIrq()
	}
}

// ------------------------------------------------------------ CPSR

// CPSR assembles the status register: NZCV, I and the mode.
func (m *Machine) CPSR() uint32 {
	return m.N<<31 | m.Z<<30 | m.C<<29 | m.V<<28 | m.IBit<<7 | m.Mode
}

func (m *Machine) setCPSR(val uint32) error {
	newMode := val & 0x1F
	if _, ok := m.bank[newMode]; !ok {
		return emuErr("mode 0x%x at %s", newMode, m.Symbolize(m.R[15]))
	}
	if newMode != m.Mode {
		m.bank[m.Mode] = [2]uint32{m.R[13], m.R[14]}
		b := m.bank[newMode]
		m.R[13], m.R[14] = b[0], b[1]
		m.Mode = newMode
	}
	m.N, m.Z, m.C, m.V = (val>>31)&1, (val>>30)&1, (val>>29)&1, (val>>28)&1
	m.IBit = (val >> 7) & 1
	return nil
}

// ------------------------------------------------------------ suspend

// enterSuspend: 0x6f bit 7 (the B87 SDK's sleep_start): the chip stops until
// a wake source enabled in analog 0x26 fires. Only suspend (analog 0x7e = 0)
// is modelled.
func (m *Machine) enterSuspend(val uint32) error {
	if mode := m.Analog[0x7E]; mode != 0 {
		return emuErr("sleep with analog 0x7e = 0x%02x: only suspend (0) is modelled at %s", mode, m.Symbolize(m.R[15]))
	}
	v := m.stimerRaw()
	m.Asleep = &v
	m.sleepSince = m.Ms()
	m.event(fmt.Sprintf("suspend (0x6f = 0x%02x, wake sources analog 0x26 = 0x%02x)", val, m.Analog[0x26]))
	return nil
}

// padLevels returns (levels, floating) of a port's pads as they are
// electrically: a GPIO pad with its output enabled drives its output bit;
// otherwise its pull resistor decides, and with none it floats.
func (m *Machine) padLevels(port int) (byte, byte) {
	lvl, flt := m.padLevelsBase(port)
	if m.PadLevelsHook != nil {
		return m.PadLevelsHook(port, lvl, flt)
	}
	return lvl, flt
}

// PadLevels is padLevels for models outside the package.
func (m *Machine) PadLevels(port int) (byte, byte) { return m.padLevels(port) }

func (m *Machine) padLevelsBase(port int) (byte, byte) {
	r, b := m.Regs, 0x580+8*port
	var lvl, flt byte
	for bit := uint(0); bit < 8; bit++ {
		if (r[b+6]>>bit)&1 != 0 && (r[b+2]>>bit)&1 == 0 {
			lvl |= ((r[b+3] >> bit) & 1) << bit
			continue
		}
		pull := (m.Analog[0x0E+2*port+int(bit>>2)] >> (2 * (bit & 3))) & 3
		if pull == 1 || pull == 3 {
			lvl |= 1 << bit
		} else if pull == 0 {
			flt |= 1 << bit
		}
	}
	return lvl, flt
}

// wakeStatus returns the analog 0x44 bits of the enabled wake sources that
// fire now.
func (m *Machine) wakeStatus() byte {
	src := m.Analog[0x26]
	var st byte
	if src&0x08 != 0 {
		for port := 0; port < 4; port++ {
			en := m.Analog[0x27+port]
			if en == 0 {
				continue
			}
			lvl, flt := m.padLevels(port)
			if en&flt != 0 {
				m.FloatWakes[[2]int{port, int(en & flt)}] = true
			}
			pol := m.Analog[0x21+port]
			if en&^flt&((^lvl&pol)|(lvl&^pol)) != 0 {
				st |= 0x08
			}
		}
	}
	if src&0x10 != 0 {
		for _, f := range m.CoreWakeSources {
			if f() {
				st |= 0x04
				break
			}
		}
	}
	if src&0x20 != 0 && m.k32Wake != nil && (m.k32Now()-*m.k32Wake)&m32 < 0x80000000 {
		st |= 0x02
	}
	return st
}

// SleepUntil advances a suspended machine by one chunk, or wakes it.
func (m *Machine) SleepUntil(maxCycles int64) {
	if st := m.wakeStatus(); st != 0 {
		m.Analog[0x44] |= st
		m.Asleep = nil
		m.Wakes = append(m.Wakes, Wake{m.Ms(), st})
		m.event(fmt.Sprintf("wake: analog 0x44 = 0x%02x after %.1f ms", st, m.Ms()-m.sleepSince))
		if m.OnWake != nil {
			m.OnWake(st)
		}
		return
	}
	chunk := maxCycles - m.Cycles
	if c := m.CPUHz / 1000; c < chunk {
		chunk = c
	}
	if chunk < 1 {
		chunk = 1
	}
	if m.Analog[0x26]&0x20 != 0 && m.k32Wake != nil {
		// The timer wake comes at the 32 kHz tick that reaches the wake
		// value, not at the next check (the Python sleep_until).
		if d := (*m.k32Wake - m.k32Now()) & m32; d < 0x80000000 {
			now := m.Ms()
			need := int64(math.Ceil((float64(int64(now*32.768)+int64(d))/32.768 - now) * float64(m.CPUHz) / 1000))
			if need < chunk {
				chunk = need
			}
			if chunk < 1 {
				chunk = 1
			}
		}
	}
	// The clocks are off: the system timer and Timer0-2 (so the watchdog) stand still.
	m.Cycles += chunk
	m.tickBaseCy += chunk
	m.tick2Base += chunk
	m.wdFed += chunk
	for n := 0; n < 2; n++ {
		if m.tmrOn[n] {
			m.tmrBase[n] += chunk
		}
	}
}

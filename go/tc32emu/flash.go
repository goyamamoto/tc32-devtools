// Package tc32emu is the Go port of ../../emulator/tc32emu.py, the canonical
// TC32 (Telink TLSR825x/827x) instruction-set emulator with the TLSR8278
// peripherals. It follows the Python file method by method, and
// checks/lockstep.sh requires the two to produce the same trace, instruction
// by instruction (emulator/trace.py, cmd/tc32emu-trace). Behaviour changes
// go into the Python file first.
//
// SPDX-License-Identifier: Apache-2.0
package tc32emu

import (
	"fmt"
	"math/bits"
	"os"
)

// Flash is an SPI NOR flash behind the MSPI registers, and the XIP backing
// store.
type Flash struct {
	Mem    []byte // FlashMem bytes, whatever the part's size
	Size   int    // the part's size: what lies at or above it is refused
	Beyond int    // the address of the byte last clocked out if the part does not have it, else -1
	// BeyondMode is what a taken read there gives: "stop" (an error, the
	// default), "ff" or "wrap" (TC32EMU_FLASH_BEYOND; the Python FLASH_BEYOND).
	BeyondMode string
	Jedec      [3]byte
	MID        int // the JEDEC ID read low byte first: (capacity << 16) | (type << 8) | manufacturer
	// SFDP is what 0x5a (3 address bytes, 1 dummy byte) reads at those
	// addresses, 0xff beyond; nil for a part without. Part is the part as the
	// SDK names it: MID, or 0x01000000 | MID for a Zbit C part (a Zbit ID with
	// the SFDP signature 0x53 at 0). SetSFDP keeps them together.
	SFDP []byte
	Part int
	// Status is the status register (16 bits: 0x05 reads the low byte, 0x35
	// the high one), non-volatile as on the part; its protection bits per
	// flashProtection (the Python FLASH_PROTECTION).
	Status   int
	UID      []byte // 0x4B: 3 address bytes and 1 dummy byte, then 16 bytes
	wel      bool
	csLow    bool
	tx       []byte
	Rx       byte
	Commands map[byte]int // command byte -> count, every command the flash was sent
	DPD      bool         // deep power-down (0xb9) until released (0xab): it answers nothing else
	// BusyUntil is when WIP (0x05 bit 0) clears: the machine's cycle count,
	// 0 when idle. machine is whose Cycles and CPUHz it counts (NewMachine
	// sets it; nil: never busy).
	BusyUntil int64
	machine   *Machine
	// Log keeps programs, erases and other writes as (op, addr, len).
	Log []FlashOp
	// OnWrite, when set, is called as a page program or an erase is carried
	// out ("program", "erase") or refused by the block protection
	// ("protected"), with the log entry's values, at that moment of the
	// machine's time (the Python on_write). nil: nothing is called.
	OnWrite func(op string, addr, n int)
}

// FlashOp is one entry of Flash.Log.
type FlashOp struct {
	Op   string
	Addr int
	Len  int
	Data string // status writes: the bytes after the command, hex (the Python log's third field)
}

// NewFlash makes an erased flash: a part of size bytes (FlashSize for the
// environment's) with the default IDs of the Python emulator (a Puya P25Q80).
// The array keeps FlashMem bytes.
func NewFlash(size int) *Flash {
	return NewFlashJedec(size, [3]byte{0x85, 0x60, 0x14})
}

// NewFlashJedec is NewFlash with the part's JEDEC ID (manufacturer, type,
// capacity), the Python Flash(size, jedec): the capacity byte follows the
// size when the ID says 0x14 (0x13 for 512 KB), and MID (which picks the
// protection table) follows the ID. Set the ID here, not on the struct
// afterwards, or MID keeps the default's.
func NewFlashJedec(size int, jedec [3]byte) *Flash {
	mode := os.Getenv("TC32EMU_FLASH_BEYOND")
	if mode == "" {
		mode = "stop"
	}
	if mode != "stop" && mode != "ff" && mode != "wrap" {
		panic("TC32EMU_FLASH_BEYOND: " + mode)
	}
	f := &Flash{Mem: make([]byte, max(size, FlashMem)), Size: size, Beyond: -1, BeyondMode: mode,
		Jedec: jedec, Rx: 0xFF, Commands: map[byte]int{}}
	if size < FlashMem && jedec[2] == 0x14 {
		f.Jedec[2] = byte(bits.Len(uint(size)) - 1)
	}
	f.MID = int(f.Jedec[2])<<16 | int(f.Jedec[1])<<8 | int(f.Jedec[0])
	f.Part = f.MID
	for i := range f.Mem {
		f.Mem[i] = 0xFF
	}
	f.UID = make([]byte, 16)
	for i := range f.UID {
		f.UID[i] = byte(0xA0 + i)
	}
	return f
}

// Select drives chip select: low = selected. Deselecting commits the command,
// which is an error when a program or erase names an address the part does
// not have.
func (f *Flash) Select(low bool) error {
	var err error
	if f.csLow && !low {
		err = f.commit()
	}
	if low && !f.csLow {
		f.tx = f.tx[:0]
	}
	f.csLow = low
	return err
}

// SetSFDP gives the part SFDP data (the Python Flash(sfdp=...)); a Zbit ID
// with the signature 0x53 at 0 is then the C part for the protection table.
func (f *Flash) SetSFDP(data []byte) {
	f.SFDP = data
	f.Part = f.MID
	if (f.MID == 0x13325E || f.MID == 0x14325E) && len(data) > 0 && data[0] == 0x53 {
		f.Part = 0x01000000 | f.MID
	}
}

// SPIBusy is the Python spi_busy(): a transaction that an XIP access would
// cut: more than the command byte sent, or a command whose address/data
// phase is next.
func (f *Flash) SPIBusy() bool {
	if len(f.tx) > 1 {
		return true
	}
	if len(f.tx) == 1 {
		switch f.tx[0] {
		case 0x01, 0x02, 0x03, 0x05, 0x0B, 0x11, 0x20, 0x31, 0x35, 0x4B, 0x52, 0x5A, 0x9F, 0xD8:
			return true
		}
	}
	return false
}

// Abort is the Python abort(): a chip reset takes the chip select high
// without a commit.
func (f *Flash) Abort() {
	f.csLow, f.tx, f.Beyond = false, f.tx[:0], -1
}

// FlashTppUs, FlashTseUs, FlashTbe32Us and FlashTbe64Us are the Python
// FLASH_TPP_US, FLASH_TSE_US, FLASH_TBE32_US and FLASH_TBE64_US
// (TC32EMU_FLASH_TPP_US, _TSE_US, _TBE32_US, _TBE64_US; microseconds, 0: no
// busy time): how long WIP stays set after a page program, a sector erase and
// a 32 KB and a 64 KB block erase the part executes (DS-TLSR8278-E Table
// 19-13: 1.6 ms, 150 ms, 0.5 s and 0.8 s typical).
var (
	FlashTppUs   = int64(sizeFromEnv("TC32EMU_FLASH_TPP_US", 0))
	FlashTseUs   = int64(sizeFromEnv("TC32EMU_FLASH_TSE_US", 0))
	FlashTbe32Us = int64(sizeFromEnv("TC32EMU_FLASH_TBE32_US", 0))
	FlashTbe64Us = int64(sizeFromEnv("TC32EMU_FLASH_TBE64_US", 0))
)

// Busy is the Python busy(): a program or erase with a busy time still
// running. BusyUntil is cleared once passed, so an idle part costs one test
// of it.
func (f *Flash) Busy() bool {
	if f.BusyUntil == 0 {
		return false
	}
	if f.machine.Cycles < f.BusyUntil {
		return true
	}
	f.BusyUntil = 0
	return false
}

// BusyLeftUs is the Python busy_left_us(): the busy time left, in
// microseconds (for messages).
func (f *Flash) BusyLeftUs() float64 {
	return float64((f.BusyUntil-f.machine.Cycles)*1_000_000) / float64(f.machine.CPUHz)
}

func (f *Flash) startBusy(us int64) {
	if us != 0 && f.machine != nil {
		// Rounded up to a whole cycle; at least 1, so BusyUntil is not 0.
		f.BusyUntil = f.machine.Cycles + (us*f.machine.CPUHz+999_999)/1_000_000
	}
}

// refuseBusy is the Python _refuse_busy(): while busy the part takes nothing
// but status reads (0x05, 0x35).
func (f *Flash) refuseBusy(cmd byte) error {
	if cmd != 0x05 && cmd != 0x35 && f.Busy() {
		f.tx = f.tx[:0]
		return emuErr("flash command 0x%02x while the flash is busy with a program or erase (WIP, %.0f us left): the part takes only status reads until WIP clears",
			cmd, f.BusyLeftUs())
	}
	return nil
}

// Clock sends one byte and returns the byte clocked in; an error when the
// part is busy and the byte is not part of a status read.
func (f *Flash) Clock(b byte) (byte, error) {
	if !f.csLow {
		return 0xFF, nil
	}
	f.tx = append(f.tx, b)
	cmd, n := f.tx[0], len(f.tx)
	f.Beyond = -1
	if n > 1 && f.BusyUntil != 0 {
		if err := f.refuseBusy(cmd); err != nil {
			return 0xFF, err
		}
	}
	if f.DPD {
		return 0xFF, nil
	}
	return f.clockData(cmd, n), nil
}

// clockData is the byte clocked in for command cmd with n bytes sent.
func (f *Flash) clockData(cmd byte, n int) byte {
	switch {
	case cmd == 0x03 || cmd == 0x0B:
		skip := 4
		if cmd == 0x0B {
			skip = 5
		}
		if n > skip {
			a := (int(f.tx[1])<<16 | int(f.tx[2])<<8 | int(f.tx[3])) + (n - skip - 1)
			if f.Size < FlashMem && a >= f.Size {
				f.Beyond = a // the firmware taking this byte is an error or logged (Machine.RegRead)
				if f.BeyondMode == "wrap" {
					return f.Mem[a%f.Size]
				}
				return 0xFF
			}
			return f.Mem[a%len(f.Mem)]
		}
		return 0xFF
	case cmd == 0x05:
		v := byte(f.Status & 0xFC)
		if f.wel {
			v |= 0x02
		}
		if f.Busy() {
			v |= 0x01
		}
		return v
	case cmd == 0x35:
		return byte(f.Status >> 8)
	case cmd == 0x9F && 1 < n && n <= 4:
		return f.Jedec[n-2]
	case cmd == 0x5A && n > 5:
		a := (int(f.tx[1])<<16 | int(f.tx[2])<<8 | int(f.tx[3])) + (n - 6)
		if a < len(f.SFDP) {
			return f.SFDP[a]
		}
		return 0xFF
	case cmd == 0x4B && n > 5:
		return f.UID[(n-6)%len(f.UID)]
	}
	return 0xFF
}

func (f *Flash) commit() error {
	if len(f.tx) == 0 {
		return nil
	}
	cmd := f.tx[0]
	addr, hasAddr := 0, false
	if len(f.tx) >= 4 {
		addr, hasAddr = int(f.tx[1])<<16|int(f.tx[2])<<8|int(f.tx[3]), true
	}
	f.Commands[cmd]++
	if f.BusyUntil != 0 {
		if err := f.refuseBusy(cmd); err != nil {
			return err
		}
	}
	if f.DPD {
		f.DPD = cmd != 0xAB
		f.tx = f.tx[:0]
		return nil
	}
	if f.Size < FlashMem && hasAddr && (cmd == 0x02 || cmd == 0x20 || cmd == 0x52 || cmd == 0xD8) {
		n := 1
		if cmd == 0x02 {
			n = len(f.tx) - 4
		}
		if addr+n > f.Size {
			f.tx = f.tx[:0]
			op := "erase"
			if cmd == 0x02 {
				op = "program"
			}
			return emuErr("flash %s (0x%02x) at 0x%x: the part has 0x%x bytes", op, cmd, addr, f.Size)
		}
	}
	switch {
	case cmd == 0xB9:
		f.DPD = true
	case (cmd == 0x01 || cmd == 0x31 || cmd == 0x11) && f.wel:
		// Write status register: 0x01 with one byte (the low byte) or two (low,
		// high), 0x31 the high byte; 0x11 (a third register) is only logged.
		data := f.tx[1:]
		f.Log = append(f.Log, FlashOp{"status", int(cmd), len(data), fmt.Sprintf("%x", data)})
		f.wel = false
		if cmd == 0x01 && len(data) >= 1 {
			f.Status = f.Status&0xFF00 | int(data[0]&0xFC)
			if len(data) >= 2 {
				f.Status = f.Status&0x00FF | int(data[1])<<8
			}
		} else if cmd == 0x31 && len(data) >= 1 {
			f.Status = f.Status&0x00FF | int(data[0])<<8
		}
		if _, _, listed := f.ProtectedRange(); !listed {
			f.tx = f.tx[:0]
			return emuErr("flash status 0x%04x (0x%02x): the SDK's table for part 0x%06x does not list its protection", f.Status, cmd, f.Part)
		}
	case (cmd == 0x60 || cmd == 0xC7) && f.wel:
		for i := range f.Mem {
			f.Mem[i] = 0xFF
		}
		f.Log = append(f.Log, FlashOp{"chip erase", 0, len(f.Mem), ""})
		f.wel = false
	case cmd == 0x06:
		f.wel = true
	case cmd == 0x04:
		f.wel = false
	case cmd == 0x02 && f.wel && hasAddr:
		data := f.tx[4:]
		page := addr &^ 0xFF
		f.wel = false
		if f.Protected(addr, len(data)) {
			f.wrote("protected", addr, len(data))
			break
		}
		for i, b := range data {
			a := page | ((addr + i) & 0xFF)
			f.Mem[a] &= b
		}
		f.wrote("program", addr, len(data))
		f.startBusy(FlashTppUs)
	case (cmd == 0x20 || cmd == 0x52 || cmd == 0xD8) && f.wel && hasAddr:
		size := map[byte]int{0x20: 0x1000, 0x52: 0x8000, 0xD8: 0x10000}[cmd]
		base := addr &^ (size - 1)
		f.wel = false
		if f.Protected(base, size) {
			f.wrote("protected", base, size)
			break
		}
		for i := base; i < base+size && i < len(f.Mem); i++ {
			f.Mem[i] = 0xFF
		}
		f.wrote("erase", base, size)
		switch cmd {
		case 0x20:
			f.startBusy(FlashTseUs)
		case 0x52:
			f.startBusy(FlashTbe32Us)
		default:
			f.startBusy(FlashTbe64Us)
		}
	}
	f.tx = f.tx[:0]
	return nil
}

// Block protection: the status register's BP (and, on the Puya parts,
// CMP/SEC/TB) bits and the address range each value protects, as the SDK's
// per-part headers list them (tc_ble_single_sdk drivers/B87/flash/
// flash_mid<mid>.h, drivers/B85 for the P25Q40); the Python FLASH_PROTECTION.
// A part whose mid is not here has no protection modelled. A value a part's
// table does not list is an error (the SDK does not say what it protects).
// ProtectedAddrs is a protected address range, First to Last inclusive.
type ProtectedAddrs struct{ First, Last int }

type protTable struct {
	mask   int
	ranges map[int]*ProtectedAddrs // nil: nothing protected
}

// lowTable: GigaDevice GD25LD40C/80C and Zbit ZB25WD40B/80B, bits 4:2 protect
// the low part of the array: all but the top 8, 16, 32, 64, 128, 256 KB (0x04
// to 0x18) or all (0x1c), the SDK's LOW_xxxK values.
func lowTable(size int) protTable {
	top := size - 1
	return protTable{0x1C, map[int]*ProtectedAddrs{
		0x00: nil, 0x04: {0, top - 0x2000}, 0x08: {0, top - 0x4000}, 0x0C: {0, top - 0x8000},
		0x10: {0, top - 0x10000}, 0x14: {0, top - 0x20000}, 0x18: {0, top - 0x40000}, 0x1C: {0, top},
	}}
}

// puyaTable: Puya P25Q40 (mid 0x136085) and P25Q80 (0x146085), 16-bit
// status, mask 0x407c (CMP bit 14, SEC bit 6, TB bit 5, BP2:0 bits 4:2); the
// SDK headers' values with the aliases in their comments.
func puyaTable(size int) protTable {
	top, k := size-1, 1024
	r := map[int]*ProtectedAddrs{
		0x0000: nil, 0x0020: nil, 0x407C: nil,
		0x0004: {size - 64*k, top}, 0x0008: {size - 128*k, top}, 0x000C: {size - 256*k, top},
		0x0024: {0, 64*k - 1}, 0x0028: {0, 128*k - 1}, 0x002C: {0, 256*k - 1},
		0x0044: {size - 4*k, top}, 0x0048: {size - 8*k, top}, 0x004C: {size - 16*k, top},
		0x0050: {size - 32*k, top}, 0x0054: {size - 32*k, top},
		0x0064: {0, 4*k - 1}, 0x0068: {0, 8*k - 1}, 0x006C: {0, 16*k - 1},
		0x0070: {0, 32*k - 1}, 0x0074: {0, 32*k - 1},
		0x4044: {0, top - 4*k}, 0x4048: {0, top - 8*k}, 0x404C: {0, top - 16*k},
		0x4050: {0, top - 32*k}, 0x4054: {0, top - 32*k},
		0x4064: {4 * k, top}, 0x4068: {8 * k, top}, 0x406C: {16 * k, top},
		0x4070: {32 * k, top}, 0x4074: {32 * k, top},
		0x007C: {0, top}, 0x4000: {0, top}, 0x4040: {0, top}, 0x4020: {0, top}, 0x4060: {0, top},
	}
	if size == 0x100000 { // P25Q80
		for v, p := range map[int]*ProtectedAddrs{
			0x0010: {0x80000, top}, 0x4030: {0x80000, top}, 0x0030: {0, 0x7FFFF}, 0x4010: {0, 0x7FFFF},
			0x4004: {0, top - 64*k}, 0x4008: {0, top - 128*k}, 0x400C: {0, top - 256*k},
			0x4024: {64 * k, top}, 0x4028: {128 * k, top}, 0x402C: {256 * k, top},
		} {
			r[v] = p
		}
	} else { // P25Q40
		for v, p := range map[int]*ProtectedAddrs{
			0x4030: nil, 0x4004: {0, top - 64*k}, 0x4008: {0, top - 128*k},
			0x4024: {64 * k, top}, 0x4028: {128 * k, top},
		} {
			r[v] = p
		}
	}
	return protTable{0x407C, r}
}

// zbitCTable: Zbit ZB25WD40C (the SDK's 0x0113325e) and ZB25WD80C
// (0x0114325e), told from the B parts by SFDP; 16-bit status, mask 0x407c;
// flash_mid0113325e.h and flash_mid0114325e.h with their comments' aliases.
func zbitCTable(size int) protTable {
	top, k := size-1, 1024
	r := map[int]*ProtectedAddrs{
		0x0000: nil,
		0x0004: {size - 64*k, top}, 0x0008: {size - 128*k, top}, 0x000C: {size - 256*k, top},
		0x0024: {0, 64*k - 1}, 0x0028: {0, 128*k - 1}, 0x002C: {0, 256*k - 1},
		0x0044: {size - 4*k, top}, 0x0048: {size - 8*k, top}, 0x004C: {size - 16*k, top},
		0x0050: {size - 32*k, top},
		0x0064: {0, 4*k - 1}, 0x0068: {0, 8*k - 1}, 0x006C: {0, 16*k - 1}, 0x0070: {0, 32*k - 1},
		0x4004: {0, top - 64*k}, 0x4008: {0, top - 128*k},
		0x4024: {64 * k, top}, 0x4028: {128 * k, top},
		0x4044: {0, top - 4*k}, 0x4048: {0, top - 8*k}, 0x404C: {0, top - 16*k}, 0x4050: {0, top - 32*k},
		0x4064: {4 * k, top}, 0x4068: {8 * k, top}, 0x406C: {16 * k, top},
	}
	var more map[int]*ProtectedAddrs
	if size == 0x100000 { // ZB25WD80C
		more = map[int]*ProtectedAddrs{
			0x0010: {0x80000, top}, 0x001C: {0, top}, 0x003C: {0, top}, 0x005C: {0, top}, 0x007C: {0, top},
			0x0030: {0, 0x7FFFF}, 0x0054: {size - 32*k, top}, 0x0074: {0, 32*k - 1},
			0x400C: {0, top - 256*k}, 0x402C: {256 * k, top}, 0x4054: {0, top - 32*k},
			0x4070: {32 * k, top}, 0x4074: {32 * k, top},
		}
	} else { // ZB25WD40C
		more = map[int]*ProtectedAddrs{
			0x0010: {0, top}, 0x0030: {0, top}, 0x005C: {0, top}, 0x007C: {0, top},
			0x0058: {size - 32*k, top}, 0x0078: {0, 32*k - 1}, 0x4058: {0, top - 32*k},
			0x4078: {32 * k, top},
		}
	}
	for v, p := range more {
		r[v] = p
	}
	return protTable{0x407C, r}
}

// Keyed by the part as the SDK names it (Flash.Part).
var flashProtection = map[int]protTable{
	0x1360C8: lowTable(0x80000), 0x13325E: lowTable(0x80000),
	0x1460C8: lowTable(0x100000), 0x14325E: lowTable(0x100000),
	0x136085: puyaTable(0x80000), 0x146085: puyaTable(0x100000),
	0x0113325E: zbitCTable(0x80000), 0x0114325E: zbitCTable(0x100000),
}

// ProtectedRange is the Python protected_range(): the protected addresses
// (ProtectedAddrs First, Last) with protected true; protected false when nothing is (or the
// part's protection is not modelled); listed false when the status value is
// one the SDK's table does not list.
func (f *Flash) ProtectedRange() (r ProtectedAddrs, protected bool, listed bool) {
	t, ok := flashProtection[f.Part]
	if !ok {
		return ProtectedAddrs{}, false, true
	}
	p, ok := t.ranges[f.Status&t.mask]
	if !ok {
		return ProtectedAddrs{}, false, false
	}
	if p == nil {
		return ProtectedAddrs{}, false, true
	}
	return *p, true, true
}

// Protected is the Python protected(): true when any of the n bytes at addr
// lies in the protected range.
func (f *Flash) Protected(addr, n int) bool {
	r, protected, _ := f.ProtectedRange()
	return protected && addr <= r.Last && addr+n-1 >= r.First
}

// wrote is the Python _wrote(): the log entry, then OnWrite.
func (f *Flash) wrote(op string, addr, n int) {
	f.Log = append(f.Log, FlashOp{op, addr, n, ""})
	if f.OnWrite != nil {
		f.OnWrite(op, addr, n)
	}
}

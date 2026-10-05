// Package ble is the Go port of ../../emulator/ble_radio.py (Radio, the
// TLSR8278 RF block as a BLE peripheral's link layer drives it) and
// ../../emulator/ble_central.py (Central, a scripted BLE central). The
// Python files are canonical; this port follows them method by method and
// keeps their known-answer checks as Go tests.
//
// SPDX-License-Identifier: Apache-2.0
package ble

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"sort"

	"github.com/goyamamoto/tc32-devtools/go/ble/aes128"
	"github.com/goyamamoto/tc32-devtools/go/tc32emu"
)

const (
	m32         = 0xFFFFFFFF
	TicksPerUs  = 16
	RX, TX      = 0x1, 0x2
	RXTO, CRC2  = 0x4, 0x10
	DONE, FSMTO = 0x20, 0x40
	FIRSTTO     = 0x400
	// TurnaroundMs: TPLL, from the end of the chip's packet to the start of the peer's reply.
	TurnaroundMs = 0.150
)

// TPLLPeer hears the chip's packets in Telink's private format and answers
// in its receive window (a 2.4G dongle model): the reply payload, or nil.
type TPLLPeer interface {
	OnTPLL(payload []byte, mhz int, accessCode []byte) []byte
}

// SRX is an open receive window in Telink's private format.
type SRX struct {
	T     float64
	Start uint32
	MHz   int
	AC    []byte
	RX    int
}

// TPLLTx is one packet the chip sent in Telink's private format.
type TPLLTx struct {
	Ms      float64
	MHz     int
	AC      []byte
	Payload []byte
}

// AdvAA is the advertising access address as a link layer writes 0x408..0x40b (MSB first).
var AdvAA = []byte{0x8e, 0x89, 0xbe, 0xd6}

// LLNames and ATTNames name the LL control opcodes and ATT opcodes in logs.
var LLNames = map[byte]string{0x00: "LL_CONNECTION_UPDATE_IND", 0x01: "LL_CHANNEL_MAP_IND", 0x02: "LL_TERMINATE_IND",
	0x03: "LL_ENC_REQ", 0x04: "LL_ENC_RSP", 0x05: "LL_START_ENC_REQ", 0x06: "LL_START_ENC_RSP",
	0x07: "LL_UNKNOWN_RSP", 0x08: "LL_FEATURE_REQ", 0x09: "LL_FEATURE_RSP",
	0x0a: "LL_PAUSE_ENC_REQ", 0x0b: "LL_PAUSE_ENC_RSP", 0x0c: "LL_VERSION_IND",
	0x0d: "LL_REJECT_IND", 0x0e: "LL_PERIPHERAL_FEATURE_REQ", 0x0f: "LL_CONNECTION_PARAM_REQ",
	0x10: "LL_CONNECTION_PARAM_RSP", 0x11: "LL_REJECT_EXT_IND", 0x12: "LL_PING_REQ",
	0x13: "LL_PING_RSP", 0x14: "LL_LENGTH_REQ", 0x15: "LL_LENGTH_RSP", 0x16: "LL_PHY_REQ",
	0x17: "LL_PHY_RSP", 0x18: "LL_PHY_UPDATE_IND", 0x19: "LL_MIN_USED_CHANNELS_IND"}
var ATTNames = map[byte]string{0x01: "Error Rsp", 0x02: "Exchange MTU Req", 0x03: "Exchange MTU Rsp", 0x04: "Find Info Req",
	0x05: "Find Info Rsp", 0x08: "Read By Type Req", 0x09: "Read By Type Rsp", 0x0a: "Read Req",
	0x0b: "Read Rsp", 0x0c: "Read Blob Req", 0x0d: "Read Blob Rsp", 0x10: "Read By Group Type Req",
	0x11: "Read By Group Type Rsp", 0x12: "Write Req", 0x13: "Write Rsp", 0x52: "Write Cmd",
	0x1b: "Handle Value Ntf", 0x1d: "Handle Value Ind", 0x1e: "Handle Value Cfm"}

// AirtimeUs: 1M PHY: preamble 1, access address 4, header 2, payload, CRC 3 bytes.
func AirtimeUs(pdu []byte) int { return (1 + 4 + 2 + int(pdu[1]) + 3) * 8 }

// Peer is what the radio needs of a central (ble_central.Central, or a test's recorder).
type Peer interface {
	OnAdv(pdu []byte, ch byte) []byte // the reply to an ADV_IND (CONNECT_IND), or nil
	OnConnectSent(pdu []byte, tEnd float64)
	Drop(pdu []byte) bool
	OnAir(pdu []byte, ch byte, txAddr uint32) []byte // the next PDU in this event, or nil
}

// AirLostPeer is a central that is told of a reply lost on air (the Python
// air_lost); AirLostTxPeer one told that its next packet in the event was
// (air_lost_tx).
type AirLostPeer interface {
	AirLost(pdu []byte)
}

type AirLostTxPeer interface {
	AirLostTx(pdu []byte)
}

// Scanner is a central that can answer advertising with a SCAN_REQ (the
// Python scan_request); the radio asks it when no central connects.
type Scanner interface {
	ScanRequest(pdu []byte, ch byte) []byte
}

// ScanReq is one SCAN_REQ a central sent (Radio.ScanReqs).
type ScanReq struct {
	Ms      float64
	Ch      byte
	Central Peer
	PDU     []byte
}

// BRX is a peripheral connection event the link layer scheduled.
type BRX struct {
	T       float64
	Start   uint32
	Ch      byte
	AA      []byte
	CRCInit uint32
	LastSN  int
	NESN    int
	RX      int
	F03     byte
	OnAir   *[2]float64 // the last central packet's start and end (ms)
}

type sched struct {
	ms    float64
	seq   int
	fn    func()
	owner string
}

// Radio is the TLSR8278 RF block model.
type Radio struct {
	M       *tc32emu.Machine
	Log     func(string)
	Central Peer
	// Centrals hear the advertising after Central; ConnCentral is the one whose
	// CONNECT_IND was sent last; ScanReqs, each SCAN_REQ sent.
	Centrals    []Peer
	ConnCentral Peer
	ScanReqs    []ScanReq
	sched       []sched
	seq         int
	FIFO        [16]uint16
	WPtr        int
	RPtr        int
	Brx         *BRX
	snRb        int
	nesnRb      int
	ts          uint32
	AdvTx       []AdvTx
	Cmds        []Cmd
	Misses      []Miss
	AdvFIFO     []AdvFIFO // each advertising packet sent with the TX FIFO not empty
	FIFOLog     []FIFOEntry
	aesIn       []byte
	aesOut      []byte
	AESOps      int
	Leads       []float64 // ms from the BRX command to each central packet
	RxOn        []float64 // us from the BRX start tick (0xf18) to each central packet
	TPLL        TPLLPeer  // the peer in Telink's private format, or nil
	Srx         *SRX      // the open SRX window
	TpllTx      []TPLLTx  // every TPLL packet sent

	// RFWritten: RFSuspendRegs the firmware has written since the last
	// reset; RFUninit: of those, the ones not written again since a suspend
	// that lost them (SuspendRF "lost"); RFLosses counts those suspends;
	// RFRefused the commands the radio did not start for that (the Python
	// rf_written, rf_uninit, rf_losses, rf_refused).
	RFWritten map[int]bool
	RFUninit  map[int]bool
	RFLosses  int
	RFRefused []RFRefusal
	rfPowerOn map[int]byte
	prevRead  func(o, size int) (uint32, bool, error)
	prevWrite func(o, size int, val uint32) (bool, error)
	prevIdle  func(int64) int64
	prevUT    func() error
	pending   error

	// Air loses packets on their way (the Python Radio.air); nil: every packet
	// arrives. TxLost: the last CentralTx's packet was lost on air.
	Air    *Air
	TxLost bool

	// LateStarts: (ms, us) of each BRX command written after its start tick
	// (0xf18): the Python late_starts.
	LateStarts [][2]float64
}

// AdvTx, Cmd, Miss and FIFOEntry are the radio's logs.
type AdvTx struct {
	Ms  float64
	Ch  byte
	PDU []byte
}
type Cmd struct {
	Ms  float64
	Cmd byte
	Ch  byte
}
type Miss struct {
	Ms  float64
	Ch  int // the BLE channel, or the TPLL frequency in MHz
	Why string
}
type FIFOEntry struct {
	Ms   float64
	WPtr int
	Addr uint16
}
type AdvFIFO struct {
	Ms         float64
	Ch         byte
	RPtr, WPtr int
}

// NewRadio attaches the model to m, as Radio.__init__ does.
func NewRadio(m *tc32emu.Machine, log func(string)) *Radio {
	if log == nil {
		log = func(string) {}
	}
	r := &Radio{M: m, Log: log, RFUninit: map[int]bool{}, RFWritten: map[int]bool{}, rfPowerOn: map[int]byte{}}
	for _, a := range RFSuspendRegs {
		r.rfPowerOn[a] = m.Regs[a]
	}
	r.prevRead, r.prevWrite = m.RegReadHook, m.RegWriteHook
	m.RegReadHook, m.RegWriteHook = r.regRead, r.regWrite
	r.prevUT = m.UpdateTimeHook
	m.UpdateTimeHook = func() error {
		if r.prevUT != nil {
			if err := r.prevUT(); err != nil {
				return err
			}
		}
		return r.Tick()
	}
	m.LevelSources = append(m.LevelSources, r.irqLevel)
	m.ResetHooks = append(m.ResetHooks, r.OnReset)
	// An idle loop jumps to the next timer compare (Machine.Run): not past the
	// radio's next scheduled step, or the air traffic would run late.
	r.prevIdle = m.IdleSkipHook
	m.IdleSkipHook = func(n int64) int64 {
		if r.prevIdle != nil {
			n = r.prevIdle(n)
		}
		if len(r.sched) > 0 {
			lim := int64((r.sched[0].ms - m.Ms()) * float64(m.CPUHz) / 1000)
			if lim < 0 {
				lim = 0
			}
			if lim < n {
				n = lim
			}
		}
		return n
	}
	return r
}

// ------------------------------------------------------------ time

// At runs fn at simulated time ms. owner "central" marks the central's own
// steps, which a reset of the machine does not cancel.
func (r *Radio) At(ms float64, fn func(), owner string) {
	if owner == "" {
		owner = "radio"
	}
	r.seq++
	r.sched = append(r.sched, sched{ms, r.seq, fn, owner})
	sort.SliceStable(r.sched, func(i, j int) bool {
		if r.sched[i].ms != r.sched[j].ms {
			return r.sched[i].ms < r.sched[j].ms
		}
		return r.sched[i].seq < r.sched[j].seq
	})
}

// Tick runs the steps whose time has come.
func (r *Radio) Tick() error {
	now := r.M.Ms()
	for len(r.sched) > 0 && r.sched[0].ms <= now {
		fn := r.sched[0].fn
		r.sched = r.sched[1:]
		fn()
		if r.pending != nil {
			err := r.pending
			r.pending = nil
			return err
		}
	}
	return nil
}

// OnReset: the machine was reset (watchdog or software): the radio's state
// and its pending steps go; the central goes on, as a real one would.
func (r *Radio) OnReset() {
	kept := r.sched[:0:0]
	for _, e := range r.sched {
		if e.owner == "central" {
			kept = append(kept, e)
		}
	}
	r.sched = kept
	r.FIFO = [16]uint16{}
	r.WPtr, r.RPtr = 0, 0
	r.Brx = nil
	r.snRb, r.nesnRb = 0, 0
	r.ts = 0
	r.aesIn, r.aesOut = nil, nil
	r.Srx = nil
	r.RFUninit = map[int]bool{} // the registers are at their power-on values again
	r.RFWritten = map[int]bool{}
}

// TicksAgo returns the system timer value us microseconds ago.
func (r *Radio) TicksAgo(us float64) uint32 {
	return (r.M.StimerNow() - uint32(int64(us*TicksPerUs))) & m32
}

// ------------------------------------------------------------ registers

func (r *Radio) irqLevel() uint32 {
	rg := r.M.Regs
	if (uint32(rg[0xF20])|uint32(rg[0xF21])<<8)&(uint32(rg[0xF1C])|uint32(rg[0xF1D])<<8) != 0 {
		return 1 << 13
	}
	return 0
}

// SetStatus sets bits of the RF IRQ status (0xf20/0xf21).
func (r *Radio) SetStatus(bits uint32) {
	r.M.Regs[0xF20] |= byte(bits & 0xFF)
	r.M.Regs[0xF21] |= byte(bits >> 8)
}

// rxBusy is 0x448 bit 5 under TC32EMU_RX_BUSY.
func (r *Radio) rxBusy() bool {
	switch RxBusy {
	case "stuck":
		return true
	case "air":
		if b := r.Brx; b != nil && b.OnAir != nil {
			t := r.M.Ms()
			return b.OnAir[0] <= t && t < b.OnAir[1]
		}
	}
	return false
}

func (r *Radio) regRead(o, size int) (uint32, bool, error) {
	if o <= 0x448 && 0x448 < o+size && RxBusy != "off" {
		v, ok := uint32(0), false
		if r.prevRead != nil {
			var err error
			if v, ok, err = r.prevRead(o, size); err != nil {
				return 0, false, err
			}
		}
		if !ok {
			for i := 0; i < size; i++ {
				v |= uint32(r.M.Regs[o+i]) << (8 * i)
			}
		}
		if r.rxBusy() {
			v |= 0x20 << (8 * (0x448 - o))
		}
		return v, true, nil
	}
	if size == 1 {
		switch o {
		case 0xC2A:
			return uint32(r.RPtr), true, nil
		case 0xC2B:
			return uint32(r.WPtr), true, nil
		case 0xF22:
			return uint32(r.snRb), true, nil
		case 0xF23:
			return uint32(r.nesnRb << 4), true, nil
		}
	}
	if o == 0x450 && size == 4 {
		return r.ts, true, nil
	}
	if o == 0x548 && size == 4 {
		var w [4]byte
		copy(w[:], r.aesOut)
		if len(r.aesOut) > 4 {
			r.aesOut = r.aesOut[4:]
		} else {
			r.aesOut = nil
		}
		return binary.LittleEndian.Uint32(w[:]), true, nil
	}
	if r.prevRead != nil {
		return r.prevRead(o, size)
	}
	return 0, false, nil
}

func (r *Radio) aesWrite(o int, val uint32) {
	rg := r.M.Regs
	if o == 0x540 {
		rg[0x540] = byte(val&0x01) | 0x02 // data feed wanted, not finished
		r.aesIn, r.aesOut = nil, nil
		return
	}
	var w [4]byte
	binary.LittleEndian.PutUint32(w[:], val)
	r.aesIn = append(r.aesIn, w[:]...)
	if len(r.aesIn) == 16 {
		key := append([]byte(nil), rg[0x550:0x560]...)
		if rg[0x540]&1 != 0 {
			r.aesOut = aes128.Decrypt(key, r.aesIn)
		} else {
			r.aesOut = aes128.Encrypt(key, r.aesIn)
		}
		r.aesIn = nil
		r.AESOps++
		rg[0x540] = (rg[0x540] & 0x01) | 0x04
	}
}

// baseWrite writes through the hooks below the radio, or the machine's own register write.
func (r *Radio) baseWrite(o, size int, val uint32) error {
	if r.prevWrite != nil {
		if handled, err := r.prevWrite(o, size, val); handled || err != nil {
			return err
		}
	}
	// The machine's RegWrite would call our hook again: do the base write by
	// temporarily detaching.
	hook := r.M.RegWriteHook
	r.M.RegWriteHook = nil
	err := r.M.RegWrite(o, size, val)
	r.M.RegWriteHook = hook
	return err
}

func (r *Radio) regWrite(o, size int, val uint32) (bool, error) {
	rg := r.M.Regs
	if SuspendRF == "lost" {
		for a := o; a < o+size; a++ {
			if rfSuspendSet[a] {
				r.RFWritten[a] = true
				delete(r.RFUninit, a)
			}
		}
	}
	if o <= 0xF21 && o+size > 0xF20 {
		for i := 0; i < size; i++ {
			a, b := o+i, byte(val>>(8*uint(i)))
			if a == 0xF20 || a == 0xF21 {
				rg[a] &^= b
			} else if err := r.baseWrite(a, 1, uint32(b)); err != nil {
				return true, err
			}
		}
		return true, nil
	}
	if o == 0xC2A && size == 1 {
		if val&0x10 != 0 {
			if TxFIFOClear == "position" {
				r.RPtr = r.WPtr
			} else {
				r.RPtr, r.WPtr = 0, 0
			}
		}
		if val&0x40 != 0 {
			r.RPtr = int(val & 0xF)
		}
		if val&0x20 != 0 {
			r.RPtr = (r.RPtr + 1) & 0xF
		}
		rg[0xC2A] = byte(val)
		return true, nil
	}
	if (o == 0x540 && size == 1) || (o == 0x548 && size == 4) {
		r.aesWrite(o, val)
		return true, nil
	}
	if o == 0xC2C && size == 2 {
		r.FIFO[r.WPtr] = uint16(val)
		r.FIFOLog = append(r.FIFOLog, FIFOEntry{r.M.Ms(), r.WPtr, uint16(val)})
		r.WPtr = (r.WPtr + 1) & 0xF
		return true, nil
	}
	if err := r.baseWrite(o, size, val); err != nil {
		return true, err
	}
	if SuspendRF == "lost" && o <= 0x6F && 0x6F < o+size && r.M.Asleep != nil {
		for _, a := range RFSuspendRegs {
			rg[a] = r.rfPowerOn[a]
		}
		r.RFUninit = map[int]bool{}
		for a := range r.RFWritten {
			r.RFUninit[a] = true
		}
		r.RFLosses++
	}
	if o == 0x61 && size == 1 && val&1 != 0 {
		r.Brx = nil // RF reset (a link layer pulses it before each event)
	}
	if o <= 0xF00 && 0xF00 < o+size {
		r.onCmd(rg[0xF00])
	}
	return true, nil
}

// ------------------------------------------------------------ commands

func (r *Radio) onCmd(cmd byte) {
	m, rg, t := r.M, r.M.Regs, r.M.Ms()
	if len(r.RFUninit) > 0 && cmd != 0x80 {
		if (len(r.RFRefused) == 0 || r.RFRefused[len(r.RFRefused)-1].Loss != r.RFLosses) && m.Event != nil {
			first := -1
			for a := range r.RFUninit {
				if first < 0 || a < first {
					first = a
				}
			}
			m.Event(fmt.Sprintf("radio: command 0x%02x after a suspend, with %d of the radio registers it had set up not written again (first 0x%x): nothing sent or heard until they are",
				cmd, len(r.RFUninit), first))
		}
		r.RFRefused = append(r.RFRefused, RFRefusal{round3(t), cmd, r.RFLosses})
		return
	}
	if r.TPLL != nil && r.tpllFormat() {
		switch cmd {
		case 0x85, 0x87:
			r.Cmds = append(r.Cmds, Cmd{t, cmd, rg[0x40D]})
			r.At(t+0.006, func() { r.tpllTxStart(cmd == 0x87) }, "radio")
			return
		case 0x86:
			r.Cmds = append(r.Cmds, Cmd{t, cmd, rg[0x40D]})
			start := m.StimerNow()
			if rg[0xF16]&4 != 0 {
				start = binary.LittleEndian.Uint32(rg[0xF18:])
			}
			s := &SRX{T: t, Start: start, MHz: r.tpllMHz(), AC: r.tpllAccessCode()}
			r.Srx = s
			if rg[0xF03]&2 != 0 {
				us := binary.LittleEndian.Uint32(rg[0xF28:]) + 1
				r.At(t+float64(us)/1000, func() {
					if s.RX == 0 {
						r.srxTimeout(s)
					}
				}, "radio")
			}
			return
		case 0x80:
			r.Srx = nil
		}
	}
	r.Cmds = append(r.Cmds, Cmd{t, cmd, rg[0x40D]})
	switch {
	case cmd == 0x80:
		r.Brx = nil
	case cmd == 0x82:
		start := m.StimerNow()
		if rg[0xF16]&4 != 0 {
			start = binary.LittleEndian.Uint32(rg[0xF18:])
		}
		if late := m.StimerNow() - start; late > 0 && late < 0x80000000 {
			r.LateStarts = append(r.LateStarts, [2]float64{t, float64(late) / 16})
			r.Log(fmt.Sprintf("[%9.3f] radio: BRX command %.0f us after its start tick", t, float64(late)/16))
		}
		b := &BRX{T: t, Start: start, Ch: rg[0x40D], AA: append([]byte(nil), rg[0x408:0x40C]...),
			CRCInit: uint32(rg[0x424]) | uint32(rg[0x425])<<8 | uint32(rg[0x426])<<16,
			LastSN:  int(rg[0xF03]>>4) & 1, NESN: int(rg[0xF03]>>5) & 1, F03: rg[0xF03]}
		r.Brx = b
		if rg[0xF03]&1 != 0 { // FSM timeout
			us := binary.LittleEndian.Uint32(rg[0xF2C:])
			r.At(t+float64(us)/1000, func() { r.timeout(b, FSMTO) }, "radio")
		}
		if rg[0xF03]&2 != 0 { // first RX timeout
			us := binary.LittleEndian.Uint32(rg[0xF28:])
			if us < 0x0FFFFFFF {
				r.At(t+float64(us)/1000, func() {
					if b.RX == 0 {
						r.timeout(b, FIRSTTO)
					}
				}, "radio")
			}
		}
	case cmd == 0x85 || cmd == 0x87:
		// rf_start_stx2rx writes the DMA address 0xc0c after the command and
		// schedules the start 100 ticks ahead, so the packet is read when the
		// transmission starts, not at the command.
		r.At(t+0.006, func() { r.advTxStart(cmd, t) }, "radio")
	}
}

func (r *Radio) readBytes(a uint32, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		v, err := r.M.Read(a+uint32(i), 1)
		if err != nil && r.pending == nil {
			r.pending = err
		}
		out[i] = byte(v)
	}
	return out
}

func (r *Radio) advTxStart(cmd byte, t0 float64) {
	m, rg := r.M, r.M.Regs
	t := m.Ms()
	a := 0x840000 | uint32(binary.LittleEndian.Uint16(rg[0xC0C:]))
	raw := r.readBytes(a, 6+40)
	pdu := raw[4 : 6+int(raw[5])]
	r.AdvTx = append(r.AdvTx, AdvTx{t, rg[0x40D], pdu})
	// What the chip sends with packets still in the TX FIFO is not documented:
	// recorded and missed, the packet at 0xc0c sent as always.
	if r.RPtr != r.WPtr && !r.tpllFormat() {
		r.AdvFIFO = append(r.AdvFIFO, AdvFIFO{t, rg[0x40D], r.RPtr, r.WPtr})
		why := fmt.Sprintf("advertising with %d packet(s) in the TX FIFO (0xc2a %d, 0xc2b %d)",
			(r.WPtr-r.RPtr)&0xF, r.RPtr, r.WPtr)
		r.Misses = append(r.Misses, Miss{t, int(rg[0x40D]), why})
		if len(r.AdvFIFO) == 1 && m.Event != nil {
			m.Event("radio: " + why + "; what the chip sends then is not documented")
		}
	}
	r.SetStatus(TX) // a link layer polls TX done after STX/STX2RX
	centrals := r.Offered()
	if len(centrals) > 0 && r.Air != nil && r.Air.Lost(ToHost, rg[0x40D], t) {
		r.Log(fmt.Sprintf("[%9.3f] radio: advertising packet on ch %d lost on air", t, rg[0x40D]))
		centrals = nil
	}
	if cmd == 0x87 && len(centrals) > 0 && bytes.Equal(rg[0x408:0x40C], AdvAA) {
		// One answer per advertising packet: the first central that connects,
		// else the first that scans.
		var who Peer
		var rsp []byte
		for _, c := range centrals {
			if rsp = c.OnAdv(pdu, rg[0x40D]); rsp != nil {
				who = c
				break
			}
		}
		if rsp == nil {
			for _, c := range centrals {
				if s, ok := c.(Scanner); ok {
					if rsp = s.ScanRequest(pdu, rg[0x40D]); rsp != nil {
						who = c
						r.ScanReqs = append(r.ScanReqs, ScanReq{t, rg[0x40D], c, rsp})
						break
					}
				}
			}
		}
		if rsp != nil {
			tRx := t + float64(AirtimeUs(pdu)+150)/1000
			if r.Air != nil && r.Air.Lost(ToKeyboard, rg[0x40D], tRx) {
				r.Log(fmt.Sprintf("[%9.3f] radio: answer to the advertising packet on ch %d lost on air", tRx, rg[0x40D]))
				if rsp[0]&0xF == 5 { // CONNECT_IND: its central cannot know
					r.At(tRx+float64(AirtimeUs(rsp))/1000, func() { who.OnConnectSent(rsp, r.M.Ms()) }, "radio")
				}
				return
			}
			// The DMA streams the header in as the packet arrives (the adv event
			// polls the entry's header word); RX status at its end.
			r.At(tRx+0.056, func() { r.dmaRx(rsp, tRx, false) }, "radio")
			r.At(tRx+float64(AirtimeUs(rsp))/1000, func() { r.advRxEnd(rsp, tRx, who) }, "radio")
		}
	}
}

// Offered is the centrals that hear advertising, in the order they answer:
// Central, then Centrals.
func (r *Radio) Offered() []Peer {
	var out []Peer
	if r.Central != nil {
		out = append(out, r.Central)
	}
	for _, c := range r.Centrals {
		if c != r.Central {
			out = append(out, c)
		}
	}
	return out
}

// LinkCentral is the central of the connection: the one whose CONNECT_IND
// was sent, while it is still attached, else Central.
func (r *Radio) LinkCentral() Peer {
	if c := r.ConnCentral; c != nil {
		for _, x := range r.Offered() {
			if x == c {
				return c
			}
		}
	}
	return r.Central
}

func (r *Radio) timeout(b *BRX, bit uint32) {
	if r.Brx == b {
		r.Brx = nil
		r.SetStatus(bit)
		r.Log(fmt.Sprintf("[%9.3f] radio: BRX timeout 0x%x", r.M.Ms(), bit))
	}
}

func (r *Radio) dmaRx(pdu []byte, tStart float64, status bool) uint32 {
	m, rg := r.M, r.M.Regs
	a := 0x840000 | uint32(binary.LittleEndian.Uint16(rg[0xC08:]))
	ln := int(pdu[1])
	anchor := (m.StimerNow() - uint32(int64((m.Ms()-tStart)*1000*TicksPerUs))) & m32
	ts := (anchor + 0x500) & m32
	buf := make([]byte, 0, ln+17)
	buf = binary.LittleEndian.AppendUint32(buf, uint32(ln+13))
	buf = append(buf, pdu...)
	buf = append(buf, 0, 0, 0)
	buf = binary.LittleEndian.AppendUint32(buf, ts)
	buf = append(buf, 0, 0, 0xC8, 0)
	for i, v := range buf {
		if err := m.Write(a+uint32(i), 1, uint32(v)); err != nil && r.pending == nil {
			r.pending = err
		}
	}
	r.ts = ts
	if status {
		r.SetStatus(RX)
	}
	return a
}

func (r *Radio) advRxEnd(pdu []byte, tRx float64, who Peer) {
	r.SetStatus(RX)
	if who != nil && pdu[0]&0xF == 5 { // CONNECT_IND
		r.ConnCentral = who
		who.OnConnectSent(pdu, r.M.Ms())
	}
}

// ------------------------------------------------------------ connection events

// CentralTx: the central starts sending pdu now; false if the peripheral misses it.
func (r *Radio) CentralTx(pdu []byte, ch byte, aa []byte) bool {
	m, t, b := r.M, r.M.Ms(), r.Brx
	r.TxLost = false
	why := ""
	switch {
	case b == nil:
		why = "peripheral not listening"
	case b.Ch != ch:
		why = fmt.Sprintf("peripheral on channel %d, central on %d", b.Ch, ch)
	case !bytes.Equal(b.AA, aa):
		why = fmt.Sprintf("access address %x != %x", b.AA, aa)
	case (m.StimerNow()-b.Start)&m32 >= 0x80000000:
		why = "BRX scheduled later"
	}
	if why != "" {
		r.Misses = append(r.Misses, Miss{t, int(ch), why})
		r.Log(fmt.Sprintf("[%9.3f] radio: central packet on ch %d missed: %s", t, ch, why))
		return false
	}
	if r.Air != nil && r.Air.Lost(ToKeyboard, ch, t) {
		r.TxLost = true
		r.Log(fmt.Sprintf("[%9.3f] radio: central packet on ch %d lost on air", t, ch))
		return false
	}
	r.Leads = append(r.Leads, t-b.T)
	r.RxOn = append(r.RxOn, float64((m.StimerNow()-b.Start)&m32)/TicksPerUs)
	b.OnAir = &[2]float64{t, t + float64(AirtimeUs(pdu))/1000}
	r.At(t+float64(AirtimeUs(pdu))/1000, func() { r.rxDone(b, pdu, t) }, "radio")
	return true
}

func (r *Radio) rxDone(b *BRX, pdu []byte, tStart float64) {
	if r.Brx != b {
		r.Misses = append(r.Misses, Miss{r.M.Ms(), int(b.Ch), "BRX stopped during the packet"})
		return
	}
	hdr0 := pdu[0]
	rxSN, rxNESN := int(hdr0>>3)&1, int(hdr0>>2)&1
	if rxNESN != b.LastSN { // our packet in flight was acked
		if r.RPtr != r.WPtr {
			r.RPtr = (r.RPtr + 1) & 0xF
		}
		b.LastSN ^= 1
	}
	var txAddr uint32
	haveTx := r.RPtr != r.WPtr
	if haveTx {
		txAddr = 0x840000 | uint32(r.FIFO[r.RPtr])
	}
	more := haveTx && ((r.RPtr+1)&0xF) != r.WPtr
	if rxSN == b.NESN {
		b.NESN ^= 1
	}
	txSN := b.LastSN
	r.snRb, r.nesnRb = txSN, b.NESN
	b.RX++
	r.dmaRx(pdu, tStart, true)
	r.At(r.M.Ms()+0.150, func() { r.txStart(b, txAddr, haveTx, txSN, more) }, "radio")
}

func (r *Radio) txStart(b *BRX, txAddr uint32, haveTx bool, txSN int, more bool) {
	m, rg := r.M, r.M.Regs
	if r.Brx != b {
		return
	}
	if !haveTx {
		txAddr = 0x840000 | uint32(binary.LittleEndian.Uint16(rg[0xC0C:]))
	}
	hl := r.readBytes(txAddr+4, 2)
	hdr0, ln := hl[0], int(hl[1])
	payload := r.readBytes(txAddr+6, ln)
	md := 0
	if more || hdr0&0x10 != 0 {
		md = 1
	}
	pdu := append([]byte{(hdr0 & 0xE3) | byte(b.NESN<<2) | byte(txSN<<3) | byte(md<<4), byte(ln)}, payload...)
	r.At(m.Ms()+float64(AirtimeUs(pdu))/1000, func() { r.txDone(b, pdu, txAddr) }, "radio")
}

func (r *Radio) txDone(b *BRX, pdu []byte, txAddr uint32) {
	if r.Brx != b {
		return
	}
	r.SetStatus(TX)
	c := r.LinkCentral()
	if c != nil && c.Drop(pdu) {
		r.At(r.M.Ms()+0.010, func() { r.endEvent(b) }, "radio") // the central never heard it
		return
	}
	if c != nil && r.Air != nil && r.Air.Lost(ToHost, b.Ch, r.M.Ms()) {
		r.Log(fmt.Sprintf("[%9.3f] radio: reply on ch %d lost on air", r.M.Ms(), b.Ch))
		if p, ok := c.(AirLostPeer); ok {
			p.AirLost(pdu)
		}
		r.At(r.M.Ms()+0.010, func() { r.endEvent(b) }, "radio")
		return
	}
	var nxt []byte
	if c != nil {
		nxt = c.OnAir(pdu, b.Ch, txAddr)
	}
	if nxt != nil {
		r.At(r.M.Ms()+0.150, func() { r.centralNext(c, nxt, b) }, "radio")
	} else {
		r.At(r.M.Ms()+0.010, func() { r.endEvent(b) }, "radio")
	}
}

// centralNext: the central's next packet in the event; one lost on air is
// told to it (AirLostTx).
func (r *Radio) centralNext(c Peer, pdu []byte, b *BRX) {
	if !r.CentralTx(pdu, b.Ch, b.AA) && r.TxLost {
		if p, ok := c.(AirLostTxPeer); ok {
			p.AirLostTx(pdu)
		}
	}
}

func (r *Radio) endEvent(b *BRX) {
	if r.Brx == b {
		r.Brx = nil
		r.SetStatus(DONE)
	}
}

// ------------------------------------------------------------ TPLL (Telink's private format)

func (r *Radio) tpllFormat() bool { return r.M.Regs[0x404]&3 == 2 }

func (r *Radio) tpllMHz() int {
	rg := r.M.Regs
	return int(rg[0x1245]&0x3F)<<6 | int(rg[0x1244]>>2)
}

func (r *Radio) tpllAccessCode() []byte {
	rg := r.M.Regs
	return append([]byte(nil), rg[0x408:0x408+int(rg[0x405]&7)]...)
}

// TpllAirtimeUs is the air time of a packet with n payload bytes.
func (r *Radio) TpllAirtimeUs(n int) float64 {
	rg := r.M.Regs
	mbps := 1.0
	if rg[0x1220] == 0x04 {
		mbps = 2
	}
	return float64((int(rg[0x402]&0x1F)+int(rg[0x405]&7)+1+n+2)*8) / mbps
}

func (r *Radio) srxTimeout(s *SRX) {
	if r.Srx == s {
		r.Srx = nil
		r.SetStatus(FIRSTTO)
	}
}

func (r *Radio) stx2rxTimeout(s *SRX) {
	if r.Srx == s {
		r.Srx = nil
		r.SetStatus(RXTO)
	}
}

func (r *Radio) tpllTxStart(stx2rx bool) {
	m, rg := r.M, r.M.Regs
	a := 0x840000 | uint32(binary.LittleEndian.Uint16(rg[0xC0C:]))
	n := int(r.readBytes(a+4, 1)[0] & 0x3F)
	payload := r.readBytes(a+5, n)
	mhz, ac := r.tpllMHz(), r.tpllAccessCode()
	r.TpllTx = append(r.TpllTx, TPLLTx{m.Ms(), mhz, ac, payload})
	r.At(m.Ms()+r.TpllAirtimeUs(n)/1000, func() { r.tpllTxDone(payload, mhz, ac, stx2rx) }, "radio")
}

func (r *Radio) tpllTxDone(payload []byte, mhz int, ac []byte, stx2rx bool) {
	r.SetStatus(TX)
	if stx2rx {
		m, rg := r.M, r.M.Regs
		s := &SRX{T: m.Ms(), Start: m.StimerNow(), MHz: mhz, AC: ac}
		r.Srx = s
		if rg[0xF03]&4 != 0 {
			us := uint32(binary.LittleEndian.Uint16(rg[0xF0A:])) + 1
			r.At(m.Ms()+float64(us)/1000, func() {
				if s.RX == 0 {
					r.stx2rxTimeout(s)
				}
			}, "radio")
		}
	}
	var reply []byte
	if r.TPLL != nil {
		reply = r.TPLL.OnTPLL(payload, mhz, ac)
	}
	if reply != nil {
		reply = append([]byte(nil), reply...)
		r.At(r.M.Ms()+TurnaroundMs, func() { r.tpllRxStart(reply, mhz, ac) }, "radio")
	}
}

func (r *Radio) tpllRxStart(reply []byte, mhz int, ac []byte) {
	m, s, t := r.M, r.Srx, r.M.Ms()
	why := ""
	switch {
	case s == nil:
		why = "not listening"
	case s.MHz != mhz:
		why = fmt.Sprintf("listening on %d MHz", s.MHz)
	case !bytes.Equal(s.AC, ac):
		why = fmt.Sprintf("access code %x != %x", s.AC, ac)
	case (m.StimerNow()-s.Start)&m32 >= 0x80000000:
		why = "SRX scheduled later"
	}
	if why != "" {
		r.Misses = append(r.Misses, Miss{t, mhz, why})
		r.Log(fmt.Sprintf("[%9.3f] radio: reply on %d MHz missed: %s", t, mhz, why))
		return
	}
	s.RX++
	r.At(t+r.TpllAirtimeUs(len(reply))/1000, func() { r.tpllRxDone(s, reply, t) }, "radio")
}

func (r *Radio) tpllRxDone(s *SRX, reply []byte, tStart float64) {
	m, rg := r.M, r.M.Regs
	if r.Srx != s {
		r.Misses = append(r.Misses, Miss{m.Ms(), s.MHz, "SRX stopped during the packet"})
		return
	}
	a := 0x840000 | uint32(binary.LittleEndian.Uint16(rg[0xC08:]))
	n := len(reply)
	anchor := (m.StimerNow() - uint32(int64((m.Ms()-tStart)*1000*TicksPerUs))) & m32
	buf := binary.LittleEndian.AppendUint32(nil, uint32(n+11))
	buf = append(buf, byte(n))
	buf = append(buf, reply...)
	buf = append(buf, 0, 0)
	buf = binary.LittleEndian.AppendUint32(buf, anchor)
	buf = append(buf, 0, 0, 0xC8, 0)
	for i, v := range buf {
		if err := m.Write(a+uint32(i), 1, uint32(v)); err != nil && r.pending == nil {
			r.pending = err
		}
	}
	r.Srx = nil
	r.SetStatus(RX)
}

// RFInitRegs are the registers a link layer's radio initialisation writes:
// the Python RF_INIT_REGS.
var RFInitRegs = []int{0x12D2, 0x12D3, 0x127B, 0x1279, 0x124A, 0x124B, 0x1254, 0x1255, 0x1256, 0x1257, 0x1276, 0x134E, 0x134C,
	0x401, 0x402, 0x430, 0x460, 0x461, 0x462, 0x463, 0x464, 0x465, 0xF06, 0xF0C, 0xF0E, 0xF10}

// RFPowerRegs are the PA power registers setting the TX power changes,
// which a link layer sets again after every suspend (the Python
// RF_POWER_REGS); RFSuspendRegs are both lists
// (RF_SUSPEND_REGS).
var RFPowerRegs = []int{0x1225, 0x1226, 0x1227}

var RFSuspendRegs = append(append([]int{}, RFInitRegs...), RFPowerRegs...)

var rfSuspendSet = func() map[int]bool {
	s := map[int]bool{}
	for _, a := range RFSuspendRegs {
		s[a] = true
	}
	return s
}()

// SuspendRF is the Python SUSPEND_RF (TC32EMU_SUSPEND_RF): "keep" (the
// default) leaves the radio's registers through a suspend; "lost" sets
// RFSuspendRegs back to their values when the radio was attached and starts
// no command but 0x80 until each of them the firmware had written since the
// last reset has been written again.
var SuspendRF = suspendRFFromEnv()

func suspendRFFromEnv() string {
	v := os.Getenv("TC32EMU_SUSPEND_RF")
	if v == "" {
		return "keep"
	}
	if v != "keep" && v != "lost" {
		panic("TC32EMU_SUSPEND_RF: " + v)
	}
	return v
}

// RxBusy is the Python RX_BUSY (TC32EMU_RX_BUSY): 0x448 bit 5, which a link
// layer reads as "a packet is being received" (not documented). "off" (the
// default): the register reads as written; "air": 1 while a central packet
// is on air in an open BRX, from its start to its end; "stuck": always 1.
var RxBusy = func() string {
	v := os.Getenv("TC32EMU_RX_BUSY")
	if v == "" {
		return "off"
	}
	if v != "off" && v != "air" && v != "stuck" {
		panic("TC32EMU_RX_BUSY: " + v)
	}
	return v
}()

// TxFIFOClear is the Python TXFIFO_CLEAR (TC32EMU_TXFIFO_CLEAR): where
// writing 0xc2a with bit 4 leaves the TX FIFO pointers, which no document
// states. "position" (the default): the read pointer takes the write
// pointer's value and the write pointer stays; "zero": both are 0.
var TxFIFOClear = txFIFOClearFromEnv()

func txFIFOClearFromEnv() string {
	v := os.Getenv("TC32EMU_TXFIFO_CLEAR")
	if v == "" {
		return "position"
	}
	if v != "position" && v != "zero" {
		panic("TC32EMU_TXFIFO_CLEAR: " + v)
	}
	return v
}

// RFRefusal is one entry of Radio.RFRefused: a command the radio did not
// start because a suspend lost its setup (the Python rf_refused).
type RFRefusal struct {
	Ms   float64
	Cmd  byte
	Loss int
}

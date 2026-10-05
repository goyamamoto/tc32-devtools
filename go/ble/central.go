// SPDX-License-Identifier: Apache-2.0
package ble

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"math/big"
	"os"
	"strconv"

	"github.com/goyamamoto/tc32-devtools/go/ble/aes128"
	"github.com/goyamamoto/tc32-devtools/go/tc32emu"
)

// AttScript is the Python att_script generator: Next gets what the
// generator would be sent (nil at the start, the ATT response bytes, or a
// PairResult) and returns the next ATT request, or pair to start pairing,
// or done when the script is over.
type AttScript interface {
	Next(c *Central, value any) (req []byte, pair bool, done bool)
}

// PairResult is what the script gets after pairing.
type PairResult struct {
	OK     bool
	Reason byte
}

// Params are the connection parameters (the Python p dict).
type Params struct {
	Interval, Latency, Timeout, Hop, WinSize, WinOffset int
}

// Update is a pending LL_CONNECTION_UPDATE_IND (the Python update dict).
type Update struct {
	WinSize, WinOffset, Interval, Latency, Timeout, Instant int
	Applied                                                 float64
	IsApplied                                               bool
}

// ChmUpdate is a pending LL_CHANNEL_MAP_IND.
type ChmUpdate struct {
	Chm     []byte
	Instant int
	Applied float64
}

type inflight struct {
	llid    byte
	payload []byte
	plain   []byte
	tag     any
}

type queued struct {
	llid    byte
	payload []byte
	tag     any // QueueData's; nil for the central's own PDUs
}

// Enc is the encryption state (the Python enc dict).
type Enc struct {
	SK, IV    []byte
	Tx, Rx    bool
	TxC, RxC  int
	LTK       *big.Int
	SKDm, IVm []byte
	MicFail   int
}

// SMP is the pairing state (the Python smp dict).
type SMP struct {
	Preq, Pres      []byte
	T0              float64
	Keys            map[string]string
	Mrand, Sconfirm []byte
	Srand           []byte
	SconfirmOK      bool
	Failed          int
	DoneMs          float64
	SecurityRequest string
	Started         bool
	SentKeys        map[string]string // the central's IdKey, when it sent it
}

// CentralSkipEvery is the Python SKIP_EVERY (TC32EMU_CENTRAL_SKIP_EVERY): N > 0
// makes the central send nothing at every Nth connection event (k % N == 0)
// from event 20 on, so the peripheral's own timer ends those events; 0 (the
// default) never skips.
var CentralSkipEvery = func() int {
	v := os.Getenv("TC32EMU_CENTRAL_SKIP_EVERY")
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		panic("TC32EMU_CENTRAL_SKIP_EVERY: " + v)
	}
	return n
}()

// Central is a scripted BLE central: link layer, L2CAP, signaling, ATT client, SMP.
type Central struct {
	M                *tc32emu.Machine
	Radio            *Radio
	Log              func(string)
	P                Params
	ConnectAfterMs   float64
	AnchorInWindowMs float64
	AA               []byte // as sent (LSO first)
	AAReg            []byte // as a link layer writes 0x408..0x40b
	InitA            []byte // the address this central connects and scans from
	InitAType        int    // its TxAdd: 0 public, 1 random
	Identity         []byte // the public identity address, LSO first
	OwnAddr          string // "public", "rpa" or "rpa-wrong-irk"
	IRK              []byte
	InitKeyDist      byte // the Pairing Request's InitKeyDist: 0, or IdKey (0x02)
	ActiveScan       bool
	AckTerminate     bool // acknowledge the peripheral's LL_TERMINATE_IND before leaving
	State            string
	SN, NESN         int
	inflight         *inflight
	txq              []queued
	Event            int
	unmapped         int
	Chm              []byte
	chmUpdate        *ChmUpdate
	Transcript       []string
	l2               *l2frame
	MaxExch          int
	exch             int
	Stats            struct{ Events, Replies, Missed, Retx int }
	Records          map[string]any
	Channels         [][2]int
	Sent             [][2]float64 // (event, ms) of every packet the central started
	Ntf              []Notification
	AttLog           []AttEntry
	Script           AttScript
	attWaiting       bool
	attStarted       bool
	EventHooks       []func(*Central, int)
	SendVersion      bool
	SendFeatures     bool
	versionSent      bool
	silentFrom       int
	silent           bool
	AdvA             []byte
	AdvARandom       int
	AdvPDU           []byte
	Pair             bool
	ApplyUpdate      bool
	RefuseUpdate     bool                                  // the Python refuse_update
	OnParamRequest   func(req ParamRequest, action string) // the Python on_param_request
	DropAt           map[int]bool                          // events whose first non-empty reply the central loses
	Dropped          []Drop
	update           *Update
	rng              *pyRandom
	Enc              Enc
	SMP              SMP
	TConnEnd         float64
	Anchor0          float64
	IntervalMs       float64
	baseT            float64
	baseK            int
	CurCh            byte
	lastPlain        []byte
	FlashLogStart    int

	// Overridable steps (a Python subclass overrides the methods): they are
	// set to the defaults by NewCentral.
	OnAdvFn        func(c *Central, pdu []byte, ch byte) []byte
	StartPairingFn func(c *Central)
	ScriptStepFn   func(c *Central, value any)
	LLCtrlFn       func(c *Central, p []byte)

	// Establish is the Python establish: a connection with nothing heard by
	// event 6 was not established, State "failed". Heard: a reply of the
	// peripheral in this connection.
	Establish bool
	Heard     bool
	// MicEnds is the Python mic_ends: a packet of the peripheral that fails
	// its MIC ends the connection at once, State "mic-failed".
	MicEnds bool
	// LLRules is the Python ll_rules; encBusy: the central's LL_ENC_REQ is
	// under way; updateLead: the events the update's instant was given ahead.
	LLRules    bool
	encBusy    bool
	updateLead int

	// OnBond is the Python on_bond: called when the peripheral's keys are all
	// in, the central's own queued but not sent.
	OnBond func()

	// RxData and OnAck are the Python rx_data and on_ack, for another host
	// above this link layer: with RxData set the LL data PDUs received (LLID
	// 1, 2) go to it, not to the central's own L2CAP; OnAck hears the tag of
	// a PDU queued with QueueData when the peripheral has acknowledged it.
	RxData func(llid byte, payload []byte)
	OnAck  func(tag any)
}

type l2frame struct {
	ln   int
	cid  int
	data []byte
}

// Notification is a received Handle Value Notification/Indication.
type Notification struct {
	Ms     float64
	Handle int
	Value  string // hex, "(ind)" appended for an indication
}

// AttEntry is one line of the ATT log.
type AttEntry struct {
	Ms   float64
	Dir  string
	Name string
	Hex  string
}

// Drop is a reply the central deliberately lost.
type Drop struct {
	Ms    float64
	Event int
	Hex   string
}

// Options are Central's constructor arguments with the Python defaults.
type Options struct {
	Interval, Latency, Timeout, Hop, WinSize, WinOffset int
	ConnectAfterMs, AnchorInWindowMs                    float64
	Log                                                 func(string)
	MaxExch                                             int
	Pair, ApplyUpdate, RefuseUpdate                     bool
	OnParamRequest                                      func(req ParamRequest, action string)
	DropAt                                              []int
	Chm                                                 []byte
	AttScript                                           AttScript
	EventHooks                                          []func(*Central, int)
	SendVersion, SendFeatures                           bool
	// The Python join, aa (0: 0x71764129), active_scan, own_addr ("": "public"),
	// irk (nil: DefaultIRK) and dist_id.
	Join       bool
	AA         uint32
	ActiveScan bool
	OwnAddr    string
	IRK        []byte
	DistID     bool
	// NoTerminateAck is the Python ack_terminate=False: the central leaves at
	// the peripheral's LL_TERMINATE_IND without acknowledging it.
	NoTerminateAck bool
	// Establish, MicEnds and LLRules are the Python establish, mic_ends and ll_rules.
	Establish bool
	MicEnds   bool
	LLRules   bool
}

// DefaultIRK is the central's IRK, LSO first, as Identity Information carries it.
var DefaultIRK = []byte{0xA0, 0xA1, 0xA2, 0xA3, 0xA4, 0xA5, 0xA6, 0xA7, 0xA8, 0xA9, 0xAA, 0xAB, 0xAC, 0xAD, 0xAE, 0xAF}

// rpaN counts the resolvable private addresses made so far in this process.
var rpaN uint32

// DefaultOptions are the Python defaults.
func DefaultOptions() Options {
	return Options{Interval: 24, Latency: 0, Timeout: 500, Hop: 7, WinSize: 2, WinOffset: 0, ConnectAfterMs: 30,
		AnchorInWindowMs: 0.3, Log: func(s string) { fmt.Println(s) }, MaxExch: 4, Pair: true,
		Chm: []byte{0xff, 0xff, 0xff, 0xff, 0x1f}, SendVersion: true, SendFeatures: true}
}

// NewCentral makes a central on the radio and registers it there.
func NewCentral(m *tc32emu.Machine, radio *Radio, o Options) *Central {
	if o.OwnAddr == "" {
		o.OwnAddr = "public"
	}
	if o.OwnAddr != "public" && o.OwnAddr != "rpa" && o.OwnAddr != "rpa-wrong-irk" {
		panic("own_addr: " + o.OwnAddr)
	}
	if o.AA == 0 {
		o.AA = 0x71764129
	}
	if o.IRK == nil {
		o.IRK = DefaultIRK
	}
	c := &Central{M: m, Radio: radio, Log: o.Log,
		P:              Params{o.Interval, o.Latency, o.Timeout, o.Hop, o.WinSize, o.WinOffset},
		ConnectAfterMs: o.ConnectAfterMs, AnchorInWindowMs: o.AnchorInWindowMs,
		AA:       binary.LittleEndian.AppendUint32(nil, o.AA),
		AAReg:    binary.BigEndian.AppendUint32(nil, o.AA),
		Identity: []byte{0x66, 0x55, 0x44, 0x33, 0x22, 0x11}, OwnAddr: o.OwnAddr,
		IRK: append([]byte(nil), o.IRK...), ActiveScan: o.ActiveScan, AckTerminate: !o.NoTerminateAck,
		State: "scan", Event: -1,
		Chm: append([]byte(nil), o.Chm...), MaxExch: o.MaxExch, Records: map[string]any{},
		Script: o.AttScript, EventHooks: append([]func(*Central, int){}, o.EventHooks...),
		SendVersion: o.SendVersion, SendFeatures: o.SendFeatures, AdvARandom: 1, Pair: o.Pair,
		ApplyUpdate: o.ApplyUpdate, RefuseUpdate: o.RefuseUpdate, OnParamRequest: o.OnParamRequest,
		DropAt: map[int]bool{}, rng: newPyRandom(1)}
	if c.Log == nil {
		c.Log = func(string) {}
	}
	c.Establish, c.MicEnds, c.LLRules = o.Establish, o.MicEnds, o.LLRules
	for _, e := range o.DropAt {
		c.DropAt[e] = true
	}
	c.OnAdvFn = (*Central).onAdv
	c.StartPairingFn = (*Central).startPairing
	c.ScriptStepFn = (*Central).scriptStep
	c.LLCtrlFn = (*Central).llCtrl
	if o.DistID {
		c.InitKeyDist = 0x02
	}
	c.NewAddress()
	if o.Join {
		radio.Centrals = append(radio.Centrals, c)
	} else {
		radio.Central = c
	}
	return c
}

// NewAddress is the Python new_address: the public identity, or a resolvable
// private address with the next prand (OwnAddr "rpa"; "rpa-wrong-irk" hashes
// it with another IRK than the one DistID distributes).
func (c *Central) NewAddress() {
	if c.OwnAddr == "public" {
		c.InitA, c.InitAType = append([]byte(nil), c.Identity...), 0
		return
	}
	prand := (0x15A3C7 + 0x0B1D2F*rpaN) & 0x3FFFFF
	rpaN++
	key := append([]byte(nil), c.IRK...)
	if c.OwnAddr != "rpa" {
		for i := range key {
			key[i] ^= 0xFF
		}
	}
	c.InitA, c.InitAType = aes128.RPA(key, prand), 1
}

// ScanRequest is the Scanner method (the Python scan_request): with
// ActiveScan, a SCAN_REQ for a scannable advertising packet (ADV_IND,
// ADV_SCAN_IND) heard in state "scan"; else nil.
func (c *Central) ScanRequest(pdu []byte, ch byte) []byte {
	if !c.ActiveScan || c.State != "scan" || len(pdu) < 8 || (pdu[0]&0xF != 0 && pdu[0]&0xF != 6) {
		return nil
	}
	req := append([]byte{0x03 | byte(c.InitAType<<6) | (pdu[0]&0x40)<<1, 12}, c.InitA...)
	req = append(req, pdu[2:8]...)
	kind := "ADV_IND"
	if pdu[0]&0xF == 6 {
		kind = "ADV_SCAN_IND"
	}
	c.rec("air", fmt.Sprintf("ch %d <- %s %x", ch, kind, pdu))
	c.rec("air", fmt.Sprintf("ch %d -> SCAN_REQ %x", ch, req))
	return req
}

func round3(x float64) float64 { return math.Round(x*1000) / 1000 }

// ------------------------------------------------------------ advertising

// OnAdv is the Peer method: the reply to an ADV_IND.
func (c *Central) OnAdv(pdu []byte, ch byte) []byte { return c.OnAdvFn(c, pdu, ch) }

func (c *Central) onAdv(pdu []byte, ch byte) []byte {
	if c.State != "scan" || c.M.Ms() < c.ConnectAfterMs || pdu[0]&0xF != 0 || len(pdu) < 8 {
		return nil
	}
	c.AdvA = append([]byte(nil), pdu[2:8]...)
	c.AdvARandom = int(pdu[0]>>6) & 1 // the advertiser's TxAdd
	payload := append([]byte(nil), c.InitA...)
	payload = append(payload, c.AdvA...)
	payload = append(payload, c.AA...)
	payload = append(payload, 0x55, 0x44, 0x33, byte(c.P.WinSize))
	payload = binary.LittleEndian.AppendUint16(payload, uint16(c.P.WinOffset))
	payload = binary.LittleEndian.AppendUint16(payload, uint16(c.P.Interval))
	payload = binary.LittleEndian.AppendUint16(payload, uint16(c.P.Latency))
	payload = binary.LittleEndian.AppendUint16(payload, uint16(c.P.Timeout))
	payload = append(payload, c.Chm...)
	payload = append(payload, byte(c.P.Hop|(1<<5)))
	hdr := []byte{0x05 | byte(c.InitAType<<6) | byte(c.AdvARandom<<7), byte(len(payload))} // CONNECT_IND, RxAdd as advertised
	c.State = "connecting"
	c.AdvPDU = pdu
	c.rec("air", fmt.Sprintf("ch %d <- ADV_IND %x", ch, pdu))
	c.rec("air", fmt.Sprintf("ch %d -> CONNECT_IND %x interval %d latency %d timeout %d hop %d", ch,
		append(append([]byte(nil), hdr...), payload...), c.P.Interval, c.P.Latency, c.P.Timeout, c.P.Hop))
	return append(hdr, payload...)
}

// OnConnectSent is the Peer method: the CONNECT_IND has left the air.
func (c *Central) OnConnectSent(pdu []byte, tEnd float64) {
	c.State = "connected"
	c.Heard = false
	c.encBusy = false
	c.TConnEnd = tEnd
	// The products are rounded before they are added (float64()), as Python's.
	clk := c.clock()
	c.Anchor0 = tEnd + float64(1.25*clk) + float64(float64(1.25*clk)*float64(c.P.WinOffset)) + c.AnchorInWindowMs
	c.IntervalMs = float64(float64(c.P.Interval)*1.25) * clk
	c.baseT, c.baseK = c.Anchor0, 0
	// The central starts the link layer procedures a real one would.
	if c.SendVersion {
		c.QueueLL([]byte{0x0C, 0x0B, 0x0F, 0x00, 0x34, 0x12}) // LL_VERSION_IND 5.2, Broadcom, 0x1234
		c.versionSent = true
	}
	if c.SendFeatures {
		c.QueueLL([]byte{0x08, 0x01, 0, 0, 0, 0, 0, 0, 0}) // LL_FEATURE_REQ (encryption)
	}
	c.Radio.At(c.Anchor0+c.jitterMs(), func() { c.connEvent(0) }, "central")
}

// ------------------------------------------------------------ the air (air.go)

// clock is the central's time for one unit of the peripheral's: 1.0 without an air.
func (c *Central) clock() float64 {
	if c.Radio.Air == nil {
		return 1.0
	}
	return c.Radio.Air.Clock()
}

func (c *Central) jitterMs() float64 {
	if c.Radio.Air == nil {
		return 0.0
	}
	return c.Radio.Air.JitterMs()
}

// Reclock is the Python reclock: the radio's air changed during a connection:
// from the next event on, the interval is the new clock's. The next event
// keeps the time it has.
func (c *Central) Reclock() {
	if c.State != "connected" || c.Event < 0 {
		return
	}
	k := c.Event + 1
	c.baseT, c.baseK = c.baseT+float64(float64(k-c.baseK)*c.IntervalMs), k
	c.IntervalMs = float64(float64(c.P.Interval)*1.25) * c.clock()
}

// txNote is the transcript's note on a packet the peripheral did not take.
func (c *Central) txNote(ok bool) string {
	if ok {
		return ""
	}
	if c.Radio.TxLost {
		return "   (LOST on air)"
	}
	return "   (MISSED by the peripheral)"
}

// AirLost: the peripheral's reply was lost on air: nothing is received.
func (c *Central) AirLost(pdu []byte) {
	c.rec("rx", fmt.Sprintf("ev %4d ch %2d <- %s  (LOST on air)", c.Event, c.CurCh, Fmt(pdu)))
}

// AirLostTx: the central's packet after a reply in this event was lost on air.
func (c *Central) AirLostTx(pdu []byte) {
	c.rec("tx", fmt.Sprintf("ev %4d ch %2d -> the packet above  (LOST on air)", c.Event, c.CurCh))
}

// ------------------------------------------------------------ link layer

func (c *Central) chmUsed(ch int) bool { return (c.Chm[ch>>3]>>(ch&7))&1 != 0 }

// Channel is channel selection algorithm #1 (Core Vol 6 Part B 4.5.8.2).
func (c *Central) Channel() int {
	c.unmapped = (c.unmapped + c.P.Hop) % 37
	if c.chmUsed(c.unmapped) {
		return c.unmapped
	}
	var used []int
	for ch := 0; ch < 37; ch++ {
		if c.chmUsed(ch) {
			used = append(used, ch)
		}
	}
	return used[c.unmapped%len(used)]
}

func (c *Central) connEvent(k int) {
	if c.State != "connected" && c.State != "terminate-ack" {
		return
	}
	if c.Establish && k >= 6 && !c.Heard {
		c.State = "failed"
		c.Records["establish_failed_ms"] = round3(c.M.Ms())
		c.rec("tx", fmt.Sprintf("ev %4d nothing heard from the peripheral in 6 events: the connection was not"+
			" established", k))
		return
	}
	c.Event = k
	c.Stats.Events++
	if u := c.chmUpdate; u != nil && k == u.Instant {
		c.Chm = u.Chm
		u.Applied = round3(c.M.Ms())
		c.Records["chm_update_applied"] = map[string]any{"chm": hex.EncodeToString(u.Chm), "instant": u.Instant,
			"applied": u.Applied}
		c.chmUpdate = nil
	}
	ch := c.Channel()
	c.Channels = append(c.Channels, [2]int{k, ch})
	c.exch = 0
	c.CurCh = byte(ch)
	c.hookEvent(k)
	for _, h := range c.EventHooks {
		h(c, k)
	}
	if c.State != "connected" && c.State != "terminate-ack" {
		return
	}
	skipped := CentralSkipEvery > 0 && k >= 20 && k%CentralSkipEvery == 0
	if skipped {
		c.rec("tx", fmt.Sprintf("ev %4d ch %2d -> nothing (TC32EMU_CENTRAL_SKIP_EVERY)", k, ch))
	}
	silent := c.silent && k >= c.silentFrom
	if c.State == "terminate-ack" && (silent || !skipped) {
		// The peripheral's LL_TERMINATE_IND is acknowledged by this packet (NESN
		// advanced when it arrived), then the central leaves (Core Vol 6 Part B 5.1.6).
		if !silent {
			pdu := []byte{0x01 | byte(c.NESN<<2) | byte(c.SN<<3), 0}
			if c.inflight != nil {
				pdu = c.nextPDU()
			}
			c.Sent = append(c.Sent, [2]float64{float64(k), c.M.Ms()})
			ok := c.Radio.CentralTx(pdu, byte(ch), c.AAReg)
			c.rec("tx", fmt.Sprintf("ev %4d ch %2d -> %s  (acknowledges LL_TERMINATE_IND)%s", k, ch, Fmt(pdu), c.txNote(ok)))
			c.Records["terminate_ack_ms"] = round3(c.M.Ms())
		}
		c.State = "terminated"
		return
	}
	if !silent && !skipped {
		pdu := c.nextPDU()
		c.Sent = append(c.Sent, [2]float64{float64(k), c.M.Ms()})
		ok := c.Radio.CentralTx(pdu, byte(ch), c.AAReg)
		c.rec("tx", fmt.Sprintf("ev %4d ch %2d -> %s%s", k, ch, c.fmtTx(pdu), c.txNote(ok)))
		if !ok && !c.Radio.TxLost {
			c.Stats.Missed++
		}
	}
	nxt := c.baseT + float64(float64(k+1-c.baseK)*c.IntervalMs)
	if u := c.update; u != nil && !u.IsApplied && k+1 == u.Instant {
		// Core Vol 6 Part B 5.1.1: the transmit window starts WinOffset after the
		// anchor the old parameters would give the instant.
		clk := c.clock()
		c.baseT, c.baseK = nxt+float64(float64(1.25*clk)*float64(u.WinOffset))+c.AnchorInWindowMs, k+1
		c.IntervalMs = float64(float64(u.Interval)*1.25) * clk
		c.P.Interval, c.P.Latency, c.P.Timeout = u.Interval, u.Latency, u.Timeout
		u.Applied, u.IsApplied = round3(c.baseT), true
		c.Records["conn_update_applied"] = *u
		nxt = c.baseT
	}
	c.Radio.At(nxt+c.jitterMs(), func() { c.connEvent(k + 1) }, "central")
}

// nextPDU: the packet to send: the one in flight again, or the next queued
// one (encrypted once, when it is taken, so retransmissions repeat it).
func (c *Central) nextPDU() []byte {
	if c.inflight == nil {
		llid, payload, tag := c.takeTagged()
		plain := payload
		if c.Enc.Tx && len(payload) > 0 {
			nonce := append(aes128.BytesLE(big.NewInt(int64(c.Enc.TxC)|1<<39), 5), c.Enc.IV...)
			payload = aes128.CCMEncrypt(c.Enc.SK, nonce, llid, payload)
			c.Enc.TxC++
		}
		c.inflight = &inflight{llid, payload, plain, tag}
	} else {
		c.Stats.Retx++
	}
	f := c.inflight
	md := 0
	if c.more() {
		md = 1
	}
	pdu := append([]byte{f.llid | byte(c.NESN<<2) | byte(c.SN<<3) | byte(md<<4), byte(len(f.payload))}, f.payload...)
	if string(f.plain) == string(f.payload) {
		c.lastPlain = nil
	} else {
		c.lastPlain = append([]byte{pdu[0], byte(len(f.plain))}, f.plain...)
	}
	return pdu
}

// encPDU: a queued PDU the central may send while its LL_ENC_REQ is under
// way: LL_START_ENC_RSP, LL_TERMINATE_IND.
func encPDU(q queued) bool {
	return q.llid == 3 && len(q.payload) > 0 && (q.payload[0] == 0x06 || q.payload[0] == 0x02)
}

// take is the Python take: the next queued PDU to send, or an empty one
// (LLRules: ble_central.py's docstring).
func (c *Central) take() (byte, []byte) {
	llid, payload, _ := c.takeTagged()
	return llid, payload
}

// takeTagged is the Python take_tagged: take, and with the PDU the tag
// QueueData gave it (nil for any other).
func (c *Central) takeTagged() (byte, []byte, any) {
	if !c.LLRules {
		if len(c.txq) == 0 {
			return 1, []byte{}, nil
		}
		q := c.txq[0]
		c.txq = c.txq[1:]
		return q.llid, q.payload, q.tag
	}
	if c.encBusy {
		for i, q := range c.txq {
			if encPDU(q) {
				c.txq = append(c.txq[:i:i], c.txq[i+1:]...)
				return q.llid, q.payload, nil
			}
		}
		return 1, []byte{}, nil
	}
	if len(c.txq) == 0 {
		return 1, []byte{}, nil
	}
	q := c.txq[0]
	c.txq = c.txq[1:]
	if q.llid == 3 && len(q.payload) > 0 && q.payload[0] == 0x03 {
		c.encBusy = true
	} else if q.llid == 3 && len(q.payload) > 0 && q.payload[0] == 0x00 && c.update != nil && !c.update.IsApplied {
		c.update.Instant = (c.Event + c.updateLead) & 0xFFFF
		p := append([]byte(nil), q.payload[:10]...)
		q.payload = binary.LittleEndian.AppendUint16(p, uint16(c.update.Instant))
	}
	return q.llid, q.payload, q.tag
}

// more: whether a PDU waits behind the one in flight (the MD bit).
func (c *Central) more() bool {
	if c.LLRules && c.encBusy {
		for _, q := range c.txq {
			if encPDU(q) {
				return true
			}
		}
		return false
	}
	return len(c.txq) > 0
}

// OnAir is the Peer method: the peripheral's reply; the next PDU to send in this event, or nil.
func (c *Central) OnAir(pdu []byte, ch byte, txAddr uint32) []byte {
	c.Stats.Replies++
	c.Heard = true
	hdr0 := pdu[0]
	llid, nesn, sn, md := hdr0&3, int(hdr0>>2)&1, int(hdr0>>3)&1, int(hdr0>>4)&1
	acked := nesn != c.SN
	if acked {
		c.SN ^= 1
		if c.inflight != nil && c.inflight.llid == 3 && len(c.inflight.plain) > 0 && c.inflight.plain[0] == 0x02 {
			c.State = "terminated" // our LL_TERMINATE_IND got through
			c.Records["terminate_acked_ms"] = round3(c.M.Ms())
		}
		if c.inflight != nil && c.inflight.tag != nil && c.OnAck != nil {
			c.OnAck(c.inflight.tag)
		}
		c.inflight = nil
	}
	isNew := sn == c.NESN
	if isNew {
		c.NESN ^= 1
	}
	payload, shown, note := pdu[2:2+int(pdu[1])], pdu, ""
	if isNew && pdu[1] != 0 && c.Enc.Rx {
		nonce := append(aes128.BytesLE(big.NewInt(int64(c.Enc.RxC)), 5), c.Enc.IV...)
		var ok bool
		payload, ok = aes128.CCMDecrypt(c.Enc.SK, nonce, hdr0, payload)
		c.Enc.RxC++
		shown = append([]byte{hdr0, byte(len(payload))}, payload...)
		if ok {
			note = "  (decrypted)"
		} else {
			note = "  (decrypted, MIC FAILED)"
			c.Enc.MicFail++
		}
	}
	dup, nack := "", ""
	if !isNew {
		dup = "  (dup)"
	}
	if !acked {
		nack = "  (our packet not acked)"
	}
	c.rec("rx", fmt.Sprintf("ev %4d ch %2d <- %s%s%s%s", c.Event, ch, Fmt(shown), note, dup, nack))
	if c.MicEnds && note == "  (decrypted, MIC FAILED)" {
		c.State = "mic-failed"
		c.Records["mic_failure_ms"] = round3(c.M.Ms())
		c.rec("rx", fmt.Sprintf("ev %4d the central leaves the connection: MIC failure (0x3d)", c.Event))
		return nil
	}
	if isNew && pdu[1] != 0 {
		c.rxPDU(llid, payload)
	}
	c.exch++
	if c.State != "connected" {
		return nil
	}
	if (md != 0 || c.more() || c.inflight != nil) && c.exch < c.MaxExch {
		nxt := c.nextPDU()
		c.rec("tx", fmt.Sprintf("ev %4d ch %2d -> %s", c.Event, ch, c.fmtTx(nxt)))
		return nxt
	}
	return nil
}

// Drop is the Peer method: whether the central loses this reply.
func (c *Central) Drop(pdu []byte) bool {
	if c.DropAt[c.Event] && pdu[1] != 0 {
		delete(c.DropAt, c.Event)
		c.Dropped = append(c.Dropped, Drop{round3(c.M.Ms()), c.Event, hex.EncodeToString(pdu)})
		c.rec("rx", fmt.Sprintf("ev %4d ch %2d <- %s  (LOST: the central ignores it)", c.Event, c.CurCh, Fmt(pdu)))
		return true
	}
	return false
}

func (c *Central) fmtTx(pdu []byte) string {
	if c.lastPlain != nil {
		return Fmt(c.lastPlain) + "  (sent encrypted)"
	}
	return Fmt(pdu)
}

// QueueLL queues an LL control PDU.
func (c *Central) QueueLL(payload []byte) { c.txq = append(c.txq, queued{3, payload, nil}) }

// QueueData is the Python queue_data: one LL data PDU of another host (LLID 2
// starts an L2CAP frame, 1 goes on with it), at most 27 octets; OnAck(tag) is
// called when the peripheral acknowledges it.
func (c *Central) QueueData(llid byte, payload []byte, tag any) {
	c.txq = append(c.txq, queued{llid, append([]byte(nil), payload...), tag})
}

// QueueL2CAP queues an L2CAP frame, fragmented into 27-byte PDUs.
func (c *Central) QueueL2CAP(cid int, data []byte) {
	frame := binary.LittleEndian.AppendUint16(nil, uint16(len(data)))
	frame = binary.LittleEndian.AppendUint16(frame, uint16(cid))
	frame = append(frame, data...)
	first := true
	for len(frame) > 0 {
		n := 27
		if len(frame) < n {
			n = len(frame)
		}
		llid := byte(1)
		if first {
			llid = 2
		}
		c.txq = append(c.txq, queued{llid, append([]byte(nil), frame[:n]...), nil})
		frame = frame[n:]
		first = false
	}
}

// ------------------------------------------------------------ LL procedures a test starts

// ChannelMapUpdate sends LL_CHANNEL_MAP_IND; the central switches at the instant.
func (c *Central) ChannelMapUpdate(chm []byte, instant int) {
	c.chmUpdate = &ChmUpdate{Chm: append([]byte(nil), chm...), Instant: instant & 0xFFFF}
	p := append([]byte{0x01}, chm...)
	c.QueueLL(binary.LittleEndian.AppendUint16(p, uint16(instant&0xFFFF)))
}

// ConnectionUpdate sends LL_CONNECTION_UPDATE_IND; the central moves at the instant.
func (c *Central) ConnectionUpdate(winSize, winOffset, interval, latency, timeout, instant int) {
	c.update = &Update{winSize, winOffset, interval, latency, timeout, instant & 0xFFFF, 0, false}
	c.updateLead = (instant - c.Event) & 0xFFFF
	p := []byte{0x00, byte(winSize)}
	for _, v := range []int{winOffset, interval, latency, timeout, instant & 0xFFFF} {
		p = binary.LittleEndian.AppendUint16(p, uint16(v))
	}
	c.QueueLL(p)
}

// Terminate sends LL_TERMINATE_IND; the central leaves once it is acknowledged.
func (c *Central) Terminate(reason byte) { c.QueueLL([]byte{0x02, reason}) }

// GoSilent: from this event on the central sends nothing (it went away).
func (c *Central) GoSilent(event int) { c.silentFrom, c.silent = event, true }

func (c *Central) rxPDU(llid byte, payload []byte) {
	switch {
	case llid == 3:
		c.LLCtrlFn(c, payload)
	case c.RxData != nil:
		c.RxData(llid, payload)
	case llid == 2:
		ln, cid := int(binary.LittleEndian.Uint16(payload)), int(binary.LittleEndian.Uint16(payload[2:]))
		c.l2 = &l2frame{ln, cid, append([]byte(nil), payload[4:]...)}
		c.l2Check()
	case llid == 1 && c.l2 != nil:
		c.l2.data = append(c.l2.data, payload...)
		c.l2Check()
	}
}

func (c *Central) l2Check() {
	if len(c.l2.data) >= c.l2.ln {
		f := c.l2
		c.l2 = nil
		c.l2cap(f.cid, f.data[:f.ln])
	}
}

func appendRecord(c *Central, key string, v any) {
	list, _ := c.Records[key].([]any)
	c.Records[key] = append(list, v)
}

func llName(op byte) string {
	if n, ok := LLNames[op]; ok {
		return n
	}
	return fmt.Sprintf("0x%x", op)
}

func attName(op byte) string {
	if n, ok := ATTNames[op]; ok {
		return n
	}
	return fmt.Sprintf("0x%x", op)
}

func u16s(p []byte, n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = int(binary.LittleEndian.Uint16(p[2*i:]))
	}
	return out
}

func (c *Central) llCtrl(p []byte) {
	op := p[0]
	appendRecord(c, "ll_from_peripheral", []any{round3(c.M.Ms()), llName(op), hex.EncodeToString(p)})
	switch {
	case op == 0x0C:
		c.Records["peripheral_version"] = map[string]any{"VersNr": int(p[1]),
			"CompId": fmt.Sprintf("0x%x", int(p[2])|int(p[3])<<8), "SubVersNr": fmt.Sprintf("0x%x", int(p[4])|int(p[5])<<8)}
		if !c.versionSent {
			c.QueueLL([]byte{0x0C, 0x0B, 0x0F, 0x00, 0x34, 0x12})
			c.versionSent = true
		}
	case op == 0x09:
		c.Records["peripheral_features"] = hex.EncodeToString(p[1:9])
	case op == 0x0E:
		c.Records["peripheral_features_req"] = hex.EncodeToString(p[1:9])
		c.QueueLL([]byte{0x09, 0x01, 0, 0, 0, 0, 0, 0, 0})
	case op == 0x14:
		c.Records["peripheral_length_req"] = u16s(p[1:], 4)
		c.QueueLL([]byte{0x15, 27, 0, 0x48, 0x01, 27, 0, 0x48, 0x01})
	case op == 0x15:
		c.Records["peripheral_length_rsp"] = u16s(p[1:], 4)
	case op == 0x0F:
		c.Records["peripheral_conn_param_req"] = hex.EncodeToString(p[1:])
		c.QueueLL([]byte{0x07, 0x0F})
	case op == 0x12:
		c.QueueLL([]byte{0x13})
	case op == 0x16:
		c.Records["peripheral_phy_req"] = hex.EncodeToString(p[1:3])
		c.QueueLL([]byte{0x07, 0x16})
	case op == 0x02:
		c.Records["peripheral_terminate"] = hex.EncodeToString(p)
		c.Records["peripheral_terminate_ms"] = round3(c.M.Ms())
		if c.AckTerminate {
			c.State = "terminate-ack"
		} else {
			c.State = "terminated"
		}
	case op == 0x04 && c.Enc.SKDm == nil: // an LL_ENC_REQ a test sent by hand
		appendRecord(c, "peripheral_enc_rsp", hex.EncodeToString(p))
	case op == 0x04: // LL_ENC_RSP: SKDs, IVs
		skd := aes128.FromLE(append(append([]byte(nil), c.Enc.SKDm...), p[1:9]...))
		c.Enc.SK = aes128.Bytes128(aes128.E(c.Enc.LTK, skd))
		c.Enc.IV = append(append([]byte(nil), c.Enc.IVm...), p[9:13]...)
	case op == 0x05: // LL_START_ENC_REQ (sent unencrypted)
		c.Enc.Tx, c.Enc.Rx = true, true
		c.txq = append([]queued{{3, []byte{0x06}, nil}}, c.txq...) // LL_START_ENC_RSP, the first encrypted packet
	case op == 0x06: // the peripheral's LL_START_ENC_RSP (encrypted)
		c.Records["encryption_on_ms"] = round3(c.M.Ms())
		c.encBusy = false
	case op == 0x0D || op == 0x11:
		appendRecord(c, "peripheral_reject", hex.EncodeToString(p))
		c.encBusy = false
	case op == 0x07:
		appendRecord(c, "peripheral_unknown_rsp", hex.EncodeToString(p))
	case op == 0x13:
		appendRecord(c, "peripheral_ping_rsp", round3(c.M.Ms()))
	default:
		c.QueueLL([]byte{0x07, op})
	}
}

// ------------------------------------------------------------ L2CAP

func (c *Central) l2cap(cid int, data []byte) {
	switch cid {
	case 4:
		c.attRx(data)
	case 5:
		code, ident := data[0], data[1]
		appendRecord(c, "signaling_from_peripheral", []any{round3(c.M.Ms()), hex.EncodeToString(data)})
		switch code {
		case 0x12:
			v := u16s(data[4:], 4)
			mn, mx, lat, to := v[0], v[1], v[2], v[3]
			c.Records["l2cap_conn_param_update_req"] = map[string]any{"t_ms": round3(c.M.Ms()), "event": c.Event,
				"interval_min": mn, "interval_max": mx, "latency": lat, "timeout": to}
			req := ParamRequest{TMs: round3(c.M.Ms()), Event: c.Event, IntervalMin: int(mn), IntervalMax: int(mx),
				Latency: int(lat), Timeout: int(to), Instant: -1}
			action := "accepted"
			if c.RefuseUpdate {
				c.QueueL2CAP(5, []byte{0x13, ident, 2, 0, 1, 0})
				action = "refused"
			} else {
				c.QueueL2CAP(5, []byte{0x13, ident, 2, 0, 0, 0})
				if c.ApplyUpdate && c.update == nil {
					c.ConnectionUpdate(1, 0, mx, lat, to, c.Event+12)
					action, req.Instant = "applied", c.update.Instant
				}
			}
			if c.OnParamRequest != nil {
				c.OnParamRequest(req, action)
			}
		case 0x13:
		default:
			c.QueueL2CAP(5, []byte{0x01, ident, 2, 0, 0, 0})
		}
	case 6:
		appendRecord(c, "smp_from_peripheral", []any{round3(c.M.Ms()), hex.EncodeToString(data)})
		c.smpRx(data)
	default:
		appendRecord(c, "l2cap_other", []any{cid, hex.EncodeToString(data)})
	}
}

// ------------------------------------------------------------ ATT

// AttSend sends an ATT PDU.
func (c *Central) AttSend(pdu []byte) {
	c.AttLog = append(c.AttLog, AttEntry{round3(c.M.Ms()), "->", attName(pdu[0]), hex.EncodeToString(pdu)})
	c.QueueL2CAP(4, pdu)
}

func (c *Central) attRx(data []byte) {
	op := data[0]
	c.AttLog = append(c.AttLog, AttEntry{round3(c.M.Ms()), "<-", attName(op), hex.EncodeToString(data)})
	switch {
	case op == 0x1B:
		c.Ntf = append(c.Ntf, Notification{round3(c.M.Ms()), int(data[1]) | int(data[2])<<8, hex.EncodeToString(data[3:])})
		return
	case op == 0x1D:
		c.Ntf = append(c.Ntf, Notification{round3(c.M.Ms()), int(data[1]) | int(data[2])<<8, hex.EncodeToString(data[3:]) + " (ind)"})
		c.AttSend([]byte{0x1E})
		return
	case op == 0x02: // the server exchanges MTU
		c.Records["peripheral_mtu_req"] = int(data[1]) | int(data[2])<<8
		c.AttSend([]byte{0x03, 247, 0})
		return
	case op == 0x04 || op == 0x08 || op == 0x0A || op == 0x0C || op == 0x10 || op == 0x12: // other server requests: not supported
		c.AttSend([]byte{0x01, op, 0, 0, 0x06})
		return
	}
	if c.attWaiting {
		c.attWaiting = false
		c.ScriptStepFn(c, data)
	}
}

// ScriptStep feeds the ATT script (the Python script_step).
func (c *Central) ScriptStep(value any) { c.ScriptStepFn(c, value) }

func (c *Central) scriptStep(value any) {
	if c.Script == nil {
		return
	}
	req, pair, done := c.Script.Next(c, value)
	if done {
		return
	}
	if pair {
		c.StartPairingFn(c)
		return
	}
	c.attWaiting = true
	c.AttSend(req)
}

// ------------------------------------------------------------ SMP (legacy Just Works) and LL encryption

func (c *Central) smpSend(d []byte) { c.QueueL2CAP(6, d) }

// StartPairing starts SMP legacy Just Works pairing (the Python start_pairing).
func (c *Central) StartPairing() { c.StartPairingFn(c) }

func (c *Central) startPairing() {
	// IO NoInputNoOutput, no OOB, AuthReq bonding, 16-octet keys, initiator
	// InitKeyDist (nothing, or IdKey with DistID), responder EncKey | IdKey.
	c.SMP = SMP{Preq: []byte{0x01, 0x03, 0x00, 0x01, 0x10, c.InitKeyDist, 0x03}, T0: round3(c.M.Ms()), Keys: map[string]string{},
		Started: true}
	c.FlashLogStart = len(c.M.Flash.Log)
	c.smpSend(c.SMP.Preq)
}

func (c *Central) smpRx(d []byte) {
	code, s := d[0], &c.SMP
	switch {
	case code == 0x02: // Pairing Response
		s.Pres = append([]byte(nil), d[:7]...)
		s.Mrand = c.rng.bytes(16)
		mc := aes128.C1(big.NewInt(0), aes128.FromLE(s.Mrand), s.Preq, s.Pres, c.InitAType, c.InitA, c.AdvARandom, c.AdvA)
		c.smpSend(append([]byte{0x03}, aes128.BytesLE(mc, 16)...))
	case code == 0x03: // Pairing Confirm
		s.Sconfirm = append([]byte(nil), d[1:17]...)
		c.smpSend(append([]byte{0x04}, s.Mrand...))
	case code == 0x04: // Pairing Random
		s.Srand = append([]byte(nil), d[1:17]...)
		want := aes128.C1(big.NewInt(0), aes128.FromLE(s.Srand), s.Preq, s.Pres, c.InitAType, c.InitA, c.AdvARandom, c.AdvA)
		s.SconfirmOK = string(aes128.BytesLE(want, 16)) == string(s.Sconfirm)
		stk := aes128.S1(big.NewInt(0), aes128.FromLE(s.Srand), aes128.FromLE(s.Mrand))
		c.Enc.LTK = stk
		c.Enc.SKDm = c.rng.bytes(8)
		c.Enc.IVm = c.rng.bytes(4)
		p := append([]byte{0x03}, make([]byte, 10)...) // LL_ENC_REQ: Rand 0, EDIV 0
		p = append(p, c.Enc.SKDm...)
		c.QueueLL(append(p, c.Enc.IVm...))
	case code == 0x05: // Pairing Failed
		s.Failed = int(d[1])
		c.ScriptStepFn(c, PairResult{false, d[1]})
	case code >= 0x06 && code <= 0x09:
		s.Keys[map[byte]string{6: "LTK", 7: "EDIV_Rand", 8: "IRK", 9: "identity_address"}[code]] = hex.EncodeToString(d[1:])
		if code == 0x09 {
			// The responder's keys come first (Core Vol 3 Part H 3.6.1); then the
			// central's IdKey, when it offered it and the response kept it.
			if len(s.Pres) >= 6 && s.Preq[5]&s.Pres[5]&0x02 != 0 {
				c.smpSend(append([]byte{0x08}, c.IRK...))
				c.smpSend(append([]byte{0x09, 0x00}, c.Identity...))
				s.SentKeys = map[string]string{"IRK": hex.EncodeToString(c.IRK),
					"identity_address": hex.EncodeToString(append([]byte{0}, c.Identity...))}
			}
			s.DoneMs = round3(c.M.Ms())
			if c.OnBond != nil {
				c.OnBond()
			}
			c.ScriptStepFn(c, PairResult{true, 0})
		}
	case code == 0x0B:
		s.SecurityRequest = hex.EncodeToString(d)
	}
}

func (c *Central) hookEvent(k int) {
	if c.Script != nil && !c.attStarted && k >= 4 {
		c.attStarted = true
		c.ScriptStepFn(c, nil)
	}
}

// ------------------------------------------------------------ output

func (c *Central) rec(kind, text string) {
	line := fmt.Sprintf("[%9.3f] %-3s %s", c.M.Ms(), kind, text)
	c.Transcript = append(c.Transcript, line)
	c.Log(line)
}

// Fmt describes a data channel PDU as the Python Central.fmt does.
func Fmt(pdu []byte) string {
	hdr0, ln := pdu[0], int(pdu[1])
	llid := hdr0 & 3
	bits := fmt.Sprintf("NESN%d SN%d MD%d", (hdr0>>2)&1, (hdr0>>3)&1, (hdr0>>4)&1)
	p := pdu[2 : 2+ln]
	var what string
	switch {
	case llid == 1 && ln == 0:
		what = "empty"
	case llid == 3:
		what = llName(p[0]) + " " + hex.EncodeToString(p[1:])
	case llid == 2 && ln >= 4:
		l2len, cid := int(binary.LittleEndian.Uint16(p)), int(binary.LittleEndian.Uint16(p[2:]))
		body := p[4:]
		switch {
		case cid == 4 && len(body) > 0:
			what = fmt.Sprintf("ATT %s %x", attName(body[0]), body)
		case cid == 5:
			what = fmt.Sprintf("L2CAP signaling %x", body)
		case cid == 6:
			what = fmt.Sprintf("SMP %x", body)
		default:
			what = fmt.Sprintf("L2CAP cid %d len %d %x", cid, l2len, body)
		}
	default:
		what = fmt.Sprintf("LLID%d %x", llid, p)
	}
	return fmt.Sprintf("[%s len %2d] %s", bits, ln, what)
}

// ParamRequest is the Python on_param_request's req: the peripheral's L2CAP
// Connection Parameter Update Request as the central received it, and the
// update's instant when the central applies it (-1 when it does not).
type ParamRequest struct {
	TMs                                        float64
	Event                                      int
	IntervalMin, IntervalMax, Latency, Timeout int
	Instant                                    int
}

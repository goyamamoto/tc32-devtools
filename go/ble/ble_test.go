// SPDX-License-Identifier: Apache-2.0
package ble

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/goyamamoto/tc32-devtools/go/ble/aes128"
	"github.com/goyamamoto/tc32-devtools/go/tc32emu"
)

// The radio, reset and central checks of checks/ble_models_check.py, on a
// machine that is never run (only its registers, memory and clock).

type recorder struct{ replies [][]byte }

func (r *recorder) OnAdv([]byte, byte) []byte     { return nil }
func (r *recorder) OnConnectSent([]byte, float64) {}
func (r *recorder) Drop([]byte) bool              { return false }
func (r *recorder) OnAir(pdu []byte, _ byte, _ uint32) []byte {
	r.replies = append(r.replies, pdu)
	return nil
}

func machine(t *testing.T) *tc32emu.Machine {
	fl := tc32emu.NewFlash(tc32emu.FlashSize)
	fl.Mem[8] = 0x4B // the boot ROM's bootable-slot mark
	m, err := tc32emu.NewMachine(fl, -1, 1)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func runUntil(t *testing.T, m *tc32emu.Machine, ms float64) {
	for m.Ms() < ms {
		m.Cycles += m.CPUHz / 100_000 // 10 us
		if err := m.UpdateTime(); err != nil {
			t.Fatal(err)
		}
	}
}

func w(t *testing.T, m *tc32emu.Machine, o, size int, v uint32) {
	if err := m.RegWrite(o, size, v); err != nil {
		t.Fatal(err)
	}
}

func rd(t *testing.T, m *tc32emu.Machine, o, size int) uint32 {
	v, err := m.RegRead(o, size)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestRadio(t *testing.T) {
	m := machine(t)
	r := NewRadio(m, nil)
	w(t, m, 0x74A, 1, 0xC1|0x02) // the system timer on

	r.SetStatus(0x21)
	w(t, m, 0xF20, 2, 0x0001)
	if v := rd(t, m, 0xF20, 2); v != 0x0020 {
		t.Fatalf("RF status write-one-to-clear: %#04x", v)
	}

	empty, data, rxbuf := uint32(0x840100), uint32(0x840140), uint32(0x840200)
	for i, v := range []byte{2, 0, 0, 0, 0x01, 0} { // an empty PDU
		m.Write(empty+uint32(i), 1, uint32(v))
	}
	for i, v := range append([]byte{5, 0, 0, 0, 0x02, 3}, []byte("abc")...) { // LLID 2, 3 octets
		m.Write(data+uint32(i), 1, uint32(v))
	}
	w(t, m, 0xC2A, 1, 0x10)
	w(t, m, 0xC2C, 2, empty&0xFFFF)
	w(t, m, 0xC2C, 2, data&0xFFFF)
	fifo := [2]uint32{rd(t, m, 0xC2A, 1), rd(t, m, 0xC2B, 1)}
	w(t, m, 0xC2A, 1, 0x40|1)
	set1 := rd(t, m, 0xC2A, 1)
	w(t, m, 0xC2A, 1, 0x20)
	step := rd(t, m, 0xC2A, 1)
	w(t, m, 0xC2A, 1, 0x40|0)
	if fifo != [2]uint32{0, 2} || set1 != 1 || step != 2 || rd(t, m, 0xC2A, 1) != 0 {
		t.Fatalf("TX FIFO: %v set1 %d step %d", fifo, set1, step)
	}

	aa := []byte{0x71, 0x76, 0x41, 0x29} // 0x408..0x40b as written
	for i, v := range aa {
		w(t, m, 0x408+i, 1, uint32(v))
	}
	w(t, m, 0x40D, 1, 7)
	w(t, m, 0xF03, 1, 0x10) // last SN 1, NESN 0
	w(t, m, 0xC08, 2, rxbuf&0xFFFF)
	w(t, m, 0xC0C, 2, empty&0xFFFF)
	start := (m.StimerNow() + 1600) & 0xFFFFFFFF // 100 us ahead
	w(t, m, 0xF18, 4, start)
	w(t, m, 0xF16, 1, rd(t, m, 0xF16, 1)|4)
	w(t, m, 0xF00, 1, 0x82)
	rec := &recorder{}
	r.Central = rec
	pdu := []byte{0x01, 0} // empty PDU, NESN 0, SN 0
	early := r.CentralTx(pdu, 7, aa)
	t0 := m.Ms() + 0.2
	runUntil(t, m, t0)
	anchor := m.StimerNow()
	ok := r.CentralTx(pdu, 7, aa)
	runUntil(t, m, t0+1.0)
	if early || len(r.Misses) == 0 || r.Misses[0].Why != "BRX scheduled later" || !ok {
		t.Fatalf("BRX at 0xf18: early %v misses %v ok %v", early, r.Misses, ok)
	}
	ent := make([]byte, 20)
	for i := range ent {
		v, _ := m.Read(rxbuf+uint32(i), 1)
		ent[i] = byte(v)
	}
	ts := binary.LittleEndian.Uint32(ent[9:])
	if binary.LittleEndian.Uint32(ent) != 13 || string(ent[4:6]) != string(pdu) || (ts-anchor)&0xFFFFFFFF != 0x500 {
		t.Fatalf("RX DMA entry: %x, ts-anchor %#x", ent, (ts-anchor)&0xFFFFFFFF)
	}
	if len(rec.replies) != 1 || hex.EncodeToString(rec.replies[0]) != "0603616263" || rd(t, m, 0xC2A, 1) != 1 ||
		rd(t, m, 0xF22, 1)&1 != 0 || rd(t, m, 0xF23, 1) != 0x10 {
		t.Fatalf("ack and reply: %x rptr %d f22 %#x f23 %#x", rec.replies, rd(t, m, 0xC2A, 1), rd(t, m, 0xF22, 1),
			rd(t, m, 0xF23, 1))
	}
	before := len(r.LateStarts)
	w(t, m, 0xF18, 4, (m.StimerNow()-1600)&0xFFFFFFFF) // 100 us ago
	w(t, m, 0xF00, 1, 0x82)
	if before != 0 || len(r.LateStarts) != 1 || r.LateStarts[0][1] < 99 || r.LateStarts[0][1] > 101 || r.Brx == nil {
		t.Fatalf("a BRX command written after its start tick: late starts %d -> %v, BRX %v", before, r.LateStarts, r.Brx)
	}
}

func TestRadioReset(t *testing.T) {
	m := machine(t)
	r := NewRadio(m, nil)
	var ran []string
	r.At(5.0, func() { ran = append(ran, "radio") }, "radio")
	r.At(5.0, func() { ran = append(ran, "central") }, "central")
	w(t, m, 0xC2A, 1, 0x10)
	w(t, m, 0xC2C, 2, 0x0100)
	w(t, m, 0xF00, 1, 0x82)
	before := [3]int{int(rd(t, m, 0xC2B, 1)), btoi(r.Brx != nil), len(r.sched)}
	if err := m.Reset(0, false); err != nil {
		t.Fatal(err)
	}
	after := [3]int{int(rd(t, m, 0xC2B, 1)), btoi(r.Brx != nil), len(r.sched)}
	w(t, m, 0x74A, 1, 0xC1|0x02)
	runUntil(t, m, 6.0)
	if before != [3]int{1, 1, 2} || after != [3]int{0, 0, 1} || len(ran) != 1 || ran[0] != "central" {
		t.Fatalf("reset: %v -> %v, ran %v", before, after, ran)
	}
}

// TC32EMU_TXFIFO_CLEAR: after three appends and two steps, a clear leaves both
// pointers at 3 ("position", the default) or at 0 ("zero").
func TestFIFOClear(t *testing.T) {
	given := TxFIFOClear
	defer func() { TxFIFOClear = given }()
	if _, set := os.LookupEnv("TC32EMU_TXFIFO_CLEAR"); !set && given != "position" {
		t.Fatalf("TX FIFO clear: the default is %s", given)
	}
	for _, c := range []struct {
		mode string
		want [2]uint32
	}{{"position", [2]uint32{3, 3}}, {"zero", [2]uint32{0, 0}}} {
		TxFIFOClear = c.mode
		m := machine(t)
		NewRadio(m, nil)
		w(t, m, 0xC2A, 1, 0x10)
		for i := 0; i < 3; i++ {
			w(t, m, 0xC2C, 2, 0x0100)
		}
		w(t, m, 0xC2A, 1, 0x20)
		w(t, m, 0xC2A, 1, 0x20)
		w(t, m, 0xC2A, 1, 0x10)
		if got := [2]uint32{rd(t, m, 0xC2A, 1), rd(t, m, 0xC2B, 1)}; got != c.want {
			t.Fatalf("TX FIFO clear, %s: read/write pointers %v after the clear", c.mode, got)
		}
	}
}

// TC32EMU_RX_BUSY: 0x448 bit 5 in the middle of a central packet in an open
// BRX and after its end: 0 and 0 ("off"), 1 and 0 ("air"), 1 and 1 ("stuck").
func TestRxBusy(t *testing.T) {
	given := RxBusy
	defer func() { RxBusy = given }()
	if _, set := os.LookupEnv("TC32EMU_RX_BUSY"); !set && given != "off" {
		t.Fatalf("0x448 bit 5: the default is %s", given)
	}
	pdu := []byte{0x01, 0} // an empty PDU, 80 us on air
	aa := []byte{0x71, 0x76, 0x41, 0x29}
	for _, c := range []struct {
		mode string
		want [2]uint32
	}{{"off", [2]uint32{0, 0}}, {"air", [2]uint32{1, 0}}, {"stuck", [2]uint32{1, 1}}} {
		RxBusy = c.mode
		m := machine(t)
		r := NewRadio(m, nil)
		r.Central = &recorder{}
		w(t, m, 0x74A, 1, 0xC1|0x02)
		for i, v := range aa {
			w(t, m, 0x408+i, 1, uint32(v))
		}
		w(t, m, 0x40D, 1, 7)
		w(t, m, 0xF16, 1, 0) // start now
		w(t, m, 0xF00, 1, 0x82)
		runUntil(t, m, m.Ms()+0.05)
		ok := r.CentralTx(pdu, 7, aa)
		t0 := m.Ms()
		runUntil(t, m, t0+0.04)
		mid := (rd(t, m, 0x448, 1) >> 5) & 1
		runUntil(t, m, t0+0.1)
		after := (rd(t, m, 0x448, 1) >> 5) & 1
		if !ok || [2]uint32{mid, after} != c.want {
			t.Fatalf("0x448 bit 5, %s: %d during the central's packet, %d after it (taken %v)", c.mode, mid, after, ok)
		}
	}
}

// TC32EMU_CENTRAL_SKIP_EVERY=7: from event 20 on, the central sends nothing at
// events 21, 28 and 35 of 14-39; 0 sends at every event.
func TestCentralSkip(t *testing.T) {
	given := CentralSkipEvery
	defer func() { CentralSkipEvery = given }()
	if _, set := os.LookupEnv("TC32EMU_CENTRAL_SKIP_EVERY"); !set && given != 0 {
		t.Fatalf("central skip: the default is %d", given)
	}
	for _, c := range []struct {
		n    int
		want []int
	}{{0, nil}, {7, []int{21, 28, 35}}} {
		CentralSkipEvery = c.n
		m := machine(t)
		r := NewRadio(m, nil)
		o := DefaultOptions()
		o.Log = func(string) {}
		ce := NewCentral(m, r, o)
		ce.State = "connected"
		for k := 14; k < 40; k++ {
			ce.connEvent(k)
		}
		sent := map[int]bool{}
		for _, s := range ce.Sent {
			sent[int(s[0])] = true
		}
		var skipped []int
		for k := 14; k < 40; k++ {
			if !sent[k] {
				skipped = append(skipped, k)
			}
		}
		if fmt.Sprint(skipped) != fmt.Sprint(c.want) {
			t.Fatalf("central skip %d: events without a packet %v", c.n, skipped)
		}
	}
}

// The peripheral's LL_TERMINATE_IND: the central acknowledges it with an empty
// PDU (NESN advanced) in the next event and leaves; with NoTerminateAck, or
// gone silent, it leaves without sending.
func TestTerminateAck(t *testing.T) {
	for _, cs := range []struct {
		name          string
		noAck, silent bool
		want          int
	}{{"default", false, false, 1}, {"NoTerminateAck", true, false, 0}, {"gone silent", false, true, 0}} {
		m := machine(t)
		r := NewRadio(m, nil)
		o := DefaultOptions()
		o.Log, o.NoTerminateAck = func(string) {}, cs.noAck
		c := NewCentral(m, r, o)
		c.State, c.baseT, c.baseK, c.IntervalMs = "connected", 0, 0, 30
		c.connEvent(1)
		c.NESN ^= 1 // the IND was new: its SN taken
		c.llCtrl([]byte{0x02, 0x13})
		if cs.silent {
			c.GoSilent(2)
		}
		afterInd := c.State
		for k := 2; k < 6; k++ {
			c.connEvent(k)
		}
		var after []int
		for _, s := range c.Sent {
			if s[0] >= 2 {
				after = append(after, int(s[0]))
			}
		}
		acked := false
		for _, l := range c.Transcript {
			if strings.Contains(l, "(acknowledges LL_TERMINATE_IND)") && strings.Contains(l, "NESN1 SN0") {
				acked = true
			}
		}
		wantInd := "terminate-ack"
		if cs.noAck {
			wantInd = "terminated"
		}
		if len(after) != cs.want || (cs.want == 1 && (after[0] != 2 || !acked)) || c.State != "terminated" ||
			afterInd != wantInd {
			t.Fatalf("LL_TERMINATE_IND, %s: state %s then %s; sent at %v (ack line %v)", cs.name, afterInd, c.State,
				after, acked)
		}
	}
}

// advertise sends one ADV_IND by STX2RX on channel 37 (a random AdvA) and
// waits for an answer to arrive.
func advertise(t *testing.T, m *tc32emu.Machine) {
	buf := uint32(0x840300)
	for i, v := range []byte{8, 0, 0, 0, 0x40, 6, 0xc1, 0xc2, 0xc3, 0xc4, 0xc5, 0xc6} {
		m.Write(buf+uint32(i), 1, uint32(v))
	}
	w(t, m, 0xC0C, 2, buf&0xFFFF)
	for i, v := range AdvAA {
		w(t, m, 0x408+i, 1, uint32(v))
	}
	w(t, m, 0x40D, 1, 37)
	w(t, m, 0xF00, 1, 0x87)
	runUntil(t, m, m.Ms()+1.0)
}

// Two centrals on one radio: the first CONNECT_IND wins and its central gets
// the link; with none connecting, the first active scanner sends a SCAN_REQ.
func TestCentrals(t *testing.T) {
	m := machine(t)
	r := NewRadio(m, nil)
	w(t, m, 0x74A, 1, 0xC1|0x02)
	o := DefaultOptions()
	o.Log = func(string) {}
	oa := o
	oa.ConnectAfterMs, oa.ActiveScan = 1e9, true
	a := NewCentral(m, r, oa)
	ob := o
	ob.ConnectAfterMs, ob.Join, ob.AA = 0, true, 0x5A3C9617
	b := NewCentral(m, r, ob)
	advertise(t, m)
	if r.Central != Peer(a) || len(r.Centrals) != 1 || a.State != "scan" || b.State != "connected" ||
		r.ConnCentral != Peer(b) || r.LinkCentral() != Peer(b) || len(r.ScanReqs) != 0 {
		t.Fatalf("two centrals, the second connects: states %s %s, SCAN_REQs %d", a.State, b.State, len(r.ScanReqs))
	}
	r.Centrals = nil
	if r.LinkCentral() != Peer(a) {
		t.Fatal("the connecting central detached: the link does not go to Radio.Central")
	}

	m = machine(t)
	r = NewRadio(m, nil)
	w(t, m, 0x74A, 1, 0xC1|0x02)
	o.ConnectAfterMs = 1e9
	a = NewCentral(m, r, o)
	ob = o
	ob.ActiveScan, ob.Join, ob.OwnAddr = true, true, "rpa"
	b = NewCentral(m, r, ob)
	oc := o
	oc.ActiveScan, oc.Join = true, true
	c := NewCentral(m, r, oc)
	advertise(t, m)
	if len(r.ScanReqs) != 1 {
		t.Fatalf("no central connects: %d SCAN_REQs", len(r.ScanReqs))
	}
	sr := r.ScanReqs[0].PDU
	if r.ScanReqs[0].Central != Peer(b) || sr[0] != 0xC3 || sr[1] != 12 || !bytes.Equal(sr[2:8], b.InitA) ||
		hex.EncodeToString(sr[8:14]) != "c1c2c3c4c5c6" || a.State != "scan" || b.State != "scan" || c.State != "scan" ||
		r.ConnCentral != nil {
		t.Fatalf("no central connects: SCAN_REQ %x; states %s %s %s", sr, a.State, b.State, c.State)
	}
}

// Resolvable private addresses and the central's IdKey distribution.
func TestPrivacy(t *testing.T) {
	m := machine(t)
	r := NewRadio(m, nil)
	o := DefaultOptions()
	o.Log = func(string) {}
	pub := NewCentral(m, r, o)
	if !bytes.Equal(pub.InitA, pub.Identity) || pub.InitAType != 0 {
		t.Fatalf("own_addr public: InitA %x type %d", pub.InitA, pub.InitAType)
	}
	type got struct {
		a        string
		bits     uint32
		resolves bool
	}
	var gs []got
	for _, own := range []string{"rpa", "rpa", "rpa-wrong-irk"} {
		oo := o
		oo.OwnAddr, oo.ConnectAfterMs = own, 0
		c := NewCentral(m, r, oo)
		a := c.InitA
		prand := uint32(a[3]) | uint32(a[4])<<8 | uint32(a[5])<<16
		h := uint32(a[0]) | uint32(a[1])<<8 | uint32(a[2])<<16
		gs = append(gs, got{hex.EncodeToString(a), prand >> 22, aes128.Ah(aes128.FromLE(c.IRK), prand) == h})
		ci := c.OnAdv([]byte{0x40, 6, 0xc1, 0xc2, 0xc3, 0xc4, 0xc5, 0xc6}, 37)
		if ci == nil || ci[0] != 0xC5 || !bytes.Equal(ci[2:8], a) {
			t.Fatalf("own_addr %s: CONNECT_IND %x", own, ci)
		}
	}
	if gs[0].bits != 1 || gs[1].bits != 1 || gs[2].bits != 1 || gs[0].a == gs[1].a || !gs[0].resolves ||
		!gs[1].resolves || gs[2].resolves {
		t.Fatalf("RPAs: %+v", gs)
	}
	for _, cs := range []struct {
		dist     bool
		presInit byte
		want     bool
	}{{false, 0, false}, {true, 0x02, true}, {true, 0, false}} {
		oo := o
		oo.DistID = cs.dist
		c := NewCentral(m, r, oo)
		c.StartPairing()
		c.txq = nil
		c.SMP.Pres = []byte{0x02, 0x03, 0x00, 0x01, 0x10, cs.presInit, 0x03}
		c.smpRx([]byte{0x09, 0x00, 1, 2, 3, 4, 5, 6})
		var sent []string
		for _, q := range c.txq {
			sent = append(sent, hex.EncodeToString(q.payload[4:]))
		}
		wantPreq := byte(0)
		if cs.dist {
			wantPreq = 0x02
		}
		ok := c.SMP.Preq[5] == wantPreq
		if cs.want {
			ok = ok && len(sent) == 2 && sent[0] == "08"+hex.EncodeToString(c.IRK) &&
				sent[1] == "0900"+hex.EncodeToString(c.Identity)
		} else {
			ok = ok && len(sent) == 0
		}
		if !ok {
			t.Fatalf("dist_id %v, response InitKeyDist %#x: preq %x, sent %v", cs.dist, cs.presInit, c.SMP.Preq, sent)
		}
	}
}

// An advertising packet sent while the TX FIFO holds packets is recorded and
// missed; with the FIFO emptied (0xc2a = 0x10), or in the TPLL format, it is not.
func TestAdvFIFO(t *testing.T) {
	m := machine(t)
	r := NewRadio(m, nil)
	w(t, m, 0x74A, 1, 0xC1|0x02)
	adv := uint32(0x840300)
	for i, v := range []byte{8, 0, 0, 0, 0x00, 6, 0xC0, 1, 2, 3, 4, 0xC5} { // ADV_IND, AdvA only
		m.Write(adv+uint32(i), 1, uint32(v))
	}
	w(t, m, 0xC0C, 2, adv&0xFFFF)
	w(t, m, 0x40D, 1, 37)
	for _, c := range []struct {
		name      string
		fifo, fmt int
	}{{"FIFO emptied", 0, 1}, {"one packet queued", 1, 1}, {"two packets queued", 2, 1},
		{"one packet queued, TPLL format", 1, 2}, {"emptied again", 0, 1}} {
		w(t, m, 0x404, 1, uint32(c.fmt))
		w(t, m, 0xC2A, 1, 0x10)
		for i := 0; i < c.fifo; i++ {
			w(t, m, 0xC2C, 2, 0x0100)
		}
		nTx, nRec, nMiss := len(r.AdvTx), len(r.AdvFIFO), len(r.Misses)
		w(t, m, 0xF00, 1, 0x85)
		runUntil(t, m, m.Ms()+0.1)
		sent, rec, miss := len(r.AdvTx)-nTx, r.AdvFIFO[nRec:], r.Misses[nMiss:]
		want := 0
		if c.fifo > 0 && c.fmt == 1 {
			want = 1
		}
		ok := sent == 1 && len(rec) == want && len(miss) == want
		if ok && want == 1 {
			ok = rec[0].Ch == 37 && (rec[0].WPtr-rec[0].RPtr)&0xF == c.fifo &&
				miss[0].Why == fmt.Sprintf("advertising with %d packet(s) in the TX FIFO (0xc2a %d, 0xc2b %d)",
					c.fifo, rec[0].RPtr, rec[0].WPtr)
		}
		if !ok {
			t.Fatalf("advertising, %s: sent %d, recorded %v, missed %v", c.name, sent, rec, miss)
		}
	}
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestCentral(t *testing.T) {
	m := machine(t)
	r := NewRadio(m, nil)
	want := map[string][]int{"all channels": {7, 14, 21, 28, 35, 5, 12, 19}, "channels 0, 2, 4, 6, 36": {4, 36, 2, 6, 0, 0, 4, 36}}
	chms := map[string][]byte{"all channels": {0xff, 0xff, 0xff, 0xff, 0x1f}, "channels 0, 2, 4, 6, 36": {0x55, 0, 0, 0, 0x10}}
	for name, chm := range chms {
		o := DefaultOptions()
		o.Chm, o.Log = chm, func(string) {}
		c := NewCentral(m, r, o)
		for i, wch := range want[name] {
			if ch := c.Channel(); ch != wch {
				t.Fatalf("%s: channel %d = %d, want %d", name, i, ch, wch)
			}
		}
	}
	var rxadd []int
	for txadd := 0; txadd < 2; txadd++ {
		o := DefaultOptions()
		o.ConnectAfterMs, o.Log = 0, func(string) {}
		c := NewCentral(m, r, o)
		adv := append([]byte{byte(txadd << 6), 6}, []byte{0xc1, 0xc2, 0xc3, 0xc4, 0xc5, 0xc6}...)
		rxadd = append(rxadd, int(c.OnAdv(adv, 37)[0]>>7))
	}
	if rxadd[0] != 0 || rxadd[1] != 1 {
		t.Fatalf("CONNECT_IND RxAdd: %v", rxadd)
	}
}

// The peripheral's L2CAP Connection Parameter Update Request: the Python
// ble_models_check.py cases (accepted, applied at event + 12, refused).
func TestParamRequest(t *testing.T) {
	m := machine(t)
	r := NewRadio(m, nil)
	req := []byte{0x12, 0x07, 8, 0, 6, 0, 6, 0, 44, 0, 0x2c, 0x01}
	cases := []struct {
		name          string
		apply, refuse bool
		result        int
		action        string
	}{
		{"default", false, false, 0, "accepted"},
		{"apply_update", true, false, 0, "applied"},
		{"refuse_update", false, true, 1, "refused"},
		{"both", true, true, 1, "refused"},
	}
	for _, k := range cases {
		o := DefaultOptions()
		o.Log, o.ApplyUpdate, o.RefuseUpdate = func(string) {}, k.apply, k.refuse
		var got []ParamRequest
		var acts []string
		o.OnParamRequest = func(q ParamRequest, a string) { got, acts = append(got, q), append(acts, a) }
		c := NewCentral(m, r, o)
		c.Event = 40
		c.l2cap(5, req)
		result := -1
		for _, q := range c.txq {
			p := q.payload
			if len(p) >= 10 && p[2] == 5 && p[3] == 0 && p[4] == 0x13 {
				result = int(p[8]) | int(p[9])<<8
			}
		}
		if len(got) != 1 {
			t.Fatalf("%s: %d callbacks", k.name, len(got))
		}
		q, a := got[0], acts[0]
		wantInstant := -1
		if k.action == "applied" {
			wantInstant = 52
		}
		if result != k.result || a != k.action || q.IntervalMin != 6 || q.IntervalMax != 6 || q.Latency != 44 ||
			q.Timeout != 300 || q.Event != 40 || q.Instant != wantInstant || (c.update != nil) != (k.action == "applied") {
			t.Errorf("%s: result %d, action %s, req %+v", k.name, result, a, q)
		}
	}
}

// TC32EMU_SUSPEND_RF: the Python ble_models_check.py suspend_rf() cases.
func TestSuspendRF(t *testing.T) {
	defer func() { SuspendRF = "keep" }()
	regs := RFSuspendRegs
	var no401 []int
	for _, a := range regs {
		if a != 0x401 {
			no401 = append(no401, a)
		}
	}
	cases := []struct {
		mode  string
		setup []int
	}{{"lost", regs}, {"lost", no401}, {"keep", regs}}
	for i, k := range cases {
		SuspendRF = k.mode
		m := machine(t)
		r := NewRadio(m, nil)
		lost := k.mode == "lost"
		wr := func(o int, v uint32) {
			if err := m.RegWrite(o, 1, v); err != nil {
				t.Fatal(err)
			}
		}
		powerOn := m.Regs[0x1276]
		for _, a := range k.setup {
			wr(a, 0x5A)
		}
		m.Analog[0x26] = 0
		wr(0x6F, 0x81)
		m.Asleep = nil
		want := byte(0x5A)
		if lost {
			want = powerOn
		}
		if m.Regs[0x1276] != want {
			t.Errorf("case %d: 0x1276 after the suspend 0x%02x", i, m.Regs[0x1276])
		}
		wantUninit := 0
		if lost {
			wantUninit = len(k.setup)
		}
		if len(r.RFUninit) != wantUninit {
			t.Errorf("case %d: %d registers to set again, want %d", i, len(r.RFUninit), wantUninit)
		}
		wr(0xF00, 0x82)
		if (r.Brx == nil) != lost {
			t.Errorf("case %d: a BRX before the setup: started %v", i, r.Brx != nil)
		}
		wr(0xF00, 0x80)
		for _, a := range k.setup[:len(k.setup)-1] {
			wr(a, 0x5A)
		}
		wr(0xF00, 0x82)
		if (r.Brx == nil) != lost {
			t.Errorf("case %d: one register still missing: started %v", i, r.Brx != nil)
		}
		wr(0xF00, 0x80)
		wr(k.setup[len(k.setup)-1], 0x5A)
		wr(0xF00, 0x82)
		if r.Brx == nil {
			t.Errorf("case %d: its setup written again: the BRX did not start", i)
		}
	}
	SuspendRF = "lost"
	m := machine(t)
	r := NewRadio(m, nil)
	for _, a := range regs {
		if err := m.RegWrite(a, 1, 0x5A); err != nil {
			t.Fatal(err)
		}
	}
	m.Analog[0x26] = 0
	if err := m.RegWrite(0x6F, 1, 0x81); err != nil {
		t.Fatal(err)
	}
	m.Asleep = nil
	if err := m.Reset(-1, false); err != nil {
		t.Fatal(err)
	}
	if err := m.RegWrite(0xF00, 1, 0x82); err != nil {
		t.Fatal(err)
	}
	if r.Brx == nil || len(r.RFUninit) != 0 || len(r.RFWritten) != 0 {
		t.Errorf("a reset after the suspend did not end the refusal")
	}
}

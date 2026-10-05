// SPDX-License-Identifier: Apache-2.0
package ble

import (
	"bytes"
	"testing"
)

type tpllPeer struct {
	heard []TPLLTx
	reply []byte
}

func (p *tpllPeer) OnTPLL(payload []byte, mhz int, ac []byte) []byte {
	p.heard = append(p.heard, TPLLTx{0, mhz, ac, payload})
	return p.reply
}

// The TPLL cases of checks/ble_models_check.py.
func TestTPLL(t *testing.T) {
	m := machine(t)
	r := NewRadio(m, nil)
	w(t, m, 0x74A, 1, 0xC1|0x02)
	rg := m.Regs
	rg[0x404] = 0x02
	rg[0x405] = 3
	copy(rg[0x408:0x40B], []byte{0xaa, 0xbb, 0xcc})
	rg[0x402] = 0x01
	rg[0x1220] = 0x04
	rg[0x1244], rg[0x1245] = byte((2450&0x3F)<<2), byte(2450>>6)
	txbuf, rxbuf := uint32(0x840300), uint32(0x840400)
	for i, v := range append([]byte{9, 0, 0, 0, 0x04}, []byte("kbd!")...) {
		m.Write(txbuf+uint32(i), 1, uint32(v))
	}
	w(t, m, 0xC0C, 2, txbuf&0xFFFF)
	w(t, m, 0xC08, 2, rxbuf&0xFFFF)
	peer := &tpllPeer{reply: []byte{0x11, 0x22}}
	r.TPLL = peer
	if r.tpllMHz() != 2450 || !bytes.Equal(r.tpllAccessCode(), []byte{0xaa, 0xbb, 0xcc}) || r.TpllAirtimeUs(4) != 44 {
		t.Fatalf("registers: %d MHz %x %v us", r.tpllMHz(), r.tpllAccessCode(), r.TpllAirtimeUs(4))
	}
	t0 := m.Ms()
	w(t, m, 0xF00, 1, 0x85)
	runUntil(t, m, t0+0.5)
	if len(peer.heard) != 1 || string(peer.heard[0].Payload) != "kbd!" || peer.heard[0].MHz != 2450 ||
		rg[0xF20]&0x02 == 0 || len(r.TpllTx) != 1 || len(r.Misses) == 0 || r.Misses[len(r.Misses)-1].Why != "not listening" {
		t.Fatalf("STX: heard %v, status %#x, misses %v", peer.heard, rg[0xF20], r.Misses)
	}
	w(t, m, 0xF20, 1, 0xFF)
	w(t, m, 0xF00, 1, 0x86)
	w(t, m, 0xF00, 1, 0x85)
	t1 := m.Ms()
	runUntil(t, m, t1+0.5)
	ent := make([]byte, 17)
	for i := range ent {
		v, _ := m.Read(rxbuf+uint32(i), 1)
		ent[i] = byte(v)
	}
	if ent[0] != 13 || ent[4] != 2 || ent[5] != 0x11 || ent[6] != 0x22 || !bytes.Equal(ent[13:17], []byte{0, 0, 0xC8, 0}) ||
		rg[0xF20]&0x01 == 0 || r.Srx != nil {
		t.Fatalf("SRX then STX: entry %x status %#x srx %v", ent, rg[0xF20], r.Srx)
	}
	w(t, m, 0xF20, 1, 0xFF)
	rg[0xF03] = 0x02
	w(t, m, 0xF28, 4, 100)
	w(t, m, 0xF00, 1, 0x86)
	t2 := m.Ms()
	runUntil(t, m, t2+0.05)
	openAt50 := r.Srx != nil
	runUntil(t, m, t2+0.2)
	if !openAt50 || r.Srx != nil || (uint32(rg[0xF20])|uint32(rg[0xF21])<<8)&0x400 == 0 {
		t.Fatal("SRX first timeout")
	}
	r.TPLL = nil
	w(t, m, 0xF00, 1, 0x80)
	if r.Brx != nil || r.Srx != nil {
		t.Fatal("0x80 must close the windows")
	}
}

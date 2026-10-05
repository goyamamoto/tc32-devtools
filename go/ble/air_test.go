// SPDX-License-Identifier: Apache-2.0
package ble

import (
	"fmt"
	"math"
	"reflect"
	"testing"
)

// The checks of checks/ble_air_check.py. The lost packets and the jitter are
// the numbers that check prints (Python's random.Random draws).

type airRecorder struct {
	replies, lost [][]byte
	connects      int
}

func (r *airRecorder) OnAdv([]byte, byte) []byte     { return nil }
func (r *airRecorder) OnConnectSent([]byte, float64) { r.connects++ }
func (r *airRecorder) Drop([]byte) bool              { return false }
func (r *airRecorder) OnAir(pdu []byte, _ byte, _ uint32) []byte {
	r.replies = append(r.replies, pdu)
	return nil
}
func (r *airRecorder) AirLost(pdu []byte) { r.lost = append(r.lost, pdu) }

func mustAir(t *testing.T, seed uint32, lossKeyboard, lossHost int, bad []byte, badLoss int, ppm float64, jitterUs int) *Air {
	a, err := NewAir(seed, lossKeyboard, lossHost, bad, badLoss, ppm, jitterUs)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func lostOf(a *Air, way int, ch byte, now float64, n int) []int {
	var out []int
	for i := 0; i < n; i++ {
		if a.Lost(way, ch, now) {
			out = append(out, i)
		}
	}
	return out
}

func TestAir(t *testing.T) {
	a := mustAir(t, 1, 300_000, 50_000, nil, 0, 0, 0)
	k, h := lostOf(a, ToKeyboard, 5, 0, 40), lostOf(a, ToHost, 5, 0, 40)
	wantK := []int{0, 2, 6, 7, 8, 9, 16, 17, 21, 23, 25, 26, 27, 30, 34, 36, 37, 38}
	wantH := []int{12, 32}
	if !reflect.DeepEqual(k, wantK) || !reflect.DeepEqual(h, wantH) || a.Sent != [2]int{40, 40} ||
		a.LostN != [2]int{len(wantK), len(wantH)} {
		t.Fatalf("seed 1, 30 %% and 5 %%: lost %v and %v, sent %v, lost %v", k, h, a.Sent, a.LostN)
	}

	a = mustAir(t, 1, 0, 0, nil, 0, 0, 0)
	if len(lostOf(a, ToKeyboard, 5, 0, 100)) != 0 || len(lostOf(a, ToHost, 5, 0, 100)) != 0 ||
		a.rng[ToKeyboard].randrange(Million) != newPyRandom(4).randrange(Million) {
		t.Fatal("probability 0: a packet lost or a number drawn")
	}

	bad := make([]byte, 0, 10)
	for ch := byte(11); ch <= 20; ch++ {
		bad = append(bad, ch)
	}
	a = mustAir(t, 2, 0, 0, bad, Million, 0, 0)
	var lost []byte
	for ch := byte(0); ch < 40; ch++ {
		if a.Lost(ToHost, ch, 0) {
			lost = append(lost, ch)
		}
	}
	if !reflect.DeepEqual(lost, bad) {
		t.Fatalf("bad channels 11-20: lost on %v", lost)
	}

	a = mustAir(t, 1, 300_000, 0, nil, 0, 0, 0)
	a.BlackoutUntil = 10.0
	for _, way := range []int{ToKeyboard, ToHost} {
		for _, now := range []float64{0, 9.999} {
			if !a.Lost(way, 5, now) {
				t.Fatalf("blackout: a packet to the %s at %v ms arrived", WayNames[way], now)
			}
		}
	}
	if after := lostOf(a, ToKeyboard, 5, 10.0, 40); !reflect.DeepEqual(after, wantK) || a.Lost(ToHost, 5, 10.0) {
		t.Fatalf("after the blackout: lost %v (a draw was made in it)", after)
	}

	a = mustAir(t, 1, 0, 0, nil, 0, 0, 20)
	var j []int
	for i := 0; i < 12; i++ {
		j = append(j, int(math.Round(a.JitterMs()*1000)))
	}
	if want := []int{16, -15, 11, -4, -18, -20, -11, 17, 10, 3, 0, -19}; !reflect.DeepEqual(j, want) {
		t.Fatalf("jitter 20 us, seed 1: %v us", j)
	}
	if mustAir(t, 1, 0, 0, nil, 0, 0, 0).JitterMs() != 0 {
		t.Fatal("jitter 0: not 0")
	}

	if mustAir(t, 1, 0, 0, nil, 0, 250, 0).Clock() != 1.00025 || mustAir(t, 1, 0, 0, nil, 0, -40, 0).Clock() != 0.99996 ||
		mustAir(t, 1, 0, 0, nil, 0, 0, 0).Clock() != 1.0 {
		t.Fatal("clock")
	}
	for _, c := range []struct {
		p    float64
		want int
	}{{0.05, 50_000}, {1, Million}, {0.000001, 1}} {
		if n, err := Millionths(c.p); err != nil || n != c.want {
			t.Fatalf("Millionths(%v) = %d, %v", c.p, n, err)
		}
	}
	if _, err := NewAir(1, 0, Million+1, nil, 0, 0, 0); err == nil {
		t.Fatal("loss_host 1000001 accepted")
	}
	if _, err := NewAir(1, 0, 0, []byte{40}, 0, 0, 0); err == nil {
		t.Fatal("bad channel 40 accepted")
	}
	if _, err := NewAir(1, 0, 0, nil, 0, 0, -1); err == nil {
		t.Fatal("jitter -1 accepted")
	}
}

// airBRX opens a BRX on channel 7 with an empty packet to send.
func airBRX(t *testing.T, r *Radio, rec Peer) (aa []byte, rxbuf uint32) {
	m := r.M
	empty := uint32(0x840100)
	rxbuf = 0x840200
	for i, v := range []byte{2, 0, 0, 0, 0x01, 0} {
		m.Write(empty+uint32(i), 1, uint32(v))
	}
	aa = []byte{0x71, 0x76, 0x41, 0x29}
	for i, v := range aa {
		w(t, m, 0x408+i, 1, uint32(v))
	}
	w(t, m, 0x40D, 1, 7)
	w(t, m, 0xF03, 1, 0x10)
	w(t, m, 0xC08, 2, rxbuf&0xFFFF)
	w(t, m, 0xC0C, 2, empty&0xFFFF)
	for i := uint32(0); i < 20; i++ {
		m.Write(rxbuf+i, 1, 0xEE)
	}
	start := (m.StimerNow() + 1600) & 0xFFFFFFFF // 100 us ahead
	w(t, m, 0xF18, 4, start)
	w(t, m, 0xF16, 1, rd(t, m, 0xF16, 1)|4)
	w(t, m, 0xF20, 2, 0xFFFF)
	w(t, m, 0xF00, 1, 0x82)
	r.Central = rec
	return aa, rxbuf
}

func TestAirRadio(t *testing.T) {
	pdu := []byte{0x01, 0}
	// Every central packet lost.
	m := machine(t)
	r := NewRadio(m, nil)
	w(t, m, 0x74A, 1, 0xC1|0x02)
	r.Air = mustAir(t, 1, Million, 0, nil, 0, 0, 0)
	rec := &airRecorder{}
	aa, rxbuf := airBRX(t, r, rec)
	early := r.CentralTx(pdu, 7, aa)
	earlyLost, drawn := r.TxLost, r.Air.Sent[ToKeyboard]
	runUntil(t, m, m.Ms()+0.2)
	ok := r.CentralTx(pdu, 7, aa)
	runUntil(t, m, m.Ms()+1.0)
	if early || earlyLost || drawn != 0 || len(r.Misses) != 1 {
		t.Fatalf("a packet before the start tick: early %v lost %v drawn %d misses %v", early, earlyLost, drawn, r.Misses)
	}
	entry := r.readBytes(rxbuf, 6)
	if ok || !r.TxLost || len(r.Misses) != 1 || r.Brx == nil || string(entry) != "\xee\xee\xee\xee\xee\xee" ||
		rd(t, m, 0xF20, 2) != 0 || len(rec.replies) != 0 || r.Air.LostN[ToKeyboard] != 1 {
		t.Fatalf("a packet lost on air: ok %v TxLost %v misses %d brx %v entry %x status %#x", ok, r.TxLost,
			len(r.Misses), r.Brx != nil, entry, rd(t, m, 0xF20, 2))
	}
	// None lost: taken; then the reply lost.
	m = machine(t)
	r = NewRadio(m, nil)
	w(t, m, 0x74A, 1, 0xC1|0x02)
	r.Air = mustAir(t, 1, 0, Million, nil, 0, 0, 0)
	rec = &airRecorder{}
	aa, _ = airBRX(t, r, rec)
	runUntil(t, m, m.Ms()+0.2)
	ok = r.CentralTx(pdu, 7, aa)
	runUntil(t, m, m.Ms()+1.0)
	if !ok || r.TxLost || len(rec.lost) != 1 || fmt.Sprintf("%x", rec.lost[0]) != "0500" || len(rec.replies) != 0 ||
		r.Brx != nil || rd(t, m, 0xF20, 2) != RX|TX|DONE {
		t.Fatalf("the reply lost on air: ok %v lost %x replies %d brx %v status %#x", ok, rec.lost, len(rec.replies),
			r.Brx != nil, rd(t, m, 0xF20, 2))
	}
}

func airAdvertise(t *testing.T, m interface {
	Write(uint32, int, uint32) error
}, r *Radio) {
	buf := uint32(0x840300)
	for i, v := range append([]byte{8, 0, 0, 0, 0x40, 6}, 0xc1, 0xc2, 0xc3, 0xc4, 0xc5, 0xc6) {
		m.Write(buf+uint32(i), 1, uint32(v))
	}
	w(t, r.M, 0xC0C, 2, buf&0xFFFF)
	for i, v := range AdvAA {
		w(t, r.M, 0x408+i, 1, uint32(v))
	}
	w(t, r.M, 0x40D, 1, 37)
	w(t, r.M, 0xF20, 2, 0xFFFF)
	w(t, r.M, 0xF00, 1, 0x87)
	runUntil(t, r.M, r.M.Ms()+1.0)
}

func quiet() Options {
	o := DefaultOptions()
	o.Log = func(string) {}
	return o
}

func TestAirAdvertising(t *testing.T) {
	m := machine(t)
	r := NewRadio(m, nil)
	w(t, m, 0x74A, 1, 0xC1|0x02)
	r.Air = mustAir(t, 1, 0, Million, nil, 0, 0, 0)
	o := quiet()
	o.ConnectAfterMs = 0
	c := NewCentral(m, r, o)
	airAdvertise(t, m, r)
	if c.State != "scan" || len(r.AdvTx) != 1 || rd(t, m, 0xF20, 2) != TX {
		t.Fatalf("an advertising packet lost: state %s, sent %d, status %#x", c.State, len(r.AdvTx), rd(t, m, 0xF20, 2))
	}

	m = machine(t)
	r = NewRadio(m, nil)
	w(t, m, 0x74A, 1, 0xC1|0x02)
	r.Air = mustAir(t, 1, Million, 0, nil, 0, 0, 0)
	o.Establish = true
	c = NewCentral(m, r, o)
	airAdvertise(t, m, r)
	state := c.State
	runUntil(t, m, m.Ms()+6.5*30+5)
	if _, failed := c.Records["establish_failed_ms"]; state != "connected" || r.ConnCentral != nil ||
		rd(t, m, 0xF20, 2) != TX || c.State != "failed" || c.Stats.Events != 6 || !failed {
		t.Fatalf("a CONNECT_IND lost: state %s then %s after %d events, status %#x", state, c.State, c.Stats.Events,
			rd(t, m, 0xF20, 2))
	}
}

// step runs the radio's next scheduled step at its time and returns that time.
func step(r *Radio) float64 {
	s := r.sched[0]
	r.sched = r.sched[1:]
	r.M.Cycles = int64(s.ms*float64(r.M.CPUHz)/1000) + 1
	s.fn()
	return s.ms
}

func TestAirCentral(t *testing.T) {
	var plain [2]float64
	for _, ppm := range []float64{0, 250, -40} {
		m := machine(t)
		r := NewRadio(m, nil)
		w(t, m, 0x74A, 1, 0xC1|0x02)
		if ppm != 0 {
			r.Air = mustAir(t, 1, 0, 0, nil, 0, ppm, 0)
		}
		o := quiet()
		o.WinOffset = 2
		c := NewCentral(m, r, o)
		c.AdvA = make([]byte, 6)
		c.OnConnectSent(nil, 0.0)
		f := 1 + ppm/1e6
		for k := 0; k < 4; k++ {
			got, want := c.baseT+float64(k)*c.IntervalMs, (1.25+2.5)*f+0.3+float64(k)*30*f
			if math.Abs(got-want) > 1e-9 {
				t.Fatalf("clock %+v ppm: event %d at %v, want %v", ppm, k, got, want)
			}
		}
		if r.sched[0].ms != c.Anchor0 {
			t.Fatalf("clock %+v ppm: event 0 scheduled at %v, anchor %v", ppm, r.sched[0].ms, c.Anchor0)
		}
		if ppm == 0 {
			plain = [2]float64{c.Anchor0, c.IntervalMs}
		}
	}
	if plain != [2]float64{0.0 + 1.25 + 1.25*2 + 0.3, 24 * 1.25} {
		t.Fatalf("without an air: %v", plain)
	}

	// A connection update under a clock.
	m := machine(t)
	r := NewRadio(m, nil)
	w(t, m, 0x74A, 1, 0xC1|0x02)
	r.Air = mustAir(t, 1, 0, 0, nil, 0, 250, 0)
	o := quiet()
	o.SendVersion, o.SendFeatures = false, false
	c := NewCentral(m, r, o)
	c.AdvA = make([]byte, 6)
	c.OnConnectSent(nil, 0.0)
	c.ConnectionUpdate(1, 3, 6, 0, 300, 2)
	for k := 0; k < 3; k++ {
		step(r)
	}
	f := 1.00025
	if want := c.Anchor0 + 2*30*f + 1.25*3*f + 0.3; math.Abs(c.baseT-want) > 1e-9 || math.Abs(c.IntervalMs-7.5*f) > 1e-12 ||
		c.baseK != 2 {
		t.Fatalf("a connection update under +250 ppm: anchor %v (want %v), interval %v, base event %d", c.baseT, want,
			c.IntervalMs, c.baseK)
	}

	// Jitter moves each event's start, not the times they are counted from.
	m = machine(t)
	r = NewRadio(m, nil)
	w(t, m, 0x74A, 1, 0xC1|0x02)
	r.Air = mustAir(t, 1, 0, 0, nil, 0, 0, 20)
	c = NewCentral(m, r, quiet())
	c.AdvA = make([]byte, 6)
	c.OnConnectSent(nil, 0.0)
	wantUs := []int{16, -15, 11, -4, -18, -20, -11, 17}
	for k, us := range wantUs {
		if got, want := step(r), c.Anchor0+float64(k)*30.0+float64(us)/1000; got != want {
			t.Fatalf("jitter: event %d starts at %v, want %v", k, got, want)
		}
	}

	// establish.
	for _, tc := range []struct {
		name      string
		establish bool
		hear      bool
		state     string
		events    int
	}{{"establish, nothing heard", true, false, "failed", 6}, {"establish, a reply at event 3", true, true, "connected", 10},
		{"the default, nothing heard", false, false, "connected", 10}} {
		m := machine(t)
		r := NewRadio(m, nil)
		w(t, m, 0x74A, 1, 0xC1|0x02)
		o := quiet()
		o.Establish = tc.establish
		c := NewCentral(m, r, o)
		c.AdvA = make([]byte, 6)
		c.OnConnectSent(nil, 0.0)
		for k := 0; k < 10 && len(r.sched) > 0; k++ {
			step(r)
			if tc.hear && k == 3 {
				c.OnAir([]byte{0x01, 0}, 0, 0)
			}
		}
		if c.State != tc.state || c.Stats.Events != tc.events {
			t.Fatalf("%s: state %s after %d events", tc.name, c.State, c.Stats.Events)
		}
	}
}

// airConnected is a central in a connection that began at 0, with nothing queued.
func airConnected(t *testing.T, set func(*Options)) *Central {
	m := machine(t)
	r := NewRadio(m, nil)
	w(t, m, 0x74A, 1, 0xC1|0x02)
	o := quiet()
	o.SendVersion, o.SendFeatures = false, false
	if set != nil {
		set(&o)
	}
	c := NewCentral(m, r, o)
	c.AdvA = make([]byte, 6)
	c.OnConnectSent(nil, 0.0)
	return c
}

func takeName(llid byte, p []byte) string {
	switch {
	case llid == 1 && len(p) == 0:
		return "empty"
	case llid == 3 && p[0] == 0:
		return fmt.Sprintf("%s instant %d", llName(p[0]), int(p[10])|int(p[11])<<8)
	case llid == 3:
		return llName(p[0])
	}
	return "L2CAP"
}

func TestAirRules(t *testing.T) {
	for _, on := range []bool{true, false} {
		c := airConnected(t, func(o *Options) { o.MicEnds = on })
		c.Event = 3
		c.Enc.SK, c.Enc.IV, c.Enc.Rx = make([]byte, 16), make([]byte, 8), true
		nxt := c.OnAir(append([]byte{0x02, 9}, make([]byte, 9)...), 0, 0) // 5 octets and a MIC that is not theirs
		_, rec := c.Records["mic_failure_ms"]
		want := "connected"
		if on {
			want = "mic-failed"
		}
		if c.State != want || nxt != nil || rec != on || c.Enc.MicFail != 1 {
			t.Fatalf("a packet that fails its MIC, MicEnds %v: state %s, next %x, %d counted", on, c.State, nxt, c.Enc.MicFail)
		}
	}

	want := map[bool]struct {
		order   []string
		more    []bool
		instant int
	}{
		true: {[]string{"LL_ENC_REQ", "empty", "LL_START_ENC_RSP", "empty", "L2CAP", "LL_CONNECTION_UPDATE_IND instant 21"},
			[]bool{false, true, true}, 21},
		false: {[]string{"LL_ENC_REQ", "L2CAP", "LL_START_ENC_RSP", "LL_CONNECTION_UPDATE_IND instant 17", "empty", "empty"},
			[]bool{true, true, false}, 17},
	}
	for _, on := range []bool{true, false} {
		c := airConnected(t, func(o *Options) { o.LLRules = on })
		c.Event = 5
		c.QueueLL(append([]byte{0x03}, make([]byte, 22)...)) // LL_ENC_REQ
		c.QueueL2CAP(5, []byte{0x13, 1, 2, 0, 0, 0})
		c.ConnectionUpdate(1, 0, 6, 0, 300, 5+12)
		var order []string
		var more []bool
		take := func() { order = append(order, takeName(c.take())) }
		take()
		more = append(more, c.more())
		take()
		c.Enc.SK, c.Enc.IV = make([]byte, 16), make([]byte, 8)
		c.llCtrl([]byte{0x05}) // the peripheral's LL_START_ENC_REQ
		more = append(more, c.more())
		take()
		take()
		c.llCtrl([]byte{0x06}) // its LL_START_ENC_RSP
		c.Event = 9
		more = append(more, c.more())
		take()
		take()
		if w := want[on]; !reflect.DeepEqual(order, w.order) || !reflect.DeepEqual(more, w.more) || c.update.Instant != w.instant {
			t.Fatalf("LLRules %v: order %v, more %v, instant %d", on, order, more, c.update.Instant)
		}
	}

	// Reclock: an air put on the radio during the connection.
	rc := airConnected(t, nil)
	var starts []float64
	for k := 0; k < 6; k++ {
		if k == 3 {
			rc.Radio.Air = mustAir(t, 1, 0, 0, nil, 0, 250, 0)
			rc.Reclock()
		}
		starts = append(starts, step(rc.Radio))
	}
	for k, s := range starts {
		want := rc.Anchor0 + float64(k)*30.0
		if k > 3 {
			want = rc.Anchor0 + 90.0 + float64(k-3)*30.0*1.00025
		}
		if math.Abs(s-want) > 1e-9 || (k == 3 && s != rc.Anchor0+90.0) {
			t.Fatalf("Reclock before event 3 with +250 ppm: event %d at %v, want %v", k, s, want)
		}
	}

	c := airConnected(t, func(o *Options) { o.LLRules = true })
	c.Event = 5
	c.QueueLL(append([]byte{0x03}, make([]byte, 22)...))
	c.QueueL2CAP(5, []byte{0x13, 1, 2, 0, 0, 0})
	first, held := takeName(c.take()), takeName(c.take())
	c.llCtrl([]byte{0x0D, 0x06}) // LL_REJECT_IND, key missing
	if after := takeName(c.take()); first != "LL_ENC_REQ" || held != "empty" || after != "L2CAP" {
		t.Fatalf("LLRules, LL_REJECT_IND: %s, %s, then %s", first, held, after)
	}

	// Another host's data: QueueData with its tag, OnAck and RxData.
	c = airConnected(t, nil)
	c.Event = 3
	var got, sent []string
	var acked []any
	c.RxData = func(llid byte, p []byte) { got = append(got, fmt.Sprintf("%d %x", llid, p)) }
	c.OnAck = func(tag any) { acked = append(acked, tag) }
	c.QueueData(2, []byte{0x03, 0x00, 0x04, 0x00, 0x0a, 0x03, 0x00}, "first")
	c.QueueData(1, []byte{0x55}, "second")
	c.QueueLL([]byte{0x12}) // LL_PING_REQ: no tag
	shape := func(p []byte) { sent = append(sent, fmt.Sprintf("%d %x", p[0]&3, p[2:])) }
	shape(c.nextPDU())
	shape(c.OnAir([]byte{0x01, 0}, 0, 0))                                                     // acknowledges nothing: the first one again
	shape(c.OnAir(append([]byte{0x02 | 1<<2 | 1<<3, 5}, 0x01, 0x00, 0x04, 0x00, 0x0b), 0, 0)) // acknowledges; L2CAP start
	shape(c.OnAir([]byte{0x01, 1, 0x77}, 0, 0))                                               // acknowledges; continuation
	last := c.OnAir([]byte{0x01 | 1<<2 | 1<<3, 0}, 0, 0)                                      // acknowledges the LL_PING_REQ
	wantSent := []string{"2 030004000a0300", "2 030004000a0300", "1 55", "3 12"}
	if !reflect.DeepEqual(sent, wantSent) || !reflect.DeepEqual(acked, []any{"first", "second"}) ||
		!reflect.DeepEqual(got, []string{"2 010004000b", "1 77"}) || c.l2 != nil || last != nil {
		t.Fatalf("another host's data: sent %v, acknowledged %v, received %v, own L2CAP frame %v, last %x", sent, acked, got, c.l2, last)
	}
}

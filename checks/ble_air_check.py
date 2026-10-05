#!/usr/bin/env python3
"""Known answers for the air model (emulator/ble_air.py) and its use by the
radio and the central, without any firmware image.

- Air: the lost packets of each way are the draws of random.Random(4 * seed)
  and (4 * seed + 1) below the probability in millionths, printed here for a
  port to repeat; probability 0 draws nothing and loses nothing; a bad
  channel takes bad_loss; in a blackout every packet is lost and nothing is
  drawn; the jitter is a whole number of microseconds in -jitter_us..jitter_us
  from random.Random(4 * seed + 2).
- Radio: a central packet in an open BRX lost on air is not taken (central_tx
  False, tx_lost set, no miss recorded, no RX DMA entry, the BRX still open);
  one sent before the BRX's start tick is a miss and draws nothing; the chip's
  reply lost on air is told to the central (air_lost) and not given to on_air,
  and the event ends.
- Advertising: a packet lost on its way to the host is offered to no central;
  a CONNECT_IND lost on its way to the keyboard leaves no RX status, and its
  central is told it was sent.
- Central: with clock_ppm the events are (1 + ppm / 1e6) connection intervals
  apart, also after a connection update; jitter moves each event's start and
  not the next one's time; establish=True stops at event 6 with nothing
  heard (state "failed"), and not once a reply came; without it, and without
  an air, the central goes on.
- Without an air the central's event times are those of the plain
  arithmetic, bit for bit.
- mic_ends: a packet of the peripheral that fails its MIC ends the connection
  (state "mic-failed"); the default counts it and stays connected.
- ll_rules: with LL_ENC_REQ taken, an L2CAP PDU and an
  LL_CONNECTION_UPDATE_IND queued behind it wait (empty PDUs, MD 0) through
  LL_START_ENC_RSP until the peripheral's LL_START_ENC_RSP, and the update's
  instant is counted from the event it is then first sent in; an
  LL_REJECT_IND ends the wait too. The default sends the queue in its order
  with the instant as given.
- reclock: an air put on the radio during a connection leaves the next
  event at its time and makes the events after it the new clock's interval
  apart.

Usage: ble_air_check.py

SPDX-License-Identifier: Apache-2.0
"""
import os
import random
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path[:0] = [ROOT, os.path.join(ROOT, "emulator")]
import ble_air  # noqa: E402
import ble_central  # noqa: E402
import ble_radio  # noqa: E402
import tc32emu as te  # noqa: E402

failures = []


def check(cond, text):
    print(("ok   " if cond else "FAIL ") + text)
    if not cond:
        failures.append(text)


class Recorder:
    """A central that sends nothing by itself and keeps what the radio tells it."""

    def __init__(self):
        self.replies, self.lost, self.connects = [], [], []

    def drop(self, pdu):
        return False

    def on_air(self, pdu, ch, tx_addr):
        self.replies.append((pdu, ch, tx_addr))
        return None

    def air_lost(self, pdu):
        self.lost.append(pdu)

    def on_adv(self, pdu, ch):
        return self.answer

    def on_connect_sent(self, pdu, t_end):
        self.connects.append((pdu, round(t_end, 3)))


def machine():
    """A machine that is never run: only its registers, memory and clock."""
    fl = te.Flash()
    fl.mem[8] = 0x4B                          # the boot ROM's bootable-slot mark
    m = te.Machine(fl, max_log=0)
    return m


def run_until(m, ms):
    while m.ms() < ms:
        m.cycles += m.cpu_hz // 100_000          # 10 us
        m.update_time()


def air():
    a = ble_air.Air(seed=1, loss_keyboard=300_000, loss_host=50_000)
    got = {w: [i for i in range(40) if a.lost(w, 5, 0.0)] for w in ble_air.WAYS}
    want = {}
    for i, w in enumerate(ble_air.WAYS):
        rng = random.Random(4 + i)
        want[w] = [j for j in range(40) if rng.randrange(1_000_000) < a.loss[w]]
    check(got == want and a.sent == {"keyboard": 40, "host": 40}
          and a.lost_n == {w: len(got[w]) for w in ble_air.WAYS},
          f"seed 1, 30 % to the keyboard, 5 % to the host: of 40 packets each way, lost {got['keyboard']} and"
          f" {got['host']}")

    a = ble_air.Air(seed=1)
    check(not any(a.lost(w, 5, 0.0) for w in ble_air.WAYS for _ in range(100))
          and a.rng["keyboard"].randrange(1_000_000) == random.Random(4).randrange(1_000_000),
          "probability 0: nothing lost and nothing drawn")

    a = ble_air.Air(seed=2, bad=range(11, 21), bad_loss=1_000_000)
    lost = [ch for ch in range(40) if a.lost("host", ch, 0.0)]
    check(lost == list(range(11, 21)), f"bad channels 11-20 at probability 1, the others at 0: lost on {lost}")

    a = ble_air.Air(seed=1, loss_keyboard=300_000)
    a.blackout_until = 10.0
    dark = [a.lost(w, 5, t) for w in ble_air.WAYS for t in (0.0, 9.999)]
    after = [i for i in range(40) if a.lost("keyboard", 5, 10.0)]
    rng = random.Random(4)
    check(all(dark) and after == [i for i in range(40) if rng.randrange(1_000_000) < 300_000]
          and not a.lost("host", 5, 10.0),
          f"blackout until 10 ms: every packet before it lost, both ways, with no draw; from 10 ms on {after[:4]}...")

    a = ble_air.Air(seed=1, jitter_us=20)
    j = [a.jitter_ms() for _ in range(12)]
    rng = random.Random(6)
    check(j == [(rng.randrange(41) - 20) / 1000 for _ in range(12)] and all(-0.020 <= x <= 0.020 for x in j)
          and ble_air.Air(seed=1).jitter_ms() == 0.0,
          f"jitter 20 us, seed 1: {[round(x * 1000) for x in j]} us; 0 without")

    check(ble_air.Air(clock_ppm=250).clock() == 1.00025 and ble_air.Air(clock_ppm=-40).clock() == 0.99996
          and ble_air.Air().clock() == 1.0, "clock: 250 ppm -> 1.00025, -40 ppm -> 0.99996, 0 -> 1.0")
    check(ble_air.millionths("0.05") == 50_000 and ble_air.millionths(1) == 1_000_000
          and ble_air.millionths(0.000001) == 1, "millionths: 0.05 -> 50000, 1 -> 1000000, 0.000001 -> 1")
    for bad in (dict(loss_host=1_000_001), dict(bad=[40]), dict(jitter_us=-1)):
        try:
            ble_air.Air(**bad)
            refused = False
        except ValueError:
            refused = True
        check(refused, f"refused: {bad}")


def brx(m, r, rec):
    """An open BRX on channel 7 with an empty packet to send, as ble_models_check's."""
    empty, rxbuf = 0x840100, 0x840200
    for i, v in enumerate([2, 0, 0, 0, 0x01, 0]):
        m.write(empty + i, 1, v)
    aa = bytes.fromhex("71764129")
    for i, v in enumerate(aa):
        m.reg_write(0x408 + i, 1, v)
    m.reg_write(0x40D, 1, 7)
    m.reg_write(0xF03, 1, 0x10)
    m.reg_write(0xC08, 2, rxbuf & 0xFFFF)
    m.reg_write(0xC0C, 2, empty & 0xFFFF)
    for i in range(20):
        m.write(rxbuf + i, 1, 0xEE)
    start = (m.stimer_now() + 1600) & 0xFFFFFFFF                # 100 us ahead
    m.reg_write(0xF18, 4, start)
    m.reg_write(0xF16, 1, m.reg_read(0xF16, 1) | 4)
    m.reg_write(0xF20, 2, 0xFFFF)
    m.reg_write(0xF00, 1, 0x82)
    r.central = rec
    return aa, rxbuf


def radio():
    pdu = bytes([0x01, 0])
    # Every central packet lost.
    m = machine()
    r = ble_radio.Radio(m)
    m.reg_write(0x74A, 1, te.STIMER_CTRL_RESET | 0x02)
    r.air = ble_air.Air(loss_keyboard=1_000_000)
    rec = Recorder()
    aa, rxbuf = brx(m, r, rec)
    early = r.central_tx(pdu, 7, aa)
    early_lost, drawn = r.tx_lost, r.air.sent["keyboard"]
    run_until(m, m.ms() + 0.2)
    ok = r.central_tx(pdu, 7, aa)
    run_until(m, m.ms() + 1.0)
    entry = bytes(m.read(rxbuf + i, 1) for i in range(6))
    check(not early and not early_lost and drawn == 0 and len(r.misses) == 1,
          f"a packet before the BRX's start tick: a miss ({r.misses[0][2]}), not asked of the air")
    check(not ok and r.tx_lost and len(r.misses) == 1 and r.brx is not None and entry == b"\xee" * 6
          and m.reg_read(0xF20, 2) == 0 and not rec.replies and r.air.lost_n["keyboard"] == 1,
          "a packet in the open BRX lost on air: not taken, tx_lost set, no miss, no RX DMA entry, no RF status,"
          " the BRX still open")
    # None lost: taken; then the reply lost.
    m = machine()
    r = ble_radio.Radio(m)
    m.reg_write(0x74A, 1, te.STIMER_CTRL_RESET | 0x02)
    r.air = ble_air.Air(loss_host=1_000_000)
    rec = Recorder()
    aa, rxbuf = brx(m, r, rec)
    run_until(m, m.ms() + 0.2)
    ok = r.central_tx(pdu, 7, aa)
    run_until(m, m.ms() + 1.0)
    check(ok and not r.tx_lost and rec.lost == [bytes([0x05, 0])] and not rec.replies and r.brx is None
          and m.reg_read(0xF20, 2) == ble_radio.RX | ble_radio.TX | ble_radio.DONE,
          f"the reply lost on air: the central is told ({[p.hex() for p in rec.lost]}) and takes none; the event"
          f" ends (RF status 0x{m.reg_read(0xF20, 2):04x})")


def advertise(m, r):
    """One ADV_IND by STX2RX on channel 37 and the time for an answer to arrive."""
    buf = 0x840300
    for i, v in enumerate([8, 0, 0, 0, 0x40, 6] + list(bytes.fromhex("c1c2c3c4c5c6"))):
        m.write(buf + i, 1, v)
    m.reg_write(0xC0C, 2, buf & 0xFFFF)
    for i, v in enumerate(ble_radio.ADV_AA):
        m.reg_write(0x408 + i, 1, v)
    m.reg_write(0x40D, 1, 37)
    m.reg_write(0xF20, 2, 0xFFFF)
    m.reg_write(0xF00, 1, 0x87)
    run_until(m, m.ms() + 1.0)


def advertising():
    log = lambda s: None  # noqa: E731
    m = machine()
    r = ble_radio.Radio(m)
    m.reg_write(0x74A, 1, te.STIMER_CTRL_RESET | 0x02)
    r.air = ble_air.Air(loss_host=1_000_000)
    c = ble_central.Central(m, r, connect_after_ms=0, log=log)
    advertise(m, r)
    check(c.state == "scan" and len(r.adv_tx) == 1 and m.reg_read(0xF20, 2) == ble_radio.TX,
          f"an advertising packet lost on its way to the host: sent ({len(r.adv_tx)}), the central still in"
          f" state {c.state}")

    m = machine()
    r = ble_radio.Radio(m)
    m.reg_write(0x74A, 1, te.STIMER_CTRL_RESET | 0x02)
    r.air = ble_air.Air(loss_keyboard=1_000_000)
    c = ble_central.Central(m, r, connect_after_ms=0, log=log, establish=True)
    advertise(m, r)
    state = c.state
    run_until(m, m.ms() + 6.5 * 30 + 5)
    check(state == "connected" and r.conn_central is None and m.reg_read(0xF20, 2) == ble_radio.TX
          and c.state == "failed" and c.stats["events"] == 6 and "establish_failed_ms" in c.records,
          f"a CONNECT_IND lost on its way to the keyboard: no RX status, the central {state}; nothing heard in"
          f" {c.stats['events']} events: state {c.state}")


def central():
    log = lambda s: None  # noqa: E731
    plain = None
    for ppm in (0, 250, -40):
        m = machine()
        r = ble_radio.Radio(m)
        m.reg_write(0x74A, 1, te.STIMER_CTRL_RESET | 0x02)
        if ppm:
            r.air = ble_air.Air(clock_ppm=ppm)
        c = ble_central.Central(m, r, interval=24, win_offset=2, log=log)
        c.adva, c.adva_random = bytes(6), 0
        c.on_connect_sent(b"", 0.0)
        f = 1 + ppm / 1e6
        times = [c.base_t + k * c.interval_ms for k in range(4)]
        want = [(1.25 + 2.5) * f + 0.3 + k * 30 * f for k in range(4)]
        check(all(abs(a - b) < 1e-9 for a, b in zip(times, want)) and r.sched[0][0] == c.anchor0,
              f"clock {ppm:+d} ppm: event 0 at {times[0]:.6f} ms, the events {c.interval_ms:.6f} ms apart")
        if ppm == 0:
            plain = (c.anchor0, c.interval_ms)
    check(plain == (0.0 + 1.25 + 1.25 * 2 + 0.3, 24 * 1.25),
          f"without an air the times are the plain arithmetic's, bit for bit: {plain}")

    # A connection update under a clock: the window offset and the new interval are the central's time.
    m = machine()
    r = ble_radio.Radio(m)
    m.reg_write(0x74A, 1, te.STIMER_CTRL_RESET | 0x02)
    r.air = ble_air.Air(clock_ppm=250)
    c = ble_central.Central(m, r, interval=24, log=log, send_version=False, send_features=False)
    r.central_tx = lambda pdu, ch, aa: True
    c.adva, c.adva_random = bytes(6), 0
    c.on_connect_sent(b"", 0.0)
    c.connection_update(1, 3, 6, 0, 300, 2)
    for k in range(3):
        t, _, fn, _ = r.sched.pop(0)
        m.cycles = int(t * m.cpu_hz / 1000) + 1
        fn()
    f = 1.00025
    want = c.anchor0 + 2 * 30 * f + 1.25 * 3 * f + 0.3
    check(abs(c.base_t - want) < 1e-9 and abs(c.interval_ms - 7.5 * f) < 1e-12 and c.base_k == 2,
          f"a connection update at instant 2 under +250 ppm: the new anchor at {c.base_t:.6f} ms, the interval"
          f" {c.interval_ms:.6f} ms")

    # Jitter moves each event's start, not the times they are counted from.
    m = machine()
    r = ble_radio.Radio(m)
    m.reg_write(0x74A, 1, te.STIMER_CTRL_RESET | 0x02)
    r.air = ble_air.Air(seed=1, jitter_us=20)
    c = ble_central.Central(m, r, interval=24, log=log)
    r.central_tx = lambda pdu, ch, aa: True
    c.adva, c.adva_random = bytes(6), 0
    c.on_connect_sent(b"", 0.0)
    starts = []
    for k in range(8):
        t, _, fn, _ = r.sched.pop(0)
        starts.append(t)
        m.cycles = int(t * m.cpu_hz / 1000) + 1
        fn()
    rng = random.Random(6)
    want = [c.anchor0 + k * 30.0 + (rng.randrange(41) - 20) / 1000 for k in range(8)]
    check(starts == want, f"jitter 20 us: the events start {[round((s - c.anchor0 - k * 30) * 1000) for k, s in enumerate(starts)]}"
          " us off their times")

    # establish: stops at event 6 with nothing heard; goes on once a reply came, or without the option.
    for name, kw, hear, want in (("establish, nothing heard", dict(establish=True), False, ("failed", 6)),
                                 ("establish, a reply at event 3", dict(establish=True), True, ("connected", 10)),
                                 ("the default, nothing heard", {}, False, ("connected", 10))):
        m = machine()
        r = ble_radio.Radio(m)
        m.reg_write(0x74A, 1, te.STIMER_CTRL_RESET | 0x02)
        c = ble_central.Central(m, r, interval=24, log=log, **kw)
        r.central_tx = lambda pdu, ch, aa: False
        c.adva, c.adva_random = bytes(6), 0
        c.on_connect_sent(b"", 0.0)
        for k in range(10):
            if not r.sched:
                break
            t, _, fn, _ = r.sched.pop(0)
            m.cycles = int(t * m.cpu_hz / 1000) + 1
            fn()
            if hear and k == 3:
                c.on_air(bytes([0x01, 0]), 0, None)
        check((c.state, c.stats["events"]) == want, f"{name}: state {c.state} after {c.stats['events']} events")


def connected(**kw):
    """A central in a connection that began at 0, with nothing queued."""
    m = machine()
    r = ble_radio.Radio(m)
    m.reg_write(0x74A, 1, te.STIMER_CTRL_RESET | 0x02)
    c = ble_central.Central(m, r, log=lambda s: None, send_version=False, send_features=False, **kw)
    c.adva, c.adva_random = bytes(6), 0
    c.on_connect_sent(b"", 0.0)
    return c


def rules():
    for name, kw, want in (("mic_ends", dict(mic_ends=True), ("mic-failed", None, True)),
                           ("the default", {}, ("connected", None, False))):
        c = connected(**kw)
        c.event, c.cur_ch = 3, 0
        c.enc.update(sk=bytes(16), iv=bytes(8), rx=True)
        nxt = c.on_air(bytes([0x02, 9]) + bytes(9), 0, None)      # 5 octets and a MIC that is not theirs
        got = (c.state, nxt, "mic_failure_ms" in c.records)
        check(got == want and c.enc["mic_fail"] == 1, f"a packet that fails its MIC, {name}: state {c.state},"
              f" {c.enc['mic_fail']} failure counted")

    def name(item):
        llid, p = item
        if llid == 1 and not p:
            return "empty"
        if llid == 3:
            return ble_radio.LL_NAMES[p[0]] + (f" instant {p[10] | p[11] << 8}" if p[0] == 0 else "")
        return "L2CAP"

    want = {True: (["LL_ENC_REQ", "empty", "LL_START_ENC_RSP", "empty", "L2CAP", "LL_CONNECTION_UPDATE_IND instant 21"],
                   [False, True, True]),
            False: (["LL_ENC_REQ", "L2CAP", "LL_START_ENC_RSP", "LL_CONNECTION_UPDATE_IND instant 17", "empty", "empty"],
                    [True, True, False])}
    for on in (True, False):
        c = connected(ll_rules=on)
        c.event = 5
        c.queue_ll(bytes([0x03]) + bytes(22))                      # LL_ENC_REQ
        c.queue_l2cap(5, bytes([0x13, 1, 2, 0, 0, 0]))
        c.connection_update(1, 0, 6, 0, 300, 5 + 12)
        order, more = [c.take()], [c.more()]
        order.append(c.take())
        c.enc.update(sk=bytes(16), iv=bytes(8))
        c.ll_ctrl(bytes([0x05]))                                   # the peripheral's LL_START_ENC_REQ
        more.append(c.more())
        order.append(c.take())
        order.append(c.take())
        c.ll_ctrl(bytes([0x06]))                                   # its LL_START_ENC_RSP
        c.event = 9
        more.append(c.more())
        order.append(c.take())
        order.append(c.take())
        got = [name(x) for x in order]
        check((got, more) == want[on] and c.update["instant"] == (21 if on else 17),
              f"ll_rules {on}: sent in the order {got}; more to send after LL_ENC_REQ, after LL_START_ENC_REQ and"
              f" after the peripheral's LL_START_ENC_RSP: {more}")

    # reclock: an air put on the radio during the connection.
    c = connected()
    r, m = c.radio, c.m
    starts = []
    for k in range(6):
        if k == 3:
            r.air = ble_air.Air(clock_ppm=250)
            c.reclock()
        t, _, fn, _ = r.sched.pop(0)
        starts.append(t)
        m.cycles = int(t * m.cpu_hz / 1000) + 1
        fn()
    want = [c.anchor0 + k * 30.0 for k in range(4)] + [c.anchor0 + 90.0 + n * 30.0 * 1.00025 for n in (1, 2)]
    check(all(abs(a - b) < 1e-9 for a, b in zip(starts, want)) and starts[3] == c.anchor0 + 90.0,
          f"reclock before event 3 with +250 ppm: events 0-3 30 ms apart, event 4 {starts[4] - starts[3]:.6f} ms"
          f" and event 5 {starts[5] - starts[4]:.6f} ms after the one before")

    c = connected(ll_rules=True)
    c.event = 5
    c.queue_ll(bytes([0x03]) + bytes(22))
    c.queue_l2cap(5, bytes([0x13, 1, 2, 0, 0, 0]))
    first, held = c.take(), c.take()
    c.ll_ctrl(bytes([0x0D, 0x06]))                                 # LL_REJECT_IND, key missing
    after = c.take()
    check((name(first), name(held), name(after)) == ("LL_ENC_REQ", "empty", "L2CAP"),
          f"ll_rules: an LL_REJECT_IND ends the wait: {name(first)}, {name(held)}, then {name(after)}")

    # Another host's data: queue_data() with its tag, on_ack and rx_data.
    c = connected()
    c.event, c.cur_ch = 3, 0
    got, acked, own = [], [], []
    c.rx_data = lambda llid, p: got.append((llid, bytes(p)))
    c.on_ack = acked.append
    c.l2cap = lambda cid, data: own.append(cid)
    c.queue_data(2, b"\x03\x00\x04\x00\x0a\x03\x00", "first")
    c.queue_data(1, b"\x55", "second")
    c.queue_ll(bytes([0x12]))                                      # LL_PING_REQ: no tag
    sent = [c.next_pdu()]
    sent.append(c.on_air(bytes([0x01, 0]), 0, None))               # acknowledges nothing: the first one again
    sent.append(c.on_air(bytes([0x02 | 1 << 2 | 1 << 3, 5]) + b"\x01\x00\x04\x00\x0b", 0, None))   # acknowledges; L2CAP start
    sent.append(c.on_air(bytes([0x01, 1]) + b"\x77", 0, None))     # acknowledges; continuation
    last = c.on_air(bytes([0x01 | 1 << 2 | 1 << 3, 0]), 0, None)   # acknowledges the LL_PING_REQ
    shape = [(p[0] & 3, bytes(p[2:])) for p in sent]
    check(shape == [(2, b"\x03\x00\x04\x00\x0a\x03\x00")] * 2 + [(1, b"\x55"), (3, b"\x12")]
          and acked == ["first", "second"] and got == [(2, b"\x01\x00\x04\x00\x0b"), (1, b"\x77")] and not own
          and last is None,
          f"another host's data: sent {[(l, p.hex()) for l, p in shape]}, acknowledged {acked}, received"
          f" {[(l, p.hex()) for l, p in got]}, {len(own)} frame(s) to the central's own L2CAP")


def main():
    air()
    radio()
    advertising()
    central()
    rules()
    print(f"\n{len(failures)} failure(s)")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())

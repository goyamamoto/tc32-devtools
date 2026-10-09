"""A scripted BLE central for tc32emu, next to the radio model (ble_radio.py).

It connects to the image under test and talks to it over the modelled
TLSR8278 radio: link layer (channel selection algorithm #1 with a channel
map, SN/NESN, retransmissions, LL control, encryption), L2CAP, signaling,
an ATT client and SMP legacy Just Works pairing, through a whole
connection.

What a test adds:
- att_script: a function of the central returning a generator. It yields
  ATT requests (bytes) and gets each response, or yields ("pair",) and gets
  the pairing result. Without one the central sends no ATT.
- event_hooks: functions (central, event) run at the start of every
  connection event, for key presses or scripted LL control.
- The helpers queue_ll(), channel_map_update(), connection_update(),
  terminate() and go_silent() for LL procedures from those hooks.
- join=True: the central is added to Radio.centrals instead of becoming
  Radio.central, so several hear the advertising (ble_radio.py); aa= gives
  each its own access address.
- active_scan=True: scan_request() answers ADV_IND and ADV_SCAN_IND with a
  SCAN_REQ while the central scans; the radio asks it when no central
  connects to that packet.
- own_addr: "public" (the default) connects from the public identity
  address (identity); "rpa" from a resolvable private address (Core Vol 3
  Part H 2.2.2) made from irk with a new prand for each central, so for
  each connection a host makes with a new central; "rpa-wrong-irk" hashes
  it with another IRK. inita and inita_type (TxAdd) are the address used;
  new_address() draws another.
- The peripheral's LL_TERMINATE_IND: the central acknowledges it with its
  packet in the next connection event (an empty PDU, or its unacknowledged
  one again, NESN advanced) and leaves (Core Vol 6 Part B 5.1.6; state
  "terminate-ack" until then). ack_terminate=False leaves at once, as a
  central whose acknowledgement is lost; a central gone silent leaves
  without one.
- dist_id=True: the Pairing Request's InitKeyDist is IdKey
  (init_key_dist); when the response keeps it, the central sends its
  Identity Information (irk) and Identity Address Information (public,
  identity) after the peripheral's keys.
- Another host above this link layer (an HCI controller's): with rx_data
  set, each LL data PDU received (LLID 1 or 2) goes to rx_data(llid,
  payload) and this central's own L2CAP, ATT and SMP stay out of it;
  queue_data(llid, payload, tag) queues one LL data PDU of that host, and
  on_ack(tag) hears when the peripheral has acknowledged it.

With an air on the radio (Radio.air, ble_air.py):
- The central's clock is the air's: the transmit window's delay and the
  connection interval last clock() times their length, so its events move
  away from where the peripheral's clock puts them; each event starts
  jitter_ms() off its time. A test that changes the air during a connection
  calls reclock(): the interval is the new clock's from the next event on.
- A packet of its own lost on air is marked in the transcript (LOST on air)
  and not counted as missed by the peripheral; a reply lost on air is not
  taken (air_lost), so the central's packet stays unacknowledged and goes
  out again.
- establish=True: a connection in which the central has heard nothing by
  event 6 was not established (Core Vol 6 Part B 4.5.2: six connection
  intervals); the central stops, state "failed". The default goes on, as
  without an air.

mic_ends=True: a packet of the peripheral that fails its MIC ends the
connection at once, with no packet (Core Vol 6 Part B 5.1.3.1: the link layer
leaves the connection state, reason MIC failure, 0x3d); state "mic-failed".
The default counts the failure (enc["mic_fail"]) and goes on.

ll_rules=True: the central keeps two rules of the link layer that the
default does not. While its LL_ENC_REQ is under way (sent, and neither the
peripheral's LL_START_ENC_RSP nor its rejection received) it sends only that
procedure's LL_START_ENC_RSP and LL_TERMINATE_IND, and what else is queued
waits (Core Vol 6 Part B 5.1.3.1). And an LL_CONNECTION_UPDATE_IND's instant
is as far ahead of the event it is first sent in as connection_update() was
given it ahead of the event it was called in, so a queue that holds it back
does not use the instant up; until it is sent the central keeps its old
parameters, whatever instant it was given. An update first sent in a later
packet of the event just before its instant, after that event has timed the
next one, still moves the instant's event to its transmit window. The default
sends the queue in its order with the instant as given and moves at that
instant.

TC32EMU_CENTRAL_SKIP_EVERY=N (N > 0): from connection event 20 on, the
central sends nothing at every Nth event (k % N == 0), so the peripheral's
own timer ends those events. 0, the default, never skips.

SPDX-License-Identifier: Apache-2.0
"""
import os
import random
import struct

import aes128
import ble_sc
import p256
from ble_radio import LL_NAMES, ATT_NAMES

SKIP_EVERY = int(os.environ.get("TC32EMU_CENTRAL_SKIP_EVERY", "0"))
if SKIP_EVERY < 0:
    raise ValueError("TC32EMU_CENTRAL_SKIP_EVERY: " + str(SKIP_EVERY))
DEFAULT_IRK = bytes(range(0xA0, 0xB0))      # the central's IRK, LSO first, as Identity Information carries it
_rpa_n = 0                                  # resolvable private addresses made so far in this process


class Central:
    """A scripted BLE central: link layer, L2CAP, signaling, ATT client, SMP."""

    def __init__(self, m, radio, interval=24, latency=0, timeout=500, hop=7, win_size=2, win_offset=0,
                 connect_after_ms=30, anchor_in_window_ms=0.3, log=print, max_exch=4, pair=True,
                 apply_update=False, drop_at=(), chm=b"\xff\xff\xff\xff\x1f", att_script=None,
                 event_hooks=(), send_version=True, send_features=True, refuse_update=False,
                 on_param_request=None, join=False, aa=0x71764129, active_scan=False, own_addr="public",
                 irk=DEFAULT_IRK, dist_id=False, ack_terminate=True, establish=False, mic_ends=False,
                 ll_rules=False, param_update_lead=0):
        if own_addr not in ("public", "rpa", "rpa-wrong-irk"):
            raise ValueError("own_addr: " + own_addr)
        self.m, self.radio = m, radio
        self.establish = establish
        self.mic_ends = mic_ends
        self.ll_rules = ll_rules
        self.enc_busy = False            # ll_rules: the central's LL_ENC_REQ is under way
        self.update_lead = 0             # ll_rules: the events the update's instant was given ahead
        self.update_sent = False         # ll_rules: the update's PDU has gone out, its instant set
        self.heard = False               # a reply of the peripheral in this connection
        if join:
            radio.centrals.append(self)
        else:
            radio.central = self
        self.log = log
        self.p = dict(interval=interval, latency=latency, timeout=timeout, hop=hop,
                      win_size=win_size, win_offset=win_offset)
        self.connect_after_ms = connect_after_ms
        self.anchor_in_window_ms = anchor_in_window_ms
        self.aa = struct.pack("<I", aa)
        self.aa_reg = self.aa[::-1]                 # as a link layer writes 0x408..0x40b
        self.identity = bytes.fromhex("665544332211")   # the public identity address, LSO first
        self.own_addr, self.irk = own_addr, bytes(irk)
        self.init_key_dist = 0x02 if dist_id else 0x00  # IdKey: the central's IRK and identity address
        self.active_scan = active_scan
        self.ack_terminate = ack_terminate
        self.new_address()
        self.state = "scan"
        self.sn = self.nesn = 0
        self.inflight = None
        self.txq = []
        self.event = -1
        self.unmapped = 0
        self.chm = bytes(chm)
        self.chm_update = None
        self.transcript = []
        self.l2 = None
        self.max_exch = max_exch
        self.exch = 0
        self.stats = dict(events=0, replies=0, missed=0, retx=0)
        self.records = {}
        self.channels = []
        self.sent = []                   # (event, ms) of every packet the central started
        self.ntf = []
        self.att_log = []
        self.script = att_script(self) if att_script else None
        self.att_waiting = False
        self.att_started = False
        self.event_hooks = list(event_hooks)
        self.send_version = send_version
        self.send_features = send_features
        self.version_sent = False
        self.silent_from = None
        self.adva = None
        self.adva_random = 1
        self.pair = pair
        self.apply_update = apply_update
        self.param_update_lead = param_update_lead
        # The peripheral's L2CAP Connection Parameter Update Request: answered
        # accepted (result 0) and, with apply_update, followed by an
        # LL_CONNECTION_UPDATE_IND whose instant is param_update_lead events
        # after the event it is queued in (0: 12); with ll_rules the lead counts
        # from the event the PDU first goes out in. With refuse_update answered
        # rejected (result 1) and never applied. on_param_request(req, action),
        # when set, is called after the answer is queued: req the request's
        # values (t_ms, event, interval_min, interval_max, latency, timeout,
        # instant: the update's instant when applied, else None), action
        # "accepted", "applied" or "refused".
        self.refuse_update = refuse_update
        self.on_param_request = on_param_request
        self.on_bond = None              # called when the peripheral's keys are all in, the central's own queued but not sent
        self.rx_data = None              # rx_data(llid, payload): the LL data PDUs received go to it, not to l2cap()
        self.on_ack = None               # on_ack(tag): a PDU queued with queue_data was acknowledged
        self.drop_at = set(drop_at)      # events whose first non-empty reply the central loses
        self.dropped = []
        self.update = None
        self.rng = random.Random(1)
        self.enc = dict(sk=None, iv=None, tx=False, rx=False, txc=0, rxc=0, ltk=None, skdm=None, ivm=None,
                        mic_fail=0)
        self.smp = {}
        # Secure Connections (start_pairing with sc): the central's IO capability (0 DisplayOnly, 3
        # NoInputNoOutput), whether it asks for MITM protection, its private key (None: drawn from rng) and
        # the passkey it shows (None: drawn from rng); displayed_passkey is the one it showed in a pairing.
        self.io_cap = 0x03
        self.mitm = False
        self.sc_priv = None
        self.passkey = None
        self.displayed_passkey = None
        # A fault the central commits on purpose, for the responder's negative tests: "confirm" (a wrong
        # confirm value), "dhkey" (a wrong DHKey check), "offcurve" (a public key off the curve), "stall"
        # (nothing after its public key), "keysize7" (a 7-octet key asked for; no fault, the keys are masked),
        # "keysize6" (a key size below the least allowed).
        self.fault = None

    # ------------------------------------------------------------ addresses and advertising
    def new_address(self):
        """The address this central connects and scans from: the public identity, or a resolvable
        private address with the next prand (own_addr "rpa"; "rpa-wrong-irk" hashes it with another
        IRK than the one dist_id distributes)."""
        global _rpa_n
        if self.own_addr == "public":
            self.inita, self.inita_type = self.identity, 0
            return
        prand = (0x15A3C7 + 0x0B1D2F * _rpa_n) & 0x3FFFFF
        _rpa_n += 1
        key = self.irk if self.own_addr == "rpa" else bytes(b ^ 0xFF for b in self.irk)
        self.inita, self.inita_type = aes128.rpa(key, prand), 1

    def scan_request(self, pdu, ch):
        """With active_scan, a SCAN_REQ for a scannable advertising packet (ADV_IND, ADV_SCAN_IND)
        heard in state "scan"; else None. The radio asks only when no central connects."""
        if not self.active_scan or self.state != "scan" or len(pdu) < 8 or pdu[0] & 0xF not in (0, 6):
            return None
        req = bytes([0x03 | (self.inita_type << 6) | (pdu[0] & 0x40) << 1, 12]) + self.inita + pdu[2:8]
        self.rec("air", f"ch {ch} <- {'ADV_IND' if pdu[0] & 0xF == 0 else 'ADV_SCAN_IND'} {pdu.hex()}")
        self.rec("air", f"ch {ch} -> SCAN_REQ {req.hex()}")
        return req

    def on_adv(self, pdu, ch):
        if self.state != "scan" or self.m.ms() < self.connect_after_ms or pdu[0] & 0xF != 0 or len(pdu) < 8:
            return None
        self.adva = pdu[2:8]
        self.adva_random = (pdu[0] >> 6) & 1          # the advertiser's TxAdd
        payload = self.inita + self.adva + self.aa + bytes([0x55, 0x44, 0x33]) + bytes([self.p["win_size"]]) \
            + struct.pack("<HHHH", self.p["win_offset"], self.p["interval"], self.p["latency"],
                          self.p["timeout"]) + self.chm + bytes([self.p["hop"] | (1 << 5)])
        hdr = bytes([0x05 | (self.inita_type << 6) | (self.adva_random << 7), len(payload)])  # CONNECT_IND, RxAdd as advertised
        self.state = "connecting"
        self.adv_pdu = pdu
        self.rec("air", f"ch {ch} <- ADV_IND {pdu.hex()}")
        self.rec("air", f"ch {ch} -> CONNECT_IND {(hdr + payload).hex()} interval {self.p['interval']} "
                 f"latency {self.p['latency']} timeout {self.p['timeout']} hop {self.p['hop']}")
        return hdr + payload

    def on_connect_sent(self, pdu, t_end):
        self.state = "connected"
        self.heard = False
        self.enc_busy = False
        self.t_conn_end = t_end
        clk = self.clock()
        self.anchor0 = t_end + 1.25 * clk + 1.25 * clk * self.p["win_offset"] + self.anchor_in_window_ms
        self.interval_ms = self.p["interval"] * 1.25 * clk
        self.base_t, self.base_k = self.anchor0, 0
        # The central starts the link layer procedures a real one would.
        if self.send_version:
            self.queue_ll(bytes([0x0C, 0x0B, 0x0F, 0x00, 0x34, 0x12]))      # LL_VERSION_IND 5.2, Broadcom, 0x1234
            self.version_sent = True
        if self.send_features:
            self.queue_ll(bytes([0x08]) + bytes([0x01, 0, 0, 0, 0, 0, 0, 0]))  # LL_FEATURE_REQ (encryption)
        self.radio.at(self.anchor0 + self.jitter_ms(), lambda: self.conn_event(0), owner="central")

    # ------------------------------------------------------------ the air (ble_air.py)
    def clock(self):
        """The central's time for one unit of the peripheral's: 1.0 without an air."""
        air = self.radio.air
        return 1.0 if air is None else air.clock()

    def jitter_ms(self):
        air = self.radio.air
        return 0.0 if air is None else air.jitter_ms()

    def reclock(self):
        """The radio's air changed during a connection: from the next event on, the interval is the new
        clock's. The next event keeps the time it has."""
        if self.state != "connected" or self.event < 0:
            return
        k = self.event + 1
        self.base_t, self.base_k = self.base_t + (k - self.base_k) * self.interval_ms, k
        self.interval_ms = self.p["interval"] * 1.25 * self.clock()

    def tx_note(self, ok):
        """The transcript's note on a packet the peripheral did not take."""
        if ok:
            return ""
        return "   (LOST on air)" if self.radio.tx_lost else "   (MISSED by the peripheral)"

    def air_lost(self, pdu):
        """The peripheral's reply was lost on air: nothing is received."""
        self.rec("rx", f"ev {self.event:4d} ch {self.cur_ch:2d} <- {self.fmt(pdu)}  (LOST on air)")

    def air_lost_tx(self, pdu):
        """The central's packet after a reply in this event was lost on air."""
        self.rec("tx", f"ev {self.event:4d} ch {self.cur_ch:2d} -> the packet above  (LOST on air)")

    # ------------------------------------------------------------ link layer
    def chm_used(self, ch):
        return (self.chm[ch >> 3] >> (ch & 7)) & 1

    def channel(self):
        """Channel selection algorithm #1 (Core Vol 6 Part B 4.5.8.2)."""
        self.unmapped = (self.unmapped + self.p["hop"]) % 37
        if self.chm_used(self.unmapped):
            return self.unmapped
        used = [ch for ch in range(37) if self.chm_used(ch)]
        return used[self.unmapped % len(used)]

    def conn_event(self, k):
        if self.state not in ("connected", "terminate-ack"):
            return
        if self.establish and k >= 6 and not self.heard:
            self.state = "failed"
            self.records["establish_failed_ms"] = round(self.m.ms(), 3)
            self.rec("tx", f"ev {k:4d} nothing heard from the peripheral in 6 events: the connection was not"
                     " established")
            return
        u = self.update
        if self.ll_rules and u and not u.get("applied") and self.update_sent and k == u["instant"]:
            # The update went out after the event before its instant had timed this one with the old
            # parameters: this event moves to the update's transmit window.
            self.move(u, k, self.base_t + (k - self.base_k) * self.interval_ms)
            self.radio.at(self.base_t + self.jitter_ms(), lambda: self.conn_event(k), owner="central")
            return
        self.event = k
        self.stats["events"] += 1
        u = self.chm_update
        if u and k == u["instant"]:
            self.chm = u["chm"]
            u["applied"] = round(self.m.ms(), 3)
            self.records["chm_update_applied"] = dict(u, chm=u["chm"].hex())
            self.chm_update = None
        ch = self.channel()
        self.channels.append((k, ch))
        self.exch = 0
        self.cur_ch = ch
        self.hook_event(k)
        for h in self.event_hooks:
            h(self, k)
        if self.state not in ("connected", "terminate-ack"):
            return
        skipped = SKIP_EVERY > 0 and k >= 20 and k % SKIP_EVERY == 0
        if skipped:
            self.rec("tx", f"ev {k:4d} ch {ch:2d} -> nothing (TC32EMU_CENTRAL_SKIP_EVERY)")
        silent = self.silent_from is not None and k >= self.silent_from
        if self.state == "terminate-ack" and (silent or not skipped):
            # The peripheral's LL_TERMINATE_IND is acknowledged by this packet (NESN advanced
            # when it arrived), then the central leaves (Core Vol 6 Part B 5.1.6).
            if not silent:
                pdu = self.next_pdu() if self.inflight else bytes([0x01 | (self.nesn << 2) | (self.sn << 3), 0])
                self.sent.append((k, self.m.ms()))
                ok = self.radio.central_tx(pdu, ch, self.aa_reg)
                self.rec("tx", f"ev {k:4d} ch {ch:2d} -> {self.fmt(pdu)}  (acknowledges LL_TERMINATE_IND)"
                         + self.tx_note(ok))
                self.records["terminate_ack_ms"] = round(self.m.ms(), 3)
            self.state = "terminated"
            return
        if silent or skipped:
            ok = None
        else:
            pdu = self.next_pdu()
            self.sent.append((k, self.m.ms()))
            ok = self.radio.central_tx(pdu, ch, self.aa_reg)
            self.rec("tx", f"ev {k:4d} ch {ch:2d} -> {self.fmt_tx(pdu)}" + self.tx_note(ok))
            if not ok and not self.radio.tx_lost:
                self.stats["missed"] += 1
        nxt = self.base_t + (k + 1 - self.base_k) * self.interval_ms
        u = self.update
        if u and not u.get("applied") and (self.update_sent or not self.ll_rules) and k + 1 == u["instant"]:
            self.move(u, k + 1, nxt)
            nxt = self.base_t
        self.radio.at(nxt + self.jitter_ms(), lambda: self.conn_event(k + 1), owner="central")

    def move(self, u, k, anchor):
        """The connection update u from event k, its instant, on (Core Vol 6 Part B 5.1.1: the transmit
        window starts WinOffset after anchor, the one the old parameters give the instant)."""
        clk = self.clock()
        self.base_t, self.base_k = anchor + 1.25 * clk * u["win_offset"] + self.anchor_in_window_ms, k
        self.interval_ms = u["interval"] * 1.25 * clk
        self.p.update(interval=u["interval"], latency=u["latency"], timeout=u["timeout"])
        u["applied"] = round(self.base_t, 3)
        self.records["conn_update_applied"] = dict(u)

    def next_pdu(self):
        """The packet to send: the one in flight again, or the next queued one
        (encrypted once, when it is taken, so retransmissions repeat it)."""
        if self.inflight is None:
            llid, payload, tag = self.take_tagged()
            plain = payload
            if self.enc["tx"] and payload:
                nonce = (self.enc["txc"] | 1 << 39).to_bytes(5, "little") + self.enc["iv"]
                payload = aes128.ccm_encrypt(self.enc["sk"], nonce, llid, payload)
                self.enc["txc"] += 1
            self.inflight = (llid, payload, plain, tag)
        else:
            self.stats["retx"] += 1
        llid, payload, plain = self.inflight[:3]
        md = 1 if self.more() else 0
        pdu = bytes([llid | (self.nesn << 2) | (self.sn << 3) | (md << 4), len(payload)]) + payload
        self.last_plain = None if plain == payload else bytes([pdu[0], len(plain)]) + plain
        return pdu

    @staticmethod
    def enc_pdu(item):
        """A queued PDU the central may send while its LL_ENC_REQ is under way: LL_START_ENC_RSP, LL_TERMINATE_IND."""
        return item[0] == 3 and item[1][:1] in (b"\x06", b"\x02")

    def take(self):
        """The next queued PDU to send, or an empty one (ll_rules: the module's docstring)."""
        return self.take_tagged()[:2]

    def take_tagged(self):
        """take(), and with the PDU the tag queue_data() gave it (None for any other)."""
        if not self.ll_rules:
            item = self.txq.pop(0) if self.txq else (1, b"")
            return item[0], item[1], item[2] if len(item) > 2 else None
        if self.enc_busy:
            for i, item in enumerate(self.txq):
                if self.enc_pdu(item):
                    item = self.txq.pop(i)
                    return item[0], item[1], None
            return (1, b"", None)
        if not self.txq:
            return (1, b"", None)
        item = self.txq.pop(0)
        llid, p, tag = item[0], item[1], item[2] if len(item) > 2 else None
        if llid == 3 and p[:1] == b"\x03":
            self.enc_busy = True
        elif llid == 3 and p[:1] == b"\x00" and self.update and not self.update.get("applied"):
            self.update["instant"] = (self.event + self.update_lead) & 0xFFFF
            self.update_sent = True
            p = p[:10] + struct.pack("<H", self.update["instant"])
        return llid, p, tag

    def more(self):
        """Whether a PDU waits behind the one in flight (the MD bit)."""
        if self.ll_rules and self.enc_busy:
            return any(self.enc_pdu(item) for item in self.txq)
        return bool(self.txq)

    def on_air(self, pdu, ch, tx_addr):
        """The peripheral's reply; returns the next PDU to send in this event, or None."""
        self.stats["replies"] += 1
        self.heard = True
        hdr0 = pdu[0]
        llid, nesn, sn, md = hdr0 & 3, (hdr0 >> 2) & 1, (hdr0 >> 3) & 1, (hdr0 >> 4) & 1
        acked = nesn != self.sn
        if acked:
            self.sn ^= 1
            if self.inflight and self.inflight[0] == 3 and self.inflight[2][:1] == b"\x02":
                self.state = "terminated"            # our LL_TERMINATE_IND got through
                self.records["terminate_acked_ms"] = round(self.m.ms(), 3)
            if self.inflight and self.inflight[3] is not None and self.on_ack:
                self.on_ack(self.inflight[3])
            self.inflight = None
        new = sn == self.nesn
        if new:
            self.nesn ^= 1
        payload, shown, note = pdu[2:2 + pdu[1]], pdu, ""
        if new and pdu[1] and self.enc["rx"]:
            nonce = self.enc["rxc"].to_bytes(5, "little") + self.enc["iv"]
            payload, ok = aes128.ccm_decrypt(self.enc["sk"], nonce, hdr0, payload)
            self.enc["rxc"] += 1
            shown, note = bytes([hdr0, len(payload)]) + payload, "  (decrypted" + (")" if ok else ", MIC FAILED)")
            if not ok:
                self.enc["mic_fail"] += 1
        self.rec("rx", f"ev {self.event:4d} ch {ch:2d} <- {self.fmt(shown)}{note}" + ("" if new else "  (dup)")
                 + ("" if acked else "  (our packet not acked)"))
        if self.mic_ends and note.endswith("MIC FAILED)"):
            self.state = "mic-failed"
            self.records["mic_failure_ms"] = round(self.m.ms(), 3)
            self.rec("rx", f"ev {self.event:4d} the central leaves the connection: MIC failure (0x3d)")
            return None
        if new and pdu[1]:
            self.rx_pdu(llid, payload)
        self.exch += 1
        if self.state != "connected":
            return None
        if (md or self.more() or self.inflight) and self.exch < self.max_exch:
            nxt = self.next_pdu()
            self.rec("tx", f"ev {self.event:4d} ch {ch:2d} -> {self.fmt_tx(nxt)}")
            return nxt
        return None

    def drop(self, pdu):
        if self.event in self.drop_at and pdu[1]:
            self.drop_at.discard(self.event)
            self.dropped.append((round(self.m.ms(), 3), self.event, pdu.hex()))
            self.rec("rx", f"ev {self.event:4d} ch {self.cur_ch:2d} <- {self.fmt(pdu)}  (LOST: the central ignores it)")
            return True
        return False

    def fmt_tx(self, pdu):
        if self.last_plain is not None:
            return self.fmt(self.last_plain) + "  (sent encrypted)"
        return self.fmt(pdu)

    def queue_ll(self, payload):
        self.txq.append((3, payload))

    def queue_l2cap(self, cid, data):
        frame = struct.pack("<HH", len(data), cid) + data
        first = True
        while frame:
            chunk, frame = frame[:27], frame[27:]
            self.txq.append((2 if first else 1, chunk))
            first = False

    def queue_data(self, llid, payload, tag):
        """One LL data PDU of another host (LLID 2 starts an L2CAP frame, 1 goes on with it), at most 27
        octets; on_ack(tag) is called when the peripheral acknowledges it."""
        self.txq.append((llid, bytes(payload), tag))

    # ------------------------------------------------------------ LL procedures a test starts
    def channel_map_update(self, chm, instant):
        """LL_CHANNEL_MAP_IND; the central switches at the instant."""
        self.chm_update = dict(chm=bytes(chm), instant=instant & 0xFFFF)
        self.queue_ll(bytes([0x01]) + bytes(chm) + struct.pack("<H", instant & 0xFFFF))

    def connection_update(self, win_size, win_offset, interval, latency, timeout, instant):
        """LL_CONNECTION_UPDATE_IND; the central moves at the instant."""
        self.update = dict(win_size=win_size, win_offset=win_offset, interval=interval, latency=latency,
                           timeout=timeout, instant=instant & 0xFFFF)
        self.update_lead = (instant - self.event) & 0xFFFF
        self.update_sent = False
        self.queue_ll(bytes([0x00, win_size]) + struct.pack("<HHHHH", win_offset, interval, latency, timeout,
                                                            instant & 0xFFFF))

    def terminate(self, reason=0x13):
        """LL_TERMINATE_IND; the central leaves once it is acknowledged."""
        self.queue_ll(bytes([0x02, reason]))

    def go_silent(self, event):
        """From this event on the central sends nothing (it went away)."""
        self.silent_from = event

    def rx_pdu(self, llid, payload):
        if llid == 3:
            self.ll_ctrl(payload)
        elif self.rx_data is not None:
            self.rx_data(llid, payload)
        elif llid == 2:
            ln, cid = struct.unpack_from("<HH", payload)
            self.l2 = [ln, cid, payload[4:]]
            self.l2_check()
        elif llid == 1 and self.l2:
            self.l2[2] += payload
            self.l2_check()

    def l2_check(self):
        ln, cid, data = self.l2
        if len(data) >= ln:
            self.l2 = None
            self.l2cap(cid, data[:ln])

    def ll_ctrl(self, p):
        op = p[0]
        name = LL_NAMES.get(op, hex(op))
        self.records.setdefault("ll_from_peripheral", []).append((round(self.m.ms(), 3), name, p.hex()))
        if op == 0x0C:
            self.records["peripheral_version"] = dict(VersNr=p[1], CompId=hex(p[2] | p[3] << 8),
                                                      SubVersNr=hex(p[4] | p[5] << 8))
            if not self.version_sent:
                self.queue_ll(bytes([0x0C, 0x0B, 0x0F, 0x00, 0x34, 0x12]))
                self.version_sent = True
        elif op == 0x09:
            self.records["peripheral_features"] = p[1:9].hex()
        elif op == 0x0E:
            self.records["peripheral_features_req"] = p[1:9].hex()
            self.queue_ll(bytes([0x09, 0x01, 0, 0, 0, 0, 0, 0, 0]))
        elif op == 0x14:
            self.records["peripheral_length_req"] = struct.unpack_from("<HHHH", p, 1)
            self.queue_ll(bytes([0x15]) + struct.pack("<HHHH", 27, 328, 27, 328))
        elif op == 0x15:
            self.records["peripheral_length_rsp"] = struct.unpack_from("<HHHH", p, 1)
        elif op == 0x0F:
            self.records["peripheral_conn_param_req"] = struct.unpack_from("<HHHHBHHHHHHH", p, 1)
            self.queue_ll(bytes([0x07, 0x0F]))
        elif op == 0x12:
            self.queue_ll(bytes([0x13]))
        elif op == 0x16:
            self.records["peripheral_phy_req"] = p[1:3].hex()
            self.queue_ll(bytes([0x07, 0x16]))
        elif op == 0x02:
            self.records["peripheral_terminate"] = p.hex()
            self.records["peripheral_terminate_ms"] = round(self.m.ms(), 3)
            self.state = "terminate-ack" if self.ack_terminate else "terminated"
        elif op == 0x04 and self.enc["skdm"] is None:   # an LL_ENC_REQ a test sent by hand
            self.records.setdefault("peripheral_enc_rsp", []).append(p.hex())
        elif op == 0x04:                            # LL_ENC_RSP: SKDs, IVs
            skd = int.from_bytes(self.enc["skdm"] + p[1:9], "little")
            self.enc["sk"] = aes128.e(self.enc["ltk"], skd).to_bytes(16, "big")
            self.enc["iv"] = self.enc["ivm"] + p[9:13]
        elif op == 0x05:                            # LL_START_ENC_REQ (sent unencrypted)
            self.enc["tx"] = self.enc["rx"] = True
            self.txq.insert(0, (3, bytes([0x06])))  # LL_START_ENC_RSP, the first encrypted packet
        elif op == 0x06:                            # the peripheral's LL_START_ENC_RSP (encrypted)
            self.records["encryption_on_ms"] = round(self.m.ms(), 3)
            self.enc_busy = False
            s = self.smp
            if s.get("pka") is not None and s.get("pres") and (s["pres"][6] & 0x07) == 0:
                self.smp_complete()                   # Secure Connections, nothing to come from the responder
        elif op in (0x0D, 0x11):
            self.records.setdefault("peripheral_reject", []).append(p.hex())
            self.enc_busy = False
        elif op == 0x07:
            self.records.setdefault("peripheral_unknown_rsp", []).append(p.hex())
        elif op == 0x13:
            self.records.setdefault("peripheral_ping_rsp", []).append(round(self.m.ms(), 3))
        else:
            self.queue_ll(bytes([0x07, op]))

    # ------------------------------------------------------------ L2CAP
    def l2cap(self, cid, data):
        if cid == 4:
            self.att_rx(data)
        elif cid == 5:
            code, ident = data[0], data[1]
            self.records.setdefault("signaling_from_peripheral", []).append((round(self.m.ms(), 3), data.hex()))
            if code == 0x12:
                mn, mx, lat, to = struct.unpack_from("<HHHH", data, 4)
                self.records["l2cap_conn_param_update_req"] = dict(t_ms=round(self.m.ms(), 3), event=self.event,
                                                                    interval_min=mn, interval_max=mx,
                                                                    latency=lat, timeout=to)
                req = dict(self.records["l2cap_conn_param_update_req"], instant=None)
                if self.refuse_update:
                    self.queue_l2cap(5, bytes([0x13, ident]) + struct.pack("<HH", 2, 1))
                    action = "refused"
                else:
                    self.queue_l2cap(5, bytes([0x13, ident]) + struct.pack("<HH", 2, 0))
                    action = "accepted"
                    if self.apply_update and self.update is None:
                        self.connection_update(1, 0, mx, lat, to, self.event + (self.param_update_lead or 12))
                        action, req["instant"] = "applied", self.update["instant"]
                if self.on_param_request is not None:
                    self.on_param_request(req, action)
            elif code == 0x13:
                pass
            else:
                self.queue_l2cap(5, bytes([0x01, ident]) + struct.pack("<HH", 2, 0))
        elif cid == 6:
            self.records.setdefault("smp_from_peripheral", []).append((round(self.m.ms(), 3), data.hex()))
            self.smp_rx(data)
        else:
            self.records.setdefault("l2cap_other", []).append((cid, data.hex()))

    # ------------------------------------------------------------ ATT
    def att_send(self, pdu):
        self.att_log.append((round(self.m.ms(), 3), "->", ATT_NAMES.get(pdu[0], hex(pdu[0])), pdu.hex()))
        self.queue_l2cap(4, pdu)

    def att_rx(self, data):
        op = data[0]
        self.att_log.append((round(self.m.ms(), 3), "<-", ATT_NAMES.get(op, hex(op)), data.hex()))
        if op == 0x1B:
            self.ntf.append((round(self.m.ms(), 3), data[1] | data[2] << 8, data[3:].hex()))
            return
        if op == 0x1D:
            self.ntf.append((round(self.m.ms(), 3), data[1] | data[2] << 8, data[3:].hex() + " (ind)"))
            self.att_send(bytes([0x1E]))
            return
        if op == 0x02:          # the server exchanges MTU
            self.records["peripheral_mtu_req"] = data[1] | data[2] << 8
            self.att_send(bytes([0x03]) + struct.pack("<H", 247))
            return
        if op in (0x04, 0x08, 0x0A, 0x0C, 0x10, 0x12):   # other server requests: not supported
            self.att_send(bytes([0x01, op, 0, 0, 0x06]))
            return
        if self.att_waiting:
            self.att_waiting = False
            self.script_step(data)

    def script_step(self, value):
        if self.script is None:
            return
        try:
            req = self.script.send(value)
        except StopIteration:
            return
        if isinstance(req, tuple) and req[0] == "pair":
            self.start_pairing()
            return
        self.att_waiting = True
        self.att_send(req)

    # ------------------------------------------------------------ SMP (legacy Just Works) and LL encryption
    def smp_send(self, d):
        self.queue_l2cap(6, d)

    def start_pairing(self, sc=False):
        # No OOB, AuthReq bonding (with sc also Secure Connections, and MITM when asked for), 16-octet keys,
        # initiator init_key_dist (nothing, or IdKey with dist_id), responder EncKey | IdKey; the IO
        # capability NoInputNoOutput unless io_cap says otherwise.
        auth = 0x01 | (0x08 if sc else 0) | (0x04 if sc and self.mitm else 0)
        key_size = {"keysize7": 7, "keysize6": 6}.get(self.fault, 0x10)
        self.smp = dict(preq=bytes([0x01, self.io_cap if sc else 0x03, 0x00, auth, key_size, self.init_key_dist, 0x03]),
                        t0=round(self.m.ms(), 3), keys={})
        self.m.flash_log_start = len(self.m.flash.log)
        self.smp_send(self.smp["preq"])

    # ------------------------------------------------------------ Secure Connections (initiator)
    def sc_method(self):
        """Just Works, or Passkey Entry with this side showing the passkey (Core 2.3.5.1, Table 2.8 with the
        responder KeyboardOnly or NoInputNoOutput)."""
        s = self.smp
        rio = s["pres"][1]
        if not ((s["preq"][3] | s["pres"][3]) & 0x04) or rio == 0x03 or self.io_cap == 0x03:
            return "jw"
        return "passkey-display"

    def sc_start(self):
        s = self.smp
        while True:
            priv = self.sc_priv if self.sc_priv is not None else self.rng.getrandbits(256)
            if 1 <= priv < p256.N:
                break
            self.sc_priv = None
        s["priv"], s["pkb"] = priv, None
        s["pka"] = p256.point_to_le(p256.public_key(priv))
        s["method"] = self.sc_method()
        s["round"] = 0
        if s["method"] == "passkey-display":
            s["passkey"] = self.passkey if self.passkey is not None else self.rng.randrange(1000000)
            self.displayed_passkey = s["passkey"]
        pk = s["pka"]
        if self.fault == "offcurve":
            pk = pk[:63] + bytes([pk[63] ^ 0x01])
        self.smp_send(bytes([0x0C]) + pk)

    def sc_z(self):
        s = self.smp
        if s["method"] == "jw":
            return 0
        return 0x80 | ((s["passkey"] >> s["round"]) & 1)

    def sc_send_confirm(self):
        s = self.smp
        s["na"] = bytes(self.rng.randrange(256) for _ in range(16))
        cai = ble_sc.f4(s["pka"][:32], s["pkb"][:32], s["na"], self.sc_z())
        if self.fault == "confirm":
            cai = bytes([cai[0] ^ 0x01]) + cai[1:]
        self.smp_send(bytes([0x03]) + cai)

    def sc_dhkey_check(self):
        s = self.smp
        dh = p256.dhkey(s["priv"], p256.point_from_le(s["pkb"]))
        a1 = ble_sc.addr7(self.inita_type, self.inita)
        a2 = ble_sc.addr7(self.adva_random, self.adva)
        s["mackey"], ltk = ble_sc.f5(dh, s["na"], s["nb"], a1, a2)
        key_size = min(s["preq"][4], s["pres"][4])
        ltk = ltk[:key_size] + bytes(16 - key_size)
        s["ltk"] = ltk.hex()
        r = s["passkey"].to_bytes(16, "little") if s["method"] != "jw" else bytes(16)
        s["r"] = r
        ea = ble_sc.f6(s["mackey"], s["na"], s["nb"], r, ble_sc.iocap_of(s["preq"]), a1, a2)
        if self.fault == "dhkey":
            ea = bytes([ea[0] ^ 0x01]) + ea[1:]
        self.smp_send(bytes([0x0D]) + ea)
        self.enc["ltk"] = int.from_bytes(ltk, "little")

    def sc_rx(self, code, d):
        """The Secure Connections PDUs; True when handled here."""
        s = self.smp
        if code == 0x0C:                            # Pairing Public Key (the responder's)
            s["pkb"] = bytes(d[1:65])
            pk = p256.point_from_le(s["pkb"])
            s["pkb_on_curve"] = p256.on_curve(pk)
            if not s["pkb_on_curve"]:
                self.smp_send(bytes([0x05, 0x0B]))
                return True
            if self.fault == "stall":
                return True                      # and nothing more: the responder's timeout
            if s["method"] == "passkey-display":
                self.sc_send_confirm()           # round 0: the initiator's confirm comes first
            return True
        if code == 0x03 and s.get("pkb") is not None:   # Pairing Confirm (Cb, or a round's Cbi)
            s["cb"] = bytes(d[1:17])
            if self.fault == "stall":
                return True
            if s["method"] == "jw":
                s["na"] = bytes(self.rng.randrange(256) for _ in range(16))
            self.smp_send(bytes([0x04]) + s["na"])
            return True
        if code == 0x04 and s.get("pkb") is not None:   # Pairing Random (Nb, or Nbi)
            s["nb"] = bytes(d[1:17])
            ok = ble_sc.f4(s["pkb"][:32], s["pka"][:32], s["nb"], self.sc_z()) == s["cb"]
            s.setdefault("confirms_ok", []).append(ok)
            if not ok:
                s["sconfirm_ok"] = False
                self.smp_send(bytes([0x05, 0x04]))
                self.script_step(dict(ok=False, reason=0x04))
                return True
            s["round"] += 1
            if s["method"] != "jw" and s["round"] < 20:
                self.sc_send_confirm()
                return True
            s["sconfirm_ok"] = True
            self.sc_dhkey_check()
            return True
        if code == 0x0D:                            # DHKey Check (Eb)
            a1 = ble_sc.addr7(self.inita_type, self.inita)
            a2 = ble_sc.addr7(self.adva_random, self.adva)
            eb = ble_sc.f6(s["mackey"], s["nb"], s["na"], s["r"], ble_sc.iocap_of(s["pres"]), a2, a1)
            s["dhkey_check_ok"] = bytes(d[1:17]) == eb
            if not s["dhkey_check_ok"]:
                self.smp_send(bytes([0x05, 0x0B]))
                self.script_step(dict(ok=False, reason=0x0B))
                return True
            self.enc["skdm"] = bytes(self.rng.randrange(256) for _ in range(8))
            self.enc["ivm"] = bytes(self.rng.randrange(256) for _ in range(4))
            self.queue_ll(bytes([0x03]) + bytes(8) + bytes(2) + self.enc["skdm"] + self.enc["ivm"])  # LL_ENC_REQ
            return True
        return False

    def smp_rx(self, d):
        code, s = d[0], self.smp
        if code == 0x02 and (d[3] & s["preq"][3] & 0x08):   # Pairing Response, Secure Connections agreed
            s["pres"] = bytes(d[:7])
            self.sc_start()
        elif s.get("pka") is not None and self.sc_rx(code, d):
            pass
        elif code == 0x02:                          # Pairing Response (legacy)
            s["pres"] = bytes(d[:7])
            s["mrand"] = bytes(self.rng.randrange(256) for _ in range(16))
            mc = aes128.c1(0, int.from_bytes(s["mrand"], "little"), s["preq"], s["pres"], self.inita_type, self.inita,
                           self.adva_random, self.adva)
            self.smp_send(bytes([0x03]) + mc.to_bytes(16, "little"))
        elif code == 0x03:                          # Pairing Confirm
            s["sconfirm"] = bytes(d[1:17])
            self.smp_send(bytes([0x04]) + s["mrand"])
        elif code == 0x04:                          # Pairing Random
            s["srand"] = bytes(d[1:17])
            want = aes128.c1(0, int.from_bytes(s["srand"], "little"), s["preq"], s["pres"], self.inita_type,
                             self.inita, self.adva_random, self.adva)
            s["sconfirm_ok"] = want.to_bytes(16, "little") == s["sconfirm"]
            stk = aes128.s1(0, int.from_bytes(s["srand"], "little"), int.from_bytes(s["mrand"], "little"))
            self.enc["ltk"] = stk
            self.enc["skdm"] = bytes(self.rng.randrange(256) for _ in range(8))
            self.enc["ivm"] = bytes(self.rng.randrange(256) for _ in range(4))
            self.queue_ll(bytes([0x03]) + bytes(8) + bytes(2) + self.enc["skdm"] + self.enc["ivm"])  # LL_ENC_REQ
        elif code == 0x05:                          # Pairing Failed
            s["failed"] = d[1]
            self.script_step(dict(ok=False, reason=d[1]))
        elif code in (0x06, 0x07, 0x08, 0x09):
            s["keys"][{6: "LTK", 7: "EDIV_Rand", 8: "IRK", 9: "identity_address"}[code]] = d[1:].hex()
            if code == 0x09:
                self.smp_complete()
        elif code == 0x0B:
            s["security_request"] = d.hex()

    def smp_complete(self):
        """The responder's keys are in (Core Vol 3 Part H 3.6.1), or it distributes none: the central's IdKey
        goes out when it offered it and the response kept it, and the pairing is done."""
        s = self.smp
        if s.get("done_ms"):
            return
        if s["preq"][5] & s.get("pres", bytes(7))[5] & 0x02:
            self.smp_send(bytes([0x08]) + self.irk)
            self.smp_send(bytes([0x09, 0x00]) + self.identity)
            s["sent_keys"] = dict(IRK=self.irk.hex(), identity_address=(bytes([0]) + self.identity).hex())
        s["done_ms"] = round(self.m.ms(), 3)
        if self.on_bond is not None:
            self.on_bond()
        self.script_step(dict(ok=True))

    def hook_event(self, k):
        if self.script is not None and not self.att_started and k >= 4:
            self.att_started = True
            self.script_step(None)

    # ------------------------------------------------------------ output
    def rec(self, kind, text):
        line = f"[{self.m.ms():9.3f}] {kind:3s} {text}"
        self.transcript.append(line)
        self.log(line)

    @staticmethod
    def fmt(pdu):
        hdr0, ln = pdu[0], pdu[1]
        llid = hdr0 & 3
        bits = f"NESN{(hdr0 >> 2) & 1} SN{(hdr0 >> 3) & 1} MD{(hdr0 >> 4) & 1}"
        p = pdu[2:2 + ln]
        if llid == 1 and ln == 0:
            what = "empty"
        elif llid == 3:
            what = LL_NAMES.get(p[0], hex(p[0])) + " " + p[1:].hex()
        elif llid == 2 and ln >= 4:
            l2len, cid = struct.unpack_from("<HH", p)
            body = p[4:]
            if cid == 4 and body:
                what = f"ATT {ATT_NAMES.get(body[0], hex(body[0]))} {body.hex()}"
            elif cid == 5:
                what = f"L2CAP signaling {body.hex()}"
            elif cid == 6:
                what = f"SMP {body.hex()}"
            else:
                what = f"L2CAP cid {cid} len {l2len} {body.hex()}"
        else:
            what = f"LLID{llid} {p.hex()}"
        return f"[{bits} len {ln:2d}] {what}"

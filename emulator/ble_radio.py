"""TLSR8278 radio model for tc32emu: the RF state machine, its DMA, the
hardware TX FIFO, SN/NESN and the AES block, as far as a BLE peripheral's link
layer drives them through connection, pairing, encryption and notifications.
What no document states is marked so below, and where a firmware could
depend on it a TC32EMU_* option chooses the behaviour. ble_central.py (or a
test) sets Radio.central; with no central the radio only transmits.

More centrals can hear the advertising: Radio.centrals, after Radio.central.
Each advertising packet (STX2RX) gets one answer: on_adv(pdu, channel) is
asked of each in that order and the first CONNECT_IND goes out; if none
connects, scan_request(pdu, channel) is asked of each and the first SCAN_REQ
goes out (scan_reqs records it). A CONNECT_IND makes its central the
connection's (conn_central): its packets' replies go to it (drop, on_air)
while it is attached, else to Radio.central.

Radio.air (ble_air.Air; None, the default: every packet arrives) loses
packets on their way:
- An advertising packet lost on its way to the host is offered to no central.
- An answer to it lost on its way to the keyboard (CONNECT_IND, SCAN_REQ)
  leaves no RX DMA entry and no RX status; the central of a CONNECT_IND is
  still told it was sent (on_connect_sent), as it cannot know.
- A central's packet in a connection event is asked about only when the
  receiver would have taken it; lost, central_tx returns False with tx_lost
  set, and the BRX stays open as when no packet came.
- The chip's reply lost on its way to the host: the central is told
  (air_lost, when it has one) and takes no reply, and the event ends as when
  the central has nothing more to send.

- 0xf00 commands: 0x80 stop, 0x82 BRX (peripheral connection event, RX
  first), 0x85 STX, 0x87 STX2RX (advertising). A command starts at the
  system timer tick in 0xf18 when 0xf16 bit 2 is set. What the chip does with
  a tick that has passed when the command is written is in no document: the
  model starts a BRX at once, logs it and records it in late_starts.
- RF IRQ status 0xf20/0xf21 is write-one-to-clear; interrupt source 13 is the
  level of (status & 0xf1c/0xf1d mask).
- RX: DMA to 0x840000 | 0xc08: dma_len u32 = len + 13, header, payload, 3
  CRC bytes, 4 timestamp bytes, 3 bytes (RSSI at p[len+15]), status p[len+16]
  (bit 0 = CRC error). 0x450 reads the timestamp of the last packet
  (anchor + 0x500 ticks).
- TX FIFO: u16 SRAM addresses written to 0xc2c append at wptr (0xc2b);
  0xc2a reads rptr; writing it empties the FIFO (bit 4), sets rptr (bit 6,
  low 4 bits) or steps it (bit 5). FIFO[rptr] is the packet in flight; the
  packet at 0xc0c goes out when the FIFO is empty.
- TC32EMU_TXFIFO_CLEAR: where bit 4 leaves the pointers, which no document
  states. "position" (the default): rptr takes wptr's value and wptr stays,
  so a firmware must read 0xc2a back to know the position (after writing
  0x10, save 0xc2a & 0xf and later write 0x40 | that value). "zero": both
  pointers are 0.
- TC32EMU_RX_BUSY: 0x448 bit 5, which a link layer reads as "a packet is
  being received" (not documented). "off" (the default): the register reads
  as written, so the bit is 0; "air": 1 while a central packet is on air in
  an open BRX, from its start to its end; "stuck": always 1, the worst case
  for a firmware that waits on it.
- Advertising (STX, STX2RX in the BLE format) sends the packet at 0xc0c. What
  the chip sends when the TX FIFO still holds packets (rptr != wptr) is not
  documented; a device that advertised so could not be found or connected
  to until a power-on. The model sends 0xc0c's packet as always and records the
  transmission in adv_fifo and as a miss.
- SN/NESN from 0xf03 bits 4-5 at the event start, read back from 0xf22 bit 0
  and 0xf23 bit 4.
- AES block: 0x540 control (bit 0 decrypt, bit 2 done), key 0x550-0x55f in
  standard AES order, data in and out as four u32 at 0x548.

Telink's private packet format (TPLL, the SDK's tbl_rf_private_* modes; 0x404
bits 1:0 = 2, BLE 1, Zigbee and Hybee 0), with a peer set in Radio.tpll (a
2.4G dongle model, for example) that hears the chip's packets and answers in
its receive window. With no peer, nothing changes:
- STX (0xf00 = 0x85): the packet at 0x840000 | 0xc0c, u32 dma_len, header
  (bits 5:0 = L), payload[L], goes out 6 us after the command (0xc0c is
  written after it) and ends after its air time; then TX status, and
  peer.on_tpll(payload, mhz, access_code) -> the reply payload or None.
  mhz = ((0x1245 & 0x3f) << 6) | (0x1244 >> 2); the access code is 0x408..
  for 0x405 & 7 bytes, as the registers hold them.
- STX2RX (0xf00 = 0x87): as STX, and at the packet's end the chip listens by
  itself, as after an SRX started then; with 0xf03 bit 2 set, no packet within
  0xf0a (16 bits) + 1 us ends it with the RX timeout status (0x4).
- A reply starts 150 us after the end of the packet. The chip hears it when
  an SRX (0xf00 = 0x86) window is open on the same frequency and access
  code when it starts: RX DMA at 0x840000 | 0xc08, u32 dma_len = L + 11,
  header, payload, CRC[2], timestamp[4], 2 bytes, RSSI, status (bit 0 = CRC
  error), then RX status. Otherwise a miss is logged.
- SRX opens at 0xf18 when 0xf16 bit 2 is set, else now, and ends with the
  packet or with 0x80. The first timeout (0xf28 + 1 us, status 0x400) fires
  only when 0xf03 bit 1 is set; without it a firmware needs a timeout of
  its own.
- Air time: preamble (0x402 bits 4:0) + access code + header + L + CRC 2
  bytes at 2 Mbps when 0x1220 = 0x04 (the 2M tables), 1 Mbps otherwise.

- TC32EMU_SUSPEND_RF=lost (the default "keep" leaves everything as it was):
  a suspend (0x6f bit 7, analog 0x7e = 0) sets the registers a link layer
  sets again after one (RF_SUSPEND_REGS: those its radio initialisation
  writes, and the PA power bits in 0x1225-0x1227 that setting the TX power
  changes) back to their values when the radio model was attached, and the
  radio starts no command but 0x80 until every one of them that the firmware
  had written since the last reset has been written again (its own setup: a
  BLE setup may write all of them, a setup for Telink's private format all
  but 0x401); a refused command is logged and sends and hears nothing. A
  reset (the registers at their power-on values) ends a refusal and starts
  the record of writes afresh. The SDK's ble_remote says the radio's
  settings "will be reset in suspend"; what the chip's registers hold
  after a suspend is in none of our documents, so the model makes the radio
  unusable until the firmware sets it up again rather than guess values.

SPDX-License-Identifier: Apache-2.0
"""
import os

import aes128

M32 = 0xFFFFFFFF
TICKS_PER_US = 16
RX, TX, RXTO, CRC2, DONE, FSMTO, FIRSTTO = 0x1, 0x2, 0x4, 0x10, 0x20, 0x40, 0x400
TURNAROUND_MS = 0.150            # TPLL: from the end of the chip's packet to the start of the peer's reply
ADV_AA = bytes.fromhex("8e89bed6")     # 0x408..0x40b as a link layer writes them (MSB first)
# The registers a link layer's radio initialisation writes, byte by byte: 26 bytes in 17
# writes (0x12d2, 0x124a and 0x464 are 16-bit writes, 0x1254 and 0x460 32-bit).
RF_INIT_REGS = (0x12D2, 0x12D3, 0x127B, 0x1279, 0x124A, 0x124B, 0x1254, 0x1255, 0x1256, 0x1257,
                0x1276, 0x134E, 0x134C, 0x401, 0x402, 0x430, 0x460, 0x461, 0x462, 0x463, 0x464, 0x465,
                0xF06, 0xF0C, 0xF0E, 0xF10)
# The PA power registers setting the TX power changes (0x1225 bit 6, 0x1226 bit 7, 0x1227 bits 4:0),
# which a link layer sets again after every suspend.
RF_POWER_REGS = (0x1225, 0x1226, 0x1227)
RF_SUSPEND_REGS = RF_INIT_REGS + RF_POWER_REGS
RF_SUSPEND_SET = frozenset(RF_SUSPEND_REGS)
SUSPEND_RF = os.environ.get("TC32EMU_SUSPEND_RF", "keep")
if SUSPEND_RF not in ("keep", "lost"):
    raise ValueError("TC32EMU_SUSPEND_RF: " + SUSPEND_RF)
RX_BUSY = os.environ.get("TC32EMU_RX_BUSY", "off")
if RX_BUSY not in ("off", "air", "stuck"):
    raise ValueError("TC32EMU_RX_BUSY: " + RX_BUSY)
TXFIFO_CLEAR = os.environ.get("TC32EMU_TXFIFO_CLEAR", "position")
if TXFIFO_CLEAR not in ("position", "zero"):
    raise ValueError("TC32EMU_TXFIFO_CLEAR: " + TXFIFO_CLEAR)

LL_NAMES = {0x00: "LL_CONNECTION_UPDATE_IND", 0x01: "LL_CHANNEL_MAP_IND", 0x02: "LL_TERMINATE_IND",
            0x03: "LL_ENC_REQ", 0x04: "LL_ENC_RSP", 0x05: "LL_START_ENC_REQ", 0x06: "LL_START_ENC_RSP",
            0x07: "LL_UNKNOWN_RSP", 0x08: "LL_FEATURE_REQ", 0x09: "LL_FEATURE_RSP",
            0x0a: "LL_PAUSE_ENC_REQ", 0x0b: "LL_PAUSE_ENC_RSP", 0x0c: "LL_VERSION_IND",
            0x0d: "LL_REJECT_IND", 0x0e: "LL_PERIPHERAL_FEATURE_REQ", 0x0f: "LL_CONNECTION_PARAM_REQ",
            0x10: "LL_CONNECTION_PARAM_RSP", 0x11: "LL_REJECT_EXT_IND", 0x12: "LL_PING_REQ",
            0x13: "LL_PING_RSP", 0x14: "LL_LENGTH_REQ", 0x15: "LL_LENGTH_RSP", 0x16: "LL_PHY_REQ",
            0x17: "LL_PHY_RSP", 0x18: "LL_PHY_UPDATE_IND", 0x19: "LL_MIN_USED_CHANNELS_IND"}
ATT_NAMES = {0x01: "Error Rsp", 0x02: "Exchange MTU Req", 0x03: "Exchange MTU Rsp", 0x04: "Find Info Req",
             0x05: "Find Info Rsp", 0x08: "Read By Type Req", 0x09: "Read By Type Rsp", 0x0a: "Read Req",
             0x0b: "Read Rsp", 0x0c: "Read Blob Req", 0x0d: "Read Blob Rsp", 0x10: "Read By Group Type Req",
             0x11: "Read By Group Type Rsp", 0x12: "Write Req", 0x13: "Write Rsp", 0x52: "Write Cmd",
             0x1b: "Handle Value Ntf", 0x1d: "Handle Value Ind", 0x1e: "Handle Value Cfm"}


def airtime_us(pdu):
    """1M PHY: preamble 1, access address 4, header 2, payload, CRC 3 bytes."""
    return (1 + 4 + 2 + pdu[1] + 3) * 8


class Radio:
    """The TLSR8278 RF block as far as a BLE peripheral's link layer uses it."""

    def __init__(self, m, log=None):
        self.m = m
        self.log = log or (lambda s: None)
        self.sched = []
        self.seq = 0
        self.fifo = [0] * 16
        self.wptr = self.rptr = 0
        self.brx = None
        self.sn_rb = self.nesn_rb = 0
        self.ts = 0
        self.central = None
        self.centrals = []       # more centrals that hear advertising, after central
        self.conn_central = None # the central whose CONNECT_IND was sent last
        self.air = None          # ble_air.Air: packets lost on their way; None: every packet arrives
        self.tx_lost = False     # the last central_tx's packet was lost on air
        self.scan_reqs = []      # (ms, channel, central, SCAN_REQ PDU) of each SCAN_REQ sent
        self.adv_tx = []
        self.cmds = []
        self.misses = []
        self.adv_fifo = []       # (ms, channel, rptr, wptr) of each advertising packet sent with the TX FIFO not empty
        self.fifo_log = []
        self.aes_in, self.aes_out, self.aes_ops = b"", b"", 0
        self.leads = []          # ms from the BRX command to each central packet
        self.rx_on = []          # us from the BRX start tick (0xf18) to each central packet
        self.late_starts = []    # (ms, us) of each BRX command written after its start tick (0xf18)
        self.tpll = None         # TPLL peer: on_tpll(payload, mhz, access_code) -> reply payload or None
        self.srx = None          # the open SRX window
        self.tpll_tx = []        # (ms, mhz, access code, payload) of every TPLL packet sent
        self.rf_power_on = {a: m.regs_mem[a] for a in RF_SUSPEND_REGS}
        self.rf_uninit = set()   # of rf_written, those not written again since a suspend (SUSPEND_RF "lost")
        self.rf_written = set()  # RF_SUSPEND_REGS the firmware has written since the last reset
        self.rf_losses = 0       # suspends that lost them
        self.rf_refused = []     # (ms, command, loss number) the radio did not start for that
        self._rd, self._wr = m.reg_read, m.reg_write
        m.reg_read, m.reg_write = self.reg_read, self.reg_write
        ut = m.update_time

        def update_time():
            ut()
            self.tick()
        m.update_time = update_time
        m.level_sources.append(self.irq_level)
        m.reset_hooks = getattr(m, "reset_hooks", []) + [self.on_reset]
        # An idle loop jumps to the next timer compare (Machine.run): not past
        # the radio's next scheduled step, or the air traffic would run late.
        cts = m.cycles_to_stimer

        def cycles_to_stimer(target):
            n = cts(target)
            if self.sched:
                n = min(n, max(0, int((self.sched[0][0] - m.ms()) * m.cpu_hz / 1000)))
            return n
        m.cycles_to_stimer = cycles_to_stimer

    # ------------------------------------------------------------ time
    def at(self, ms, fn, owner="radio"):
        """Run fn at simulated time ms. owner "central" marks the central's own
        steps, which a reset of the machine does not cancel."""
        self.seq += 1
        self.sched.append((ms, self.seq, fn, owner))
        self.sched.sort(key=lambda e: e[:2])

    def tick(self):
        now = self.m.ms()
        while self.sched and self.sched[0][0] <= now:
            fn = self.sched.pop(0)[2]
            fn()

    def on_reset(self):
        """The machine was reset (watchdog or software): the radio's state and
        its pending steps go; the central goes on, as a real one would."""
        self.sched = [e for e in self.sched if e[3] == "central"]
        self.fifo = [0] * 16
        self.wptr = self.rptr = 0
        self.brx = None
        self.sn_rb = self.nesn_rb = 0
        self.ts = 0
        self.aes_in, self.aes_out = b"", b""
        self.srx = None
        self.rf_uninit = set()   # the registers are at their power-on values again
        self.rf_written = set()

    def ticks_ago(self, us):
        return (self.m.stimer_now() - int(us * TICKS_PER_US)) & M32

    # ------------------------------------------------------------ registers
    def irq_level(self):
        r = self.m.regs_mem
        return (1 << 13) if (r[0xF20] | r[0xF21] << 8) & (r[0xF1C] | r[0xF1D] << 8) else 0

    def set_status(self, bits):
        r = self.m.regs_mem
        r[0xF20] |= bits & 0xFF
        r[0xF21] |= bits >> 8

    def rx_busy(self):
        """0x448 bit 5 under TC32EMU_RX_BUSY."""
        if RX_BUSY == "stuck":
            return True
        if RX_BUSY == "air" and self.brx is not None and "on_air" in self.brx:
            t0, t1 = self.brx["on_air"]
            return t0 <= self.m.ms() < t1
        return False

    def reg_read(self, o, size):
        if o <= 0x448 < o + size and RX_BUSY != "off":
            v = self._rd(o, size)
            return v | (0x20 << 8 * (0x448 - o)) if self.rx_busy() else v
        if size == 1:
            if o == 0xC2A:
                return self.rptr
            if o == 0xC2B:
                return self.wptr
            if o == 0xF22:
                return self.sn_rb
            if o == 0xF23:
                return self.nesn_rb << 4
        if o == 0x450 and size == 4:
            return self.ts
        if o == 0x548 and size == 4:
            v = int.from_bytes(self.aes_out[:4].ljust(4, b"\0"), "little")
            self.aes_out = self.aes_out[4:]
            return v
        return self._rd(o, size)

    def aes_write(self, o, size, val):
        r = self.m.regs_mem
        if o == 0x540:
            r[0x540] = (val & 0x01) | 0x02          # data feed wanted, not finished
            self.aes_in, self.aes_out = b"", b""
            return
        self.aes_in += (val & 0xFFFFFFFF).to_bytes(4, "little")
        if len(self.aes_in) == 16:
            key = bytes(r[0x550:0x560])
            f = aes128.decrypt if r[0x540] & 1 else aes128.encrypt
            self.aes_out = f(key, self.aes_in)
            self.aes_in = b""
            self.aes_ops += 1
            r[0x540] = (r[0x540] & 0x01) | 0x04

    def reg_write(self, o, size, val):
        m, r = self.m, self.m.regs_mem
        if SUSPEND_RF == "lost":
            for a in range(o, o + size):
                if a in RF_SUSPEND_SET:
                    self.rf_written.add(a)
                    self.rf_uninit.discard(a)
        if o <= 0xF21 and o + size > 0xF20:
            for i in range(size):
                a, b = o + i, (val >> (8 * i)) & 0xFF
                if a in (0xF20, 0xF21):
                    r[a] &= ~b & 0xFF
                else:
                    self._wr(a, 1, b)
            return
        if o == 0xC2A and size == 1:
            if val & 0x10:
                if TXFIFO_CLEAR == "position":
                    self.rptr = self.wptr
                else:
                    self.rptr = self.wptr = 0
            if val & 0x40:
                self.rptr = val & 0xF
            if val & 0x20:
                self.rptr = (self.rptr + 1) & 0xF
            r[0xC2A] = val
            return
        if (o == 0x540 and size == 1) or (o == 0x548 and size == 4):
            self.aes_write(o, size, val)
            return
        if o == 0xC2C and size == 2:
            self.fifo[self.wptr] = val & 0xFFFF
            self.fifo_log.append((m.ms(), self.wptr, val & 0xFFFF))
            self.wptr = (self.wptr + 1) & 0xF
            return
        self._wr(o, size, val)
        if SUSPEND_RF == "lost" and o <= 0x6F < o + size and m.asleep is not None:
            for a in RF_SUSPEND_REGS:
                r[a] = self.rf_power_on[a]
            self.rf_uninit = set(self.rf_written)
            self.rf_losses += 1
        if o == 0x61 and size == 1 and val & 1:
            self.brx = None                      # RF reset (a link layer pulses it before each event)
        if o <= 0xF00 < o + size:
            self.on_cmd(r[0xF00])

    # ------------------------------------------------------------ commands
    def on_cmd(self, cmd):
        m, r, t = self.m, self.m.regs_mem, self.m.ms()
        if self.rf_uninit and cmd != 0x80:
            if not self.rf_refused or self.rf_refused[-1][2] != self.rf_losses:
                m.event(f"radio: command 0x{cmd:02x} after a suspend, with {len(self.rf_uninit)} of the "
                        f"radio registers it had set up not written again (first 0x{min(self.rf_uninit):x}): "
                        f"nothing sent or heard until they are")
            self.rf_refused.append((round(t, 3), cmd, self.rf_losses))
            return
        if self.tpll is not None and self.tpll_format():
            if cmd in (0x85, 0x87):
                self.cmds.append((t, cmd, r[0x40D]))
                self.at(t + 0.006, lambda: self.tpll_tx_start(cmd == 0x87))
                return
            if cmd == 0x86:
                self.cmds.append((t, cmd, r[0x40D]))
                start = int.from_bytes(r[0xF18:0xF1C], "little") if r[0xF16] & 4 else m.stimer_now()
                s = dict(t=t, start=start, mhz=self.tpll_mhz(), ac=self.tpll_access_code(), rx=0)
                self.srx = s
                if r[0xF03] & 2:
                    us = int.from_bytes(r[0xF28:0xF2C], "little") + 1
                    self.at(t + us / 1000, lambda s=s: s["rx"] == 0 and self.srx_timeout(s))
                return
            if cmd == 0x80:
                self.srx = None
        self.cmds.append((t, cmd, r[0x40D]))
        if cmd == 0x80:
            self.brx = None
            return
        if cmd == 0x82:
            start = int.from_bytes(r[0xF18:0xF1C], "little") if r[0xF16] & 4 else m.stimer_now()
            late = (m.stimer_now() - start) & M32
            if 0 < late < 0x80000000:
                self.late_starts.append((t, late / 16))
                self.log(f"[{t:9.3f}] radio: BRX command {late / 16:.0f} us after its start tick")
            b = dict(t=t, start=start, ch=r[0x40D], aa=bytes(r[0x408:0x40C]),
                     crcinit=int.from_bytes(r[0x424:0x427], "little"),
                     last_sn=(r[0xF03] >> 4) & 1, nesn=(r[0xF03] >> 5) & 1, rx=0, f03=r[0xF03])
            self.brx = b
            if r[0xF03] & 1:        # FSM timeout
                us = int.from_bytes(r[0xF2C:0xF30], "little")
                self.at(t + us / 1000, lambda b=b: self.timeout(b, FSMTO))
            if r[0xF03] & 2:        # first RX timeout
                us = int.from_bytes(r[0xF28:0xF2C], "little")
                if us < 0x0FFFFFFF:
                    self.at(t + us / 1000, lambda b=b: b["rx"] == 0 and self.timeout(b, FIRSTTO))
            return
        if cmd in (0x85, 0x87):
            # rf_start_stx2rx writes the DMA address 0xc0c after the command and
            # schedules the start 100 ticks ahead, so the packet is read when
            # the transmission starts, not at the command.
            self.at(t + 0.006, lambda cmd=cmd, t=t: self.adv_tx_start(cmd, t))
            return

    def adv_tx_start(self, cmd, t0):
        m, r = self.m, self.m.regs_mem
        t = m.ms()
        a = 0x840000 | int.from_bytes(r[0xC0C:0xC0E], "little")
        raw = bytes(m.read(a + i, 1) for i in range(6 + 40))
        pdu = raw[4:6 + raw[5]]
        self.adv_tx.append((t, r[0x40D], pdu))
        if self.rptr != self.wptr and not self.tpll_format():
            self.adv_fifo.append((t, r[0x40D], self.rptr, self.wptr))
            why = (f"advertising with {(self.wptr - self.rptr) & 0xF} packet(s) in the TX FIFO "
                   f"(0xc2a {self.rptr}, 0xc2b {self.wptr})")
            self.misses.append((t, r[0x40D], why))
            if len(self.adv_fifo) == 1:
                m.event(f"radio: {why}; what the chip sends then is not documented")
        self.set_status(TX)     # a link layer polls TX done after STX/STX2RX
        centrals = self.offered()
        if centrals and self.air is not None and self.air.lost("host", r[0x40D], t):
            self.log(f"[{t:9.3f}] radio: advertising packet on ch {r[0x40D]} lost on air")
            centrals = []
        if cmd == 0x87 and centrals and bytes(r[0x408:0x40C]) == ADV_AA:
            # One answer per advertising packet: the first central that connects,
            # else the first that scans.
            who, rsp = None, None
            for c in centrals:
                rsp = c.on_adv(pdu, r[0x40D])
                if rsp is not None:
                    who = c
                    break
            if rsp is None:
                for c in centrals:
                    rsp = c.scan_request(pdu, r[0x40D]) if hasattr(c, "scan_request") else None
                    if rsp is not None:
                        who = c
                        self.scan_reqs.append((t, r[0x40D], c, rsp))
                        break
            if rsp is not None:
                t_rx = t + (airtime_us(pdu) + 150) / 1000
                if self.air is not None and self.air.lost("keyboard", r[0x40D], t_rx):
                    self.log(f"[{t_rx:9.3f}] radio: answer to the advertising packet on ch {r[0x40D]} lost on air")
                    if rsp[0] & 0xF == 5:                            # CONNECT_IND: its central cannot know
                        self.at(t_rx + airtime_us(rsp) / 1000, lambda rsp=rsp, who=who:
                                who.on_connect_sent(rsp, self.m.ms()))
                    return
                # The DMA streams the header in as the packet arrives (the adv
                # event polls the entry's header word); RX status at its end.
                self.at(t_rx + 0.056, lambda rsp=rsp: self.dma_rx(rsp, t_rx, status=False))
                self.at(t_rx + airtime_us(rsp) / 1000,
                        lambda rsp=rsp, t_rx=t_rx, who=who: self.adv_rx_end(rsp, t_rx, who))

    def offered(self):
        """The centrals that hear advertising, in the order they answer: central, then centrals."""
        out = [self.central] if self.central is not None else []
        return out + [c for c in self.centrals if c is not self.central]

    def link_central(self):
        """The central of the connection: the one whose CONNECT_IND was sent, while it is
        still attached, else central."""
        c = self.conn_central
        return c if c is not None and any(c is x for x in self.offered()) else self.central

    def timeout(self, b, bit):
        if self.brx is b:
            self.brx = None
            self.set_status(bit)
            self.log(f"[{self.m.ms():9.3f}] radio: BRX timeout 0x{bit:x}")
        return True

    def dma_rx(self, pdu, t_start, status=True):
        m, r = self.m, self.m.regs_mem
        a = 0x840000 | int.from_bytes(r[0xC08:0xC0A], "little")
        ln = pdu[1]
        anchor = (m.stimer_now() - int((m.ms() - t_start) * 1000 * TICKS_PER_US)) & M32
        ts = (anchor + 0x500) & M32
        buf = bytearray((ln + 13).to_bytes(4, "little")) + pdu + bytes(3) + ts.to_bytes(4, "little") \
            + bytes([0, 0, 0xC8]) + bytes([0])
        for i, v in enumerate(buf):
            m.write(a + i, 1, v)
        self.ts = ts
        if status:
            self.set_status(RX)
        return a

    def adv_rx_end(self, pdu, t_rx, who):
        self.set_status(RX)
        if who is not None and pdu[0] & 0xF == 5:            # CONNECT_IND
            self.conn_central = who
            who.on_connect_sent(pdu, self.m.ms())

    # ------------------------------------------------------------ connection events
    def central_tx(self, pdu, ch, aa):
        """The central starts sending pdu now."""
        m, t, b = self.m, self.m.ms(), self.brx
        self.tx_lost = False
        why = None
        if b is None:
            why = "peripheral not listening"
        elif b["ch"] != ch:
            why = f"peripheral on channel {b['ch']}, central on {ch}"
        elif b["aa"] != aa:
            why = f"access address {b['aa'].hex()} != {aa.hex()}"
        elif ((m.stimer_now() - b["start"]) & M32) >= 0x80000000:
            why = "BRX scheduled later"
        if why:
            self.misses.append((t, ch, why))
            self.log(f"[{t:9.3f}] radio: central packet on ch {ch} missed: {why}")
            return False
        if self.air is not None and self.air.lost("keyboard", ch, t):
            self.tx_lost = True
            self.log(f"[{t:9.3f}] radio: central packet on ch {ch} lost on air")
            return False
        self.leads.append(t - b["t"])
        self.rx_on.append(((m.stimer_now() - b["start"]) & M32) / TICKS_PER_US)
        b["on_air"] = (t, t + airtime_us(pdu) / 1000)
        self.at(t + airtime_us(pdu) / 1000, lambda: self.rx_done(b, pdu, t))
        return True

    def rx_done(self, b, pdu, t_start):
        if self.brx is not b:
            self.misses.append((self.m.ms(), b["ch"], "BRX stopped during the packet"))
            return
        hdr0 = pdu[0]
        rx_sn, rx_nesn, rx_md = (hdr0 >> 3) & 1, (hdr0 >> 2) & 1, (hdr0 >> 4) & 1
        if rx_nesn != b["last_sn"]:               # our packet in flight was acked
            if self.rptr != self.wptr:
                self.rptr = (self.rptr + 1) & 0xF
            b["last_sn"] ^= 1
        tx_addr = (0x840000 | self.fifo[self.rptr]) if self.rptr != self.wptr else None
        more = tx_addr is not None and ((self.rptr + 1) & 0xF) != self.wptr
        if rx_sn == b["nesn"]:
            b["nesn"] ^= 1
        tx_sn = b["last_sn"]
        self.sn_rb, self.nesn_rb = tx_sn, b["nesn"]
        b["rx"] += 1
        self.dma_rx(pdu, t_start)
        self.at(self.m.ms() + 0.150, lambda: self.tx_start(b, tx_addr, tx_sn, more))

    def tx_start(self, b, tx_addr, tx_sn, more):
        m, r = self.m, self.m.regs_mem
        if self.brx is not b:
            return
        if tx_addr is None:
            tx_addr = 0x840000 | int.from_bytes(r[0xC0C:0xC0E], "little")
        hdr0, ln = m.read(tx_addr + 4, 1), m.read(tx_addr + 5, 1)
        payload = bytes(m.read(tx_addr + 6 + i, 1) for i in range(ln))
        md = 1 if (more or hdr0 & 0x10) else 0
        pdu = bytes([(hdr0 & 0xE3) | (b["nesn"] << 2) | (tx_sn << 3) | (md << 4), ln]) + payload
        self.at(m.ms() + airtime_us(pdu) / 1000, lambda: self.tx_done(b, pdu, tx_addr))

    def tx_done(self, b, pdu, tx_addr):
        if self.brx is not b:
            return
        self.set_status(TX)
        c = self.link_central()
        if c and c.drop(pdu):
            self.at(self.m.ms() + 0.010, lambda: self.end_event(b))    # the central never heard it
            return
        if c and self.air is not None and self.air.lost("host", b["ch"], self.m.ms()):
            self.log(f"[{self.m.ms():9.3f}] radio: reply on ch {b['ch']} lost on air")
            if hasattr(c, "air_lost"):
                c.air_lost(pdu)
            self.at(self.m.ms() + 0.010, lambda: self.end_event(b))
            return
        nxt = c.on_air(pdu, b["ch"], tx_addr) if c else None
        if nxt is not None:
            self.at(self.m.ms() + 0.150, lambda: self.central_next(c, nxt, b))
        else:
            self.at(self.m.ms() + 0.010, lambda: self.end_event(b))

    def central_next(self, c, pdu, b):
        """The central's next packet in the event; one lost on air is told to it (air_lost_tx)."""
        if not self.central_tx(pdu, b["ch"], b["aa"]) and self.tx_lost and hasattr(c, "air_lost_tx"):
            c.air_lost_tx(pdu)

    def end_event(self, b):
        if self.brx is b:
            self.brx = None
            self.set_status(DONE)

    # ------------------------------------------------------------ TPLL (Telink's private format)
    def tpll_format(self):
        return self.m.regs_mem[0x404] & 3 == 2

    def tpll_mhz(self):
        r = self.m.regs_mem
        return ((r[0x1245] & 0x3F) << 6) | (r[0x1244] >> 2)

    def tpll_access_code(self):
        r = self.m.regs_mem
        return bytes(r[0x408:0x408 + (r[0x405] & 7)])

    def tpll_airtime_us(self, n):
        r = self.m.regs_mem
        mbps = 2 if r[0x1220] == 0x04 else 1
        return ((r[0x402] & 0x1F) + (r[0x405] & 7) + 1 + n + 2) * 8 / mbps

    def srx_timeout(self, s):
        if self.srx is s:
            self.srx = None
            self.set_status(FIRSTTO)
        return True

    def stx2rx_timeout(self, s):
        if self.srx is s:
            self.srx = None
            self.set_status(RXTO)
        return True

    def tpll_tx_start(self, stx2rx=False):
        m, r = self.m, self.m.regs_mem
        a = 0x840000 | int.from_bytes(r[0xC0C:0xC0E], "little")
        n = m.read(a + 4, 1) & 0x3F
        payload = bytes(m.read(a + 5 + i, 1) for i in range(n))
        mhz, ac = self.tpll_mhz(), self.tpll_access_code()
        self.tpll_tx.append((m.ms(), mhz, ac, payload))
        self.at(m.ms() + self.tpll_airtime_us(n) / 1000, lambda: self.tpll_tx_done(payload, mhz, ac, stx2rx))

    def tpll_tx_done(self, payload, mhz, ac, stx2rx=False):
        self.set_status(TX)
        if stx2rx:
            m, r = self.m, self.m.regs_mem
            s = dict(t=m.ms(), start=m.stimer_now(), mhz=mhz, ac=ac, rx=0)
            self.srx = s
            if r[0xF03] & 4:
                us = int.from_bytes(r[0xF0A:0xF0C], "little") + 1
                self.at(m.ms() + us / 1000, lambda s=s: s["rx"] == 0 and self.stx2rx_timeout(s))
        reply = self.tpll.on_tpll(payload, mhz, ac) if self.tpll is not None else None
        if reply is not None:
            self.at(self.m.ms() + TURNAROUND_MS, lambda: self.tpll_rx_start(bytes(reply), mhz, ac))

    def tpll_rx_start(self, reply, mhz, ac):
        m, s, t = self.m, self.srx, self.m.ms()
        why = None
        if s is None:
            why = "not listening"
        elif s["mhz"] != mhz:
            why = f"listening on {s['mhz']} MHz"
        elif s["ac"] != ac:
            why = f"access code {s['ac'].hex()} != {ac.hex()}"
        elif ((m.stimer_now() - s["start"]) & M32) >= 0x80000000:
            why = "SRX scheduled later"
        if why:
            self.misses.append((t, mhz, why))
            self.log(f"[{t:9.3f}] radio: reply on {mhz} MHz missed: {why}")
            return
        s["rx"] += 1
        self.at(t + self.tpll_airtime_us(len(reply)) / 1000, lambda: self.tpll_rx_done(s, reply, t))

    def tpll_rx_done(self, s, reply, t_start):
        m, r = self.m, self.m.regs_mem
        if self.srx is not s:
            self.misses.append((m.ms(), s["mhz"], "SRX stopped during the packet"))
            return
        a = 0x840000 | int.from_bytes(r[0xC08:0xC0A], "little")
        n = len(reply)
        anchor = (m.stimer_now() - int((m.ms() - t_start) * 1000 * TICKS_PER_US)) & M32
        buf = (n + 11).to_bytes(4, "little") + bytes([n]) + reply + bytes(2) + anchor.to_bytes(4, "little") \
            + bytes([0, 0, 0xC8, 0])
        for i, v in enumerate(buf):
            m.write(a + i, 1, v)
        self.srx = None
        self.set_status(RX)

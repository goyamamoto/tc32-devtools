#!/usr/bin/env python3
"""Known answers for the BLE models in emulator/, without any firmware image.

- AES-128 (aes128.py): FIPS-197 Appendix C.1.
- CCM: RFC 3610 Packet Vector #1 (M = 8, L = 2, 8 octets of AAD), through
  the same CCM core the link layer's 4-octet-MIC CCM uses.
- SMP: the c1 and s1 sample data, Bluetooth Core Vol 3 Part H 2.2.3, 2.2.4.
- LL encryption: the session key and the first encrypted packet of the
  sample data in Bluetooth Core Vol 6 Part C 1.
- Radio model (ble_radio.py), as its docstring describes it: the RF status is
  write-one-to-clear; the TX FIFO pointers clear, set, step and append; a
  BRX scheduled at 0xf18 misses a packet that starts before its tick and
  takes one after it; the RX DMA entry is {len + 13, header, payload, CRC,
  time stamp = packet start + 0x500 ticks, ...}; an acknowledgement moves the
  FIFO on, and the reply carries the NESN, SN and MD the model sets; 0xf22
  and 0xf23 read them back.
  A reset of the machine clears the radio's state and its own pending steps,
  and keeps the central's. A clear of the TX FIFO leaves both pointers at the
  write pointer (TC32EMU_TXFIFO_CLEAR "position", the default) or at 0
  ("zero"). 0x448 bit 5 during a central packet in an open BRX and after it
  reads 0 and 0 (TC32EMU_RX_BUSY "off", the default), 1 and 0 ("air"), 1
  and 1 ("stuck"). An advertising packet sent while the TX FIFO
  holds one or two packets is recorded in adv_fifo and missed; with the FIFO
  emptied, or in the TPLL format, it is not.
- Central (ble_central.py): channel selection algorithm #1 with all channels
  and with a map of five, against the sequence worked out by hand from Core
  Vol 6 Part B 4.5.8.2; the CONNECT_IND's RxAdd follows the advertiser's
  TxAdd. With TC32EMU_CENTRAL_SKIP_EVERY=7 it sends nothing at events 21,
  28 and 35 of 14-39; with 0, the default, at none.
- The peripheral's LL_TERMINATE_IND: the central acknowledges it in the
  next event (NESN advanced) and leaves; with ack_terminate=False or gone
  silent it leaves without a packet.
- Several centrals: of two, the one that connects gets the CONNECT_IND out
  and the link, and losing it the link goes back to Radio.central; with
  none connecting, the first active scanner's SCAN_REQ goes out (TxAdd,
  RxAdd, ScanA, AdvA).
- Privacy: ah() with Core Vol 3 Part H D.7's sample (in aes128.py) and the
  RPA built from it; a public central connects from its identity; "rpa"
  centrals connect (TxAdd 1) from different RPAs that resolve with their
  IRK, "rpa-wrong-irk" from one that does not; dist_id puts IdKey in the
  Pairing Request and sends the IRK and identity address after the
  responder's keys only when the response keeps IdKey.
- aes128.ccm_decrypt refuses data too short for the MIC.

Usage: ble_models_check.py

SPDX-License-Identifier: Apache-2.0
"""
import os
import struct
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path[:0] = [os.path.join(ROOT, "common"), os.path.join(ROOT, "emulator")]
import aes128  # noqa: E402
import ble_central  # noqa: E402
import ble_sc  # noqa: E402
import p256  # noqa: E402
import ble_radio  # noqa: E402
import tc32emu as te  # noqa: E402

failures = []


def check(cond, text):
    print(("ok   " if cond else "FAIL ") + text)
    if not cond:
        failures.append(text)


def crypto():
    k, p = bytes(range(16)), bytes.fromhex("00112233445566778899aabbccddeeff")
    c = aes128.encrypt(k, p)
    check(c.hex() == "69c4e0d86a7b0430d8cdb78070b4c55a" and aes128.decrypt(k, c) == p,
          f"AES-128, FIPS-197 C.1: {c.hex()}")

    key = bytes.fromhex("c0c1c2c3c4c5c6c7c8c9cacbcccdcecf")
    nonce = bytes.fromhex("00000003020100a0a1a2a3a4a5")
    pkt = bytes(range(31))
    out = aes128.ccm(key, nonce, pkt[:8], pkt[8:], 8)
    check(out.hex() == "588c979a61c663d2f066d0c2c0f989806d5f6b61dac38417e8d12cfdf926e0",
          f"CCM, RFC 3610 Packet Vector #1: {out.hex()}")

    c1 = aes128.c1(0, 0x5783D52156AD6F0E6388274EC6702EE0, bytes.fromhex("01010000100707"),
                   bytes.fromhex("02030000080005"), 1, bytes.fromhex("a6a5a4a3a2a1"), 0,
                   bytes.fromhex("b6b5b4b3b2b1"))
    check(c1 == 0x1E1E3FEF878988EAD2A74DC5BEF13B86, f"SMP c1, Core Vol 3 Part H 2.2.3: {c1:032x}")
    s1 = aes128.s1(0, 0x000F0E0D0C0B0A091122334455667788, 0x010203040506070899AABBCCDDEEFF00)
    check(s1 == 0x9A1FE1F0E8B0F49B5B4216AE796DA062, f"SMP s1, Core Vol 3 Part H 2.2.4: {s1:032x}")

    sk = aes128.e(0x4C68384139F574D836BCF34E9DFB01BF, 0x0213243546576879ACBDCEDFE0F10213)
    nonce = (0).to_bytes(4, "little") + bytes([0x80]) + bytes.fromhex("24abdcbabebaafde")
    enc = aes128.ccm_encrypt(sk.to_bytes(16, "big"), nonce, 0x0F, bytes([0x06]))
    dec = aes128.ccm_decrypt(sk.to_bytes(16, "big"), nonce, 0x0F, enc)
    check(sk == 0x99AD1B5226A37E3E058E3B8E27C2C666 and enc.hex() == "9fcda7f448" and dec == (bytes([6]), True),
          f"LL encryption, Core Vol 6 Part C 1: SK {sk:032x}, packet 0F 05 {enc.hex()}")
    # 3 octets ending in the empty payload's last MIC octet: without the length
    # check the tail of the data would pass for the MIC.
    tail = aes128.ccm_encrypt(sk.to_bytes(16, "big"), nonce, 0x0F, b"")[-1:]
    short = aes128.ccm_decrypt(sk.to_bytes(16, "big"), nonce, 0x0F, bytes(2) + tail)
    check(short == (b"", False), f"CCM decryption of 3 octets (no room for the MIC): {short}")


def secure_connections():
    """ble_sc.py and p256.py against Core Vol 3 Part H Appendix D (the values as the SMP PDUs carry them)
    and the pairing Apache NimBLE's host tests replay (ble_sm_sc_peer_jw_iio3_rio3_b1_iat0_rat0_ik5_rk7)."""
    h = bytes.fromhex
    k = h("2b7e151628aed2a6abf7158809cf4f3c")
    m = h("6bc1bee22e409f96e93d7e117393172aae2d8a571e03ac9c9eb76fac45af8e51"
          "30c81c46a35ce411e5fbc1191a0a52eff69f2445df4f9b17ad2b417be66c3710")
    for n, want in ((0, "bb1d6929e95937287fa37d129b756746"), (16, "070a16b46b4d4144f79bdd9dd04a287c"),
                    (40, "dfa66747de9ae63030ca32611497c827"), (64, "51f0bebf7e3b9d92fc49741779363cfe")):
        check(ble_sc.aes_cmac(k, m[:n]).hex() == want, f"AES-CMAC of {n} octets (RFC 4493)")
    u = h("e69d350e480103ccdbfdf4ac1191f4efb9a5f9e9a7832c5e2cbe97f2d203b020")
    v = h("fdc57ff449dd4f6bfb7c9df1c29acb592ae7d4eefbfc0a909abbf6323d8b1855")
    x = h("abae2b71ecb2ffff3e7377d15484cbd5")
    y = h("cfc43dfff78365216e5fa725cce7e8a6")
    check(ble_sc.f4(u, v, x, 0).hex() == "2d8774a9bea1edf11cbda907f116c9f2", "f4 (Appendix D.2)")
    w = h("98a6bf73f3348d86f166f8b4136b79999b7d390aa610103405adc857a33402ec")
    a1, a2 = ble_sc.addr7(0, h("cebf37371256")), ble_sc.addr7(0, h("c1cf2d7013a7"))
    mackey, ltk = ble_sc.f5(w, x, y, a1, a2)
    check(mackey.hex() == "206e63ce206a3ffd024a08a176f16529" and ltk.hex() == "380a7594b522059823cdd76911798669",
          "f5 MacKey and LTK (Appendix D.3)")
    r = h("c80f2d0cd242da0854bb53b43b34a312")
    check(ble_sc.f6(mackey, x, y, r, bytes([0x01, 0x01, 0x02]), a1, a2).hex() == "618f95da090b6cd2c5e8d09c9873c4e3",
          "f6 (Appendix D.4)")
    check(ble_sc.g2(u, v, x, y) == 0x2f9ed5ba % 1000000, "g2 (Appendix D.5)")
    da = p256.scalar_from_le(h("bd1a3ccda6b8995899b740eb7b60ff4a503f10d2e3b3c974385fc5a3d4f6493f"))
    check(p256.point_to_le(p256.public_key(da)).hex() == u.hex() +
          "8bd28915d08e1c742430ed8fc24563765c15525abf9a32636deb2a65499c80dc",
          "P-256: the debug private key gives the debug public key (Core 2.3.5.6.1)")
    db = p256.scalar_from_le(h("548d20b8970bbc439aad106f6074d46a55c17a178b60e0b45ae658f1ea12d9fb"))
    pka = p256.point_from_le(h("bcf2d8a5dba3956c99f9110d4d2ef0bdee9b69b6cd8874be40e8e5ccdc884453"
                               "bfa9820e187a14f877fd8e922af85d39d16d921f387499dc6c2c9423f97256ab"))
    pkb = p256.point_to_le(p256.public_key(db))
    check(pkb.hex() == "728cd188d7be49b2c55c95b364e01232b6c9476337385b9c1e1b1a0609e23185"
          "193a296962d630e7e84863dc00730a707d2e29cc917771b175b8f7dcb0e29110",
          "P-256: the recorded responder's private key gives the public key it sent")
    check(p256.on_curve(pka) and not p256.on_curve((pka[0], pka[1] ^ 1)), "P-256: on the curve, and off it")
    dh = p256.dhkey(db, pka)
    na, nb = h("a4345fb3af734364cd191b5b87583166"), h("c091fbb377a2020bc6cd6c0451454539")
    ia, ra = ble_sc.addr7(0, h("ca61a06794e0")), ble_sc.addr7(0, h("33221100450a"))
    check(ble_sc.f4(pkb[:32], pka[0].to_bytes(32, "little"), nb, 0).hex() == "82edd062913d967f13c50d022b5e4316",
          "the recorded responder's confirm")
    mackey, ltk = ble_sc.f5(dh, na, nb, ia, ra)
    check(ltk.hex() == "63598a14094b946effae5e538602a36c", "the recorded LTK from the DHKey")
    io = bytes([0x09, 0x00, 0x03])
    check(ble_sc.f6(mackey, na, nb, bytes(16), io, ia, ra).hex() == "82651d02ed891344041a147c329a1e7d" and
          ble_sc.f6(mackey, nb, na, bytes(16), io, ra, ia).hex() == "063c284ae5484b51654e145e2fddfa22",
          "the recorded DHKey checks of both sides")


class Recorder:
    """A central that sends nothing by itself and keeps the replies."""

    def __init__(self):
        self.replies = []

    def drop(self, pdu):
        return False

    def on_air(self, pdu, ch, tx_addr):
        self.replies.append((pdu, ch, tx_addr))
        return None


def machine():
    """A machine that is never run: only its registers, memory and clock."""
    fl = te.Flash()
    fl.mem[8] = 0x4B                          # the boot ROM's bootable-slot mark
    return te.Machine(fl, max_log=0)


def run_until(m, ms):
    while m.ms() < ms:
        m.cycles += m.cpu_hz // 100_000          # 10 us
        m.update_time()


def radio():
    m = machine()
    r = ble_radio.Radio(m)
    m.reg_write(0x74A, 1, te.STIMER_CTRL_RESET | 0x02)       # the system timer on

    r.set_status(0x21)
    m.reg_write(0xF20, 2, 0x0001)
    check(m.reg_read(0xF20, 2) == 0x0020, f"RF status write-one-to-clear: 0x21, clear bit 0 -> 0x{m.reg_read(0xF20, 2):04x}")

    empty, data, rxbuf = 0x840100, 0x840140, 0x840200
    for i, v in enumerate([2, 0, 0, 0, 0x01, 0]):              # an empty PDU
        m.write(empty + i, 1, v)
    for i, v in enumerate([5, 0, 0, 0, 0x02, 3] + list(b"abc")):  # LLID 2, 3 octets
        m.write(data + i, 1, v)
    m.reg_write(0xC2A, 1, 0x10)
    m.reg_write(0xC2C, 2, empty & 0xFFFF)
    m.reg_write(0xC2C, 2, data & 0xFFFF)
    fifo = (m.reg_read(0xC2A, 1), m.reg_read(0xC2B, 1))
    m.reg_write(0xC2A, 1, 0x40 | 1)
    set1 = m.reg_read(0xC2A, 1)
    m.reg_write(0xC2A, 1, 0x20)
    step = m.reg_read(0xC2A, 1)
    m.reg_write(0xC2A, 1, 0x40 | 0)
    check(fifo == (0, 2) and set1 == 1 and step == 2 and m.reg_read(0xC2A, 1) == 0,
          f"TX FIFO: after clear and two appends read/write pointers {fifo}, set 1 -> {set1}, step -> {step}")

    aa = bytes.fromhex("71764129")                               # 0x408..0x40b as written
    for i, v in enumerate(aa):
        m.reg_write(0x408 + i, 1, v)
    m.reg_write(0x40D, 1, 7)
    m.reg_write(0xF03, 1, 0x10)                                  # last SN 1, NESN 0
    m.reg_write(0xC08, 2, rxbuf & 0xFFFF)
    m.reg_write(0xC0C, 2, empty & 0xFFFF)
    start = (m.stimer_now() + 1600) & 0xFFFFFFFF                # 100 us ahead
    m.reg_write(0xF18, 4, start)
    m.reg_write(0xF16, 1, m.reg_read(0xF16, 1) | 4)
    m.reg_write(0xF00, 1, 0x82)
    rec = Recorder()
    r.central = rec
    pdu = bytes([0x01, 0])                                       # empty PDU, NESN 0, SN 0
    early = r.central_tx(pdu, 7, aa)
    t0 = m.ms() + 0.2
    run_until(m, t0)
    anchor = m.stimer_now()
    ok = r.central_tx(pdu, 7, aa)
    run_until(m, t0 + 1.0)
    check(not early and r.misses and "scheduled later" in r.misses[0][2] and ok,
          f"BRX at 0xf18: a packet before the start tick is missed ({r.misses[:1]}), one after it is taken")
    ent = bytes(m.read(rxbuf + i, 1) for i in range(20))
    ts = int.from_bytes(ent[9:13], "little")
    check(int.from_bytes(ent[:4], "little") == 13 and ent[4:6] == pdu and (ts - anchor) & 0xFFFFFFFF == 0x500,
          f"RX DMA entry: length {int.from_bytes(ent[:4], 'little')}, header {ent[4:6].hex()},"
          f" time stamp {(ts - anchor) & 0xFFFFFFFF:#x} ticks after the packet start")
    reply = rec.replies[0][0] if rec.replies else b""
    check(reply == bytes([0x02 | 1 << 2 | 0 << 3, 3]) + b"abc" and m.reg_read(0xC2A, 1) == 1
          and m.reg_read(0xF22, 1) & 1 == 0 and m.reg_read(0xF23, 1) == 0x10,
          f"the acknowledgement steps over the first FIFO entry (read pointer {m.reg_read(0xC2A, 1)}); reply"
          f" {reply.hex()} (LLID 2, NESN 1, SN 0, MD 0); 0xf22 bit 0 = {m.reg_read(0xF22, 1) & 1},"
          f" 0xf23 = 0x{m.reg_read(0xF23, 1):02x}")
    before = list(r.late_starts)
    m.reg_write(0xF18, 4, (m.stimer_now() - 1600) & 0xFFFFFFFF)  # 100 us ago
    m.reg_write(0xF00, 1, 0x82)
    check(before == [] and len(r.late_starts) == 1 and 99 <= r.late_starts[0][1] <= 101 and r.brx is not None,
          f"a BRX command written after its start tick is recorded and starts at once (late_starts {before} ->"
          f" {[round(us) for _, us in r.late_starts]} us)")


def reset():
    m = machine()
    r = ble_radio.Radio(m)
    ran = []
    r.at(5.0, lambda: ran.append("radio"))
    r.at(5.0, lambda: ran.append("central"), owner="central")
    m.reg_write(0xC2A, 1, 0x10)
    m.reg_write(0xC2C, 2, 0x0100)
    m.reg_write(0xF00, 1, 0x82)
    before = (m.reg_read(0xC2B, 1), r.brx is not None, len(r.sched))
    m.reset(0)
    after = (m.reg_read(0xC2B, 1), r.brx is not None, len(r.sched))
    m.reg_write(0x74A, 1, te.STIMER_CTRL_RESET | 0x02)
    run_until(m, 6.0)
    check(before == (1, True, 2) and after == (0, False, 1) and ran == ["central"],
          f"machine reset: FIFO write pointer, BRX, pending steps {before} -> {after}; ran {ran}")


def adv_fifo():
    """An advertising packet sent while the TX FIFO holds packets is recorded and missed; with the FIFO
    emptied (0xc2a = 0x10), or in the TPLL format, it is not."""
    m = machine()
    r = ble_radio.Radio(m)
    m.reg_write(0x74A, 1, te.STIMER_CTRL_RESET | 0x02)
    adv = 0x840300
    for i, v in enumerate([8, 0, 0, 0, 0x00, 6] + [0xC0, 1, 2, 3, 4, 0xC5]):   # ADV_IND, AdvA only
        m.write(adv + i, 1, v)
    m.reg_write(0xC0C, 2, adv & 0xFFFF)
    m.reg_write(0x40D, 1, 37)
    for name, fifo, fmt in (("FIFO emptied", 0, 1), ("one packet queued", 1, 1), ("two packets queued", 2, 1),
                            ("one packet queued, TPLL format", 1, 2), ("emptied again", 0, 1)):
        m.reg_write(0x404, 1, fmt)
        m.reg_write(0xC2A, 1, 0x10)
        for _ in range(fifo):
            m.reg_write(0xC2C, 2, 0x0100)
        n_tx, n_rec, n_miss = len(r.adv_tx), len(r.adv_fifo), len(r.misses)
        m.reg_write(0xF00, 1, 0x85)
        run_until(m, m.ms() + 0.1)
        sent = len(r.adv_tx) - n_tx
        rec = r.adv_fifo[n_rec:]
        miss = [w for _t, _c, w in r.misses[n_miss:]]
        want = 1 if fifo and fmt == 1 else 0
        check(sent == 1 and len(rec) == want and len(miss) == want
              and (not want or (rec[0][1] == 37 and (rec[0][3] - rec[0][2]) & 0xF == fifo
                                and f"{fifo} packet(s) in the TX FIFO" in miss[0])),
              f"advertising, {name}: sent {sent}, recorded {rec}, missed {miss}")


def fifo_clear():
    """TC32EMU_TXFIFO_CLEAR: after three appends and two steps, a clear (0xc2a = 0x10) leaves both pointers
    at 3 ("position", the default) or at 0 ("zero"); the FIFO is empty either way."""
    given = ble_radio.TXFIFO_CLEAR
    if "TC32EMU_TXFIFO_CLEAR" not in os.environ:
        check(given == "position", f"TX FIFO clear: the default is {given}")
    for mode, want in (("position", (3, 3)), ("zero", (0, 0))):
        ble_radio.TXFIFO_CLEAR = mode
        m = machine()
        ble_radio.Radio(m)
        m.reg_write(0xC2A, 1, 0x10)
        for _ in range(3):
            m.reg_write(0xC2C, 2, 0x0100)
        m.reg_write(0xC2A, 1, 0x20)
        m.reg_write(0xC2A, 1, 0x20)
        m.reg_write(0xC2A, 1, 0x10)
        got = (m.reg_read(0xC2A, 1), m.reg_read(0xC2B, 1))
        check(got == want, f"TX FIFO clear, {mode}: read/write pointers {got} after the clear")
    ble_radio.TXFIFO_CLEAR = given


class TpllPeer:
    """A dongle stand-in: records what it hears and answers a fixed reply once."""

    def __init__(self, reply):
        self.heard, self.reply = [], reply

    def on_tpll(self, payload, mhz, ac):
        self.heard.append((payload, mhz, ac))
        return self.reply


def tpll():
    m = machine()
    r = ble_radio.Radio(m)
    m.reg_write(0x74A, 1, te.STIMER_CTRL_RESET | 0x02)
    rg = m.regs_mem
    rg[0x404] = 0x02                    # private format
    rg[0x405] = 3                       # access code of 3 bytes
    rg[0x408:0x40B] = bytes.fromhex("aabbcc")
    rg[0x402] = 0x01                    # preamble 1 byte
    rg[0x1220] = 0x04                   # 2 Mbps
    rg[0x1244], rg[0x1245] = (2450 & 0x3F) << 2, 2450 >> 6
    txbuf, rxbuf = 0x840300, 0x840400
    for i, v in enumerate([9, 0, 0, 0, 0x04] + list(b"kbd!")):     # dma_len, header L = 4, payload
        m.write(txbuf + i, 1, v)
    m.reg_write(0xC0C, 2, txbuf & 0xFFFF)
    m.reg_write(0xC08, 2, rxbuf & 0xFFFF)
    peer = TpllPeer(b"\x11\x22")
    r.tpll = peer
    check(r.tpll_mhz() == 2450 and r.tpll_access_code() == bytes.fromhex("aabbcc") and r.tpll_airtime_us(4) == 44,
          f"TPLL registers: {r.tpll_mhz()} MHz, access code {r.tpll_access_code().hex()}, air time of 4 bytes {r.tpll_airtime_us(4)} us")
    t0 = m.ms()
    m.reg_write(0xF00, 1, 0x85)                                     # STX with no SRX open: the reply is missed
    run_until(m, t0 + 0.5)
    check(peer.heard == [(b"kbd!", 2450, bytes.fromhex("aabbcc"))] and rg[0xF20] & 0x02 and r.tpll_tx and
          r.misses and r.misses[-1][2] == "not listening",
          f"STX: the peer hears {peer.heard[0][0] if peer.heard else None} on {peer.heard[0][1] if peer.heard else None} MHz, TX status set, the reply with no window open is missed ({r.misses[-1][2] if r.misses else None})")
    m.reg_write(0xF20, 1, 0xFF)
    m.reg_write(0xF00, 1, 0x86)                                     # SRX open, then STX: the reply lands
    m.reg_write(0xF00, 1, 0x85)
    t1 = m.ms()
    run_until(m, t1 + 0.5)
    ent = bytes(m.read(rxbuf + i, 1) for i in range(17))
    check(int.from_bytes(ent[:4], "little") == 13 and ent[4] == 2 and ent[5:7] == b"\x11\x22" and ent[13:17] == bytes([0, 0, 0xC8, 0])
          and rg[0xF20] & 0x01 and r.srx is None,
          f"SRX then STX: RX DMA entry {ent.hex()} (len 13, header 2, the reply, RSSI 0xc8), RX status set, the window closed")
    m.reg_write(0xF20, 1, 0xFF)
    rg[0xF03] = 0x02                                                # the first timeout, 100 us after SRX
    m.reg_write(0xF28, 4, 100)
    m.reg_write(0xF00, 1, 0x86)
    t2 = m.ms()
    run_until(m, t2 + 0.05)
    open_at_50 = r.srx is not None
    run_until(m, t2 + 0.2)
    check(open_at_50 and r.srx is None and (rg[0xF20] | rg[0xF21] << 8) & 0x400,
          "SRX first timeout (0xf03 bit 1, 0xf28 = 100 us): open at 50 us, closed with status 0x400 by 200 us")
    r.tpll = None
    m.reg_write(0xF00, 1, 0x80)
    check(r.brx is None and r.srx is None, "0x80 closes the windows")


def central():
    m = machine()
    r = ble_radio.Radio(m)
    seqs = {}
    for name, chm in (("all channels", b"\xff\xff\xff\xff\x1f"), ("channels 0, 2, 4, 6, 36", bytes.fromhex("5500000010"))):
        c = ble_central.Central(m, r, hop=7, chm=chm, log=lambda s: None)
        seqs[name] = [c.channel() for _ in range(8)]
    want = {"all channels": [7, 14, 21, 28, 35, 5, 12, 19], "channels 0, 2, 4, 6, 36": [4, 36, 2, 6, 0, 0, 4, 36]}
    for name in want:
        check(seqs[name] == want[name], f"channel selection #1, hop 7, {name}: {seqs[name]}")
    rxadd = []
    for txadd in (0, 1):
        c = ble_central.Central(m, r, connect_after_ms=0, log=lambda s: None)
        adv = bytes([0x00 | txadd << 6, 6]) + bytes.fromhex("c1c2c3c4c5c6")
        rxadd.append(c.on_adv(adv, 37)[0] >> 7)
    check(rxadd == [0, 1], f"CONNECT_IND RxAdd for a public and a random advertiser: {rxadd}")
    # The peripheral's L2CAP Connection Parameter Update Request (7.5 ms, latency 44, 3 s): accepted,
    # applied at event + 12, or refused.
    req = bytes([0x12, 0x07]) + struct.pack("<HHHHH", 8, 6, 6, 44, 300)   # code, ident, length, then the values
    for name, kw, want_result, want_action in (("default", {}, 0, "accepted"),
                                               ("apply_update", {"apply_update": True}, 0, "applied"),
                                               ("refuse_update", {"refuse_update": True}, 1, "refused"),
                                               ("both", {"apply_update": True, "refuse_update": True}, 1, "refused")):
        seen = []
        c = ble_central.Central(m, r, log=lambda s: None, on_param_request=lambda q, a: seen.append((q, a)), **kw)
        c.event = 40
        c.l2cap(5, req)
        rsp = [chunk for _, chunk in c.txq if chunk[2:4] == b"\x05\x00" and chunk[4] == 0x13]
        result = struct.unpack_from("<H", rsp[0], 8)[0] if rsp else None
        q, a = seen[0] if seen else ({}, None)
        ok = (result == want_result and a == want_action and q.get("interval_min") == 6 and q.get("interval_max") == 6
              and q.get("latency") == 44 and q.get("timeout") == 300 and q.get("event") == 40)
        ok = ok and (q.get("instant") == 52 if want_action == "applied" else q.get("instant") is None)
        ok = ok and ((c.update is not None) == (want_action == "applied"))
        check(ok, f"parameter update request, {name}: result {result}, action {a}, instant {q.get('instant')}")


def rx_busy():
    """TC32EMU_RX_BUSY: 0x448 bit 5 in the middle of a central packet in an open BRX and after its end:
    0 and 0 ("off"), 1 and 0 ("air"), 1 and 1 ("stuck")."""
    given = ble_radio.RX_BUSY
    if "TC32EMU_RX_BUSY" not in os.environ:
        check(given == "off", f"0x448 bit 5: the default is {given}")
    pdu = bytes([0x01, 0])                                       # an empty PDU, 80 us on air
    aa = bytes.fromhex("71764129")
    for mode, want in (("off", (0, 0)), ("air", (1, 0)), ("stuck", (1, 1))):
        ble_radio.RX_BUSY = mode
        m = machine()
        r = ble_radio.Radio(m)
        r.central = Recorder()
        m.reg_write(0x74A, 1, te.STIMER_CTRL_RESET | 0x02)
        for i, v in enumerate(aa):
            m.reg_write(0x408 + i, 1, v)
        m.reg_write(0x40D, 1, 7)
        m.reg_write(0xF16, 1, 0)                                 # start now
        m.reg_write(0xF00, 1, 0x82)
        run_until(m, m.ms() + 0.05)
        ok = r.central_tx(pdu, 7, aa)
        t0 = m.ms()
        run_until(m, t0 + 0.04)
        mid = (m.reg_read(0x448, 1) >> 5) & 1
        run_until(m, t0 + 0.1)
        after = (m.reg_read(0x448, 1) >> 5) & 1
        check(ok and (mid, after) == want, f"0x448 bit 5, {mode}: {mid} during the central's packet, {after} after it")
    ble_radio.RX_BUSY = given


def central_skip():
    """TC32EMU_CENTRAL_SKIP_EVERY=7: from event 20 on, the central sends nothing at events 21, 28, 35;
    0 sends at every event."""
    given = ble_central.SKIP_EVERY
    if "TC32EMU_CENTRAL_SKIP_EVERY" not in os.environ:
        check(given == 0, f"central skip: the default is {given}")
    for n, want in ((0, []), (7, [21, 28, 35])):
        ble_central.SKIP_EVERY = n
        m = machine()
        r = ble_radio.Radio(m)
        c = ble_central.Central(m, r, log=lambda s: None)
        c.state, c.base_t, c.base_k, c.interval_ms = "connected", 0.0, 0, 30.0
        sent = []
        r.central_tx = lambda pdu, ch, aa: sent.append(c.event) or True
        for k in range(14, 40):
            c.conn_event(k)
        skipped = [k for k in range(14, 40) if k not in sent]
        check(skipped == want, f"central skip {n}: events without a packet {skipped}")
    ble_central.SKIP_EVERY = given


def terminate_ack():
    """The peripheral's LL_TERMINATE_IND (Core Vol 6 Part B 5.1.6): the central acknowledges it with an
    empty PDU (NESN advanced) in the next event and leaves; with ack_terminate=False, or gone silent,
    it leaves without sending."""
    for name, kw, silent, want in (("default", {}, False, 1), ("ack_terminate=False", {"ack_terminate": False}, False, 0),
                                   ("gone silent", {}, True, 0)):
        m = machine()
        r = ble_radio.Radio(m)
        c = ble_central.Central(m, r, log=lambda s: None, **kw)
        c.state, c.base_t, c.base_k, c.interval_ms = "connected", 0.0, 0, 30.0
        sent = []
        r.central_tx = lambda pdu, ch, aa: sent.append((c.event, pdu)) or True
        c.conn_event(1)
        c.nesn ^= 1                                    # the IND was new: its SN taken
        c.ll_ctrl(bytes([0x02, 0x13]))
        if silent:
            c.go_silent(2)
        state_after_ind = c.state
        for k in range(2, 6):
            c.conn_event(k)
        after = [(k, p.hex()) for k, p in sent if k >= 2]
        want_after = [(2, bytes([0x01 | c.nesn << 2 | c.sn << 3, 0]).hex())] if want else []
        check(after == want_after and c.state == "terminated"
              and state_after_ind == ("terminate-ack" if kw == {} else "terminated"),
              f"LL_TERMINATE_IND, {name}: state {state_after_ind} then {c.state}; sent after it {after}")


def advertise(m, r, adva=bytes.fromhex("c1c2c3c4c5c6"), random_adva=True):
    """One ADV_IND by STX2RX on channel 37 and the time for an answer to arrive."""
    buf = 0x840300
    for i, v in enumerate([8, 0, 0, 0, 0x00 | (0x40 if random_adva else 0), 6] + list(adva)):
        m.write(buf + i, 1, v)
    m.reg_write(0xC0C, 2, buf & 0xFFFF)
    for i, v in enumerate(ble_radio.ADV_AA):
        m.reg_write(0x408 + i, 1, v)
    m.reg_write(0x40D, 1, 37)
    m.reg_write(0xF00, 1, 0x87)
    run_until(m, m.ms() + 1.0)


def centrals():
    """Two centrals on one radio: the first CONNECT_IND wins and its central gets the link; with none
    connecting, the first active scanner sends a SCAN_REQ; a central that only scans does not connect."""
    log = lambda s: None  # noqa: E731
    m = machine()
    r = ble_radio.Radio(m)
    m.reg_write(0x74A, 1, te.STIMER_CTRL_RESET | 0x02)
    a = ble_central.Central(m, r, connect_after_ms=1e9, active_scan=True, log=log)        # radio.central
    b = ble_central.Central(m, r, connect_after_ms=0, join=True, aa=0x5A3C9617, log=log)  # radio.centrals
    advertise(m, r)
    req = [x for x in r.scan_reqs]
    check(r.central is a and r.centrals == [b] and a.state == "scan" and b.state == "connected"
          and r.conn_central is b and r.link_central() is b and not req,
          f"two centrals, the second connects: states {a.state}, {b.state}; link to "
          f"{'b' if r.link_central() is b else 'a'}; SCAN_REQs {len(req)}")
    r.centrals.remove(b)
    check(r.link_central() is a, "the connecting central detached: the link goes to Radio.central")

    m = machine()
    r = ble_radio.Radio(m)
    m.reg_write(0x74A, 1, te.STIMER_CTRL_RESET | 0x02)
    a = ble_central.Central(m, r, connect_after_ms=1e9, log=log)
    b = ble_central.Central(m, r, connect_after_ms=1e9, active_scan=True, join=True, own_addr="rpa", log=log)
    c = ble_central.Central(m, r, connect_after_ms=1e9, active_scan=True, join=True, log=log)
    advertise(m, r)
    sr = r.scan_reqs[0][3] if r.scan_reqs else b""
    check(len(r.scan_reqs) == 1 and r.scan_reqs[0][2] is b and sr[0] == 0xC3 and sr[1] == 12
          and sr[2:8] == b.inita and sr[8:14] == bytes.fromhex("c1c2c3c4c5c6")
          and (a.state, b.state, c.state) == ("scan", "scan", "scan") and r.conn_central is None,
          f"no central connects: one SCAN_REQ, from the first active scanner, {sr.hex()} "
          f"(TxAdd 1 for its RPA, RxAdd 1 for the random AdvA, ScanA, AdvA); states {a.state} {b.state} {c.state}")


def privacy():
    """Resolvable private addresses (Core Vol 3 Part H 2.2.2) and the central's IdKey distribution."""
    log = lambda s: None  # noqa: E731
    irk = bytes.fromhex("9b7d390aa6101034 05adc857a33402ec".replace(" ", ""))   # D.7's IRK, LSO first
    a = aes128.rpa(irk, 0x708194)
    check(a.hex() == "aafb0d948170", f"rpa(D.7 IRK, prand 0x708194) = {a.hex()} (hash 0x0dfbaa, prand 0x708194)")
    m = machine()
    r = ble_radio.Radio(m)
    pub = ble_central.Central(m, r, log=log)
    check(pub.inita == pub.identity and pub.inita_type == 0, f"own_addr public: InitA {pub.inita.hex()}, type {pub.inita_type}")
    got = []
    for own in ("rpa", "rpa", "rpa-wrong-irk"):
        c = ble_central.Central(m, r, own_addr=own, connect_after_ms=0, log=log)
        a = c.inita
        prand = int.from_bytes(a[3:6], "little")
        resolves = aes128.ah(int.from_bytes(c.irk, "little"), prand) == int.from_bytes(a[:3], "little")
        got.append((own, a.hex(), prand >> 22, resolves))
        adv = bytes([0x40, 6]) + bytes.fromhex("c1c2c3c4c5c6")
        ci = c.on_adv(adv, 37)
        check(ci[0] == 0xC5 and ci[2:8] == a, f"own_addr {own}: CONNECT_IND header 0x{ci[0]:02x} (TxAdd 1), InitA {ci[2:8].hex()}")
    check(got[0][2] == got[1][2] == got[2][2] == 1 and got[0][1] != got[1][1] and got[0][3] and got[1][3]
          and not got[2][3], f"RPAs (own_addr, InitA, prand bits 23:22, resolves with the IRK): {got}")
    for dist, pres_init, want in ((False, 0x00, None), (True, 0x02, True), (True, 0x00, None)):
        c = ble_central.Central(m, r, dist_id=dist, log=log)
        c.start_pairing()
        c.txq.clear()
        c.smp["pres"] = bytes([0x02, 0x03, 0x00, 0x01, 0x10, pres_init, 0x03])
        c.smp_rx(bytes([0x09, 0x00]) + bytes.fromhex("010203040506"))       # the responder's identity address
        sent = [chunk[4:] for _, chunk in c.txq]
        keys = (sent == [bytes([0x08]) + c.irk, bytes([0x09, 0x00]) + c.identity]) if want else not sent
        check(c.smp["preq"][5] == (0x02 if dist else 0x00) and keys and c.smp.get("done_ms") is not None,
              f"dist_id {dist}, response InitKeyDist 0x{pres_init:02x}: Pairing Request InitKeyDist "
              f"0x{c.smp['preq'][5]:02x}, the central sends {[s.hex() for s in sent]}")


def suspend_rf():
    """TC32EMU_SUSPEND_RF: with "lost" a suspend resets the registers a link layer sets again after one
    (its radio initialisation and the PA power), and the radio starts nothing until each of them the firmware
    had written since the last reset is written again; a reset ends a refusal; with "keep" nothing changes."""
    regs = ble_radio.RF_SUSPEND_REGS
    no401 = [a for a in regs if a != 0x401]         # a setup for Telink's private format may write all but 0x401
    for mode, setup in (("lost", regs), ("lost", no401), ("keep", regs)):
        name = f"{mode}, {'full setup' if setup is regs else 'setup without 0x401'}"
        ble_radio.SUSPEND_RF = mode
        m = machine()
        r = ble_radio.Radio(m)
        power_on = m.regs_mem[0x1276]
        for a in setup:
            m.reg_write(a, 1, 0x5A)
        m.analog[0x26] = 0
        m.reg_write(0x6F, 1, 0x81)                 # suspend
        m.asleep = None                            # and awake again
        lost = mode == "lost"
        check(m.regs_mem[0x1276] == (power_on if lost else 0x5A), f"{name}: 0x1276 after the suspend is 0x{m.regs_mem[0x1276]:02x}")
        check(len(r.rf_uninit) == (len(setup) if lost else 0), f"{name}: {len(r.rf_uninit)} registers to set again")
        m.reg_write(0xF00, 1, 0x82)                # BRX
        check((r.brx is None) == lost, f"{name}: a BRX before the setup: {'refused' if r.brx is None else 'started'}")
        m.reg_write(0xF00, 1, 0x80)
        for a in setup[:-1]:
            m.reg_write(a, 1, 0x5A)
        m.reg_write(0xF00, 1, 0x82)
        check((r.brx is None) == lost, f"{name}: one register still missing: {'refused' if r.brx is None else 'started'}")
        m.reg_write(0xF00, 1, 0x80)
        m.reg_write(setup[-1], 1, 0x5A)
        m.reg_write(0xF00, 1, 0x82)
        check(r.brx is not None, f"{name}: its setup written again: the BRX starts")
    # A reset ends a refusal: the registers are at their power-on values and nothing is set up yet.
    ble_radio.SUSPEND_RF = "lost"
    m = machine()
    r = ble_radio.Radio(m)
    for a in regs:
        m.reg_write(a, 1, 0x5A)
    m.analog[0x26] = 0
    m.reg_write(0x6F, 1, 0x81)
    m.asleep = None
    m.reset()
    m.reg_write(0xF00, 1, 0x82)
    check(r.brx is not None and not r.rf_uninit and not r.rf_written, "lost: a reset after the suspend ends the refusal")
    ble_radio.SUSPEND_RF = "keep"


def main():
    crypto()
    secure_connections()
    radio()
    reset()
    fifo_clear()
    adv_fifo()
    tpll()
    central()
    rx_busy()
    central_skip()
    centrals()
    privacy()
    terminate_ack()
    suspend_rf()
    print(f"\n{len(failures)} failure(s)")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())

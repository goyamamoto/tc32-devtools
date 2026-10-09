#!/usr/bin/env python3
"""The ADC model (TC32EMU_ADC, register 0xffe8), on a machine that runs no
code: a pin input fills the DMA buffer with adc_code at once, as before; the
VBAT input converts once per (r_max_mc + r_max_s) cycles of the 12 MHz state
clock (tc32emu.ADC_STATE_HZ) into the next slot of the buffer, goes round it,
lands only the last round after a jump of the time, gives nothing while
powered down, and starts again at the first slot when the DMA is enabled
again. For each mode: what the codes
are (noise of the asked size around ADC_VBAT_CODE; the code alone; a stuck
code; mostly the code alone; a ramp; two values; a sequence that is the
same after the same setting; nothing; the noise around another level), and a
SHA-256 of its first 64 codes.
The digests are the Go port's too (go/tc32emu/adc_test.go): both engines
give the same codes.

SPDX-License-Identifier: Apache-2.0
"""
import hashlib
import os
import statistics
import struct
import sys

sys.path[:0] = [os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "emulator")]
import tc32emu as te  # noqa: E402

failures = []

# SHA-256 of 64 codes (u32 little-endian) per mode, after 0xffe8 = mode | param << 8.
DIGESTS = {
    "noise:0": "f39668f34e48954261e54f075e63acdd58c586ad3614b641fb3ebea7258247b8",
    "noise:0x0705": "49ddb0545586e3801bb99555ddd93e03df8f3787a57a03902297b4e16255610e",
    "off:0": "07b257c68af081ace0683be5705c4a6aa8334c38652412184434348c22c37f64",
    "stuck:0": "78459bb79475b8bdad8f407c1c9b428aa63b3f2a812470e31f90200d295ecf26",
    "biased:0": "4062d5b703c991556d2663c3efa46fa12b3d0e2a454759c779bd2a26ccf8c654",
    "ramp:4": "29dd47c52318dd68f273e99af8290af437f88e55947b8553bdc4ca65d225dfbd",
    "two:3": "3a3bed0fd0e3e7174d2a8dcfa515c4e295dff4d5c97a60e91e8413e1b17bceee",
    "attacker:4660": "ba3c7835e44eb5e04df37807ab3a4179f4e38c7152ae448448d081e7f0c43a74",
    "level:0x800": "32628d239eccabaa2186c444bcdbf4a4da58816be3767565321202fcfb58c772",
}
BUF = 0x848000            # the DMA buffer: 32 bytes, 8 slots
SLOTS = 8
PERIOD = 500              # cycles at the 24 MHz boot clock: r_max_mc 240 + r_max_s 10 at 12 MHz


def check(cond, text):
    print(("ok   " if cond else "FAIL ") + text)
    if not cond:
        failures.append(text)


def machine(mode=None, param=0):
    fl = te.Flash()
    fl.mem[8] = 0x4B
    m = te.Machine(fl, max_log=0)
    if mode is not None:
        m.reg_write(0xFFE8, 4, mode | param << 8)
    return m


def slot(m, i):
    o = BUF - te.SRAM_BASE + 4 * i
    return struct.unpack_from("<I", m.sram, o)[0]


def start(m, vbat=True):
    """The SDK's VBAT channel set-up (or a pin), the DMA on BUF, enabled."""
    a = m.analog
    a[0xEB] = 0xFF if vbat else 0x4F
    a[0xEF], a[0xF1], a[0xFC] = 0xF0, 0x0A, 0x00
    o = BUF - te.SRAM_BASE
    m.sram[o:o + 4 * SLOTS] = bytes(4 * SLOTS)
    r = m.regs_mem
    r[0xB08], r[0xB09], r[0xB0A] = BUF & 0xFF, (BUF >> 8) & 0xFF, 1
    r[0xB10] |= 0x04
    m.update_time()


def codes(m, n):
    """n conversions, one period at a time: the slot each lands in."""
    out = []
    for _ in range(n):
        m.cycles += PERIOD
        m.update_time()
        out.append(slot(m, (m.adc_done - 1) % SLOTS))
    return out


def digest(cs):
    return hashlib.sha256(b"".join(struct.pack("<I", c) for c in cs)).hexdigest()


def main():
    code = te.ADC_VBAT_CODE

    # A pin input: adc_code at once, as before.
    m = machine()
    start(m, vbat=False)
    check(all(slot(m, i) == m.adc_code for i in range(SLOTS)), "pin input: every slot adc_code at once")

    # The VBAT input's timing.
    m = machine(1)
    start(m)
    check(all(slot(m, i) == 0 for i in range(SLOTS)), "VBAT: nothing at once")
    m.cycles += PERIOD - 1
    m.update_time()
    check(slot(m, 0) == 0, "VBAT: nothing before the first period")
    m.cycles += 1
    m.update_time()
    check(slot(m, 0) == code and slot(m, 1) == 0, "VBAT: the first conversion after one period, in slot 0")
    for i in range(SLOTS):
        m.sram[BUF - te.SRAM_BASE + 4 * i:BUF - te.SRAM_BASE + 4 * i + 4] = b"\0\0\0\0"
    m.cycles += PERIOD * (SLOTS + 2)
    m.update_time()
    check(m.adc_done == SLOTS + 3 and all(slot(m, i) == code for i in range(SLOTS)),
          "VBAT: the ring goes round (11 conversions, every slot written)")
    m.cycles += PERIOD * 1000
    m.update_time()
    check(m.adc_done == SLOTS + 1003, "VBAT: after a jump of 1000 periods the count is right")
    m.regs_mem[0xB10] &= ~0x04
    m.update_time()
    check(not m.adc_on, "VBAT: the DMA off ends the run")
    for i in range(SLOTS):
        m.sram[BUF - te.SRAM_BASE + 4 * i:BUF - te.SRAM_BASE + 4 * i + 4] = b"\0\0\0\0"
    m.regs_mem[0xB10] |= 0x04
    m.update_time()
    m.cycles += PERIOD
    m.update_time()
    check(slot(m, 0) == code and slot(m, 1) == 0, "VBAT: enabled again, the next conversion is in slot 0")
    m = machine(1)
    start(m)
    m.analog[0xFC] = 0x20
    m.cycles += PERIOD * 4
    m.update_time()
    check(all(slot(m, i) == 0 for i in range(SLOTS)), "VBAT: nothing while powered down (analog 0xfc bit 5)")
    m = machine(7)
    start(m)
    m.cycles += PERIOD * 4
    m.update_time()
    check(all(slot(m, i) == 0 for i in range(SLOTS)), "never: nothing arrives")
    m = machine(1)
    m.set_cpu_hz(48_000_000)
    start(m)
    m.cycles += 2 * PERIOD - 1
    m.update_time()
    check(slot(m, 0) == 0, "VBAT at 48 MHz: a period is 1000 cycles (20.8 us)")
    m.cycles += 1
    m.update_time()
    check(slot(m, 0) == code, "VBAT at 48 MHz: the first conversion after 1000 cycles")

    # The modes.
    got = {}
    for name, mode, param in (("noise:0", 0, 0), ("noise:0x0705", 0, 0x0705), ("off:0", 1, 0),
                              ("stuck:0", 2, 0), ("biased:0", 3, 0), ("ramp:4", 4, 4), ("two:3", 5, 3),
                              ("attacker:4660", 6, 4660), ("level:0x800", 8, 0x800)):
        m = machine(mode, param)
        start(m)
        cs = codes(m, 64)
        got[name] = digest(cs)
        more = cs + codes(m, 4096 - 64)
        if name == "noise:0":
            sd, mean = statistics.pstdev(more), statistics.fmean(more)
            check(abs(sd - 2.0) < 0.1 and abs(mean - code) < 0.15,
                  f"noise 2.0 LSB: {sd:.3f} LSB rms around {mean:.2f} (code {code})")
        if name == "noise:0x0705":
            sd = statistics.pstdev(more)
            check(0.45 < sd < 0.65, f"noise 0.5 LSB (parameter 0x0705): {sd:.3f} LSB rms, rounded")
        if mode == 1:
            check(set(more) == {code}, "off: the code alone")
        if mode == 2:
            check(set(more) == {0x1555}, "stuck: 0x1555 every time")
        if mode == 3:
            same = sum(c == code for c in more) / len(more)
            check(0.93 < same < 0.99, f"biased: {same:.3f} of the codes are the code alone")
        if mode == 4:
            check(more[:9] == [code + i // 4 for i in range(1, 10)] or more[:8] == [code + i // 4 for i in range(8)],
                  f"ramp 4: up by 1 every 4 conversions ({more[:9]})")
            check(all(b - a in (0, 1) for a, b in zip(more[:2048], more[1:2048])),
                  "ramp: never down, never more than 1 (before the 13 bits wrap)")
            m2 = machine(4, 1)
            start(m2)
            wrap = codes(m2, 0x1FFF - code + 2)
            check(wrap[-2:] == [0x1FFF, 1], f"ramp: from 0x1fff back to 1, never 0 ({wrap[-2:]})")
        if mode == 5:
            check(set(more) == {code, code + 3}, "two: the code and the code + 3")
        if mode == 6:
            check(len(set(cs)) > 30 and min(more) >= code - 32 and max(more) <= code + 31,
                  f"attacker: {len(set(cs))} different codes in 64, within -32..31 of the code")
        if mode == 8:
            sd, mean = statistics.pstdev(more), statistics.fmean(more)
            check(abs(sd - 2.0) < 0.1 and abs(mean - 0x800) < 0.15,
                  f"level 0x800: {sd:.3f} LSB rms around {mean:.2f}")
        m.reg_write(0xFFE8, 4, mode | param << 8)
        m.regs_mem[0xB10] &= ~0x04
        m.update_time()
        start(m)
        check(codes(m, 64) == cs, f"{name}: setting it again starts its sequence afresh")
    try:
        machine().reg_write(0xFFE8, 4, 9)
        check(False, "an unknown mode is refused")
    except te.EmuError:
        check(True, "an unknown mode is refused")
    for name, d in got.items():
        if DIGESTS[name] is None:
            print(f"     {name} digest {d}")
        else:
            check(d == DIGESTS[name], f"{name}: digest {d[:16]} as the Go port's")

    print(f"adc_check: {'PASS' if not failures else 'FAIL'} ({len(failures)} failure(s))")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())

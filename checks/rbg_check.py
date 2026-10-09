#!/usr/bin/env python3
"""The random number generator's sources (TC32EMU_RBG, register 0xfff4) and
the 32 kHz timer's jitter (TC32EMU_K32_JITTER_NS, 0xfff8, 0xfffc), on a
machine that runs no code. For each source: when SR bit 0 reads 1, what DR
gives (the default sequence unchanged; a stuck word; bits biased as asked; a
cycle of the length asked; nothing from a block not clocked or never ready),
and a SHA-256 of its first 64 words. For the jitter: the count follows the
exact 32768 Hz within a few ticks, its edges move against the exact ones,
and a SHA-256 of the counts at fixed times. The digests are the Go port's
too (go/tc32emu/rbg_test.go): both engines give the same sequences. The AES
block (0x540-0x55f) on FIPS-197 Appendix B, both ways, as the Go TestAESBlock.

SPDX-License-Identifier: Apache-2.0
"""
import hashlib
import os
import struct
import sys

sys.path[:0] = [os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "emulator")]
import tc32emu as te  # noqa: E402

failures = []

# SHA-256 of 64 words (little-endian) per source, after 0xfff4 = mode | param << 8.
DIGESTS = {
    "lcg": "21fec69d8d40f43d3db8cc0aa5b50187ee93f5753249756e287297ec53dcfef3",
    "healthy:7": "5f3e4c48b587b6a658ddb749fca054f558a383328ba57710b6861bec4e12d041",
    "stuck:0": "8bfe96b7ab7217459a0d2f0b4b020a21e5976fec991eba4803711536093ca1b2",
    "biased:243": "03e6ae5ee4cfd96a59b7ff5d6bdb3bc0f9fdcb94017963cd02219c6fc50f58c4",
    "cycle:8": "6401d64776273163028f7e7a56f521db75ebc3e786e39498a3b263ef3d9c0f1a",
    "attacker:4660": "7fe500de9c002c813d5f8d993debc20c3be1c1a42995a6b30f56bb7f202abfdf",
}
K32_DIGEST = "7b2833aff6ee20cb0b0d95f91a7da645d29e9ef052bbf5fe729553c0b5127307"


def check(cond, text):
    print(("ok   " if cond else "FAIL ") + text)
    if not cond:
        failures.append(text)


def machine():
    fl = te.Flash()
    fl.mem[8] = 0x4B
    return te.Machine(fl, max_log=0)


def words(m, n=64):
    return [m.reg_read(0x440C, 4) for _ in range(n)]


def digest(ws):
    return hashlib.sha256(b"".join(struct.pack("<I", w) for w in ws)).hexdigest()


def main():
    m = machine()
    # The default: ready without the clock, the fixed sequence.
    check(m.reg_read(0x4408, 1) & 1 == 1, "lcg: SR bit 0 reads 1 with the block off")
    ws = words(m)
    check(ws[0] == 0x0B719151, f"lcg: the first word is the fixed sequence's ({ws[0]:#010x})")
    got = {"lcg": digest(ws)}
    for name, mode, param in (("healthy:7", 1, 7), ("stuck:0", 2, 0), ("biased:243", 3, 243),
                              ("cycle:8", 4, 8), ("attacker:4660", 5, 4660)):
        m = machine()
        m.reg_write(0xFFF4, 4, mode | param << 8)
        check(m.reg_read(0x4408, 1) & 1 == 0 and m.reg_read(0x440C, 4) == 0,
              f"{name}: not ready and DR 0 with the block off")
        m.regs_mem[0x65] |= 0x08
        m.regs_mem[0x4400] |= 0x01
        check(m.reg_read(0x4408, 1) & 1 == 1, f"{name}: ready when clocked and enabled")
        m.regs_mem[0x62] |= 0x08
        check(m.reg_read(0x4408, 1) & 1 == 0, f"{name}: not ready in reset")
        m.regs_mem[0x62] &= ~0x08
        ws = words(m)
        got[name] = digest(ws)
        if mode == 2:
            check(set(ws) == {0x5A5A5A5A}, "stuck: one word, 0x5a5a5a5a")
        if mode == 3:
            ones = sum(bin(w).count("1") for w in words(m, 1024)) / (1024 * 32)
            check(abs(ones - 243 / 256) < 0.01, f"biased: {ones:.4f} of the bits are 1 (243/256 asked)")
        if mode == 4:
            check(ws[:8] == ws[8:16] and len(set(ws[:8])) == 8, "cycle: 8 words, over and over")
        if mode in (1, 5):
            check(len(set(ws)) == 64, f"{name}: 64 different words")
        m.reg_write(0xFFF4, 4, mode | param << 8)
        check(words(m) == ws, f"{name}: setting the source again starts its sequence afresh")
    m = machine()
    m.regs_mem[0x65] |= 0x08
    m.regs_mem[0x4400] |= 0x01
    m.reg_write(0xFFF4, 4, 6)
    check(m.reg_read(0x4408, 1) & 1 == 0, "never: SR bit 0 never reads 1")
    try:
        m.reg_write(0xFFF4, 4, 7)
        check(False, "an unknown source is refused")
    except te.EmuError:
        check(True, "an unknown source is refused")
    for name, d in got.items():
        if DIGESTS[name] is None:
            print(f"     {name} digest {d}")
        else:
            check(d == DIGESTS[name], f"{name}: digest {d[:16]} as the Go port's")

    # The 32 kHz jitter.
    m = machine()
    exact = [m.k32_now()]
    m.reg_write(0xFFFC, 4, 7)
    m.reg_write(0xFFF8, 4, 200)
    counts, worst, moved = [], 0, 0
    for i in range(4000):
        m.cycles += 977          # about 20 us at the boot clock: not a whole number of 32 kHz periods
        c = m.k32_now()
        e = int(m.ms() * 32.768)
        counts.append(c)
        worst = max(worst, abs(c - e))
        moved += c != e
    check(worst <= 3, f"jitter 200 ns: the count within {worst} tick(s) of the exact one")
    check(moved > 0, f"jitter 200 ns: the count differs from the exact one at {moved} of 4000 reads")
    check(all(b >= a for a, b in zip(counts, counts[1:])), "jitter: the count never goes back")
    m.cycles += 24_000_000   # a jump of one second: only the last periods get jitter
    c = m.k32_now()
    check(abs(c - int(m.ms() * 32.768)) <= 3, "jitter: after a jump of 1 s the count is still close")
    kd = hashlib.sha256(b"".join(struct.pack("<I", c) for c in counts + [c])).hexdigest()
    if K32_DIGEST is None:
        print(f"     k32 digest {kd}")
    else:
        check(kd == K32_DIGEST, f"k32 jitter: digest {kd[:16]} as the Go port's")
    m.reg_write(0xFFF8, 4, 0)
    check(m.k32_now() == int(m.ms() * 32.768), "jitter 0: the exact count again")
    m.reg_write(0xFFF8, 4, 0x80000000)
    c0, b0 = m.k32_now(), m.reg_read(0x74B, 1)
    m.cycles += 48_000
    check(m.k32_now() == c0 and m.reg_read(0x750, 4) == c0 and m.reg_read(0x74B, 1) == b0,
          "0xfff8 bit 31: the 32 kHz count stands still")
    m.reg_write(0xFFF8, 4, 0)
    check(m.k32_now() == int(m.ms() * 32.768), "0xfff8 bit 31 clear: the count goes on")
    check(exact[0] == 0, "power-on: the count starts at 0")

    # The AES block.
    key = bytes.fromhex("2b7e151628aed2a6abf7158809cf4f3c")
    pt = bytes.fromhex("3243f6a8885a308d313198a2e0370734")
    ct = bytes.fromhex("3925841d02dc09fbdc118597196a0b32")
    m = machine()
    for dec, src, want in ((0, pt, ct), (1, ct, pt)):
        m.reg_write(0x540, 1, dec)
        m.regs_mem[0x550:0x560] = key
        for i in range(0, 16, 4):
            m.reg_write(0x548, 4, struct.unpack_from("<I", src, i)[0])
        done = m.reg_read(0x540, 1) & 0x04
        got = b"".join(struct.pack("<I", m.reg_read(0x548, 4)) for _ in range(4))
        check(done and got == want, f"AES block, {'decrypt' if dec else 'encrypt'}: {got.hex()}")
    print(f"{len(failures)} failure(s)")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())

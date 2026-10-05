#!/usr/bin/env python3
"""Find the code of a raw TC32 image by following its control flow, and
report what that code does, as evidence of what TC32 hardware runs.

Raw images built by Telink's toolchain have no symbols: code and data are
mixed. Starting at the reset vector (0) and the interrupt vector (0x10),
every reached instruction is decoded; conditional branches continue on
both sides, tjl continues after the call and at its target, and b, bx,
mov/add pc, pop {pc} and treti end a path. PC-relative loads mark their
literal as data. Only reached halfwords count, so data is never taken for
code; code reached only through pointers is missed. The boot ROM's copy of
the image's start to SRAM (0x840000, the first (header word 0x0c & 0xffff) *
16 bytes) is followed as the same bytes.

Reported per image: coverage, instruction forms (forms_check.form), loads
whose result the next instruction uses, GE/PL/LS branches, and 32-bit words in
literal pools that equal a reached function's address, even or odd (whether
code addresses the toolchain stores are even).

Usage: tc32_flow.py <image>... [--forms-out forms.tsv]

SPDX-License-Identifier: Apache-2.0
"""
import argparse
import collections
import os
import struct
import sys

sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "common"))
import tc32isa as te  # noqa: E402
import forms_check  # noqa: E402

SRAM = 0x840000


def sext(v, bits):
    return v - (1 << bits) if v & (1 << (bits - 1)) else v


def dest_of_load(t):
    top = t >> 11
    if top in (9, 0x13):
        return {(t >> 8) & 7}
    if top in (0x0D, 0x0F, 0x11) or (top in (0x0A, 0x0B) and (t >> 9) & 7 >= 3):
        return {t & 7}
    return None


def reads(t):
    """Low registers an instruction reads (enough for the load-use count)."""
    top = t >> 11
    lo = lambda s: (t >> s) & 7  # noqa: E731
    if top <= 2:
        return {lo(3)}
    if top == 3:
        return {lo(3)} | (set() if t & 0x400 else {lo(6)})
    if 5 <= top <= 7:
        return {lo(8)}
    if t >> 10 == 0x10:
        return {lo(3)} if (t >> 6) & 15 in (9, 15) else {lo(0), lo(3)}
    if t >> 10 == 0x11:
        op, rd, rm = (t >> 8) & 3, (t & 7) | ((t >> 4) & 8), (t >> 3) & 15
        return {r for r in ((rd, rm) if op < 2 else (rm,)) if r < 8}
    if top in (0x0A, 0x0B):
        return {lo(3), lo(6)} | ({lo(0)} if (t >> 9) & 7 <= 2 else set())
    if top in (0x0C, 0x0E, 0x10):
        return {lo(3), lo(0)}
    if top in (0x0D, 0x0F, 0x11):
        return {lo(3)}
    if top == 0x12:
        return {lo(8)}
    if t & 0xF600 == 0xB400 and not t & 0x800:
        return {i for i in range(8) if t >> i & 1}
    if top == 0x18:
        return {lo(8)} | {i for i in range(8) if t >> i & 1}
    if top == 0x19:
        return {lo(8)}
    return set()


class Image:
    def __init__(self, data):
        self.data = data
        w0c, = struct.unpack_from("<I", data, 0x0C)
        self.mirror = (w0c & 0xFFFF) * 16

    def offset(self, addr):
        """Image offset of a run-time address, or None."""
        if 0 <= addr < len(self.data):
            return addr
        if SRAM <= addr < SRAM + self.mirror and addr - SRAM < len(self.data):
            return addr - SRAM
        return None

    def hw(self, off):
        return struct.unpack_from("<H", self.data, off)[0]


def walk(img):
    code, literal, funcs, bad = {}, set(), {0, 0x10}, []
    work = [0, 0x10]
    while work:
        a = work.pop()
        while True:
            if a in code:
                break
            if a + 2 > len(img.data) or a in literal:
                if a in literal:
                    bad.append(("code runs into a literal", a))
                break
            hw = img.hw(a)
            code[a] = hw
            if hw & 0xFFE0 == 0x6BC0 or hw & 0xFF00 == 0xCF00:
                a += 2
                continue
            if hw & 0xFE00 == 0x6800:                    # treti
                break
            t = te.to_thumb(hw)
            top = t >> 11
            if top == 9:
                lit = ((a + 4) & ~3) + (t & 0xFF) * 4
                literal.update((lit, lit + 2))
            if top == 0x1C:                              # b
                tgt = a + 4 + sext(t & 0x7FF, 11) * 2
                if img.offset(tgt) is not None:
                    work.append(img.offset(tgt))
                break
            if top in (0x1A, 0x1B):
                cc = (t >> 8) & 15
                if cc >= 14:
                    bad.append(("undefined", a))
                    break
                tgt = a + 4 + sext(t & 0xFF, 8) * 2
                if img.offset(tgt) is not None:
                    work.append(img.offset(tgt))
                a += 2
                continue
            if top == 0x1E:                              # tjl
                if a + 4 > len(img.data):
                    break
                t2 = te.to_thumb(img.hw(a + 2))
                if t2 >> 11 != 0x1F:
                    bad.append(("bl prefix without suffix", a))
                    break
                code[a + 2] = img.hw(a + 2)
                tgt = a + 4 + (sext(t & 0x7FF, 11) << 12) + (t2 & 0x7FF) * 2
                o = img.offset(tgt)
                if o is not None:
                    funcs.add(o)
                    work.append(o)
                a += 4
                continue
            if top == 0x1F or top == 0x1D:
                bad.append(("lone bl half", a))
                break
            if t >> 10 == 0x11:
                op, rd = (t >> 8) & 3, (t & 7) | ((t >> 4) & 8)
                if op == 3 or (rd == 15 and op in (0, 2)):
                    break
            if t & 0xFF00 == 0xBD00:                     # pop {.., pc}
                break
            if top in (0x16, 0x17) and not (t & 0xFF00 == 0xB000 or t & 0xF600 == 0xB400):
                bad.append(("undefined", a))
                break
            a += 2
    return code, literal, funcs, bad


def analyse(path):
    data = open(path, "rb").read()
    if data[8:12] != b"KNLT" and data.find(b"KNLT") >= 8:     # e.g. a Zigbee OTA file's header first
        data = data[data.find(b"KNLT") - 8:]
    img = Image(data)
    code, literal, funcs, bad = walk(img)
    forms = collections.Counter()
    loaduse = loads = 0
    cond = collections.Counter()
    for a, hw in code.items():
        f = forms_check.form(hw)
        if a - 2 in code and te.to_thumb(code[a - 2]) >> 11 == 0x1E:
            f = "bl suffix"
        forms[f] += 1
        t = te.to_thumb(hw)
        d = dest_of_load(t)
        if d is not None and a + 2 in code and not te.to_thumb(code[a + 2]) >> 11 == 0x1F:
            loads += 1
            if reads(te.to_thumb(code[a + 2])) & d:
                loaduse += 1
        if t >> 12 == 0xD and (t >> 8) & 15 < 14:
            cond["eq ne cs cc mi pl vs vc hi ls ge lt gt le".split()[(t >> 8) & 15]] += 1
    starts = {a for a in funcs}
    ptr = collections.Counter()
    for lit in sorted(literal):
        if lit % 4 or lit + 4 > len(data):
            continue
        v, = struct.unpack_from("<I", data, lit)
        for base in (0, SRAM):
            o = v - base
            if 0 <= o < len(data) and (o & ~1) in starts and o & ~1 not in (0,):
                ptr["odd" if v & 1 else "even"] += 1
    return dict(bytes=len(data), code=len(code) * 2, literal=len(literal) * 2, funcs=len(funcs), bad=bad,
                forms=forms, loads=loads, loaduse=loaduse, cond=cond, ptr=ptr)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("images", nargs="+")
    ap.add_argument("--forms-out")
    a = ap.parse_args()
    total = collections.Counter()
    print("image\tbytes\tcode bytes reached\tfunctions\tloads followed\tnext uses the load\tGE/PL/LS"
          "\tcode addresses in literals even/odd\tstops")
    for p in a.images:
        r = analyse(p)
        total.update(r["forms"])
        c = r["cond"]
        print(f"{p.split('/')[-1]}\t{r['bytes']}\t{r['code']} ({100 * r['code'] // r['bytes']}%)\t{r['funcs']}"
              f"\t{r['loads']}\t{r['loaduse']}\t{c['ge']}/{c['pl']}/{c['ls']}"
              f"\t{r['ptr']['even']}/{r['ptr']['odd']}\t{len(r['bad'])}")
    if a.forms_out:
        with open(a.forms_out, "w") as f:
            for k in sorted(total):
                f.write(f"{k}\t{total[k]}\n")
    return 0


if __name__ == "__main__":
    sys.exit(main())

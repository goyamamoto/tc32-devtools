#!/usr/bin/env python3
"""Compare the images of the two build paths of the same sources.

The Thumb path re-encodes a Thumb link (thumb2tc32.py); the direct path
links TC32 code that llvm-tc32 wrote itself (-mcpu=tc32, elf2bin.py). The
two encoders are independent, so equal images check each other. This
reports every byte where the two images differ, merged into runs, each with
- its address range and the section that holds it;
- what the ELF has there: code (after a $t mapping symbol, outside sized
  data objects), data, padding between sections inside a segment, or a gap
  between segments;
- the symbol that holds it (name+offset; the nearest one below for padding);
- both images' bytes, and for code both halfwords read as Thumb.
The ELF is either path's (both link the same layout); with --elf2 the other
ELF's layout is compared too (sections and symbols: name, address, size).

Usage: path_diff.py <thumb-path.bin> <direct-path.bin> --elf <either.elf> [--elf2 <the other.elf>]
                    [--readelf <llvm-readelf>] [--max N]
Exit status 0 when the images are byte-identical (and the layouts agree).

SPDX-License-Identifier: Apache-2.0
"""
import argparse
import bisect
import os
import re
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(os.path.dirname(HERE), "common"))
import tc32isa  # noqa: E402
import toolchain  # noqa: E402


def readelf(tool, flag, elf):
    return subprocess.run([tool, flag, "-W", elf], check=True, capture_output=True, text=True).stdout


def layout(tool, elf):
    """(sections [(lma, vma, size, name, flags, index)], symbols [(vma, size, type, index, name)])."""
    secs = []
    for line in readelf(tool, "-S", elf).splitlines():
        m = re.match(r"\s*\[\s*(\d+)\]\s+(\S+)\s+(\S+)\s+([0-9a-f]+)\s+([0-9a-f]+)\s+([0-9a-f]+)\s+\S+\s+(\S*)", line)
        if m and m.group(3) not in ("NULL", "NOBITS") and "A" in m.group(7) and int(m.group(6), 16):
            secs.append([None, int(m.group(4), 16), int(m.group(6), 16), m.group(2), m.group(7), int(m.group(1)),
                         int(m.group(5), 16)])
    loads = []
    for line in readelf(tool, "-l", elf).splitlines():
        p = line.split()
        if p and p[0] == "LOAD":
            loads.append((int(p[1], 16), int(p[2], 16), int(p[3], 16), int(p[4], 16)))
    out = []
    for s in secs:
        off = s[6]
        seg = next((g for g in loads if g[0] <= off and off + s[2] <= g[0] + g[3]), None)
        if seg:
            out.append((seg[2] + off - seg[0], s[1], s[2], s[3], s[4], s[5]))
    syms = []
    for line in readelf(tool, "-s", elf).splitlines():
        p = line.split()
        if len(p) >= 8 and p[0].endswith(":") and p[6].isdigit():
            syms.append((int(p[1], 16), int(p[2], 0) if p[2].isdigit() else int(p[2], 16), p[3], int(p[6]), p[7]))
    return sorted(out), syms, loads


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("thumb_bin")
    ap.add_argument("direct_bin")
    ap.add_argument("--elf", required=True)
    ap.add_argument("--elf2")
    ap.add_argument("--readelf", default=toolchain.tool("llvm-readelf"))
    ap.add_argument("--max", type=int, default=40)
    a = ap.parse_args()
    x, y = open(a.thumb_bin, "rb").read(), open(a.direct_bin, "rb").read()
    secs, syms, loads = layout(a.readelf, a.elf)
    problems = []
    if a.elf2:
        secs2, syms2, _ = layout(a.readelf, a.elf2)
        if [s[:4] for s in secs] != [s[:4] for s in secs2]:
            problems.append("the two ELFs' loaded sections differ (name, address or size)")
        key = lambda t: (t[0], t[1], t[2], t[4])  # noqa: E731 - address, size, type, name
        only = set(map(key, syms)) ^ set(map(key, syms2))
        if only:
            problems.append(f"the two ELFs' symbols differ: {len(only)}, e.g. {sorted(only)[:3]}")
    if len(x) != len(y):
        problems.append(f"sizes differ: {len(x)} vs {len(y)} B")
    marks = {}
    objects = {}
    named = []
    for value, size, typ, ndx, name in syms:
        if re.fullmatch(r"\$[tad](\..*)?", name):
            marks.setdefault(ndx, []).append((value & ~1, name[1]))
        else:
            if typ == "OBJECT" and size:
                objects.setdefault(ndx, []).append((value, value + size))
            if typ in ("FUNC", "OBJECT", "NOTYPE") and name:
                named.append((value & ~1, size, name, ndx))
    for v in marks.values():
        v.sort()
    named.sort()
    starts = [n[0] for n in named]

    def describe(addr):
        """(lma -> section name, kind, symbol, vma) for an image offset."""
        for lma, vma, size, name, flags, ndx in secs:
            if lma <= addr < lma + size:
                v = vma + addr - lma
                mk = marks.get(ndx, [])
                j = bisect.bisect_right([m[0] for m in mk], v) - 1
                kind = mk[j][1] if j >= 0 else None
                code = "X" in flags and kind == "t" and not any(lo <= v < hi for lo, hi in objects.get(ndx, ()))
                i = bisect.bisect_right(starts, v) - 1
                while i >= 0 and named[i][3] != ndx:
                    i -= 1
                sym = f"{named[i][2]}+0x{v - named[i][0]:x}" if i >= 0 else "?"
                return name, "code" if code else "data", sym, v
        inside = any(g[2] <= addr < g[2] + g[3] for g in loads)
        i = bisect.bisect_right([s[0] for s in secs], addr) - 1
        after = secs[i][3] if i >= 0 else "the start"
        return (f"after {after}", "padding in a segment" if inside else "gap between segments", "-", None)

    runs = []
    n = min(len(x), len(y))
    i = 0
    while i < n:
        if x[i] == y[i]:
            i += 1
            continue
        j = i
        while j < n and x[j] != y[j]:
            j += 1
        runs.append((i, j))
        i = j
    total = sum(j - i for i, j in runs)
    for i, j in runs[:a.max]:
        sec, kind, sym, v = describe(i)
        line = (f"0x{i:06x}-0x{j - 1:06x} ({j - i} B) {kind} in {sec} {sym}: "
                f"thumb path {x[i:j][:8].hex()} direct {y[i:j][:8].hex()}")
        if kind == "code" and j - i <= 4 and i % 2 == 0:
            hx = int.from_bytes(x[i:i + 2], "little")
            hy = int.from_bytes(y[i:i + 2], "little")
            line += f" (as Thumb {tc32isa.to_thumb(hx):04x} / {tc32isa.to_thumb(hy):04x})"
        print(line)
    if len(runs) > a.max:
        print(f"... {len(runs) - a.max} more run(s)")
    for p in problems:
        print(p)
    verdict = "IDENTICAL" if not runs and not problems else "DIFFERENT"
    print(f"path_diff: {verdict}: {len(x)} / {len(y)} B, {total} byte(s) differ in {len(runs)} run(s)")
    return 0 if verdict == "IDENTICAL" else 1


if __name__ == "__main__":
    sys.exit(main())

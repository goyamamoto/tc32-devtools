#!/usr/bin/env python3
"""Check the TC32 image thumb2tc32.py wrote against the Thumb ELF it came from.

forms_check.py and the emulator's runs read the ELF or execute the image;
neither compares every byte of the written image with the link. This does,
with LLVM's own tools for the layout: llvm-objcopy -O binary lays the ELF
out by load address, and llvm-readelf gives the sections, program headers
and symbols, independently of thumb2tc32's ELF reader.

Each halfword of a loaded section is classified from the ELF:
- code: inside an executable section, after a $t mapping symbol, and not
  inside a sized STT_OBJECT;
- data: everything else ($d, literal pools, data objects, other sections).
Code must be thumb2tc32.encode() of the ELF's halfword (the permutation that
isa_check.py verified against Telink's objdump, and the two rewrites); data
must be the ELF's halfword unchanged. Padding between sections inside a
segment must be the ELF's bytes; between segments, 0xff (thumb2tc32's fill,
erased flash). The image must be as long as objcopy's output.

A Zephyr image's start-up copies .data from __data_region_load_start to
__data_region_start (crt0). The linker script computes the load address
itself, so an orphan section landing between rodata and data in flash makes
it wrong while everything else links (seen with .ARM.exidx in a Thumb build).
When the symbols exist, the PT_LOAD whose virtual address is
__data_region_start must have the physical address __data_region_load_start
and hold the whole region.

With --blob JSON (a manifest with the blob's size, SHA-256 and the symbol
that marks its start), the blob in the linked image is checked as the manifest describes: the symbol has the manifest's size and
sits in an executable section; the blob is either data (one $d at its
start and nothing else, which thumb2tc32.py leaves alone) or code (a $t at
its start: hand-written source that thumb2tc32.py re-encodes, with $d over
its literal pools); and its bytes in the image have the manifest's SHA-256,
for code the proof that it assembles and converts to exactly the manifest's
bytes.

With --startup JSON (a manifest of a startup proven on the chip), the
startup at the image's start is checked as the manifest describes: the
instructions of its ranges are the manifest's bytes, the header asks the
boot ROM for the manifest's copy size, the reset vector jumps to the entry,
the entry ends in a jump to the startup's first instruction (and, when it
arms the watchdog, begins with the startup's first instructions), the call
goes to the manifest's symbol, and every pool word has the fixed value or
the value of its linker expression. Both keep code that boots the chip
byte for byte as it was proven.

Usage: image_check.py <zmk.elf> <zmk.bin> [--objcopy <llvm-objcopy>] [--readelf <llvm-readelf>]
                      [--blob <manifest.json>]
                      [--startup <manifest.json>]

SPDX-License-Identifier: Apache-2.0
"""
import argparse
import bisect
import hashlib
import json
import os
import re
import struct
import subprocess
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
import thumb2tc32  # noqa: E402

sys.path.insert(0, os.path.join(os.path.dirname(HERE), "common"))
import toolchain  # noqa: E402


def thumb2tc32_to_thumb(hw):
    """The Thumb halfword of a TC32 halfword of the image (tc32isa's table)."""
    import tc32isa
    return tc32isa.to_thumb(hw)


def readelf(tool, flag, elf):
    return subprocess.run([tool, flag, "-W", elf], check=True, capture_output=True, text=True).stdout


def sections(tool, elf):
    """{index: (name, vma, file offset, size, flags, type)}."""
    out = {}
    for line in readelf(tool, "-S", elf).splitlines():
        m = re.match(r"\s*\[\s*(\d+)\]\s+(\S+)\s+(\S+)\s+([0-9a-f]+)\s+([0-9a-f]+)\s+([0-9a-f]+)\s+\S+\s+(\S*)",
                     line)
        if m and m.group(3) != "NULL":
            out[int(m.group(1))] = (m.group(2), int(m.group(4), 16), int(m.group(5), 16), int(m.group(6), 16),
                                    m.group(7), m.group(3))
    return out


def loads(tool, elf):
    """[(file offset, vaddr, paddr, filesz)] of the PT_LOAD headers."""
    out = []
    for line in readelf(tool, "-l", elf).splitlines():
        p = line.split()
        if p and p[0] == "LOAD":
            out.append((int(p[1], 16), int(p[2], 16), int(p[3], 16), int(p[4], 16)))
    return out


def symbols(tool, elf):
    """[(value, size, type, section index (-1 for ABS), name)]."""
    out = []
    for line in readelf(tool, "-s", elf).splitlines():
        p = line.split()
        if len(p) >= 8 and p[0].endswith(":") and (p[6].isdigit() or p[6] == "ABS"):
            out.append((int(p[1], 16), int(p[2], 0) if p[2].isdigit() else int(p[2], 16), p[3],
                        -1 if p[6] == "ABS" else int(p[6]), p[7]))
    return out


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("elf")
    ap.add_argument("bin")
    ap.add_argument("--objcopy", default=toolchain.tool("llvm-objcopy"))
    ap.add_argument("--readelf", default=toolchain.tool("llvm-readelf"))
    ap.add_argument("--blob", help="a blob's JSON manifest: check the blob in the image against it")
    ap.add_argument("--startup", help="a startup's JSON manifest: check the startup in the image against it")
    a = ap.parse_args()

    with tempfile.TemporaryDirectory() as d:
        raw_path = os.path.join(d, "raw.bin")
        subprocess.run([a.objcopy, "-O", "binary", a.elf, raw_path], check=True)
        raw = open(raw_path, "rb").read()
    img = open(a.bin, "rb").read()
    elf_data = open(a.elf, "rb").read()
    secs = sections(a.readelf, a.elf)
    segs = loads(a.readelf, a.elf)
    syms = symbols(a.readelf, a.elf)

    bad = []
    if len(img) != len(raw):
        bad.append(f"image {len(img)} B, objcopy {len(raw)} B")

    # Mapping symbols and data objects per section.
    marks, objects = {}, {}
    for value, size, typ, ndx, name in syms:
        if re.fullmatch(r"\$[tad](\..*)?", name):
            marks.setdefault(ndx, []).append((value & ~1, name[1]))
        elif typ == "OBJECT" and size > 0:
            objects.setdefault(ndx, []).append((value, value + size))
    for v in marks.values():
        v.sort()

    loaded = []
    base = None
    for ndx, (name, vma, off, size, flags, typ) in sorted(secs.items()):
        if "A" not in flags or typ == "NOBITS" or size == 0:
            continue
        seg = next((s for s in segs if s[0] <= off and off + size <= s[0] + s[3]), None)
        if seg is None:
            continue
        lma = seg[2] + (off - seg[0])
        loaded.append((lma, ndx, name, vma, off, size, flags))
    base = min(lo for lo, *_ in loaded)
    covered = bytearray(len(img))
    counts = {"code": 0, "data": 0, "rewritten": 0}
    for lma, ndx, name, vma, off, size, flags in loaded:
        src = elf_data[off:off + size]
        o = lma - base
        if raw[o:o + size] != src:
            bad.append(f"{name}: objcopy disagrees with the section's bytes")
        covered[o:o + size] = b"\x01" * size
        mk = marks.get(ndx, [])
        keys = [x for x, _ in mk]
        objs = objects.get(ndx, [])
        for i in range(0, size - 1, 2):
            addr = vma + i
            j = bisect.bisect_right(keys, addr) - 1
            kind = mk[j][1] if j >= 0 else None
            code = "X" in flags and kind == "t" and not any(lo <= addr < hi for lo, hi in objs)
            hw, = struct.unpack_from("<H", src, i)
            got, = struct.unpack_from("<H", img, o + i) if o + i + 2 <= len(img) else (None,)
            want = thumb2tc32.encode(hw) if code else hw
            counts["code" if code else "data"] += 1
            if code and thumb2tc32.rewrite(hw)[1] in ("movs", "udf"):
                counts["rewritten"] += 1
            if got != want:
                if len(bad) < 40:
                    bad.append(f"{name}+0x{i:x} (lma 0x{lma + i:x}, {'code' if code else 'data'}): image "
                               f"{got if got is None else f'0x{got:04x}'}, expected 0x{want:04x} (ELF 0x{hw:04x})")
                else:
                    bad.append("...")
                    break
        if "X" in flags and not mk:
            bad.append(f"{name}: executable section without mapping symbols")
    # Padding: inside a segment, the ELF's bytes (the linker's alignment fill);
    # between segments, 0xff.
    gaps = []
    for i in range(len(img)):
        if covered[i]:
            continue
        seg = next((sg for sg in segs if sg[2] <= base + i < sg[2] + sg[3]), None)
        want = elf_data[seg[0] + base + i - seg[2]] if seg else 0xFF
        if img[i] != want:
            gaps.append(i)
    if gaps:
        bad.append(f"{len(gaps)} padding bytes differ (inside a segment from the ELF, between segments from"
                   f" 0xff), first at 0x{base + gaps[0]:x}")
    # The .data copy source (Zephyr's crt0) against the ELF's data segment.
    sym = {name: value for value, _size, _typ, _ndx, name in syms}
    if {"__data_region_start", "__data_region_end", "__data_region_load_start"} <= sym.keys():
        start, end, load = sym["__data_region_start"], sym["__data_region_end"], sym["__data_region_load_start"]
        seg = next((sg for sg in segs if sg[1] == start and sg[3] > 0), None)
        if seg is None:
            bad.append(f"data copy: no PT_LOAD at __data_region_start 0x{start:x}")
        elif seg[2] != load:
            bad.append(f"data copy: __data_region_load_start 0x{load:x}, but the data segment is loaded from"
                       f" 0x{seg[2]:x} (an orphan section between rodata and data?)")
        elif end - start > seg[3]:
            bad.append(f"data copy: region 0x{end - start:x} B, but the data segment holds 0x{seg[3]:x} B")
        data_copy = f"data copy source 0x{load:x} checked"
    else:
        data_copy = "no __data_region symbols (not a Zephyr image)"
    # A blob against its manifest.
    blob = ""
    if a.blob:
        man = json.load(open(a.blob))
        if not man.get("symbol"):
            sys.exit("image_check.py: the blob's manifest names no symbol")
        bname = man["symbol"]
        blob_syms = [(v, sz, t, n) for v, sz, t, n, name in syms if name == bname]
        if len(blob_syms) != 1 or blob_syms[0][2] not in ("OBJECT", "FUNC", "NOTYPE"):
            bad.append(f"blob: {len(blob_syms)} symbol(s) {bname}, want one")
        else:
            bv, bsz, _bt, bndx = blob_syms[0]
            bv &= ~1                             # a Thumb function symbol carries bit 0
            sec = secs.get(bndx)
            found = next((x for x in loaded if x[1] == bndx), None)
            if bsz != man["size"]:
                bad.append(f"blob: size {bsz}, manifest {man['size']}")
            if sec is None or "X" not in sec[4] or found is None:
                bad.append("blob: not in a loaded executable section")
            else:
                lma, _n, name, vma, off, size, _f = found
                inside = [(x, k) for x, k in marks.get(bndx, []) if bv <= x < bv + bsz]
                code = bool(inside) and inside[0] == (bv, "t")
                if not code and inside != [(bv, "d")]:
                    bad.append(f"blob: mapping symbols inside it {inside}, "
                               f"want one $d at its start (data) or a $t at its start (code)")
                o = lma - base + (bv - vma)
                got = hashlib.sha256(img[o:o + bsz]).hexdigest()
                if got != man["blob_sha256"]:
                    bad.append(f"blob: image bytes sha256 {got[:16]}..., manifest {man['blob_sha256'][:16]}...")
                blob = f"; blob {bsz} B at 0x{lma + (bv - vma):x} checked"
    # The startup against its manifest.
    startup = ""
    if a.startup:
        man = json.load(open(a.startup))
        n0 = len(bad)
        if base != 0:
            bad.append(f"startup: the image starts at 0x{base:x}, not 0")
        code = b"".join(img[lo:hi] for lo, hi in man["code"])
        if code.hex() != man["code_hex"]:
            first = next((lo + i for lo, hi in man["code"] for i in range(0, hi - lo, 2)
                          if img[lo + i:lo + i + 2] != bytes.fromhex(man["code_hex"])[
                              sum(h - l for l, h in man["code"] if l < lo) + i:][:2]), None)
            bad.append("startup: the instructions differ from the manifest's"
                       + (f", first at 0x{first:x}" if first is not None else ""))
        if struct.unpack_from("<I", img, 0xC)[0] != man["header_copy_word"]:
            bad.append(f"startup: header word 0xc is 0x{struct.unpack_from('<I', img, 0xC)[0]:08x}, "
                       f"manifest 0x{man['header_copy_word']:08x}")

        def jump(off):
            th = thumb2tc32_to_thumb(struct.unpack_from("<H", img, off)[0])
            if th >> 11 != 0x1C:
                return None
            d = th & 0x7FF
            return off + 4 + 2 * (d - 0x800 if d & 0x400 else d)
        if jump(0) != man["entry"]:
            bad.append(f"startup: the reset vector does not jump to the entry 0x{man['entry']:x}")
        out = next((o for o in range(man["entry"], man["stub_end"], 2) if jump(o) is not None), None)
        if out is None or jump(out) != man["start"]:
            bad.append(f"startup: the entry does not end in a jump to 0x{man['start']:x}")
        elif out > man["entry"]:
            # The entry arms the watchdog: before it, the startup's delay (its first five halfwords).
            if img[man["entry"]:man["entry"] + 10] != bytes.fromhex(man["code_hex"])[:10]:
                bad.append("startup: the entry does not begin with the startup's first instructions")
        th0, th1 = (thumb2tc32_to_thumb(struct.unpack_from("<H", img, man["call"] + i)[0]) for i in (0, 2))
        hi = th0 & 0x7FF
        target = man["call"] + 4 + ((hi - 0x800 if hi & 0x400 else hi) << 12) + ((th1 & 0x7FF) << 1)
        if th0 >> 11 != 0x1E or th1 >> 11 != 0x1F or target != sym.get(man["call_symbol"], -1) & ~1:
            bad.append(f"startup: 0x{man['call']:x} is not a call to {man['call_symbol']}")
        for e in man["pool"]:
            got = struct.unpack_from("<I", img, e["offset"])[0]
            if e["kind"] == "symbol":
                # A sum of linker symbols and numbers.
                terms = [t.strip() for t in e["value"].split("+")]
                missing = [t for t in terms if not re.fullmatch(r"(0x)?[0-9a-fA-F]+", t) and t not in sym]
                if missing:
                    bad.append(f"startup: pool 0x{e['offset']:x}: no symbol {missing}")
                    continue
                want = sum(sym[t] if t in sym else int(t, 0) for t in terms) & 0xFFFFFFFF
            else:
                want = e["value"]
            if got != want:
                bad.append(f"startup: pool 0x{e['offset']:x} is 0x{got:08x}, expected 0x{want:08x} ({e['value']})")
        if len(bad) == n0:
            startup = (f"; startup checked ({len(code)} B of instructions, {len(man['pool'])} pool words, "
                       f"entry 0x{man['entry']:x}" + (" with the watchdog" if out and out > man["entry"] else "") + ")")
    print(f"{a.bin}: {len(img)} B; {len(loaded)} loaded sections; {counts['code']} code halfwords "
          f"(re-encoded, {counts['rewritten']} rewritten), {counts['data']} data halfwords (unchanged); "
          f"{len(bad)} mismatch(es); {data_copy}{blob}{startup}")
    for b in bad[:40]:
        print("  " + b)
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())

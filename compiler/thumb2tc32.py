#!/usr/bin/env python3
"""Re-encode a Thumb-1 (ARMv4T) ELF as TC32 machine code.

TC32 executes the Thumb-1 instruction set with the top five bits of each
16-bit instruction assigned differently (tc32isa.TOP, checked against Telink's
disassembler on every value, checks/isa_check.py); the other eleven bits, and
so every register, immediate and branch field, are the same.
Telink's own toolchain works this way: a Thumb compiler, and an assembler
that writes TC32 opcodes. This tool does the last step for a linked ELF from
any Thumb-1 compiler.

The ELF's mapping symbols tell code ($t) from data ($d: literal pools, jump
tables, the boot header); only code is re-encoded. Data objects (STT_OBJECT
symbols with a size) inside executable sections are left alone too. Input
sections without mapping symbols (.rodata) must not be linked into an
executable output section: the tool cannot tell them from code there (the
Zephyr linker scripts and thumb.ld keep them apart). The tool refuses what
TC32 cannot run or would read differently:
- ARM code ($a): TC32 has no ARM state;
- BLX (ARMv5T), and 32-bit Thumb-2 instructions other than a BL pair.
Thumb encodings where TC32 has its own instructions (0xb800-0xb9ff treti,
0xbbc0-0xbbdf tmcsr/tmrcs/tmssr/tmrss) are undefined in ARMv4T, so no
compiler emits them; tc32asm2thumb.py writes them as .inst.n, and they are
re-encoded like the rest (counted in the report).
Two Thumb instructions are written as others, so that the image uses only
instruction forms Telink's own code uses (forms_check.py):
- lsls rd, rm, #0 (how LLVM moves a low register to another on ARMv4T) as
  adds rd, rm, #0 (how Telink's compiler does). They differ in C and V only:
  lsls #0 keeps them, adds #0 clears them. LLVM emits this movs only where
  the flags are dead (Thumb1InstrInfo::copyPhysReg); hand-written assembly
  must not rely on movs keeping C or V;
- udf (0xdexx; LLVM's trap, which Zephyr's CODE_UNREACHABLE becomes with
  clang) as b . (a branch to itself): TC32 has no known undefined
  instruction, and a loop in place is what the watchdog then resets.
Addresses of Thumb code that the link writes as data (function pointers)
keep bit 0 set, as Telink's toolchain writes them too (its linker stores
twice() at 0 as 0x00000001, and calls through a pointer with tjex).
A halfword 0x0000 in code is left as it is: it is fill from .org or .space,
which the assembler marks as code when code precedes it (as Telink's
assembler, it writes zeros there). As an instruction it would be lsls r0, r0,
#0, which LLVM never emits.

Usage: thumb2tc32.py <in.elf> <out.bin>   (the flat image from address 0, as
llvm-objcopy -O binary writes it, with code re-encoded)

SPDX-License-Identifier: Apache-2.0
"""
import os
import struct
import sys

sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "common"))
from tc32isa import INV, to_tc32  # noqa: E402,F401

SHF_ALLOC, SHF_EXECINSTR = 0x2, 0x4
SHT_SYMTAB, SHT_NOBITS = 2, 8


class Refused(Exception):
    pass


def rewrite(hw):
    """(Thumb halfword as written, what was rewritten or None): lsls #0 and udf
    (see the module docstring); 0x0000 stays 0x0000."""
    if hw == 0x0000:                           # .org/.space fill, not lsls r0, r0, #0
        return hw, "zero"
    if hw & 0xFFC0 == 0x0000:                  # lsls rd, rm, #0 -> adds rd, rm, #0
        return 0x1C00 | (hw & 0x3F), "movs"
    if hw & 0xFF00 == 0xDE00:                  # udf -> b .
        return 0xE7FE, "udf"
    return hw, None


def encode(hw):
    """The TC32 halfword thumb2tc32 writes for a Thumb code halfword (not the
    first half of a BL pair)."""
    hw, kind = rewrite(hw)
    return hw if kind == "zero" else to_tc32(hw)


def read_elf(data):
    if data[:4] != b"\x7fELF" or data[4] != 1 or data[5] != 1:
        raise Refused("not a little-endian ELF32 file")
    e_shoff, = struct.unpack_from("<I", data, 0x20)
    e_shentsize, e_shnum, e_shstrndx = struct.unpack_from("<HHH", data, 0x2E)
    secs = []
    for i in range(e_shnum):
        name, typ, flags, addr, off, size, link, info, align, entsize = struct.unpack_from(
            "<IIIIIIIIII", data, e_shoff + i * e_shentsize)
        secs.append(dict(name=name, type=typ, flags=flags, addr=addr, off=off, size=size, link=link,
                         info=info, entsize=entsize))
    shstr = secs[e_shstrndx]
    for s in secs:
        end = data.index(b"\0", shstr["off"] + s["name"])
        s["name"] = data[shstr["off"] + s["name"]:end].decode()
    return secs


def mapping_symbols(data, secs):
    """{section index: sorted [(address, kind)]} for $t, $d and $a, and
    {section index: [(start, end)]} of data objects."""
    out, objects = {}, {}
    for s in secs:
        if s["type"] != SHT_SYMTAB:
            continue
        strtab = secs[s["link"]]
        for i in range(s["size"] // 16):
            name, value, size, info, other, shndx = struct.unpack_from("<IIIBBH", data, s["off"] + i * 16)
            end = data.index(b"\0", strtab["off"] + name)
            n = data[strtab["off"] + name:end].decode()
            if n[:2] in ("$t", "$d", "$a") and (len(n) == 2 or n[2] == "."):
                out.setdefault(shndx, []).append((value & ~1, n[1]))
            elif info & 0xF == 1 and size:      # STT_OBJECT
                objects.setdefault(shndx, []).append((value, value + size))
    for v in out.values():
        v.sort()
    return out, objects


def convert(elf_path):
    data = open(elf_path, "rb").read()
    secs = read_elf(data)
    maps, objects = mapping_symbols(data, secs)
    loaded = [s for s in secs if s["flags"] & SHF_ALLOC and s["type"] != SHT_NOBITS and s["size"]]
    # The flash image: sections at flash addresses (below the SRAM), as in the
    # linker scripts used here (text at 0; .data is loaded from its LMA by the
    # start code, and objcopy places it at the LMA: take the file's program
    # headers for that).
    image = bytearray()
    segments = []
    phoff, = struct.unpack_from("<I", data, 0x1C)
    phentsize, phnum = struct.unpack_from("<HH", data, 0x2A)
    for i in range(phnum):
        ptype, off, vaddr, paddr, filesz, memsz, flags, align = struct.unpack_from(
            "<IIIIIIII", data, phoff + i * phentsize)
        if ptype == 1 and filesz:
            if len(image) < paddr + filesz:
                image.extend(b"\xff" * (paddr + filesz - len(image)))
            image[paddr:paddr + filesz] = data[off:off + filesz]
            segments.append((vaddr, paddr, filesz))

    def at(vaddr):
        """Image offset of a run-time address (code copied to SRAM runs at
        its VMA and is stored at its LMA)."""
        for v, p, n in segments:
            if v <= vaddr < v + n:
                return p + vaddr - v
        raise Refused(f"0x{vaddr:x} is in no loaded segment")
    counts = {"code": 0, "data": 0, "movs": 0, "udf": 0, "zero": 0, "tc32only": 0}
    for idx, s in enumerate(secs):
        if s not in loaded or not s["flags"] & SHF_EXECINSTR:
            continue
        marks = maps.get(idx) or [(s["addr"], "t")]
        if marks[0][0] > s["addr"]:
            raise Refused(f"{s['name']}: no mapping symbol at its start")
        for j, (start, kind) in enumerate(marks):
            end = marks[j + 1][0] if j + 1 < len(marks) else s["addr"] + s["size"]
            if kind == "a":
                raise Refused(f"ARM code in {s['name']} at 0x{start:x}: TC32 has no ARM state")
            if kind == "d":
                counts["data"] += end - start
                continue
            a = start
            while a < end:
                inside = [o for o in objects.get(idx, ()) if o[0] <= a < o[1]]
                if inside:
                    counts["data"] += min(inside[0][1], end) - a
                    a = min(inside[0][1], end)
                    continue
                hw, = struct.unpack_from("<H", image, at(a))
                top = hw >> 11
                if hw & 0xFE00 == 0xB800 or 0xBBC0 <= hw <= 0xBBDF:
                    counts["tc32only"] += 1   # .inst.n from tc32asm2thumb.py: treti, tmcsr/tmrcs/tmssr/tmrss
                if hw & 0xFF87 == 0x4780:
                    raise Refused(f"0x{a:x}: blx (ARMv5T)")
                if top == 0x1E:            # BL prefix: its suffix must follow
                    nxt, = struct.unpack_from("<H", image, at(a) + 2)
                    if nxt >> 11 != 0x1F:
                        raise Refused(f"0x{a:x}: BL prefix 0x{hw:04x} not followed by a BL suffix (0x{nxt:04x})")
                    struct.pack_into("<HH", image, at(a), to_tc32(hw), to_tc32(nxt))
                    a += 4
                    counts["code"] += 2
                    continue
                if top in (0x1D, 0x1F):
                    raise Refused(f"0x{a:x}: 0x{hw:04x} is a 32-bit Thumb-2 or lone BL half")
                hw, kind = rewrite(hw)
                if kind:
                    counts[kind] += 1
                struct.pack_into("<H", image, at(a), hw if kind == "zero" else to_tc32(hw))
                a += 2
                counts["code"] += 1
    return image, counts


def main():
    if len(sys.argv) != 3:
        sys.exit(__doc__)
    try:
        image, counts = convert(sys.argv[1])
    except Refused as e:
        sys.exit(f"thumb2tc32: {sys.argv[1]}: {e}")
    open(sys.argv[2], "wb").write(image)
    print(f"{sys.argv[2]}: {len(image)} bytes, {counts['code']} instructions re-encoded "
          f"({counts['tc32only']} TC32-only, {counts['movs']} lsls #0 as adds #0, {counts['udf']} udf as b ., "
          f"{counts['zero']} zero fill kept), "
          f"{counts['data']} bytes of data in code sections left as they are")


if __name__ == "__main__":
    main()

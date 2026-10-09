#!/usr/bin/env python3
"""Write the flash image of a linked TC32 ELF: the direct path.

On the direct path the compiler and the assembler write TC32 machine code
themselves (llvm-tc32, clang -mcpu=tc32 -mthumb) and ld.lld links it, so the
ELF already holds the image's bytes. This tool lays them out as
thumb2tc32.py lays out the image of the Thumb path: each PT_LOAD segment's
file bytes at its physical address (code copied to SRAM is stored at its
LMA), 0xff (erased flash) between segments, the image starting at address 0.
Nothing is re-encoded, so for the same sources the two paths give the same
image (path_diff.py compares them).

The ELF must name the core tc32 (Tag_CPU_name, which clang writes for C and
assembly built with -mcpu=tc32 and ld.lld keeps): an ELF built for an ARM
core holds Thumb's encoding, which thumb2tc32.py re-encodes. An ELF without
the tag is refused too, as nothing then tells which encoding its code is in.

Usage: elf2bin.py <in.elf> <out.bin>

SPDX-License-Identifier: Apache-2.0
"""
import os
import struct
import sys

sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "common"))
import armattr  # noqa: E402


class Refused(Exception):
    pass


def convert(elf_path):
    """(image, number of PT_LOAD segments written)."""
    data = open(elf_path, "rb").read()
    try:
        cpu = armattr.cpu_name(data) or ""
    except ValueError as e:
        raise Refused(str(e))
    if cpu.lower() != "tc32":
        raise Refused(f"Tag_CPU_name is {cpu!r}, not 'tc32': not built with -mcpu=tc32 "
                      "(a Thumb ELF goes through thumb2tc32.py)")
    phoff, = struct.unpack_from("<I", data, 0x1C)
    phentsize, phnum = struct.unpack_from("<HH", data, 0x2A)
    image = bytearray()
    n = 0
    for i in range(phnum):
        ptype, off, _vaddr, paddr, filesz, _memsz, _flags, _align = struct.unpack_from(
            "<IIIIIIII", data, phoff + i * phentsize)
        if ptype == 1 and filesz:
            if len(image) < paddr + filesz:
                image.extend(b"\xff" * (paddr + filesz - len(image)))
            image[paddr:paddr + filesz] = data[off:off + filesz]
            n += 1
    if not n:
        raise Refused("no PT_LOAD segment with file bytes")
    return image, n


def main():
    if len(sys.argv) != 3:
        sys.exit(__doc__)
    try:
        image, n = convert(sys.argv[1])
    except Refused as e:
        sys.exit(f"elf2bin: {sys.argv[1]}: {e}")
    open(sys.argv[2], "wb").write(image)
    print(f"{sys.argv[2]}: {len(image)} bytes from {n} PT_LOAD segment(s), TC32 code as linked")


if __name__ == "__main__":
    main()

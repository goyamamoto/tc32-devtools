#!/usr/bin/env python3
"""Check tc32asm2thumb.py against Telink's assembler.

For each assembly file of a build, the file is preprocessed with the build's
own command. Two assemblers then build it:
- the original goes through Telink's tc32-elf-as (in Docker, as in isa_check.py);
- the translation goes through a mainstream clang for ARMv4T Thumb, and its
  code is re-encoded as thumb2tc32.py does.

Every section must then hold the same bytes. Places with a relocation are
left out: their bytes are filled in at link time. One difference is allowed:
alignment fill in code, which Telink's assembler writes as the Thumb nop
0x46c0 (TC32 strb r0, [r0, #27]) and thumb2tc32.py as TC32's tnop 0x06c0.

Usage: asm_check.py <build dir> <file.S>... [--telink <dir>] [--clang <path>]

SPDX-License-Identifier: Apache-2.0
"""
import argparse
import json
import os
import re
import shlex
import struct
import subprocess
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
import tc32asm2thumb  # noqa: E402
import thumb2tc32  # noqa: E402
sys.path.insert(0, os.path.join(os.path.dirname(HERE), "common"))
import toolchain  # noqa: E402

REL_SIZE = {2: 4, 3: 4, 10: 4, 30: 4, 102: 2, 103: 2, 11: 2}   # R_ARM_ABS32, REL32, THM_CALL, THM_JUMP24, JUMP11, JUMP8, THM_PC8


def sections(path):
    """{name: (bytes, flags)} and {name: {offset: type}} of a relocatable ELF,
    and {name: sorted [(offset, kind)]} of its mapping symbols."""
    data = open(path, "rb").read()
    shoff, = struct.unpack_from("<I", data, 0x20)
    shentsize, shnum, shstrndx = struct.unpack_from("<HHH", data, 0x2E)
    secs = [struct.unpack_from("<IIIIIIIIII", data, shoff + i * shentsize) for i in range(shnum)]
    stroff = secs[shstrndx][4]
    name = lambda s: data[stroff + s[0]:data.index(b"\0", stroff + s[0])].decode()  # noqa: E731
    out, rels, maps = {}, {}, {}
    for s in secs:
        if s[2] & 0x2 and s[1] != 8 and s[5]:                  # SHF_ALLOC, not NOBITS
            out[name(s)] = (data[s[4]:s[4] + s[5]], s[2])
    for s in secs:
        if s[1] in (9, 4):                                     # SHT_REL, SHT_RELA
            target = name(secs[s[7]])
            ent = 8 if s[1] == 9 else 12
            for i in range(s[5] // ent):
                off, info = struct.unpack_from("<II", data, s[4] + i * ent)
                rels.setdefault(target, {})[off] = info & 0xFF
        if s[1] == 2:
            strtab = secs[s[6]]
            for i in range(s[5] // 16):
                nm, value, _size, _info, _o, shndx = struct.unpack_from("<IIIBBH", data, s[4] + i * 16)
                n = data[strtab[4] + nm:data.index(b"\0", strtab[4] + nm)].decode()
                if n[:2] in ("$t", "$d") and shndx < len(secs):
                    maps.setdefault(name(secs[shndx]), []).append((value & ~1, n[1]))
    for v in maps.values():
        v.sort()
    return out, rels, maps


def preprocess(entry, src, out):
    args = shlex.split(entry["command"]) if "command" in entry else entry["arguments"]
    keep, skip = [args[0]], False
    for i, x in enumerate(args[1:], 1):
        if skip:
            keep.append(x)
            skip = False
        elif x in ("-target", "-imacros", "-include", "-isystem", "-I", "-D"):
            keep.append(x)
            skip = True
        elif x.startswith(("-D", "-I", "--target", "-isystem")):
            keep.append(x)
    r = subprocess.run(keep + ["-E", "-P", "-x", "assembler-with-cpp", src, "-o", out], cwd=entry["directory"],
                       capture_output=True, text=True)
    if r.returncode:
        raise SystemExit(f"preprocess {src}: {r.stderr[-400:]}")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("build")
    ap.add_argument("files", nargs="+")
    ap.add_argument("--telink", default=os.environ.get("TELINK_TC32", ""))
    ap.add_argument("--clang", default=toolchain.tool("clang"))
    a = ap.parse_args()
    telink = os.path.realpath(a.telink)
    db = {os.path.realpath(e["file"]): e for e in json.load(open(os.path.join(a.build, "compile_commands.json")))}
    work = os.path.realpath(tempfile.mkdtemp(prefix="asm_check_"))
    bad = 0
    for f in a.files:
        f = os.path.realpath(f)
        base = os.path.join(work, os.path.basename(f)[:-2])
        trans = base + ".thumb.S"
        with open(trans, "w") as out:
            out.write("\t.syntax unified\n\t.thumb\n")
            out.writelines(tc32asm2thumb.translate(open(f).readlines()))
        preprocess(db[f], f, base + ".tc32.s")
        # The port writes nop; Telink's assembler calls it tnop (also after a label, as in "1: nop").
        text = open(base + ".tc32.s").read()
        open(base + ".tc32.s", "w").write(re.sub(r"^(\s*(?:[\w.$]+:\s*)*)nop\b", r"\1tnop", text, flags=re.M))
        preprocess(db[f], trans, base + ".thumb.s")
        r = subprocess.run(["docker", "run", "--rm", "--platform", "linux/amd64", "--network", "none",
                            "-v", f"{telink}:{telink}", "-v", f"{work}:{work}", "debian:bookworm-slim",
                            f"{telink}/bin/tc32-elf-as", base + ".tc32.s", "-o", base + ".tc32.o"],
                           capture_output=True, text=True)
        if r.returncode:
            print(f"{f}: Telink as failed: {r.stderr[-600:]}")
            bad += 1
            continue
        r = subprocess.run([a.clang, "--target=thumbv4t-none-eabi", "-mcpu=arm7tdmi", "-c", "-x", "assembler",
                            base + ".thumb.s", "-o", base + ".thumb.o"], capture_output=True, text=True)
        if r.returncode:
            print(f"{f}: clang failed: {r.stderr[-600:]}")
            bad += 1
            continue
        tsec, trel, _ = sections(base + ".tc32.o")
        lsec, lrel, lmaps = sections(base + ".thumb.o")
        n = mism = padding = 0
        for name in sorted(set(tsec) | set(lsec)):
            if name not in tsec or name not in lsec:
                print(f"{os.path.basename(f)}: section {name} only in {'Telink' if name in tsec else 'clang'}")
                mism += 1
                continue
            tb, lb = tsec[name][0], bytearray(lsec[name][0])
            if len(tb) != len(lb):
                longer, extra = ("Telink", tb[len(lb):]) if len(tb) > len(lb) else ("clang", lb[len(tb):])
                print(f"{os.path.basename(f)}: {name}: {len(tb)} bytes (Telink) vs {len(lb)} (clang); "
                      f"{longer} ends with {extra.hex()}")
                if extra.strip(b"\0") and extra not in (b"\xc0\x46", b"\xc0\x06"):
                    mism += 1
                else:
                    padding += 1
                tb, lb = tb[:min(len(tb), len(lb))], lb[:min(len(tb), len(lb))]
            masked = set()
            for off, typ in list(trel.get(name, {}).items()) + list(lrel.get(name, {}).items()):
                masked |= set(range(off, off + REL_SIZE.get(lrel.get(name, {}).get(off, typ), 2)))
            marks = lmaps.get(name, [(0, "t")] if tsec[name][1] & 0x4 else [(0, "d")])
            for i in range(0, len(lb) - 1, 2):
                kind = [k for o, k in marks if o <= i]
                if kind and kind[-1] == "t":
                    hw, = struct.unpack_from("<H", lb, i)
                    struct.pack_into("<H", lb, i, thumb2tc32.encode(hw))
            for i in range(0, len(lb) - 1, 2):
                if i in masked or i + 1 in masked:
                    continue
                n += 1
                if tb[i:i + 2] == b"\xc0\x46" and lb[i:i + 2] == b"\xc0\x06":
                    padding += 1        # alignment fill: Telink's as writes the Thumb nop, thumb2tc32 tnop
                elif tb[i:i + 2] != lb[i:i + 2]:
                    mism += 1
                    if mism <= 8:
                        print(f"{os.path.basename(f)}: {name}+0x{i:x}: Telink {tb[i:i + 2].hex()} clang {lb[i:i + 2].hex()}")
        print(f"{os.path.basename(f)}: {n} halfwords compared, {mism} mismatch(es), "
              f"{padding} alignment fill(s) (Telink's Thumb nop 0x46c0, ours tnop 0x06c0)")
        bad += mism
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())

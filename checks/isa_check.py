#!/usr/bin/env python3
"""Check tc32emu's TC32 encoding (TOP, the TC32-only instructions) against
Telink's own disassembler, independently of the LLVM TC32 toolchain.

Every 16-bit value is written as TC32 (followed by a tnop) and, re-encoded
with tc32emu.to_thumb, as Thumb; Telink's tc32-elf-objdump disassembles the
first and a mainstream llvm-objdump (ARMv4T) the second. Every value must
decode to the same instruction with the same operands in both, except:
- values that Telink's objdump decodes and ARMv4T leaves undefined: they must
  be the TC32-only instructions (tmcsr/tmrcs/tmssr/tmrss 0x6bc0-0x6bdf, treti
  0x6800-0x69ff), or Thumb encodings ARMv4T leaves UNPREDICTABLE (empty
  register lists, bx with bit 7 set), which tc32emu refuses to run;
- values that llvm-objdump decodes and Telink's does not: ARMv5/v6 additions
  (bkpt, cpsid/cpsie, setend) and the permanently undefined udf space.
BL is checked separately on 65536 prefix/suffix pairs (targets must agree).

Telink's tc32-elf-objdump is an x86-64 Linux binary (Telink TC32 version 2.01
build, binutils 2.20; GPL, used here as a tool only). --objdump-cmd gives the
command that runs it; "{in}" is replaced by the input file's name inside
--work, and the command must print the disassembly on stdout, e.g.
  --objdump-cmd 'docker run --rm --platform linux/amd64 --network none
     -v <dir with tc32-elf-objdump>:/t -v {work}:/w debian:bookworm-slim
     /t/tc32-elf-objdump -D -b binary -m tc32 /w/{in}'

Usage: isa_check.py --objdump-cmd '<command>' [--work <dir>] [--llvm-objdump <path>]
                    [--swap-top i,j   (negative control: must report mismatches)]

SPDX-License-Identifier: Apache-2.0
"""
import argparse
import collections
import os
import random
import re
import struct
import subprocess
import sys
import tempfile

sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "emulator"))
import tc32emu as te  # noqa: E402

INV = [0] * 32
for _tc, _th in enumerate(te.TOP):
    INV[_th] = _tc
NOP_THUMB = 0x46C0                       # mov r8, r8
MN = {"tadd": "add", "taddcs": "adcs", "tadds": "adds", "tands": "ands", "tasrs": "asrs", "tbclrs": "bics",
      "tcmp": "cmp", "tcmpn": "cmn", "tj": "b", "tjcc": "blo", "tjcs": "bhs", "tjeq": "beq", "tjex": "bx",
      "tjge": "bge", "tjgt": "bgt", "tjhi": "bhi", "tjle": "ble", "tjls": "bls", "tjlt": "blt", "tjmi": "bmi",
      "tjne": "bne", "tjpl": "bpl", "tjvc": "bvc", "tjvs": "bvs", "tloadm": "ldm", "tloadr": "ldr",
      "tloadrb": "ldrb", "tloadrh": "ldrh", "tloadrsb": "ldrsb", "tloadrsh": "ldrsh", "tmov": "mov",
      "tmovns": "mvns", "tmovs": "movs", "tmuls": "muls", "tnand": "tst", "tnegs": "rsbs", "tnop": "mov",
      "tors": "orrs", "tpop": "pop", "tpush": "push", "trotrs": "rors", "tserv": "svc", "tshftls": "lsls",
      "tshftrs": "lsrs", "tstorem": "stm", "tstorer": "str", "tstorerb": "strb", "tstorerh": "strh",
      "tsub": "sub", "tsubcs": "sbcs", "tsubs": "subs", "txors": "eors"}
# Values only Telink's objdump decodes: the TC32-only instructions, and
# ARMv4T-UNPREDICTABLE encodings (tc32emu stops on them).
TC32_ONLY = {"tmcsr": 8, "tmrcs": 8, "tmssr": 8, "tmrss": 8, "treti": 512}
UNPREDICTABLE_OK = {"tjex", "tpush", "tpop", "tloadm", "tstorem"}
THUMB_ONLY = {"bkpt", "cpsid", "cpsie", "setend", "udf", "trap", "__brkdiv0"}
LINE = re.compile(r"^\s*([0-9a-f]+):\s+((?:[0-9a-f]{4} ?){1,2})\s+(.*)")


def to_tc32(hw):
    return (INV[hw >> 11] << 11) | (hw & 0x7FF)


def parse(text):
    out = {}
    for line in text.splitlines():
        m = LINE.match(line)
        if m:
            body = m.group(3).strip()
            mn, _, ops = body.partition("\t") if "\t" in body else body.partition(" ")
            out[int(m.group(1), 16)] = (mn.strip(), ops.strip())
    return out


def num(s):
    return str(int(s, 0))


def canon(ops):
    ops = re.sub(r"#(-?(?:0x[0-9a-f]+|\d+))", lambda m: "#" + num(m.group(1)), ops)
    ops = re.sub(r",\s*#0\]", "]", ops)
    ops = re.sub(r"\s+", "", ops)
    for gnu, llvm in (("sl", "r10"), ("fp", "r11"), ("ip", "r12")):
        ops = re.sub(r"\b%s\b" % gnu, llvm, ops)
    return re.sub(r"^(\d+|0x[0-9a-f]+)$", lambda m: "#" + num(m.group(1)), ops)


def norm_telink(mn, ops, a):
    if mn == "tnop":
        return "r8,r8"
    target = re.search(r"\(tadd r\d, (0x[0-9a-f]+)\)", ops)
    ops = ops.split("\t;")[0].split(";")[0]
    if mn == "tadd" and target:
        reg, _, imm = ops.split(",", 2)
        if int(target.group(1), 16) != ((a + 4) & ~3) + int(imm.strip()[1:]):
            return "target " + target.group(1)        # not Align(pc + 4, 4) + imm: a mismatch
        return reg + "," + imm
    if mn == "tnegs":
        ops += ", #0"
    if mn == "tmuls":
        ops += ", " + ops.split(",")[0]
    if mn.startswith("tj") and mn != "tjex":
        return "@" + num(ops)
    return ops


def norm_llvm(mn, ops):
    if mn == "adr":
        return ops.split("<")[0]
    if mn.startswith("b") and mn not in ("bx", "bics"):
        return "@" + num(ops.split()[0])
    ops = ops.split("@")[0].strip()
    m = re.match(r"(\w+),\s*sp,\s*(\w+)$", ops)            # add rd, sp, rd = add rd, sp
    if mn == "add" and m and m.group(1) == m.group(2):
        ops = m.group(1) + ", sp"
    return ops


def run_telink(cmd, work, name):
    r = subprocess.run(cmd.replace("{in}", name).replace("{work}", work), shell=True, capture_output=True,
                       text=True, timeout=1800)
    if r.returncode or "Disassembly" not in r.stdout:
        sys.exit(f"Telink objdump failed: {r.stderr[-400:]}")
    return parse(r.stdout)


def run_llvm(objdump, work, name, halfwords):
    src, obj = os.path.join(work, name + ".s"), os.path.join(work, name + ".o")
    with open(src, "w") as f:
        f.write(".syntax unified\n.thumb\n.text\n")
        f.writelines(".inst.n 0x%04x\n" % h for h in halfwords)
    subprocess.run(["clang", "--target=thumbv4t-none-eabi", "-mcpu=arm7tdmi", "-c", src, "-o", obj], check=True)
    r = subprocess.run([objdump, "-d", "--triple=thumbv4t-none-eabi", "--mcpu=arm7tdmi", obj],
                       capture_output=True, text=True, check=True)
    return parse(r.stdout)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--objdump-cmd", required=True)
    ap.add_argument("--work")
    ap.add_argument("--llvm-objdump")
    ap.add_argument("--swap-top", help="negative control: swap two TOP entries, e.g. 0,1 (must fail)")
    a = ap.parse_args()
    if a.swap_top:
        i, j = (int(x, 0) for x in a.swap_top.split(","))
        te.TOP[i], te.TOP[j] = te.TOP[j], te.TOP[i]
        INV[te.TOP[i]], INV[te.TOP[j]] = i, j
    work = a.work or tempfile.mkdtemp(prefix="isa_check_")
    llvm = a.llvm_objdump or subprocess.run(["xcrun", "--find", "llvm-objdump"], capture_output=True,
                                            text=True).stdout.strip() or "llvm-objdump"
    bad = []

    # All 16-bit values.
    thumb = [x for hw in range(65536) for x in (te.to_thumb(hw), NOP_THUMB)]
    with open(os.path.join(work, "all_tc32.bin"), "wb") as f:
        f.write(b"".join(struct.pack("<HH", hw, to_tc32(NOP_THUMB)) for hw in range(65536)))
    tl = run_telink(a.objdump_cmd, work, "all_tc32.bin")
    ll = run_llvm(llvm, work, "all_thumb", thumb)
    counts = collections.Counter()
    for hw in range(65536):
        addr = hw * 4
        (tm, tops), (lm, lops) = tl[addr], ll[addr]
        t_undef, l_undef = tm == "undefined", lm == "<unknown>"
        if t_undef and l_undef:
            counts["undefined in both"] += 1
        elif l_undef:
            counts["TC32 only: " + tm] += 1
            if tm not in TC32_ONLY and tm not in UNPREDICTABLE_OK:
                bad.append(f"0x{hw:04x}: Telink {tm} {tops}, ARMv4T undefined")
        elif t_undef:
            counts["ARMv4T only: " + lm] += 1
            if lm not in THUMB_ONLY:
                bad.append(f"0x{hw:04x}: Telink undefined, ARMv4T {lm} {lops}")
        elif MN.get(tm) != lm and not (tm == "tadd" and lm == "adr"):
            bad.append(f"0x{hw:04x}: Telink {tm} {tops}, ARMv4T {lm} {lops}")
        else:
            x, y = canon(norm_telink(tm, tops, addr)), canon(norm_llvm(lm, lops))
            if lm == "ldm" and "!" not in y:
                y = y.replace(",{", "!,{", 1)        # GNU prints the writeback bang even with the base listed
            if x == y:
                counts["same instruction and operands"] += 1
            else:
                bad.append(f"0x{hw:04x}: Telink {tm} {tops} ({x}), ARMv4T {lm} {lops} ({y})")
    for name, n in TC32_ONLY.items():
        if counts["TC32 only: " + name] != n:
            bad.append(f"{name}: {counts['TC32 only: ' + name]} values, expected {n}")
    emu_only = [hw for hw in range(65536) if hw & 0xFFE0 == 0x6BC0 or hw & 0xFE00 == 0x6800]
    for hw in emu_only:
        if tl[hw * 4][0] not in TC32_ONLY:
            bad.append(f"0x{hw:04x}: tc32emu runs it as TC32-only, Telink says {tl[hw * 4]}")

    # BL pairs: every prefix with some suffixes, every suffix with some prefixes.
    rnd = random.Random(1)
    some = [0, 1, 0x3FF, 0x400, 0x7FF] + rnd.sample(range(0x800), 11)
    pairs = [(p, s) for p in range(0x800) for s in some] + [(p, s) for s in range(0x800) for p in some]
    thumb = [x for p, s in pairs for x in (0xF000 | p, 0xF800 | s, NOP_THUMB, NOP_THUMB)]
    with open(os.path.join(work, "bl_tc32.bin"), "wb") as f:
        f.write(b"".join(struct.pack("<H", to_tc32(h)) for h in thumb))
    tl = run_telink(a.objdump_cmd, work, "bl_tc32.bin")
    ll = run_llvm(llvm, work, "bl_thumb", thumb)
    for i in range(len(pairs)):
        (tm, tops), (lm, lops) = tl[i * 8], ll[i * 8]
        if tm == "tjl" and lm == "bl" and int(tops, 16) == int(lops.split()[0], 16):
            counts["bl pairs with the same target"] += 1
        else:
            bad.append(f"bl pair {pairs[i]}: Telink {tm} {tops}, ARMv4T {lm} {lops}")

    for k in sorted(counts):
        print(f"{counts[k]:6}  {k}")
    for b in bad[:40]:
        print("MISMATCH", b)
    print(f"\n{len(bad)} mismatch(es)")
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())

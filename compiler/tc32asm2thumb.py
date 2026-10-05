#!/usr/bin/env python3
"""Translate TC32 assembly (Telink's syntax, as tc32-elf-as reads it) into
ARMv4T Thumb assembly for a mainstream assembler; thumb2tc32.py turns the
linked result back into TC32 machine code.

Only instruction lines change; directives, labels, comments and preprocessor
lines stay as they are, so the file is still preprocessed as before. Each
TC32 mnemonic becomes the Thumb instruction with the same encoding under the
permutation (isa_check.py):
- tmov rd, #imm -> movs; tmov between two low registers -> adds rd, rm, #0
  (how Telink's compiler moves a register); other tmov -> mov;
- tadd/tsub: with sp or pc, or two high/low register operands, the add/sub
  that does not set flags; otherwise adds/subs;
- tloadr/tstorer and the byte, halfword and signed forms, tloadm/tstorem ->
  ldr/str..., ldm/stm; tj, tjl, tjex, tj<cc> -> b, bl, bx, b<cc>;
- the ALU operations -> their flag-setting Thumb names (tbclr -> bics,
  tnand -> tst, tneg rd, rm -> rsbs rd, rm, #0, ...);
- the TC32-only instructions become .inst.n with the Thumb value
  thumb2tc32.py maps back to them (tmcsr rN -> 0xbbc0 + N, tmrcs 0xbbc8,
  tmssr 0xbbd0, tmrss 0xbbd8, treti {list} -> 0xb800 + list, + 0x100 for pc).
check_translation() in this file compares, instruction by instruction, what
Telink's assembler makes of the original with what clang and thumb2tc32 make
of the translation.

Usage: tc32asm2thumb.py <in.S> <out.S>

SPDX-License-Identifier: Apache-2.0
"""
import re
import sys

LOW = {f"r{i}" for i in range(8)}
SP_PC = {"sp", "pc", "r13", "r15"}
CC = "eq ne cs cc hs lo mi pl vs vc hi ls ge lt gt le".split()
SIMPLE = {"tloadr": "ldr", "tstorer": "str", "tloadrb": "ldrb", "tstorerb": "strb", "tloadrh": "ldrh",
          "tstorerh": "strh", "tloadrsb": "ldrsb", "tloadrsh": "ldrsh", "tloadm": "ldm", "tstorem": "stm",
          "tj": "b", "tjl": "bl", "tjex": "bx", "tpush": "push", "tpop": "pop", "tcmp": "cmp", "tcmpn": "cmn",
          "tand": "ands", "tor": "orrs", "txor": "eors", "tbclr": "bics", "tmovn": "mvns", "tmul": "muls",
          "taddc": "adcs", "tsubc": "sbcs", "trotr": "rors", "tnand": "tst", "tshftl": "lsls",
          "tshftr": "lsrs", "tasr": "asrs", "nop": "nop"}
SYSREG = {"tmcsr": 0xBBC0, "tmrcs": 0xBBC8, "tmssr": 0xBBD0, "tmrss": 0xBBD8}
INSN = re.compile(r"^(\s*)(?:([.\w$]+:)(\s*))?(t[a-z]+|nop)\b(.*)$")


class Untranslatable(Exception):
    pass


def split_ops(text):
    ops, depth, cur = [], 0, ""
    for ch in text:
        if ch in "[{":
            depth += 1
        elif ch in "]}":
            depth -= 1
        if ch == "," and depth == 0:
            ops.append(cur.strip())
            cur = ""
        else:
            cur += ch
    if cur.strip():
        ops.append(cur.strip())
    return ops


def reg_list(text):
    """Register numbers of a {...} list (r0-r7 ranges, lr, pc, r14, r15)."""
    names = {"sp": 13, "lr": 14, "pc": 15}
    out = set()
    for part in text.strip("{} ").split(","):
        part = part.strip()
        if "-" in part:
            a, b = (int(x.strip()[1:]) for x in part.split("-"))
            out |= set(range(a, b + 1))
        elif part:
            out.add(names.get(part, None) if part in names else int(part[1:]))
    return out


def translate_insn(mn, ops_text):
    ops = split_ops(ops_text)
    if mn in SYSREG:
        return f".inst.n 0x{SYSREG[mn] | int(ops[0][1:]):04x}"
    if mn == "treti":
        regs = reg_list(ops[0])
        if regs - set(range(8)) - {15}:
            raise Untranslatable(f"treti {ops[0]}")
        return f".inst.n 0x{0xB800 | (0x100 if 15 in regs else 0) | sum(1 << r for r in regs if r < 8):04x}"
    if mn == "tmov":
        a, b = ops
        if b.startswith("#"):
            return f"movs {a}, {b}"
        if a in LOW and b in LOW:
            return f"adds {a}, {b}, #0"
        return f"mov {a}, {b}"
    if mn in ("tadd", "tsub"):
        s = "add" if mn == "tadd" else "sub"
        if len(ops) == 3:
            a, b, c = ops
            if c.startswith("#"):
                return f"{s} {a}, {b}, {c}" if b in SP_PC else f"{s}s {a}, {b}, {c}"
            if {a, b, c} <= LOW:
                return f"{s}s {a}, {b}, {c}"
            raise Untranslatable(f"{mn} {ops_text.strip()}")
        a, b = ops
        if b.startswith("#"):
            return f"{s} {a}, {b}" if a in SP_PC else f"{s}s {a}, {b}"
        if mn == "tadd" and not (a in LOW and b in LOW):
            return f"add {a}, {b}"
        raise Untranslatable(f"{mn} {ops_text.strip()}")
    if mn == "tneg":
        return f"rsbs {ops[0]}, {ops[1]}, #0"
    if mn.startswith("tj") and mn[2:] in CC:
        return f"b{mn[2:]} {ops_text.strip()}"
    if mn in SIMPLE:
        return f"{SIMPLE[mn]} {ops_text.strip()}".rstrip()
    raise Untranslatable(mn)


def strip_comment(text):
    """(code, comment) of an instruction's operand text."""
    for marker in ("/*", "@", "//"):
        i = text.find(marker)
        if i >= 0:
            return text[:i], text[i:]
    return text, ""


def translate(lines):
    out = []
    for n, line in enumerate(lines, 1):
        m = INSN.match(line.rstrip("\n"))
        if not m or line.lstrip().startswith("#"):
            out.append(line)
            continue
        indent, label, gap, mn, rest = m.groups()
        code, comment = strip_comment(rest)
        try:
            new = translate_insn(mn, code)
        except (Untranslatable, ValueError, IndexError) as e:
            raise SystemExit(f"line {n}: cannot translate {mn}{code}: {e}")
        out.append(f"{indent}{label or ''}{gap or ''}{new}{(' ' + comment) if comment else ''}\n")
    return out


def main():
    if len(sys.argv) != 3:
        sys.exit(__doc__)
    lines = open(sys.argv[1]).readlines()
    out = translate(lines)
    with open(sys.argv[2], "w") as f:
        f.write("/* Translated from %s by tc32asm2thumb.py: ARMv4T Thumb, for thumb2tc32.py. */\n"
                "\t.syntax unified\n\t.thumb\n" % sys.argv[1].split("/")[-1])
        f.writelines(out)


if __name__ == "__main__":
    main()

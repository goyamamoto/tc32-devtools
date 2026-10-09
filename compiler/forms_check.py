#!/usr/bin/env python3
"""Instruction forms in an image that Telink's own code never uses.

tc32emu runs Thumb-1 semantics (ARMv4T). That TC32 behaves the same is shown
only for what Telink's code uses: a shipped firmware image's executed code
(whose behaviour in tc32emu matches the hardware) and the TLSR825x/827x
libraries of Telink's SDK (built with Telink's compiler, running on shipped
devices). Telink's compiler is taken to be right (its code runs on shipped
devices), so what it emits is also evidence: programs it built (ccdiff.py)
count too. This check lists, for a TC32 image, every instruction form it
contains, with how often Telink's code has it; a form Telink never uses fails
the check, whether or not ARM defines it (for example ldm with its base
register in the list, which ARM defines and Telink's compiler never emits).

Forms are Thumb-1 instruction classes with the corner cases that could differ
on TC32 split out (base register in an ldm/stm list, PC read or written by a
high-register op, lsl #0, shifts by an immediate 0 meaning 32, empty register
lists, the TC32-only instructions).

  forms_check.py evidence --telink-dis <objdump -d of a Telink library>...
                 [--trace <trace.pkl>] [--elf <ELF built by Telink's gcc>...]
                 [--base vendor_forms.txt] > vendor_forms.txt
      build the evidence table; --base starts from an existing table and adds
      the new sources' counts to it (for sources added later, when the
      original inputs are not at hand). The disassembly is Telink's tc32-elf-objdump
      -d output for the SDK's proj_lib/liblt_827x.a and liblt_825x.a and for
      the libgcc.a of Telink's toolchain (its __clzsi2, assembled by Telink's
      assembler, finds its table with tadd rd, pc, #imm); the
      trace is {(pc, halfword): count} of what a shipped firmware image
      executed in tc32emu; the ELF files are programs Telink's gcc built
      (ccdiff.py --compiler telink --keep-elf), each counted as a place per
      instruction.
  forms_check.py check <elf> [--thumb] [--evidence vendor_forms.txt]
      check the code of an ELF (TC32 code; --thumb: Thumb code, as
      thumb2tc32.py would re-encode it): its functions, and the code outside
      every function that a $t mapping symbol marks (hand-written assembly
      without .type/.size), outside sized data objects and other than 0x0000
      (fill), so that every instruction thumb2tc32.py re-encodes is checked.
      Literal pools (targets of PC-relative loads), $d ranges and padding
      after a branch or return are skipped; untranslated Thumb traps and nops
      elsewhere are reported. The evidence subcommand counts functions only.
      An ELF whose build attributes name the core (Tag_CPU_name) must agree
      with --thumb: tc32 (clang -mcpu=tc32, the direct path) without it, an
      ARM core (the Thumb path) with it; otherwise check stops with exit
      status 2 before reading the code.

SPDX-License-Identifier: Apache-2.0
"""
import argparse
import bisect
import collections
import os
import pickle
import re
import struct
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(os.path.dirname(HERE), "common"))
import armattr  # noqa: E402
import tc32isa as te  # noqa: E402
import thumb2tc32  # noqa: E402

ALU = "and eor lsl lsr asr adc sbc ror tst neg cmp cmn orr mul bic mvn".split()
INV = [0] * 32
for _tc, _th in enumerate(te.TOP):
    INV[_th] = _tc
# Thumb encodings an LLVM TC32 backend can leave untranslated: the trap it
# emits for __builtin_trap() (.inst.n 0xdefe) and the Thumb nop it pads code
# with. On TC32 these are other instructions. Where they follow a branch or a
# return they are padding nothing reaches; elsewhere they are reported.
RAW_THUMB = {0xDEFE: "raw Thumb trap 0xdefe (TC32: ldm r6!, {r1-r7})",
             0x46C0: "raw Thumb nop 0x46c0 (TC32: strb r0, [r0, #27])"}


def ends_flow(hw):
    """b, pop {..., pc}, bx, mov pc: the next halfword is not reached from here."""
    t = te.to_thumb(hw)
    return (t >> 11 == 0x1C or t & 0xFF00 == 0xBD00 or t & 0xFF87 == 0x4700
            or t & 0xFF87 == 0x4687)


def form(hw):
    """The form of a TC32 halfword (see the module docstring)."""
    if hw & 0xFFE0 == 0x6BC0:
        return ["tmcsr", "tmrcs", "tmssr", "tmrss"][(hw >> 3) & 3]
    if hw & 0xFE00 == 0x6800:
        return "treti" + (" pc" if hw & 0x100 else "") + ("" if hw & 0xFF else " no-lo")
    if hw & 0xFF00 == 0xCF00:
        return "tserv"
    t = te.to_thumb(hw)
    top = t >> 11
    if top <= 2:
        return ["lsl", "lsr", "asr"][top] + " imm" + ("" if (t >> 6) & 31 else " #0")
    if top == 3:
        return ("sub" if t & 0x200 else "add") + (" imm3" if t & 0x400 else " reg")
    if top <= 7:
        return ["mov", "cmp", "add", "sub"][top - 4] + " imm8"
    if t >> 10 == 0x10:
        return "alu " + ALU[(t >> 6) & 15]
    if t >> 10 == 0x11:
        op, rd, rm = (t >> 8) & 3, (t & 7) | ((t >> 4) & 8), (t >> 3) & 15
        name = ["add", "cmp", "mov", "bx"][op] + " hi"
        if op == 3 and t & 0x87:
            return "bx with bit 7 or bits 2:0 set"
        if rm == 15 or (rd == 15 and op < 2):
            name += " reads pc"
        if rd == 15 and op in (0, 2):
            name += " writes pc"
        if op < 3 and rd < 8 and rm < 8:
            name += " (both low)"
        return name
    if top == 9:
        return "ldr pc-rel"
    if top in (0x0A, 0x0B):
        return ["str", "strh", "strb", "ldrsb", "ldr", "ldrh", "ldrb", "ldrsh"][(t >> 9) & 7] + " reg"
    if 0x0C <= top <= 0x11:
        return ["str", "ldr", "strb", "ldrb", "strh", "ldrh"][top - 0x0C] + " imm5"
    if top in (0x12, 0x13):
        return ("ldr" if top & 1 else "str") + " sp"
    if top in (0x14, 0x15):
        return "add rd, " + ("sp" if top & 1 else "pc")
    if t & 0xFF00 == 0xB000:
        return "add/sub sp"
    if t & 0xF600 == 0xB400:
        n = "pop" if t & 0x800 else "push"
        return n + ((" pc" if n == "pop" else " lr") if t & 0x100 else "") + ("" if t & 0x1FF else " EMPTY")
    if top in (0x18, 0x19):
        rb = (t >> 8) & 7
        n = "ldm" if top & 1 else "stm"
        if not t & 0xFF:
            return n + " EMPTY"
        return n + (" base-in-list" if t >> rb & 1 else "")
    if top in (0x1A, 0x1B):
        return "b<cc> " + "eq ne cs cc mi pl vs vc hi ls ge lt gt le al sv".split()[(t >> 8) & 15]
    if top == 0x1C:
        return "b"
    if top == 0x1E:
        return "bl prefix"
    if top == 0x1F:
        return "bl suffix"
    return "undefined"


# ------------------------------------------------------------------ evidence
DIS = re.compile(r"^\s+([0-9a-f]+):\t([0-9a-f]{4})(?: ([0-9a-f]{4}))? ?\s*\t(\S+)")


def telink_forms(path):
    """Forms in a Telink objdump -d listing; literal pools (targets of
    PC-relative loads) and lines objdump prints as data are skipped."""
    secs, cur, member = collections.defaultdict(dict), None, None
    for line in open(path, errors="replace"):
        m = re.match(r"^(\S+):\s+file format", line)
        if m:                                   # a new archive member
            member, cur = m.group(1), None
            continue
        if line.startswith("Disassembly of section"):
            cur = (member, line.split()[-1])
            continue
        m = DIS.match(line)
        if m and cur and not m.group(4).startswith(".") and m.group(4) != "undefined":
            a = int(m.group(1), 16)
            secs[cur][a] = int(m.group(2), 16)
            if m.group(3):
                secs[cur][a + 2] = int(m.group(3), 16)
    out = collections.Counter()
    for code in secs.values():
        out.update(form(hw) for a, hw in code.items() if a not in literals(code))
    return out


def literals(code):
    lit = set()
    for a, hw in code.items():
        t = te.to_thumb(hw)
        if t >> 11 == 9:
            x = ((a + 4) & ~3) + (t & 0xFF) * 4
            lit |= {x, x + 2}
    return lit


def evidence(a):
    lib, fw_ran, gcc = collections.Counter(), collections.Counter(), collections.Counter()
    base_note = ""
    if a.base:
        for f, (l, st, g) in load_evidence(a.base).items():
            lib[f] += l
            fw_ran[f] += st
            gcc[f] += g
        base_note = f"; on top of {os.path.basename(a.base)}"
    for p in a.telink_dis:
        lib.update(telink_forms(p))
    if a.trace:
        executed, _ = pickle.load(open(a.trace, "rb"))
        for (_pc, hw), _n in executed.items():
            fw_ran[form(hw)] += 1
    for p in a.elf or ():
        gcc.update(image_forms(p)[0])
    print("# form<TAB>sites in Telink's libraries (SDK, libgcc)<TAB>sites in the executed code of an existing TC32 binary"
          "<TAB>sites in programs built by Telink's gcc")
    sources = [os.path.basename(p) for p in a.telink_dis if p != os.devnull]
    if a.trace:
        sources.append(os.path.basename(a.trace))
    if a.elf:
        sources.append(f"{len(a.elf)} ELF files built by Telink's gcc (ccdiff.py --compiler telink --keep-elf"
                       + (", checks/telink)" if any("/telink/" in p for p in a.elf) else ")"))
    print("# generated by forms_check.py evidence from: " + ", ".join(sources) + base_note)
    for f in sorted(set(lib) | set(fw_ran) | set(gcc)):
        print(f"{f}\t{lib[f]}\t{fw_ran[f]}\t{gcc[f]}")


def load_evidence(path):
    ev = {}
    for line in open(path):
        if line.startswith("#") or not line.strip():
            continue
        f, *counts = line.rstrip("\n").split("\t")
        ev[f] = tuple(int(c) for c in counts) + (0,) * (3 - len(counts))
    return ev


# ------------------------------------------------------------------ check
def elf_functions(data):
    """[(name, start, end, section bytes, section address, section index)]
    for the STT_FUNC symbols of executable sections (a function without a
    size runs to the next symbol), and {(section index, address): kind} of
    the mapping symbols ($t/$d/$a, or ccdiff/mark_data.awk's __cd_t/__cd_d)."""
    shoff, = struct.unpack_from("<I", data, 0x20)
    shentsize, shnum, _ = struct.unpack_from("<HHH", data, 0x2E)
    secs = [struct.unpack_from("<IIIIIIIIII", data, shoff + i * shentsize) for i in range(shnum)]
    funcs, maps, starts = [], {}, collections.defaultdict(set)
    for s in secs:
        if s[1] != 2:          # SHT_SYMTAB
            continue
        strtab = secs[s[6]]
        for i in range(s[5] // 16):
            name, value, size, info, _o, shndx = struct.unpack_from("<IIIBBH", data, s[4] + i * 16)
            n = data[strtab[4] + name:data.index(b"\0", strtab[4] + name)].decode()
            if shndx >= len(secs) or not secs[shndx][2] & 0x4:
                continue
            if n[:2] in ("$t", "$d", "$a"):
                maps[(shndx, value & ~1)] = n[1]
            elif n.startswith(("__cd_d", "__cd_t")):       # ccdiff/mark_data.awk
                maps[(shndx, value & ~1)] = n[5]
            starts[shndx].add(value & ~1)
            if info & 0xF == 2:
                funcs.append((n, value & ~1, size, shndx))
    out = []
    for n, v, size, shndx in funcs:
        sec = secs[shndx]
        later = sorted(x for x in starts[shndx] if x > v) + [sec[3] + sec[5]]
        end = v + size if size else later[0]
        out.append((n, v, end, data[sec[4]:sec[4] + sec[5]], sec[3], shndx))
    return out, maps


def code_sections(data):
    """[(name, address, end, section bytes, section index)] of the allocated
    executable sections with content, and [(section index, start, end)] of the
    sized data objects (STT_OBJECT) in them."""
    shoff, = struct.unpack_from("<I", data, 0x20)
    shentsize, shnum, shstrndx = struct.unpack_from("<HHH", data, 0x2E)
    secs = [struct.unpack_from("<IIIIIIIIII", data, shoff + i * shentsize) for i in range(shnum)]
    names = secs[shstrndx]
    out, objects = [], []
    for k, s in enumerate(secs):
        if s[1] != 8 and s[2] & 0x6 == 0x6 and s[5]:     # not NOBITS; SHF_ALLOC | SHF_EXECINSTR
            n = data[names[4] + s[0]:data.index(b"\0", names[4] + s[0])].decode()
            out.append((n, s[3], s[3] + s[5], data[s[4]:s[4] + s[5]], k))
        if s[1] == 2:                                      # SHT_SYMTAB
            for i in range(s[5] // 16):
                _n, value, size, info, _o, shndx = struct.unpack_from("<IIIBBH", data, s[4] + i * 16)
                if info & 0xF == 1 and size and shndx < len(secs) and secs[shndx][2] & 0x4:
                    objects.append((shndx, value, value + size))
    return out, objects


def image_forms(path, thumb=False, outside=False):
    """(Counter of forms, {form: first place}, number of functions, number of
    instructions outside them) of an ELF's functions (see the module
    docstring). With outside, also of the code outside every function: the
    halfwords of executable sections that a $t mapping symbol marks as code,
    outside the functions' ranges and the sized data objects, other than
    0x0000 (fill, as thumb2tc32.py takes it)."""
    data = open(path, "rb").read()
    funcs, maps = elf_functions(data)
    kinds = sorted(maps.items())
    kind_at = [key for key, _ in kinds]
    seen, where, done = collections.Counter(), {}, set()

    def kind(shndx, x):
        i = bisect.bisect_right(kind_at, (shndx, x)) - 1
        return kinds[i][1] if i >= 0 and kinds[i][0][0] == shndx else None

    regions = [(name, start, end, body, base, shndx, False) for name, start, end, body, base, shndx in funcs]
    if outside:
        covered = {(shndx, x) for _n, start, end, _b, _a, shndx in funcs for x in range(start, end, 2)}
        secs, objects = code_sections(data)
        regions += [(name, addr, end, body, addr, shndx, True) for name, addr, end, body, shndx in secs]
    n_outside = 0
    for name, start, end, body, base, shndx, out in regions:
        code = {}
        for x in range(start, end - 1, 2):
            if (shndx, x) in done:
                continue
            k = kind(shndx, x)
            if k == "d":
                continue
            hw, = struct.unpack_from("<H", body, x - base)
            if out and ((shndx, x) in covered or k != "t" or hw == 0
                        or any(o == shndx and lo <= x < hi for o, lo, hi in objects)):
                continue
            code[x] = thumb2tc32.encode(hw) if thumb else hw
        lit = literals(code)
        for x, hw in code.items():
            if x in lit:
                continue
            done.add((shndx, x))
            prev = code.get(x - 2)
            if hw in RAW_THUMB or hw == 0xD4D4:
                if prev is not None and (ends_flow(prev) or prev in RAW_THUMB or prev == 0xD4D4):
                    continue               # padding
                if hw in RAW_THUMB and not thumb:
                    seen[RAW_THUMB[hw]] += 1
                    where.setdefault(RAW_THUMB[hw], f"{name}+0x{x - start:x}")
                    n_outside += out
                    continue
            f = form(hw)
            seen[f] += 1
            where.setdefault(f, f"{name}+0x{x - start:x}")
            n_outside += out
    return seen, where, len(funcs), n_outside


def check(a):
    cpu = armattr.cpu_name(open(a.elf, "rb").read()) or ""
    if a.thumb and cpu.lower() == "tc32":
        print(f"forms_check: {a.elf} names the core tc32: its code is TC32 already; check it without --thumb",
              file=sys.stderr)
        return 2
    if not a.thumb and cpu and cpu.lower() != "tc32":
        print(f"forms_check: {a.elf} names the core {cpu}: its code is Thumb; check it with --thumb",
              file=sys.stderr)
        return 2
    ev = load_evidence(a.evidence)
    seen, where, nfuncs, n_outside = image_forms(a.elf, a.thumb, outside=True)
    bad = [f for f in seen if sum(ev.get(f, (0, 0, 0))) == 0]
    print(f"{'form':30} {'image':>7} {'Telink gcc':>10} {'Telink libs':>11} {'fw ran':>9}")
    for f in sorted(seen, key=lambda f: (f not in bad, f)):
        lib, fw_ran, gcc = ev.get(f, (0, 0, 0))
        print(f"{f:30} {seen[f]:7} {gcc:10} {lib:11} {fw_ran:9}"
              + (f"   NO VENDOR USE, e.g. {where[f]}" if f in bad else ""))
    print(f"\n{sum(seen.values())} instructions in {nfuncs} functions"
          + (f" and {n_outside} outside them" if n_outside else "") + "; "
          f"{len(bad)} form(s) Telink's code never uses" + (f": {', '.join(sorted(bad))}" if bad else ""))
    return 1 if bad else 0


def main():
    ap = argparse.ArgumentParser()
    sub = ap.add_subparsers(dest="cmd", required=True)
    e = sub.add_parser("evidence")
    e.add_argument("--telink-dis", nargs="+", required=True)
    e.add_argument("--trace")
    e.add_argument("--elf", nargs="+", help="ELF files built by Telink's toolchain")
    e.add_argument("--base", help="an existing evidence table to add the new sources to")
    c = sub.add_parser("check")
    c.add_argument("elf")
    c.add_argument("--thumb", action="store_true")
    c.add_argument("--evidence", default=os.path.join(HERE, "vendor_forms.txt"))
    a = ap.parse_args()
    if a.cmd == "evidence":
        evidence(a)
        return 0
    return check(a)


if __name__ == "__main__":
    sys.exit(main())

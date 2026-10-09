#!/usr/bin/env python3
"""Compare the objects of an assembly source and its standard-syntax twin.

The port's assembly sources are written in Telink's mnemonics; the Thumb
path assembles their translation (tc32asm2thumb.py). Each has a twin in
standard syntax (unified Thumb, the TC32-only instructions as .inst.n),
<name>.ual.S next to <name>.S, which the direct path assembles as it is
(clang -mcpu=tc32). Assembled with the same flags, the two must give the same
object: this compares, for two relocatable ELF files,
- every allocated section (by name): type, flags, alignment, size and bytes;
- the relocations of each such section: offset, type, addend (RELA) and the
  symbol, by name (a section symbol by its section's name);
- the symbols other than STT_FILE and those of non-allocated sections (debug
  information): name, value, size, type, binding, visibility and section,
  mapping symbols ($t, $d) included;
- the build attributes (.ARM.attributes).
Debug information (it names the source file and its lines) and the file
symbol are left out. A difference is reported by section and offset, with
the nearest symbol below it.

Usage: asm_twin_check.py <translation.o> <twin.o>

SPDX-License-Identifier: Apache-2.0
"""
import bisect
import os
import struct
import sys

SHT_SYMTAB, SHT_RELA, SHT_NOBITS, SHT_REL = 2, 4, 8, 9
SHT_ARM_ATTRIBUTES = 0x70000003
SHF_ALLOC = 0x2
STT_FUNC, STT_SECTION, STT_FILE = 2, 3, 4


class Obj:
    def __init__(self, path):
        self.path = path
        data = open(path, "rb").read()
        if data[:4] != b"\x7fELF" or data[4] != 1 or data[5] != 1:
            raise SystemExit(f"asm_twin_check: {path}: not a little-endian ELF32 file")
        if struct.unpack_from("<H", data, 0x10)[0] != 1:
            raise SystemExit(f"asm_twin_check: {path}: not a relocatable object")
        shoff, = struct.unpack_from("<I", data, 0x20)
        shentsize, shnum, shstrndx = struct.unpack_from("<HHH", data, 0x2E)
        raw = [struct.unpack_from("<IIIIIIIIII", data, shoff + i * shentsize) for i in range(shnum)]
        shstr = raw[shstrndx]

        def cstr(base, off):
            end = data.index(b"\0", base + off)
            return data[base + off:end].decode("ascii", "replace")
        self.secs = []
        for name, typ, flags, _addr, off, size, link, info, align, entsize in raw:
            body = b"" if typ == SHT_NOBITS else data[off:off + size]
            self.secs.append(dict(name=cstr(shstr[4], name), type=typ, flags=flags, size=size, link=link,
                                  info=info, align=align, entsize=entsize, data=body))
        self.syms = []
        for s in self.secs:
            if s["type"] != SHT_SYMTAB:
                continue
            strtab = raw[s["link"]][4]
            for i in range(len(s["data"]) // 16):
                name, value, size, info, other, shndx = struct.unpack_from("<IIIBBH", s["data"], i * 16)
                self.syms.append(dict(name=cstr(strtab, name), value=value, size=size, type=info & 0xF,
                                      bind=info >> 4, vis=other & 3, shndx=shndx))

    def sec_name(self, shndx):
        if shndx == 0:
            return "UND"
        if shndx >= 0xFF00:
            return {0xFFF1: "ABS", 0xFFF2: "COMMON"}.get(shndx, f"0x{shndx:x}")
        return self.secs[shndx]["name"]

    def kept(self, shndx):
        """True for an allocated section (and for UND/ABS/COMMON)."""
        return shndx == 0 or shndx >= 0xFF00 or bool(self.secs[shndx]["flags"] & SHF_ALLOC)

    def sym_name(self, sym):
        return f"[{self.sec_name(sym['shndx'])}]" if sym["type"] == STT_SECTION else sym["name"]

    def allocated(self):
        return {s["name"]: s for s in self.secs if s["flags"] & SHF_ALLOC}

    def relocs(self):
        """{section name: [(offset, type, symbol name, addend)]} for allocated sections."""
        out = {}
        for s in self.secs:
            if s["type"] not in (SHT_REL, SHT_RELA):
                continue
            target = self.secs[s["info"]]
            if not target["flags"] & SHF_ALLOC:
                continue
            step = 12 if s["type"] == SHT_RELA else 8
            rows = []
            for i in range(len(s["data"]) // step):
                off, info = struct.unpack_from("<II", s["data"], i * step)
                add = struct.unpack_from("<i", s["data"], i * step + 8)[0] if step == 12 else None
                rows.append((off, info & 0xFF, self.sym_name(self.syms[info >> 8]), add))
            out[target["name"]] = sorted(rows)
        return out

    def symbols(self):
        return sorted((self.sym_name(s), s["value"], s["size"], s["type"], s["bind"], s["vis"],
                       self.sec_name(s["shndx"]))
                      for s in self.syms[1:] if s["type"] != STT_FILE and self.kept(s["shndx"]))

    def attributes(self):
        return b"".join(s["data"] for s in self.secs if s["type"] == SHT_ARM_ATTRIBUTES)

    def where(self, sec, off):
        """The nearest symbol at or below sec+off, as name+offset."""
        rows = sorted((s["value"] & ~1 if s["type"] == STT_FUNC else s["value"], s["name"]) for s in self.syms
                      if s["shndx"] < len(self.secs) and self.sec_name(s["shndx"]) == sec and s["name"]
                      and s["type"] != STT_SECTION)
        i = bisect.bisect_right([v for v, _ in rows], off) - 1
        return f"{rows[i][1]}+0x{off - rows[i][0]:x}" if i >= 0 else f"{sec}+0x{off:x}"


def compare(a, b):
    bad = []
    sa, sb = a.allocated(), b.allocated()
    for name in sorted(set(sa) | set(sb)):
        if name not in sa or name not in sb:
            bad.append(f"section {name}: only in {(a if name in sa else b).path}")
            continue
        x, y = sa[name], sb[name]
        for k in ("type", "flags", "align", "size", "entsize"):
            if x[k] != y[k]:
                bad.append(f"section {name}: {k} 0x{x[k]:x} vs 0x{y[k]:x}")
        if x["data"] != y["data"]:
            off = next((i for i in range(min(len(x["data"]), len(y["data"])))
                        if x["data"][i] != y["data"][i]), min(len(x["data"]), len(y["data"])))
            n = sum(1 for i in range(min(len(x["data"]), len(y["data"]))) if x["data"][i] != y["data"][i])
            bad.append(f"section {name}: {n} byte(s) differ, first at 0x{off:x} ({a.where(name, off)}): "
                       f"{x['data'][off:off + 4].hex()} vs {y['data'][off:off + 4].hex()}")
    ra, rb = a.relocs(), b.relocs()
    for name in sorted(set(ra) | set(rb)):
        x, y = ra.get(name, []), rb.get(name, [])
        if x != y:
            diff = sorted(set(x) ^ set(y))
            first = diff[0] if diff else (None,)
            bad.append(f"relocations of {name}: {len(x)} vs {len(y)}, first difference "
                       + (f"at 0x{first[0]:x} ({a.where(name, first[0])})" if first[0] is not None else "in order"))
    ya, yb = a.symbols(), b.symbols()
    if ya != yb:
        only_a = sorted(set(ya) - set(yb))[:5]
        only_b = sorted(set(yb) - set(ya))[:5]
        bad.append(f"symbols: {len(ya)} vs {len(yb)}; only in the first {only_a}; only in the second {only_b}")
    if a.attributes() != b.attributes():
        bad.append("build attributes (.ARM.attributes) differ")
    return bad


def main():
    if len(sys.argv) != 3:
        sys.exit(__doc__)
    a, b = Obj(sys.argv[1]), Obj(sys.argv[2])
    bad = compare(a, b)
    secs = a.allocated()
    nbytes = sum(s["size"] for s in secs.values())
    nrel = sum(len(r) for r in a.relocs().values())
    names = f"{os.path.basename(a.path)} and {os.path.basename(b.path)}"
    if bad:
        print(f"asm_twin_check: {names} DIFFER:")
        for line in bad[:20]:
            print("  " + line)
        return 1
    print(f"asm_twin_check: {names} are the same: {len(secs)} allocated section(s), {nbytes} B, "
          f"{nrel} relocation(s), {len(a.symbols())} symbol(s)")
    return 0


if __name__ == "__main__":
    sys.exit(main())

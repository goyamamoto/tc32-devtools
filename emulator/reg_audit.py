#!/usr/bin/env python3
"""Registers an image touches, named by the Telink SDK for the TLSR8258 (B85)
and the TLSR8278 (B87).

The Zephyr TC32 port was written for the TLSR8258 and runs here on a
TLSR8278. Most registers mean the same on both; the system timer does not
(0x74c/0x74f are control bits on the TLSR8258 and the 32 kHz timer's write
value on the TLSR8278). Code and emulator written from the same TLSR8258
reading agree with each other, so tests cannot catch such a mix-up. This
audit can: every register the image touches whose SDK names differ between
the two chips must be listed in reg_reviewed.csv, with the DS-TLSR8278 or
SDK evidence that the code uses it the TLSR8278 way.

Tables: b85_registers.csv and b87_registers.csv (reg_tables.py). The
reviewed list belongs to the firmware under audit: its user sets REVIEWED to
a CSV of offset, note. Without one, every such register counts as unreviewed.

SPDX-License-Identifier: Apache-2.0
"""
import csv
import os

HERE = os.path.dirname(os.path.abspath(__file__))
REVIEWED = None  # path of the reviewed list (offset, note), set by the user


def load(name):
    rows = []
    with open(os.path.join(HERE, name)) as f:
        for r in csv.reader(f):
            if r and not r[0].startswith("#"):
                rows.append((r[0], int(r[1], 16), int(r[2])))
    return rows


def names_at(table, off):
    return sorted({n for n, a, sz in table if a <= off < a + sz})


def reviewed():
    out = {}
    if REVIEWED is None:
        return out
    with open(REVIEWED) as f:
        for r in csv.reader(f):
            if r and not r[0].startswith("#"):
                out[int(r[0], 16)] = r[1]
    return out


def audit(reg_log, symbolize):
    """reg_log: {(offset, size, 'r'|'w', pc)}. Returns rows sorted by offset:
    (offset, access, functions, b85 names, b87 names, status, note)."""
    b85, b87, rev = load("b85_registers.csv"), load("b87_registers.csv"), reviewed()
    by_off = {}
    for off, size, rw, pc in reg_log:
        for o in range(off, off + size):
            e = by_off.setdefault(o, [set(), set()])
            e[0].add(rw)
            e[1].add(symbolize(pc).split("+")[0])
    rows = []
    for o in sorted(by_off):
        n85, n87 = names_at(b85, o), names_at(b87, o)
        if n85 == n87:
            status = "same" if n85 else "unnamed"
        elif n85 and set(n85) <= set(n87):
            status = "same"   # B87 only adds names (32-bit views of the same bytes)
        elif not n87:
            status = "B85 only"
        elif not n85:
            status = "B87 only"
        else:
            status = "differs"
        if status != "same" and status != "unnamed" and o in rev:
            status += ", reviewed"
        rows.append((o, "".join(sorted(by_off[o][0])), sorted(by_off[o][1]), n85, n87, status, rev.get(o, "")))
    return rows


def unreviewed(rows):
    return [r for r in rows if r[5] in ("differs", "B85 only")]


def format_rows(rows):
    out = []
    for o, rw, funcs, n85, n87, status, note in rows:
        out.append(f"0x{o:03x} {rw:2} {status:18} B85 {','.join(n85) or '-':32} B87 {','.join(n87) or '-':32} "
                   f"{' '.join(funcs)[:80]}")
    return "\n".join(out)

#!/usr/bin/env python3
"""The register offsets the Python emulator handles specially (reg_read and
reg_write of emulator/tc32emu.py) must all appear in the Go port
(go/tc32emu/machine.go RegRead/RegWrite), and the other way round. A crude
parity guard: it compares the sets of "o == 0x..." offsets and "a <= o <= b"
ranges named in the two files' register-access code.

SPDX-License-Identifier: Apache-2.0
"""
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def section(path, start, end):
    text = open(path).read()
    i, j = text.index(start), text.index(end)
    return text[i:j]


def offsets(text):
    out = set()
    for m in re.finditer(r"o == (0x[0-9A-Fa-f]+)", text):
        out.add(int(m.group(1), 16))
    for m in re.finditer(r"o in \(([^)]*)\)", text):                 # Python: o in (0x66, 0x70)
        out |= {int(x, 16) for x in re.findall(r"0x[0-9A-Fa-f]+", m.group(1))}
    for m in re.finditer(r"(0x[0-9A-Fa-f]+) <= o (?:&& o |and o )?<= (0x[0-9A-Fa-f]+)", text):
        out |= set(range(int(m.group(1), 16), int(m.group(2), 16) + 1))
    return out


def main():
    py = section(os.path.join(ROOT, "emulator", "tc32emu.py"), "def reg_read", "def gpio_irq_update")
    go = section(os.path.join(ROOT, "go", "tc32emu", "machine.go"), "func (m *Machine) RegRead", "func (m *Machine) GpioIrqUpdate")
    a, b = offsets(py), offsets(go)
    only_py, only_go = sorted(a - b), sorted(b - a)
    print(f"register cases: Python {len(a)}, Go {len(b)}; only in Python: {[hex(x) for x in only_py]}; only in Go: {[hex(x) for x in only_go]}")
    return 1 if only_py or only_go else 0


if __name__ == "__main__":
    sys.exit(main())

#!/usr/bin/env python3
"""Write the register names of the Telink SDK's drivers/B85 (TLSR8258) and
drivers/B87 (TLSR8278) register.h as b85_registers.csv and b87_registers.csv
(name, address, size), for reg_audit.py.

Usage: reg_tables.py <tc_ble_single_sdk checkout>   (telink-semi/tc_ble_single_sdk, Apache-2.0)

SPDX-License-Identifier: Apache-2.0
"""
import os
import re
import sys

DEF = re.compile(r"#define\s+(reg_\w+)\s+REG_ADDR(8|16|32)\(\s*(0x[0-9a-fA-F]+)\s*\)")


def table(path):
    rows = []
    for line in open(path, errors="replace"):
        m = DEF.search(line)
        if m:
            rows.append((m.group(1), int(m.group(3), 16), int(m.group(2)) // 8))
    return rows


def main():
    sdk = sys.argv[1]
    here = os.path.dirname(os.path.abspath(__file__))
    for chip in ("B85", "B87"):
        rows = table(os.path.join(sdk, "tc_ble_single_sdk", "drivers", chip, "register.h"))
        with open(os.path.join(here, f"{chip.lower()}_registers.csv"), "w") as f:
            for name, addr, size in rows:
                f.write(f"{name},0x{addr:03x},{size}\n")
        print(chip, len(rows), "registers")


if __name__ == "__main__":
    main()

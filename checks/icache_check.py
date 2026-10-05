#!/usr/bin/env python3
"""The optional flash cache model (TC32EMU_ICACHE_MISS, Machine.icache_miss),
on a machine that is never run: 64 lines of 32 bytes, direct-mapped; an XIP
access beyond the RAM mirror to a line its slot does not hold costs the miss
cycles and is counted; the same line again costs nothing; a line 64 lines
further takes the slot; an access across two lines counts both; addresses in
the RAM mirror and the SRAM cost nothing; a reset empties the cache; with
the model off nothing is added. The sizes are the SDK link scripts'; the
mapping and the cost of a miss are not in our documents. The Go port has the
same cases as a test (go/tc32emu/icache_test.go).
SPDX-License-Identifier: Apache-2.0
"""
import os
import sys
sys.path[:0] = [os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "emulator")]
import tc32emu as te  # noqa: E402

failures = []


def check(cond, text):
    print(("ok   " if cond else "FAIL ") + text)
    if not cond:
        failures.append(text)


def main():
    fl = te.Flash()
    fl.mem[8] = 0x4B
    fl.mem[0x0C:0x10] = (0x10).to_bytes(4, "little")      # the boot ROM copies 0x100 bytes: the RAM mirror
    m = te.Machine(fl, max_log=0)
    check(m.sram_code_end() == 0x100, f"RAM mirror up to 0x{m.sram_code_end():x}")

    def cost(addr, size=2):
        c, n = m.cycles, m.icache_misses
        m.read(addr, size)
        return m.cycles - c, m.icache_misses - n

    m.icache_miss = 0
    check(cost(0x4000) == (0, 0), "model off: an XIP read costs nothing")
    m.icache_miss = 288
    check(cost(0x4000) == (288, 1), "first access to a line: one miss, 288 cycles")
    check(cost(0x4002) == (0, 0) and cost(0x401E) == (0, 0), "the same line again: nothing")
    check(cost(0x4020) == (288, 1), "the next line: a miss")
    check(cost(0x4000 + 64 * 32) == (288, 1), "a line 64 lines further: takes the slot")
    check(cost(0x4000) == (288, 1), "the first line again: a miss, its slot was taken")
    check(cost(0x403E, 4) == (288, 1), "a 4-byte read across two lines, the first held: one miss")
    check(cost(0x80) == (0, 0), "an address in the RAM mirror: nothing")
    m.write(te.SRAM_BASE + 0x1000, 4, 1)
    check(cost(te.SRAM_BASE + 0x1000, 4) == (0, 0), "an SRAM address: nothing")
    m.reset()
    m.icache_miss = 288
    check(cost(0x4020) == (288, 1), "after a reset the cache is empty")
    print(f"{len(failures)} failure(s)")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())

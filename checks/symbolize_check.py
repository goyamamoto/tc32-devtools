#!/usr/bin/env python3
"""Machine.symbolize() with the symbols given at construction and assigned
afterwards (a whole-keyboard runner sets the running image's symbols at each
boot): both name an address as name+0xoffset of the last symbol at or below
it, and an address below every symbol, or with no symbols, as hex. The Go
port has the same cases as a test (go/tc32emu/symbolize_test.go).

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
    m = te.Machine(fl, max_log=0)
    check(m.symbolize(0x1BAA) == "0x1baa", "no symbols: hex")
    m.sym = {"trim_analog_write": 0x1BA0, "other": 0x1000, "alias": 0x1000}
    check(m.symbolize(0x1BAA) == "trim_analog_write+0xa", f"assigned after construction: {m.symbolize(0x1BAA)}")
    check(m.symbolize(0x1004) == "other+0x4", f"two names at one address: the later by name, {m.symbolize(0x1004)}")
    check(m.symbolize(0x10) == "0x10", "below every symbol: hex")
    m.sym = None
    check(m.symbolize(0x1BAA) == "0x1baa", "cleared: hex again")
    m = te.Machine(fl, symbols={"f": 0x20}, max_log=0)
    check(m.symbolize(0x24) == "f+0x4", "given at construction")
    m.set_symbols({"g": 0x30})
    check(m.symbolize(0x34) == "g+0x4" and m.sym == {"g": 0x30}, "set_symbols(), as the Go SetSymbols()")
    print(f"{len(failures)} failure(s)")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())

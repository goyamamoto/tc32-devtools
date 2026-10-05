#!/usr/bin/env python3
"""The retention analog registers (DS-TLSR8278 Table 2-2), on a machine that
is never run: 0x3a-0x3c are 0x00, 0x00 and 0x0f at power-on (also the B87
SDK's pm.h, DEEP_ANA_REG0-2), a reset keeps what the firmware wrote, and a new
power-on clears them again; 0x35-0x39, kept in deep sleep only, are at their
defaults 0x20, 0, 0, 0 and 0xff after power-on and after every reset. The Go port has the same cases as a test
(go/tc32emu/power_on_test.go).
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
    check(bytes(m.analog[0x3A:0x3D]) == b"\x00\x00\x0f", f"power-on: analog 0x3a-0x3c = {bytes(m.analog[0x3A:0x3D]).hex()}")
    check(bytes(m.analog[0x35:0x3A]) == b"\x20\x00\x00\x00\xff", f"power-on: analog 0x35-0x39 = {bytes(m.analog[0x35:0x3A]).hex()}")
    m.analog[0x3A:0x3D] = b"\x12\x34\x56"
    m.analog[0x35:0x3A] = b"\x71\x72\x73\x74\x75"
    m.reset()
    check(bytes(m.analog[0x3A:0x3D]) == b"\x12\x34\x56", "a reset keeps 0x3a-0x3c")
    check(bytes(m.analog[0x35:0x3A]) == b"\x20\x00\x00\x00\xff", "a reset puts 0x35-0x39 at their defaults")
    m = te.Machine(fl, max_log=0)
    check(bytes(m.analog[0x3A:0x3D]) == b"\x00\x00\x0f", "a new power-on: 00 00 0f again")
    print(f"{len(failures)} failure(s)")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())

#!/usr/bin/env python3
"""What an XIP address reads when the chip has booted from slot B, on a
machine that is never run: below the slot's offset the running image (flash
0x20000 + address), at or above twice the offset the flash at that address
(the calibration sector at 0x77000, which every image reads by pointer with
the flash's own address), and in between an error, since no source says what
is there. From slot A every address reads the flash at that address. The Go
port has the same cases as a test (go/tc32emu/xip_slot_test.go).
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


def machine(slot):
    fl = te.Flash()
    fl.mem[slot + 8] = 0x4B
    fl.mem[slot + 0x0C:slot + 0x10] = bytes(4)
    for a in (0x100, 0x20100, 0x30000, 0x40000, 0x77000):
        fl.mem[a:a + 4] = a.to_bytes(4, "little")
    return te.Machine(fl, max_log=0)


def read(m, a):
    try:
        return m.read(a, 4), None
    except te.EmuError as e:
        return None, str(e)


def main():
    m = machine(0)
    check(m.boot_slot == 0, "slot A boots")
    for a in (0x100, 0x20100, 0x30000, 0x40000, 0x77000):
        check(read(m, a) == (a, None), f"slot A: 0x{a:05x} reads the flash at 0x{a:05x}")
    m = machine(0x20000)
    check(m.boot_slot == 0x20000, "slot B boots")
    check(read(m, 0x100) == (0x20100, None), "slot B: 0x00100 reads the running image, flash 0x20100")
    v, err = read(m, 0x20100)
    check(v is None and err is not None and "not documented" in err, f"slot B: 0x20100 stops ({err})")
    v, err = read(m, 0x30000)
    check(v is None and err is not None, "slot B: 0x30000 stops")
    check(read(m, 0x40000) == (0x40000, None), "slot B: 0x40000 reads the flash at 0x40000")
    check(read(m, 0x77000) == (0x77000, None), "slot B: 0x77000 (the calibration sector) reads the flash at 0x77000")
    m.write(te.REG_BASE + 0x608, 4, (1 << 24) | 0x100)
    check(int.from_bytes(m.sram[0x100:0x104], "little") == 0x20100, "slot B: the startup loader loads offset 0x100 from the running image")
    print(f"{len(failures)} failure(s)")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())

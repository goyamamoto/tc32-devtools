#!/usr/bin/env python3
"""GPIO input reads of the emulator, on a machine that is never run: the
pads' levels from the pull resistors and driven outputs, a floating pad
holding its last level (1 after reset), the input-enable mask (0x581 + 8p,
analog 0xc0 for port C), and the register alias at 0x1000000. The Go port
has the same cases as a test (go/tc32emu/gpio_test.go).

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


def machine():
    fl = te.Flash()
    fl.mem[8] = 0x4B
    return te.Machine(fl, max_log=0)


def main():
    m = machine()
    a, r = m.analog, m.regs_mem
    # Port A: pin 0 1 MOhm up, pin 1 100 kOhm down, pin 2 10 kOhm up, pin 3 none (floating, holds 1),
    # pin 4 a GPIO output driven low; pins 5-7 float.
    a[0x0E] = 0x01 | 0x02 << 2 | 0x03 << 4 | 0x00 << 6
    r[0x586] |= 0x10            # PA4 is GPIO (it is by default)
    r[0x582] &= ~0x10 & 0xFF    # output enabled
    r[0x583] &= ~0x10 & 0xFF    # driven low
    r[0x581] = 0xFF             # every input enabled
    check(m.reg_read(0x580, 1) == 0xED, f"port A pulls, a driven output, floating pads at 1: 0x{m.reg_read(0x580, 1):02x}")
    r[0x581] = 0x0F
    check(m.reg_read(0x580, 1) == 0x0D, f"port A through the input enable 0x0f: 0x{m.reg_read(0x580, 1):02x}")
    # Port D pin 0: a floating pad keeps its last level across pull changes.
    r[0x599] = 0xFF
    seq = []
    for pull in (2, 0, 1, 0):
        a[0x14] = pull
        seq.append(m.reg_read(0x598, 1) & 1)
    check(seq == [0, 0, 1, 1], f"port D pin 0: down, none, up, none reads {seq} (a floating pad holds its last level)")
    m.reset(0)
    a, r = m.analog, m.regs_mem      # reset() makes new register and analog arrays
    a[0x14] = 0
    r[0x599] = 0xFF
    check(m.reg_read(0x598, 1) & 1 == 1, "after a reset a floating pad reads 1")
    # Port C: the input enable is analog 0xc0.
    a[0x12] = a[0x13] = 0xFF    # 10 kOhm up on all
    a[0xC0] = 0x03
    check(m.reg_read(0x590, 1) == 0x03, f"port C through analog 0xc0 = 0x03: 0x{m.reg_read(0x590, 1):02x}")
    # Port B: PB7 up, input enable 0x589.
    a[0x11] = 0x03 << 6
    r[0x589] = 0x80
    check(m.reg_read(0x588, 1) == 0x80, f"port B PB7 up through 0x589 = 0x80: 0x{m.reg_read(0x588, 1):02x}")
    # A pad released with no read in between (0x5b5 bit 3 clear, so the GPIO
    # interrupt model reads nothing either) keeps the level it was driven or
    # pulled to: PD1 driven low then floating, PD2 pulled down then floating.
    m.reset(0)
    a, r = m.analog, m.regs_mem
    r[0x599] = 0xFF
    m.reg_write(0x59E, 1, r[0x59E] | 0x06)            # PD1, PD2 GPIO
    m.reg_write(0x59B, 1, r[0x59B] & ~0x02 & 0xFF)    # PD1 output 0
    m.reg_write(0x59A, 1, r[0x59A] & ~0x02 & 0xFF)    # PD1 output enabled: driven low
    m.reg_write(0x59A, 1, r[0x59A] | 0x02)            # PD1 output off: floating
    for val in (0x02 << 4, 0x00):                     # analog 0x14: PD2 100 kOhm down, then none
        m.reg_write(0xB8, 1, 0x14)
        m.reg_write(0xB9, 1, val)
        m.reg_write(0xBA, 1, 0x60)
    v = m.reg_read(0x598, 1)
    check(v & 0x06 == 0, f"port D: PD1 driven low and PD2 pulled down, then floating with no read between, read 0: 0x{v:02x}")
    # The register alias at 0x1000000.
    m.write(0x1000ABC, 1, 0x5A)
    check(r[0xABC] == 0x5A and m.read(0x800ABC, 1) == 0x5A and m.read(0x1000ABC, 1) == 0x5A,
          "a write at 0x1000abc lands in register 0xabc and reads back at both addresses")
    m.write(0x1000660 + 4, 4, 100)
    m.write(0x1000668, 4, 7)
    m.write(0x1000660, 1, 0)
    check(m.read(0x1000664, 4) == 14, f"the divider through the alias: 100 / 7 = {m.read(0x1000664, 4)}")
    print(f"\n{len(failures)} failure(s)")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())

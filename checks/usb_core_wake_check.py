#!/usr/bin/env python3
"""The USB core wake source as a level or as an edge (TC32EMU_USB_CORE_WAKE,
UsbModel.core_wake), on a machine that is never run. With the source enabled
(analog 0x26 bit 4, register 0x6e bit 2) and the host driving K: the level
wakes every sleep for as long as K lasts; the edge wakes once per K period,
so a firmware that sleeps again while the host still drives K stays asleep.
Our documents do not say which the chip does; the default is the level. The
Go port has the same cases as a test (go/tc32emu/usbmodel_wake_test.go).
SPDX-License-Identifier: Apache-2.0
"""
import os
import sys
sys.path[:0] = [os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "emulator")]
import tc32emu as te  # noqa: E402
import usb_model as um  # noqa: E402

failures = []


def check(cond, text):
    print(("ok   " if cond else "FAIL ") + text)
    if not cond:
        failures.append(text)


def main():
    fl = te.Flash()
    fl.mem[8] = 0x4B
    m = te.Machine(fl, max_log=0)
    u = um.UsbModel(m)
    check(u.core_wake == os.environ.get("TC32EMU_USB_CORE_WAKE", "level"), f"the default is {u.core_wake}")
    m.analog[0x26] |= 0x10

    def k(on):
        u.k_state = u.k_edge = on

    for mode, want in (("level", [4, 4, 4]), ("edge", [4, 0, 0])):
        u.core_wake = mode
        m.regs_mem[0x6E] |= 0x04
        k(True)
        got = [m.wake_status() & 0x04 for _ in range(3)]
        check(got == want, f"{mode}: three sleeps during one K period wake {got}, expected {want}")
        k(False)
        check(m.wake_status() & 0x04 == 0, f"{mode}: no wake once K has ended")
        k(True)
        check(m.wake_status() & 0x04 == 4, f"{mode}: the next K period wakes again")
        k(False)
        m.regs_mem[0x6E] &= ~0x04
        k(True)
        check(m.wake_status() & 0x04 == 0, f"{mode}: no wake with 0x6e bit 2 clear")
        k(False)
    print(f"{len(failures)} failure(s)")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())

#!/usr/bin/env python3
"""The 32 kHz timer wake from suspend, on a machine that runs no code: with
the wake armed (0x74c, 0x74b bit 3) and enabled (analog 0x26 bit 5), the
chip wakes at the 32 kHz tick that reaches the wake value, not at the sleep
loop's next check (a wake up to 1 ms late falls out of the firmware's clock:
the SDK's early wake, 0x24b0 system ticks, then clamps its restored system
timer, and the kernel's clock drifts). The Go port has the same cases as a
test (go/tc32emu/timer_wake_test.go).

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


def sleep_for(m, ticks, limit_ms=50.0):
    """Suspend with the timer wake ticks 32 kHz ticks on; the ms slept, or None."""
    wake = (m.k32_now() + ticks) & 0xFFFFFFFF
    m.reg_write(0x74C, 4, wake)
    m.reg_write(0x74B, 1, 0x08)
    m.analog[0x26] = 0x20
    start = m.ms()
    m.reg_write(0x6F, 1, 0x81)
    end = m.cycles + int(limit_ms * m.cpu_hz / 1000)
    while m.asleep is not None and m.cycles < end:
        m.sleep_until(end)
    if m.asleep is not None:
        return None
    return m.ms() - start, wake


def main():
    fl = te.Flash()
    fl.mem[8] = 0x4B
    m = te.Machine(fl, max_log=0)
    tick_ms = 1 / 32.768
    for ticks in (1, 7, 33, 100, 491):
        m.cycles += 1234          # a different phase against the 32 kHz clock each time
        r = sleep_for(m, ticks)
        if r is None:
            check(False, f"{ticks} ticks: no wake within 50 ms")
            continue
        slept, wake = r
        reached = ((m.k32_now() - wake) & 0xFFFFFFFF) < 0x80000000
        before = ((m.k32_now() - 1 - wake) & 0xFFFFFFFF) >= 0x80000000 or m.k32_now() == wake
        check(reached and before and m.analog[0x44] & 0x02,
              f"{ticks} ticks: awake in the 32 kHz tick that reaches the wake value "
              f"(slept {slept:.4f} ms, at most {ticks * tick_ms:.4f})")
        check(slept <= ticks * tick_ms + 1e-6, f"{ticks} ticks: not later than {ticks} ticks ({slept:.4f} ms)")
        m.analog[0x44] = 0
    print(f"{len(failures)} failure(s)")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())

#!/usr/bin/env python3
"""Timer0/1's compare, on a machine that runs no code (registers written and
the cycles advanced by hand): a mode 0 timer matches when its count reaches
the capture and starts again from 0; a capture raised above the running count
is reached; a capture written at or below the running count, a count written
past the capture and a start with the count past it stop the run
(TC32EMU_TIMER_CAPTURE_BELOW=stop, the default), or, with "wrap", match only
when the 32-bit count has wrapped round (2^32 cycles on), as hardware showed; a
match between two updates is taken before a write is judged and is in what a
read of 0x620-0x637 returns; Timer0 as Timer1.
The Go port has the same cases as a test (go/tc32emu/timer_capture_test.go).
SPDX-License-Identifier: Apache-2.0
"""
import os
import sys
sys.path[:0] = [os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "emulator")]
import tc32emu as te  # noqa: E402

failures = []
SLOT = 24000  # 500 us at 48 MHz


def check(cond, text):
    print(("ok   " if cond else "FAIL ") + text)
    if not cond:
        failures.append(text)


def machine():
    fl = te.Flash()
    fl.mem[8] = 0x4B
    return te.Machine(fl, max_log=0)


def write(m, o, size, val):
    """reg_write; the error text when it stops the run, else None."""
    try:
        m.reg_write(o, size, val)
    except te.EmuError as e:
        return str(e)
    return None


def start_t1(m, capture, tick=0):
    """Timer1 stopped, count and capture set, started in mode 0 (as the SDK's timer1_set_mode)."""
    m.reg_write(0x620, 1, m.regs_mem[0x620] & ~0x38)
    m.reg_write(0x634, 4, tick)
    m.reg_write(0x628, 4, capture)
    return write(m, 0x620, 1, (m.regs_mem[0x620] & ~0x30) | 0x08)


def start_t0(m, capture, tick=0):
    """Timer0 as start_t1 (enable 0x620 bit 0, mode bits 2:1)."""
    m.reg_write(0x620, 1, m.regs_mem[0x620] & ~0x07)
    m.reg_write(0x630, 4, tick)
    m.reg_write(0x624, 4, capture)
    return write(m, 0x620, 1, (m.regs_mem[0x620] & ~0x06) | 0x01)


def run(m, cycles):
    m.cycles += cycles
    m.update_time()


def matched(m, n=1):
    """Timer n's status bit (Timer1's by default), cleared once read."""
    hit = bool(m.regs_mem[0x623] & (1 << n))
    m.reg_write(0x623, 1, 1 << n)
    return hit


def main():
    te.TIMER_CAPTURE_BELOW = "stop"
    m = machine()
    check(start_t1(m, SLOT) is None, "Timer1 started with capture 24000")
    run(m, SLOT - 1)
    check(not matched(m), "no match one cycle before the capture")
    run(m, 1)
    check(matched(m) and m.tmr_tick(1) == 0, "a match at the capture, and the count starts again from 0")
    run(m, 3 * SLOT)
    check(matched(m) and m.tmr_tick(1) == 0, "periodic: matches go on, the count at 0 after three periods")

    m = machine()
    start_t1(m, SLOT)
    run(m, 10000)
    check(write(m, 0x628, 4, 30000) is None, "a capture raised above the running count (10000 -> 30000) is accepted")
    run(m, 19999)
    check(not matched(m), "no match before the count reaches the raised capture")
    run(m, 1)
    check(matched(m), "a match at the raised capture")

    m = machine()
    start_t1(m, SLOT)
    run(m, 20000)
    err = write(m, 0x628, 4, 10000)
    check(err is not None and "wrapped" in err, f"a capture written below the running count stops the run: {err}")

    m = machine()
    start_t1(m, SLOT)
    run(m, 10000)
    err = write(m, 0x634, 4, 30000)
    check(err is not None and "count write" in err, "a count written past the capture while running stops the run")

    m = machine()
    err = start_t1(m, SLOT, tick=30000)
    check(err is not None and "a start" in err, "a start with the count past the capture stops the run")

    m = machine()
    m.reg_write(0x620, 1, 0)
    check(write(m, 0x628, 4, 5) is None, "a capture write while the timer is stopped is not checked")

    # The fault seen on hardware: a period stretched through the capture and set back
    # after a flash write held interrupts off for more than a period.
    m = machine()
    start_t1(m, SLOT)
    run(m, 10000)
    check(write(m, 0x628, 4, SLOT + 10000 + 960) is None, "stretched: capture 34960 while the count is 10000")
    run(m, SLOT + 960)
    check(matched(m) and m.tmr_tick(1) == 0, "the stretched period matches at 34960")
    run(m, 30000)
    err = write(m, 0x628, 4, SLOT)
    check(err is not None, "set back to 24000 when the count is 30000: stops the run")

    # A match between two updates (run() brings the timers up to date every
    # 32 instructions): the count passes the capture, and the capture is
    # written, or the timer stopped and started, before the next update.
    m = machine()
    start_t1(m, SLOT)
    run(m, SLOT - 5)
    m.cycles += 10
    check(write(m, 0x628, 4, SLOT) is None and matched(m) and m.tmr_tick(1) == 5,
          "a capture write 5 cycles after a match no update has seen: the match first, the count 5, no stop")
    m = machine()
    start_t1(m, SLOT)
    run(m, SLOT - 5)
    m.cycles += 10
    m.reg_write(0x620, 1, m.regs_mem[0x620] & ~0x38)
    check(matched(m) and m.tmr_tick(1) == 5, "a stop 5 cycles after a match no update has seen: the match set, the count kept 5")
    check(write(m, 0x620, 1, (m.regs_mem[0x620] & ~0x30) | 0x08) is None, "and a start from that count does not stop the run")
    m = machine()
    start_t1(m, SLOT)
    run(m, SLOT - 5)
    m.cycles += 10
    check(write(m, 0x634, 4, 1000) is None and matched(m) and m.tmr_tick(1) == 1000,
          "a count write 5 cycles after a match no update has seen: the match is taken, the count 1000")
    m = machine()
    start_t1(m, SLOT)
    run(m, SLOT - 5)
    m.cycles += 10
    check(write(m, 0x628, 4, 30000) is None and matched(m) and m.tmr_tick(1) == 5,
          "a capture raised to 30000 5 cycles after a match no update has seen: that match is reported, the count 5")
    m = machine()
    start_t1(m, SLOT)
    run(m, SLOT - 5)
    m.cycles += 10
    m.reg_write(0x620, 4, int.from_bytes(m.regs_mem[0x620:0x623], "little") | (0x02 << 24))
    run(m, 0)
    check(not matched(m) and m.tmr_tick(1) == 5,
          "a 32-bit 0x620 write clearing Timer1's status 5 cycles after a match no update has seen clears that match")

    # Reads catch up as writes do.
    m = machine()
    start_t1(m, SLOT)
    run(m, SLOT - 5)
    m.cycles += 10
    check(m.reg_read(0x634, 4) == 5, "a count read 5 cycles after a match no update has seen returns 5, not the capture + 5")
    m = machine()
    start_t1(m, SLOT)
    run(m, SLOT - 5)
    m.cycles += 10
    check(m.reg_read(0x623, 1) & 0x02, "a status read 5 cycles after a match no update has seen shows the match")

    # Timer0 as Timer1.
    m = machine()
    check(start_t0(m, SLOT) is None, "Timer0 started with capture 24000")
    run(m, SLOT - 1)
    check(not matched(m, 0), "Timer0: no match one cycle before the capture")
    run(m, 1)
    check(matched(m, 0) and m.tmr_tick(0) == 0 and not matched(m), "Timer0: a match at the capture, its own status bit")
    run(m, 20000)
    err = write(m, 0x624, 4, 10000)
    check(err is not None and "timer 0" in err, f"Timer0: a capture written below the running count stops the run: {err}")

    # The count is 32 bits: a timer that ran past 2^32 cycles with capture 0.
    m = machine()
    start_t1(m, 0)
    run(m, (1 << 32) + 5)
    check(write(m, 0x628, 4, 100) is None and m.tmr_tick(1) == 5,
          "capture 0, 2^32 + 5 cycles on: a capture of 100 is taken with the count at 5")
    run(m, 94)
    check(not matched(m), "no match before the count reaches 100")
    run(m, 1)
    check(matched(m) and m.tmr_tick(1) == 0, "the match at 100")

    te.TIMER_CAPTURE_BELOW = "wrap"
    m = machine()
    start_t1(m, SLOT)
    run(m, 20000)
    check(write(m, 0x628, 4, 10000) is None and any("wrapped" in t for _, t in m.events),
          "wrap: the write is taken and logged as an event")
    run(m, (1 << 32) - 20000 + 10000 - 1)
    check(not matched(m), "wrap: no match until the count has wrapped round (2^32 - 10001 cycles on)")
    run(m, 1)
    check(matched(m) and m.tmr_tick(1) == 0, "wrap: the match when the count reaches the capture after the wrap")
    run(m, 10000)
    check(matched(m), "wrap: periodic again after it")
    m = machine()
    start_t1(m, SLOT)
    run(m, 20000)
    write(m, 0x628, 4, 10000)
    run(m, (1 << 32) - 20000 + 5)
    check(write(m, 0x628, 4, 100) is None and m.tmr_tick(1) == 5,
          "wrap: a capture of 100 written when the wrapped count is 5 is taken")
    run(m, 94)
    check(not matched(m), "wrap: no match before the count reaches 100")
    run(m, 1)
    check(matched(m) and m.tmr_tick(1) == 0, "wrap: the match at 100")
    te.TIMER_CAPTURE_BELOW = "stop"

    print(f"{len(failures)} failure(s)")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())

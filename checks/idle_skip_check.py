#!/usr/bin/env python3
"""Machine.run's idle skip (idle_ranges): in an idle loop the run jumps ahead
to the next system timer compare, but only while an interrupt could be taken
(0x800643 bit 0 set and the CPSR's I bit clear). An interrupt that becomes
pending while they are off is taken as soon as the code turns them on, not
at the next compare.

Each case runs a few instructions from the flash at 0x100 with the idle range
over them; a hook at the IRQ vector (0x10) stops the run when the interrupt
is taken. The system timer compare is 1 s away, so a skip there shows as a
late interrupt.
- 0x800643 = 0 with an interrupt pending: the code counts down, then writes
  0x800643 = 1; the interrupt is taken at once.
- The CPSR's I bit set with an interrupt pending: the code counts down, then
  clears it (tmcsr); the interrupt is taken at once.
- Interrupts on, none pending, the compare 10 ms away: the loop is skipped to
  the compare (a few iterations of it run, not 10 ms of them) and the timer's
  interrupt is taken there; the cycles skipped are counted in idle_skipped,
  from which a caller takes the share of the time the CPU was not idle.
The Go port has the same cases as a test (go/tc32emu/idle_skip_test.go).
SPDX-License-Identifier: Apache-2.0
"""
import os
import sys
sys.path[:0] = [os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "emulator"),
                os.path.dirname(os.path.dirname(os.path.abspath(__file__)))]
import tc32emu as te  # noqa: E402
from tc32isa import to_tc32  # noqa: E402

failures = []
IDLE = [(0x100, 0x120)]
GPIO_SRC = 18   # a latched source (irq_latch), unmasked through 0x642 bit 2


def check(cond, text):
    print(("ok   " if cond else "FAIL ") + text)
    if not cond:
        failures.append(text)


def machine(code, words=()):
    """A machine whose flash holds code (Thumb halfwords, stored as TC32) at
    0x100, then the words, and b . at the IRQ vector."""
    fl = te.Flash()
    fl.mem[8] = 0x4B
    a = 0x100
    for hw in code:
        fl.mem[a:a + 2] = to_tc32(hw).to_bytes(2, "little")
        a += 2
    for w in words:
        fl.mem[a:a + 4] = w.to_bytes(4, "little")
        a += 4
    fl.mem[0x10:0x12] = to_tc32(0xE7FE).to_bytes(2, "little")
    m = te.Machine(fl, max_log=0)
    m.r[15] = 0x100
    m.set_cpsr(te.MODE_SVC)                      # I bit clear
    m.reg_write(0x74A, 1, te.STIMER_EN)          # the system timer on
    m.reg_write(0x748, 1, te.STIMER_IRQ)
    return m


def compare_in(m, seconds):
    m.reg_write(0x744, 4, (m.stimer_now() + int(seconds * te.STIMER_HZ)) & te.M32)


def run_to_irq(m, limit):
    """The cycle count when the interrupt is taken, or None."""
    def taken(mm):
        raise te.Stop("irq")
    m.hooks[0x10] = taken
    try:
        m.run(limit, IDLE)
    except te.Stop:
        return m.cycles
    return None


def main():
    # 0x100 movs r0, #200; subs r0, #1; bne .-2; ldr r1, =0x800643; movs r2, #1;
    # strb r2, [r1]; b .; (pad); .word 0x800643
    m = machine([0x20C8, 0x3801, 0xD1FD, 0x4902, 0x2201, 0x700A, 0xE7FE, 0x46C0], [0x00800643])
    m.reg_write(0x643, 1, 0)
    m.reg_write(0x642, 1, 1 << (GPIO_SRC - 16))
    m.irq_latch |= 1 << GPIO_SRC
    compare_in(m, 1.0)
    at = run_to_irq(m, 3 * te.CPU_HZ)
    check(at is not None and at < 20_000,
          f"0x800643 = 0, an interrupt pending: taken right after the code sets 0x800643 (cycle {at}), "
          f"not at the compare 1 s on")

    # 0x100 movs r0, #200; subs r0, #1; bne .-2; tmcsr r3; b .
    m = machine([0x20C8, 0x3801, 0xD1FD, 0xBBC3, 0xE7FE])
    m.set_cpsr(te.MODE_SVC | 0x80)               # I bit set
    m.r[3] = te.MODE_SVC                          # what tmcsr writes: I clear
    m.reg_write(0x643, 1, 1)
    m.reg_write(0x642, 1, 1 << (GPIO_SRC - 16))
    m.irq_latch |= 1 << GPIO_SRC
    compare_in(m, 1.0)
    at = run_to_irq(m, 3 * te.CPU_HZ)
    check(at is not None and at < 20_000,
          f"CPSR I set, an interrupt pending: taken right after the code clears I (cycle {at}), "
          f"not at the compare 1 s on")

    # 0x100 adds r0, #1; b .-2
    m = machine([0x3001, 0xE7FD])
    m.reg_write(0x643, 1, 1)
    m.reg_write(0x642, 1, 1 << (te.IRQ_STIMER - 16))
    compare_in(m, 0.010)
    at = run_to_irq(m, te.CPU_HZ)
    check(at is not None and at >= 0.009 * te.CPU_HZ and m.r[0] < 1000,
          f"interrupts on, none pending: the loop skipped to the compare 10 ms on "
          f"(taken at cycle {at}, {m.r[0]} loop iterations run)")
    check(at is not None and 0.9 * at <= m.idle_skipped <= at,
          f"the cycles skipped there are counted as the idle loop's (idle_skipped {m.idle_skipped} of {at})")

    print(f"{len(failures)} failure(s)")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())

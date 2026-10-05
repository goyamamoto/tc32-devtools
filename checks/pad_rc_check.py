#!/usr/bin/env python3
"""The optional pad capacitance model (TC32EMU_PAD_C_PF, Machine.pad_c_pf), on
a machine that is never run. A pad held high from outside and then left to
its pull-down reads high until 1.204 x R x C has passed (the RC curve from the
rail to 0.3 VDD), counted from the update that released it; with the 10 kOhm
pull-up the same in the other direction and ten times sooner; a pad whose own
output drives it, a level that is not the pull's own and the model switched
off are there at once. C is the caller's assumption. The Go port has the same
cases as a test (go/tc32emu/pad_rc_test.go).
SPDX-License-Identifier: Apache-2.0
"""
import os
import sys
sys.path[:0] = [os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "emulator")]
import tc32emu as te  # noqa: E402

failures = []
PORT, BIT = 2, 2          # PC2, a matrix row
IN, IE = te.REG_BASE + 0x590, 0xC0


def check(cond, text):
    print(("ok   " if cond else "FAIL ") + text)
    if not cond:
        failures.append(text)


def machine(pull, c_pf, r_pct=100):
    fl = te.Flash()
    fl.mem[8] = 0x4B
    m = te.Machine(fl, max_log=0)
    m.pad_c_pf, m.pad_r_pct = c_pf, r_pct
    m.analog[0x12] = pull << 4                      # PC2's pull
    m.analog[IE] = 0xFF
    m.regs_mem[0x590 + 6] = 0xFF                    # GPIO function
    m.regs_mem[0x590 + 2] = 0xFF                    # outputs off
    m.outside = None                                # the level something outside drives PC2 to, or None
    base = m.pad_levels

    def pad_levels(port):
        lvl, flt = base(port)
        if port == PORT and m.outside is not None:
            lvl = lvl & ~(1 << BIT) | m.outside << BIT
            flt &= ~(1 << BIT)
        return lvl, flt
    m.pad_levels = pad_levels
    return m


def read(m):
    return (m.read(IN, 1) >> BIT) & 1


def release_and_read(m, level, cycles):
    """Drive PC2 to the level from outside, release it, and read it `cycles` later."""
    m.outside = level
    m.hold_pads(PORT)
    m.outside = None
    m.hold_pads(PORT)                                # the board model's update: the pad is left to its pull
    m.cycles += cycles
    return read(m)


def main():
    hz = te.Machine(te.Flash(), boot_slot=0, max_log=0).cpu_hz
    ns = lambda cycles: cycles * 1_000_000_000 // hz   # noqa: E731
    need = 1204 * 100_000 * 40 // 1_000_000             # ns, 100 kOhm and 40 pF
    n = next(c for c in range(10_000) if ns(c) >= need)
    check(need == 4816, f"100 kOhm x 40 pF x 1.204 = {need} ns ({n} cycles at {hz // 1_000_000} MHz)")

    m = machine(2, 0)
    check(release_and_read(m, 1, 0) == 0, "model off: a row on its pull-down reads low at once")
    m = machine(2, 40)
    check(release_and_read(m, 1, 0) == 1, "40 pF, 100 kOhm down: still high right after the release")
    m = machine(2, 40)
    check(release_and_read(m, 1, n - 1) == 1, f"still high {n - 1} cycles later")
    m = machine(2, 40)
    check(release_and_read(m, 1, n) == 0, f"low {n} cycles later")
    check(read(m) == 0, "and it stays low")
    m = machine(2, 40)
    release_and_read(m, 1, 10)
    m.cycles += n - 10
    check(read(m) == 0, "the time runs from the release, not from the first read")
    m = machine(2, 60, 150)
    n2 = next(c for c in range(10_000) if ns(c) >= 1204 * 100_000 * 60 * 150 // 100 // 1_000_000)
    check(release_and_read(m, 1, n2 - 1) == 1 and read(machine_after(m, 1)) == 0,
          f"60 pF at 150 % of R: high until {n2} cycles")
    m = machine(2, 40)
    release_and_read(m, 1, 5)
    m.outside = 1
    check(read(m) == 1, "driven high again before the time has passed: high")
    m.outside = None
    m.hold_pads(PORT)
    m.cycles += n - 1
    check(read(m) == 1, "and the time starts again from that release")
    m.cycles += 1
    check(read(m) == 0, "low once it has passed")

    m = machine(3, 40)
    n3 = next(c for c in range(10_000) if ns(c) >= 1204 * 10_000 * 40 // 1_000_000)
    check(release_and_read(m, 0, n3 - 1) == 0, f"10 kOhm up, 40 pF: still low {n3 - 1} cycles after a release from low")
    m = machine(3, 40)
    check(release_and_read(m, 0, n3) == 1, f"high {n3} cycles later (a tenth of the 100 kOhm time)")
    m = machine(2, 40)
    release_and_read(m, 1, n)
    check(release_and_read(m, 0, 0) == 0, "at the pull's own level already: nothing to wait for")
    m = machine(2, 40)
    m.outside = 1
    m.hold_pads(PORT)
    m.outside = 0
    m.hold_pads(PORT)
    check(read(m) == 1, "driven from outside to the pull's own level: taken as released (the model cannot tell the two apart)")

    # The pad's own output: there at once, whatever the capacitance (a scan can drive its rows low).
    m = machine(2, 40)
    m.outside = 1
    m.hold_pads(PORT)
    m.outside = None
    m.write(te.REG_BASE + 0x590 + 3, 1, 0x00)       # output level low
    m.write(te.REG_BASE + 0x590 + 2, 1, 0xFF & ~(1 << BIT))   # output on
    check(read(m) == 0, "the pad's own output low: low at once")
    m.write(te.REG_BASE + 0x590 + 2, 1, 0xFF)       # output off again
    check(read(m) == 0, "and released from there it is already at the pull's level")
    # The GPIO interrupt (edge-triggered source 18) comes when the pad's read level changes,
    # not when the contact does: a knob contact on a pull-up opens, the pad rises after the RC time.
    m = machine(3, 40)
    m.regs_mem[0x5B5] |= 0x08                       # GPIO interrupt on
    m.regs_mem[0x590 + 7] = 1 << BIT                # PC2 asks for it
    m.regs_mem[0x590 + 4] = 0                       # on a high level
    m.outside = 0
    m.hold_pads(PORT)
    m.gpio_irq_update()
    m.irq_latch &= ~(1 << 18)
    m.outside = None                                # the contact opens
    m.gpio_irq_update()                             # what a board model does at the change
    check(read(m) == 0 and not m.irq_latch & (1 << 18), "contact open, pad still low: no interrupt yet")
    m.cycles += n3 - 1
    m.pad_rc_service()
    check(not m.irq_latch & (1 << 18), "one cycle before the time: none yet")
    check(0 <= m.pad_rc_due() <= 1, f"the pad is due in {m.pad_rc_due()} cycle(s)")
    m.cycles += 1
    m.pad_rc_service()
    check(read(m) == 1 and bool(m.irq_latch & (1 << 18)), "at the time the pad reads high and the interrupt is latched")
    check(m.pad_rc_due() is None, "no pad on its way any more")
    print(f"{len(failures)} failure(s)")
    return 1 if failures else 0


def machine_after(m, cycles):
    m.cycles += cycles
    return m


if __name__ == "__main__":
    sys.exit(main())

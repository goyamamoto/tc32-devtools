#!/usr/bin/env python3
"""The flash's busy time, on a machine that runs no code (the flash driven
through its SPI methods, the cycles advanced by hand): without
TC32EMU_FLASH_TPP_US/TSE_US/TBE32_US/TBE64_US a program or erase leaves WIP
(0x05 bit 0) clear; with them WIP stays set that long after a page program, a
sector erase and a 32 KB and a 64 KB block erase that the part executes, not
after one the protection ignores; the time goes on through a chip reset
(Flash.abort(), Machine.reset(), whose clock starts again at 0 and at the
boot clock), a CPU clock change and sleep, and a power-on (a new Machine)
finds the part idle; an XIP read, and any command but a status read, while
the part is busy stops the run. The Go port has the same cases as a test
(go/tc32emu/flash_busy_test.go).
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


def spi(fl, *data, read=0):
    got = []
    fl.select(True)
    for b in data:
        fl.clock(b)
    for _ in range(read):
        got.append(fl.clock(0))
    fl.select(False)
    return got


def wip(fl):
    return spi(fl, 0x05, read=1)[0] & 0x01


def program(fl, addr, *data):
    spi(fl, 0x06)
    spi(fl, 0x02, addr >> 16 & 0xFF, addr >> 8 & 0xFF, addr & 0xFF, *data)


def erase(fl, addr, cmd):
    spi(fl, 0x06)
    spi(fl, cmd, addr >> 16 & 0xFF, addr >> 8 & 0xFF, addr & 0xFF)


def advance_us(m, us):
    m.cycles += int(us * m.cpu_hz / 1_000_000)


def error_of(f, *args, **kw):
    """The error text when f stops the run, else None."""
    try:
        f(*args, **kw)
    except te.EmuError as e:
        return str(e)
    return None


def xip_error(m, addr):
    try:
        m.read(addr, 4)
    except te.EmuError as e:
        return str(e)
    return None


def main():
    te.FLASH_TPP_US = te.FLASH_TSE_US = te.FLASH_TBE32_US = te.FLASH_TBE64_US = 0
    fl = te.Flash(size=0x80000, jedec=(0xC8, 0x60, 0x14))
    fl.mem[8] = 0x4B
    m = te.Machine(fl, max_log=0)
    program(fl, 0x40000, 0x12)
    check(fl.mem[0x40000] == 0x12 and not wip(fl), "no busy times: a program is done at once, WIP clear")

    te.FLASH_TPP_US, te.FLASH_TSE_US, te.FLASH_TBE32_US, te.FLASH_TBE64_US = 1600, 150000, 500000, 800000
    program(fl, 0x40001, 0x34)
    check(wip(fl), "TPP 1600 us: WIP set right after a page program")
    advance_us(m, 1590)
    check(wip(fl), "WIP still set 1590 us on")
    advance_us(m, 20)
    check(not wip(fl), "WIP clear 1610 us on")

    erase(fl, 0x41000, 0x20)
    advance_us(m, 149000)
    check(wip(fl), "TSE 150 ms: WIP set 149 ms after a sector erase")
    advance_us(m, 2000)
    check(not wip(fl), "and clear at 151 ms")

    for cmd, name, ms in ((0xD8, "64 KB", 800), (0x52, "32 KB", 500)):
        erase(fl, 0x50000, cmd)
        advance_us(m, (ms - 1) * 1000)
        busy_then = wip(fl)
        advance_us(m, 2000)
        check(busy_then and not wip(fl), f"TBE {ms} ms: a {name} block erase busy {ms - 1} ms on, clear at {ms + 1} ms")

    spi(fl, 0x06)
    spi(fl, 0x01, 0x1C)  # the whole 512 KB protected (the SDK's table for 0x1360c8)
    program(fl, 0x60000, 0x00)
    check(fl.log[-1] == ("protected", 0x60000, 1) and not wip(fl), "a program the protection ignores leaves WIP clear")
    spi(fl, 0x06)
    spi(fl, 0x01, 0x00)

    program(fl, 0x40002, 0x56)
    err = xip_error(m, 0x10000)
    check(err is not None and "busy" in err, f"an XIP read while the part is busy stops the run: {err}")
    fl.abort()
    check(wip(fl), "a chip reset (abort) leaves the part busy")
    advance_us(m, 2000)
    check(xip_error(m, 0x10000) is None and not wip(fl), "the XIP read goes through once the part is done")

    # Machine.reset() starts the clock again at 0; the part goes on.
    program(fl, 0x40003, 0x78)
    advance_us(m, 1000)
    m.reset()
    check(m.ms() == 0 and wip(fl), "a reset 1000 us into a page program: the clock at 0, WIP still set")
    advance_us(m, 590)
    check(wip(fl), "WIP still set 590 us after the reset")
    advance_us(m, 20)
    check(not wip(fl), "WIP clear 610 us after the reset (1610 us after the program)")

    # A clock change: the time left is converted, not the cycles kept.
    advance_us(m, 2000)
    program(fl, 0x40005, 0xBC)
    advance_us(m, 800)
    m.reg_write(0x66, 1, 0x20)   # 24 -> 48 MHz
    check(m.cpu_hz == 48_000_000, "the CPU clock at 48 MHz, 800 us into a page program")
    advance_us(m, 790)
    check(wip(fl), "WIP still set 790 us after the clock change (1590 us after the program)")
    advance_us(m, 20)
    check(not wip(fl), "WIP clear 810 us after the clock change (1610 us after the program)")

    # A reset at 48 MHz: the clock starts again at 24 MHz; the time left is kept.
    program(fl, 0x40006, 0xDE)
    advance_us(m, 1000)
    m.reset()
    check(m.cpu_hz == 24_000_000 and wip(fl), "a reset at 48 MHz 1000 us into a page program: 24 MHz, WIP still set")
    advance_us(m, 590)
    check(wip(fl), "WIP still set 590 us after the reset")
    advance_us(m, 20)
    check(not wip(fl), "WIP clear 610 us after the reset")

    # Sleep: the part goes on while the chip is suspended.
    program(fl, 0x40007, 0xF0)
    m.enter_suspend(0x81)        # no wake source enabled: it sleeps until run()'s limit
    m.run(m.cycles + 1590 * m.cpu_hz // 1_000_000)
    check(m.asleep is not None and wip(fl), "WIP still set after 1590 us of sleep")
    m.run(m.cycles + 20 * m.cpu_hz // 1_000_000)
    m.asleep = None
    check(not wip(fl), "WIP clear after 1610 us of sleep")

    # While busy the part takes status reads only.
    program(fl, 0x40008, 0x11)
    check(wip(fl) and error_of(spi, fl, 0x35, read=1) is None, "while busy: 0x05 and 0x35 are taken")
    err = error_of(spi, fl, 0x02, 0x04, 0x00, 0x09, 0x22)
    fl.select(False)
    check(err is not None and "command 0x02" in err and "us left" in err, f"while busy: a page program stops the run: {err}")
    m.reg_write(0x0D, 1, 0x00)   # the MSPI's chip select low
    m.reg_write(0x0C, 1, 0x03)
    err = error_of(m.reg_write, 0x0C, 1, 0x00)
    error_of(m.reg_write, 0x0D, 1, 0x01)
    check(err is not None and "command 0x03" in err, f"while busy: a read (0x03) through the MSPI stops the run: {err}")
    err = error_of(spi, fl, 0x9F, read=3)
    fl.select(False)
    check(err is not None and "command 0x9f" in err, f"while busy: a JEDEC ID read stops the run: {err}")
    err = error_of(spi, fl, 0x06)
    check(err is not None and "command 0x06" in err, f"while busy: a write enable stops the run: {err}")
    advance_us(m, 2000)
    check(error_of(spi, fl, 0x06) is None and error_of(spi, fl, 0x04) is None, "the write enable is taken once the part is done")

    program(fl, 0x40004, 0x9A)
    te.Machine(fl, max_log=0)
    check(not wip(fl), "a power-on (a new Machine on the flash) finds the part idle")

    te.FLASH_TPP_US = te.FLASH_TSE_US = te.FLASH_TBE32_US = te.FLASH_TBE64_US = 0
    print(f"{len(failures)} failure(s)")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())

#!/usr/bin/env python3
"""The flash model's status register and block protection, on flash objects
that no machine runs: the register is written by 0x01 (one or two bytes) and
0x31, read back by 0x05 (with WEL in bit 1) and 0x35, and kept; the BP bits
protect the range the SDK's table for the part lists (tc_ble_single_sdk
drivers/B87/flash/flash_mid<mid>.h), where a page program or an erase changes
nothing and is logged as ("protected", addr, len); a value the table does not
list is refused; a part whose mid has no table keeps the value and protects
nothing. The Go port has the same cases as a test (go/tc32emu/flash_lock_test.go).
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
    """Clock the bytes in, then `read` more; (bytes read, error text or None)."""
    got = []
    try:
        fl.select(True)
        for b in data:
            fl.clock(b)
        for _ in range(read):
            got.append(fl.clock(0))
        fl.select(False)
    except te.EmuError as e:
        fl.cs_low, fl.tx = False, []
        return got, str(e)
    return got, None


def status(fl):
    return spi(fl, 0x05, read=1)[0][0] | (spi(fl, 0x35, read=1)[0][0] << 8)


def program(fl, addr, *data):
    spi(fl, 0x06)
    return spi(fl, 0x02, addr >> 16 & 0xFF, addr >> 8 & 0xFF, addr & 0xFF, *data)[1]


def erase(fl, addr, cmd=0x20):
    spi(fl, 0x06)
    return spi(fl, cmd, addr >> 16 & 0xFF, addr >> 8 & 0xFF, addr & 0xFF)[1]


def write_status(fl, *data, cmd=0x01):
    spi(fl, 0x06)
    return spi(fl, cmd, *data)[1]


def main():
    # GigaDevice GD25LD40C (mid 0x1360c8, 512 KB): a part whose table lists 0x18.
    fl = te.Flash(size=0x80000, jedec=(0xC8, 0x60, 0x14))
    check(fl.mid == 0x1360C8, f"512 KB GD: mid 0x{fl.mid:06x}")
    check(status(fl) == 0 and fl.protected_range() is None, "status 0 after power-on: nothing protected")
    spi(fl, 0x06)
    check(spi(fl, 0x05, read=1)[0] == [0x02], "0x05 after write enable: WEL (bit 1)")
    spi(fl, 0x04)
    err = write_status(fl, 0x18)
    check(err is None and status(fl) == 0x18 and fl.protected_range() == (0, 0x3FFFF),
          f"status 0x18 written (a one-byte lock): read back 0x{status(fl):02x}, the low 256 KB protected")
    check(fl.log[-1] == ("status", 0x01, "18"), f"logged as {fl.log[-1]}")
    err = program(fl, 0x3F000, 0x00)
    check(err is None and fl.mem[0x3F000] == 0xFF and fl.log[-1] == ("protected", 0x3F000, 1),
          f"program at 0x3f000 (slot B): nothing written, logged {fl.log[-1]}")
    err = program(fl, 0x00008, 0xFF, 0x00)
    check(err is None and fl.mem[0x9] == 0xFF and fl.log[-1] == ("protected", 0x8, 2),
          "program of the boot flag at 8 (slot A): nothing written, logged protected")
    err = erase(fl, 0x20000)
    check(err is None and fl.log[-1] == ("erase", 0x20000, 0x1000) or fl.log[-1] == ("protected", 0x20000, 0x1000),
          "erase at 0x20000 logged")
    check(fl.log[-1] == ("protected", 0x20000, 0x1000), "erase at 0x20000 (slot B): ignored, logged protected")
    err = erase(fl, 0x3F000, cmd=0xD8)
    check(err is None and fl.log[-1] == ("protected", 0x30000, 0x10000),
          "64 KB block erase at 0x30000-0x3ffff: ignored, logged protected")
    err = program(fl, 0x40000, 0x5A)
    check(err is None and fl.mem[0x40000] == 0x5A and fl.log[-1] == ("program", 0x40000, 1),
          "program at 0x40000 (a settings sector): written")
    err = erase(fl, 0x68000)
    check(err is None and fl.log[-1] == ("erase", 0x68000, 0x1000), "erase at 0x68000 (the boot guard's sector): erased")
    fl.mem[0x3F000] = 0x00
    err = erase(fl, 0x3F000, cmd=0x52)
    check(err is None and fl.mem[0x3F000] == 0x00 and fl.log[-1] == ("protected", 0x38000, 0x8000),
          "32 KB block erase at 0x38000-0x3ffff: ignored, logged protected")
    check(spi(fl, 0x05, read=1)[0] == [0x18], "WEL is clear after an ignored program (0x05 = 0x18)")
    err = write_status(fl, 0x1C)
    check(err is None and fl.protected_range() == (0, 0x7FFFF), "status 0x1c: all 512 KB protected")
    err = program(fl, 0x40000, 0x00)
    check(err is None and fl.mem[0x40000] == 0x5A, "program at 0x40000 under 0x1c: nothing written")
    err = write_status(fl, 0x00)
    check(err is None and status(fl) == 0 and fl.protected_range() is None, "status 0x00 (the SDK's unlock): nothing protected")
    err = program(fl, 0x3F000, 0x00)
    check(err is None and fl.mem[0x3F000] == 0x00 and fl.log[-1] == ("program", 0x3F000, 1),
          "program at 0x3f000 after the unlock: written")
    err = write_status(fl, 0x18)
    check(status(fl) == 0x18, "locked again")
    err = spi(fl, 0x01, 0x00)[1]
    check(err is None and status(fl) == 0x18, "0x01 without write enable: the status stays 0x18")
    err = write_status(fl, 0x18 | 0x02 | 0x01)
    check(status(fl) == 0x18, "WIP and WEL bits in the written byte are not stored")
    err = write_status(fl, 0x18, 0x02)
    check(err is None and status(fl) == 0x0218 and fl.log[-1] == ("status", 0x01, "1802"),
          "two bytes with 0x01: the high byte kept and read by 0x35")
    err = write_status(fl, 0x00, cmd=0x31)
    check(err is None and status(fl) == 0x0018, "0x31 writes the high byte only")
    write_status(fl, 0x00)

    # GD25LD80C (mid 0x1460c8, 1 MB): 0x18 protects the low 768 KB there.
    fl = te.Flash(jedec=(0xC8, 0x60, 0x14))
    check(fl.mid == 0x1460C8, f"1 MB GD: mid 0x{fl.mid:06x}")
    write_status(fl, 0x18)
    check(fl.protected_range() == (0, 0xBFFFF), "status 0x18 on the 1 MB part: the low 768 KB protected (0x40000 too)")
    err = program(fl, 0x40000, 0x00)
    check(fl.mem[0x40000] == 0xFF and fl.log[-1] == ("protected", 0x40000, 1), "program at 0x40000 ignored there")
    write_status(fl, 0x14)
    check(fl.protected_range() == (0, 0xDFFFF), "status 0x14: the low 896 KB")

    # Zbit ZB25WD40B (mid 0x13325e): the same table.
    fl = te.Flash(size=0x80000, jedec=(0x5E, 0x32, 0x14))
    write_status(fl, 0x18)
    check(fl.mid == 0x13325E and fl.protected_range() == (0, 0x3FFFF), "Zbit 512 KB: 0x18 protects the low 256 KB")

    # Zbit ZB25WD40C: the B part's ID, told by SFDP (0x5a at 0 reads 0x53); 16-bit status, mask 0x407c.
    fl = te.Flash(size=0x80000, jedec=(0x5E, 0x32, 0x14))
    got, _ = spi(fl, 0x5A, 0x00, 0x00, 0x00, 0x00, read=4)
    check(got == [0xFF] * 4 and fl.part == 0x13325E, f"Zbit B: SFDP reads {bytes(got).hex()} (none), part 0x{fl.part:06x}")
    fl = te.Flash(size=0x80000, jedec=(0x5E, 0x32, 0x14), sfdp=te.SFDP_SIGNATURE)
    got, _ = spi(fl, 0x5A, 0x00, 0x00, 0x00, 0x00, read=4)
    check(got == [0x53, 0x46, 0x44, 0x50] and fl.part == 0x0113325E, f"Zbit C: SFDP reads {bytes(got).hex()}, part 0x{fl.part:06x}")
    got, _ = spi(fl, 0x5A, 0x00, 0x00, 0x02, 0x00, read=3)
    check(got == [0x44, 0x50, 0xFF], "SFDP from address 2: 44 50 then 0xff beyond the data")
    err = write_status(fl, 0x2C, 0x00)
    check(err is None and status(fl) == 0x002C and fl.protected_range() == (0, 0x3FFFF), "C part 0x002c: the low 256 KB")
    err = write_status(fl, 0x10)
    check(err is None and fl.protected_range() == (0, 0x7FFFF), "C part 0x0010: all 512 KB")
    err = write_status(fl, 0x18)
    check(err is not None and "113325e" in err, f"C part 0x0018 (a one-byte lock): not in the SDK's table, refused ({err})")
    err = write_status(fl, 0x04, 0x40)
    check(err is None and status(fl) == 0x4004 and fl.protected_range() == (0, 0x6FFFF), "C part 0x4004: the low 448 KB")
    err = write_status(fl, 0x00, 0x00)
    check(err is None and fl.protected_range() is None, "C part 0x0000: nothing protected")
    fl = te.Flash(jedec=(0x5E, 0x32, 0x14), sfdp=te.SFDP_SIGNATURE)
    write_status(fl, 0x1C)
    check(fl.part == 0x0114325E and fl.protected_range() == (0, 0xFFFFF), "ZB25WD80C 0x001c: all 1 MB")
    write_status(fl, 0x10)
    check(fl.protected_range() == (0x80000, 0xFFFFF), "ZB25WD80C 0x0010: the upper 512 KB")

    # Puya P25Q80 (mid 0x146085, the emulator's default ID): 16-bit values.
    fl = te.Flash()
    check(fl.mid == 0x146085, f"default ID: mid 0x{fl.mid:06x}")
    err = write_status(fl, 0x2C)
    check(err is None and fl.protected_range() == (0, 0x3FFFF), "0x002c: the low 256 KB")
    err = write_status(fl, 0x0C, 0x40)
    check(err is None and status(fl) == 0x400C and fl.protected_range() == (0, 0xBFFFF), "0x400c (two bytes): the low 768 KB")
    err = write_status(fl, 0x18)
    check(err is not None and "does not list" in err, f"0x18 on the Puya part: refused ({err})")
    err = write_status(fl, 0x00)
    check(err is None and status(fl) == 0x4000 and fl.protected_range() == (0, 0xFFFFF),
          "one byte 0x00 after 0x400c leaves 0x4000: all protected (the SDK's Puya unlock writes both bytes)")
    err = write_status(fl, 0x00, 0x00)
    check(err is None and fl.protected_range() is None, "0x0000 (two bytes): nothing protected")
    fl = te.Flash(size=0x80000)
    err = write_status(fl, 0x04, 0x40)
    check(fl.mid == 0x136085 and err is None and fl.protected_range() == (0, 0x6FFFF), "P25Q40 0x4004: the low 448 KB")

    # A part without a table (Winbond's manufacturer byte): the value kept, nothing protected.
    fl = te.Flash(jedec=(0xEF, 0x40, 0x14))
    err = write_status(fl, 0x1C)
    check(err is None and status(fl) == 0x1C and fl.protected_range() is None, "unknown mid 0x1440ef: status kept, nothing protected")
    err = program(fl, 0x1000, 0x00)
    check(fl.mem[0x1000] == 0x00, "program there: written")

    # An XIP read while the chip select is low: the flash is busy with an SPI transaction
    # (the SDK: a program's data "must not reside at flash").
    fl = te.Flash(jedec=(0xC8, 0x60, 0x14))
    fl.mem[8] = 0x4B
    fl.mem[0x10000:0x10004] = b"\x11\x22\x33\x44"
    m = te.Machine(fl, max_log=0)
    check(m.read(0x10000, 4) == 0x44332211, "XIP read with the chip select high: served")
    m.write(te.REG_BASE + 0x0D, 1, 0x00)                     # chip select low, nothing sent (a loader idle)
    check(m.read(0x10000, 4) == 0x44332211, "XIP read with the chip select low and no command sent: served")
    m.write(te.REG_BASE + 0x0C, 1, 0x02)                     # a page program open
    try:
        m.read(0x10000, 4)
        err = None
    except te.EmuError as e:
        err = str(e)
    check(err is not None and "chip select low" in err and "0x02" in err, f"XIP read with a page program underway: refused ({err})")
    m.write(te.REG_BASE + 0x0D, 1, 0x01)
    m.write(te.REG_BASE + 0x0D, 1, 0x00)
    m.write(te.REG_BASE + 0x0C, 1, 0xAB)                     # a one-byte command (a startup may send one)
    check(m.read(0x10000, 4) == 0x44332211, "XIP read after a bare one-byte command (0xab): served")
    m.write(te.REG_BASE + 0x0D, 1, 0x01)                     # chip select high
    check(m.read(0x10000, 4) == 0x44332211, "served again once the chip select is high")

    # on_write: called with each program, erase and protected refusal, as logged.
    fl = te.Flash(size=0x80000, jedec=(0xC8, 0x60, 0x14))
    seen = []
    fl.on_write = lambda op, addr, n: seen.append((op, addr, n))
    program(fl, 0x40000, 0x12, 0x34)
    erase(fl, 0x41000)
    write_status(fl, 0x18)
    program(fl, 0x1000, 0x00)
    erase(fl, 0x3F000, cmd=0xD8)
    check(seen == [("program", 0x40000, 2), ("erase", 0x41000, 0x1000), ("protected", 0x1000, 1),
                   ("protected", 0x30000, 0x10000)],
          f"on_write: program, erase and protected with the log's values, no status write: {seen}")
    check(seen == [op for op in fl.log if op[0] != "status"], "on_write's calls equal the log's program/erase/protected entries")

    print(f"{len(failures)} failure(s)")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())

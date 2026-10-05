#!/usr/bin/env python3
"""The emulator's flash as a part smaller than 1 MB (TC32EMU_FLASH_SIZE).

With TC32EMU_FLASH_SIZE=0x80000 the flash model is a 512 KB part: the JEDEC
ID's capacity byte is 0x13, and what lies at or above 0x80000 is refused: an
SPI read, a page program and an erase with such an address, an XIP access
there (from either boot slot: above the running image an XIP address is the flash's own). Below the size nothing changes.
Without the variable the part has 1 MB and these cases are accepted. In
both halves the startup loader (register 0x608) loads into SRAM
and is refused an offset past the SRAM's end.

The cases run on a machine that is never run. Reads go through the MSPI
registers as the SDK's flash_mspi_read_ram() drives them (command and
address to 0x0c, one more write to clock the first byte, auto mode, then one
read of 0x0c per byte, each of which clocks the next byte): a read that ends
at the part's last byte clocks the byte after it without taking it, and must
pass. Programs and erases are clocked into the flash model; the XIP accesses
go through Machine.read().
Each half runs in its own interpreter, since the size is read when tc32emu
is imported.

Usage: flash_size_check.py            (both halves)
       flash_size_check.py --half     (one half, with the environment as it is)

SPDX-License-Identifier: Apache-2.0
"""
import os
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path[:0] = [os.path.join(os.path.dirname(HERE), "emulator")]

failures = []


def check(cond, text):
    print(("ok   " if cond else "FAIL ") + text)
    if not cond:
        failures.append(text)


def half():
    import tc32emu as te
    small = te.FLASH_SIZE < te.FLASH_MEM
    size = te.FLASH_SIZE
    print(f"== flash size 0x{size:x}" + (" (TC32EMU_FLASH_SIZE)" if small else " (the default)"))

    def flash(slot=0):
        fl = te.Flash()
        fl.mem[slot + 8] = 0x4B
        return fl

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

    def read(m, addr, n):
        """flash_mspi_read_ram(0x03, addr, ...) for n bytes: (bytes taken, error text or None)."""
        reg = te.REG_BASE
        got = []
        m.write(reg + 0x0D, 1, 0x00)                     # chip select low
        for b in (0x03, addr >> 16 & 0xFF, addr >> 8 & 0xFF, addr & 0xFF, 0x00):
            m.write(reg + 0x0C, 1, b)                    # the last write clocks the first data byte
        m.write(reg + 0x0D, 1, 0x0A)                     # auto mode
        err = None
        try:
            for _ in range(n):
                got.append(m.read(reg + 0x0C, 1))
        except te.EmuError as e:
            err = str(e)
        m.write(reg + 0x0D, 1, 0x01)                     # chip select high
        return got, err

    def refused(err):
        return err is not None and f"0x{size:x} bytes" in err

    fl = flash()
    check(fl.size == size and len(fl.mem) == te.FLASH_MEM,
          f"the part has 0x{fl.size:x} bytes; the model's array keeps 0x{len(fl.mem):x}")
    got, _ = spi(fl, 0x9F, read=3)
    check(got == [0x85, 0x60, size.bit_length() - 1], f"JEDEC ID {bytes(got).hex()}: capacity byte 0x{got[2]:02x}")
    fl.mem[0x7FFFE:0x80002] = b"\x11\x22\x33\x44"
    top = 0x80000 if small else 0x100000
    # Reads, through the MSPI registers.
    m = te.Machine(fl, max_log=0)
    got, err = read(m, 0x7FFFE, 2)
    check(got == [0x11, 0x22] and err is None,
          "read 0x7fffe-0x7ffff (the part's last bytes; 0x80000 is clocked and dropped): 11 22"
          + ("" if err is None else f" ({err})"))
    got, err = read(m, 0x80000, 1)
    check(refused(err) if small else (got == [0x33] and err is None),
          "read 0x80000: " + ("refused" if small else "33") + ("" if err is None else f" ({err})"))
    got, err = read(m, 0x7FFFE, 3)
    check((refused(err) and got == [0x11, 0x22]) if small else got == [0x11, 0x22, 0x33],
          "read of 3 bytes from 0x7fffe: " + ("the third refused" if small else "11 22 33"))
    # TC32EMU_FLASH_BEYOND: a taken read beyond the part gives 0xff or wraps, and is logged.
    if small:
        n0 = len(fl.log)
        fl.beyond_mode = "ff"
        got, err = read(m, 0x7FFFE, 4)
        check(got == [0x11, 0x22, 0xFF, 0xFF] and err is None, f"beyond = ff: 4 bytes from 0x7fffe read {bytes(got).hex()}")
        fl.mem[0:2] = b"\xa1\xa2"
        fl.beyond_mode = "wrap"
        got, err = read(m, 0x7FFFE, 4)
        check(got == [0x11, 0x22, 0xA1, 0xA2] and err is None, f"beyond = wrap: they read {bytes(got).hex()} (the part's first bytes)")
        got, err = read(m, 0xFFFFFB, 4)
        check(got == list(fl.mem[0x7FFFB:0x7FFFF]) and err is None, "beyond = wrap: 0xfffffb reads the bytes at 0x7fffb")
        check([op for op in fl.log[n0:] if op[0] == "read beyond"] ==
              [("read beyond", 0x80000, 1), ("read beyond", 0x80001, 1), ("read beyond", 0x80000, 1), ("read beyond", 0x80001, 1),
               ("read beyond", 0xFFFFFB, 1), ("read beyond", 0xFFFFFC, 1), ("read beyond", 0xFFFFFD, 1), ("read beyond", 0xFFFFFE, 1)],
              "each taken byte beyond the part is logged")
        del fl.log[n0:]
        fl.mem[0:2] = b"\xff\xff"
        fl.beyond_mode = "stop"
    # Programs and erases.
    spi(fl, 0x06)
    _, err = spi(fl, 0x02, 0x07, 0xF0, 0x00, 0xA5)
    check(err is None and fl.mem[0x7F000] == 0xA5, "program 0x7f000: written")
    before = bytes(fl.mem[0x80000:0x80100])
    spi(fl, 0x06)
    _, err = spi(fl, 0x02, 0x08, 0x00, 0x10, 0x00)
    check((refused(err) and bytes(fl.mem[0x80000:0x80100]) == before) if small
          else (err is None and fl.mem[0x80010] == 0x00),
          "program 0x80010: " + ("refused, nothing written" if small else "written"))
    fl.wel = False
    spi(fl, 0x06)
    _, err = spi(fl, 0x20, 0x07, 0xF0, 0x00)
    check(err is None and fl.mem[0x7F000] == 0xFF, "erase 0x7f000: erased")
    fl.mem[0x80000] = 0x5A
    spi(fl, 0x06)
    _, err = spi(fl, 0x20, 0x08, 0x00, 0x00)
    check((refused(err) and fl.mem[0x80000] == 0x5A) if small else (err is None and fl.mem[0x80000] == 0xFF),
          "erase 0x80000: " + ("refused, nothing erased" if small else "erased"))
    ops = [(op, a) for op, a, _n in fl.log]
    check(all(a < top for _op, a in ops), f"the flash's log holds no write at or above 0x{top:x}: {ops}")
    # XIP, from slot A and from slot B.
    for slot in (0x0, 0x20000):
        fl = flash(slot)
        fl.mem[0x7FFFC:0x80004] = bytes(range(8))
        m = te.Machine(fl, max_log=0)
        last = 0x80000 - 4       # at or above twice the slot's offset an XIP address is the flash's own
        try:
            v, err = m.read(last, 4), None
        except te.EmuError as e:
            v, err = None, str(e)
        check(v == 0x03020100, f"boot slot 0x{slot:x}: XIP read 0x{last:x} (flash 0x7fffc) = 0x{v:08x}"
              if v is not None else f"boot slot 0x{slot:x}: XIP read 0x{last:x}: {err}")
        try:
            v, err = m.read(last + 4, 4), None
        except te.EmuError as e:
            v, err = None, str(e)
        check(err is not None if small else v == 0x07060504,
              f"boot slot 0x{slot:x}: XIP read 0x{last + 4:x} (flash 0x80000): "
              + ("refused" if err is not None else f"0x{v:08x}") + (f" ({err})" if err else ""))
        # The startup loader's register (0x608): bit 24 starts a load of 16 bytes of flash into SRAM at the
        # offset in bits 23:0. SRAM is smaller than any flash, so the SRAM's end is the bound it can reach.
        fl.mem[slot + 0x1000:slot + 0x1010] = bytes(range(0x40, 0x50))
        m.write(te.REG_BASE + 0x608, 4, (1 << 24) | 0x1000)
        check(bytes(m.sram[0x1000:0x1010]) == bytes(range(0x40, 0x50)),
              f"boot slot 0x{slot:x}: loader load of offset 0x1000: SRAM 0x841000 holds flash 0x{slot + 0x1000:x}")
        try:
            m.write(te.REG_BASE + 0x608, 4, (1 << 24) | te.SRAM_SIZE)
            err = None
        except te.EmuError as e:
            err = str(e)
        check(err is not None and len(m.sram) == te.SRAM_SIZE,
              f"boot slot 0x{slot:x}: loader load of offset 0x{te.SRAM_SIZE:x}: refused, SRAM keeps its size")
    print(f"{len(failures)} failure(s)")
    return 1 if failures else 0


def main():
    if "--half" in sys.argv:
        return half()
    rc = 0
    for value in ("0x80000", None):
        env = dict(os.environ)
        env.pop("TC32EMU_FLASH_SIZE", None)
        if value:
            env["TC32EMU_FLASH_SIZE"] = value
        rc |= subprocess.run([sys.executable, "-B", os.path.abspath(__file__), "--half"], env=env).returncode
    print("flash_size_check: " + ("FAIL" if rc else "PASS"))
    return rc


if __name__ == "__main__":
    sys.exit(main())

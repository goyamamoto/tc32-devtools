#!/usr/bin/env python3
"""The SPI master model and its device (SPI_DEVICE, registers 0xffd8 and
0xffdc), and the ADC's pin input codes (ADC_PIN_CODE, register 0xffe4), on
a machine that runs no code: the register writes and reads below, a trace
of every value read, and a SHA-256 of that trace, which the Go port's test
(go/tc32emu/spi_test.go) reproduces: both engines give the same values.

What is checked: the device takes the octets written to 0x08 while its chip
select (PD5, active high) is active, and answers each with the octet before
it (0xff first in a transaction); 0x09 bit 6 reads 1 for 16 * (divider + 1)
cycles after a write; a write while busy is an error; a read of 0x08 with
0x09 bit 3 clocks the next octet in without giving the device one when DO is
disabled; with CK's GPIO function back on no octet reaches the device and
0x08 reads 0; the transactions and octets counted; the octets taken back in
order. A pin input's code from 0xffe4, and back to adc_code.

SPDX-License-Identifier: Apache-2.0
"""
import hashlib
import os
import struct
import sys

sys.path[:0] = [os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "emulator")]
import tc32emu as te  # noqa: E402

DIGEST = "f42f855764c2c592b83c8f7e062ddf52850259c7116b35c7ec5f5a38f22d7ae4"
failures = []


def check(cond, text):
    print(("ok   " if cond else "FAIL ") + text)
    if not cond:
        failures.append(text)


def machine():
    fl = te.Flash()
    fl.mem[8] = 0x4B
    return te.Machine(fl, max_log=0)


def setbits(m, o, mask, v):
    m.reg_write(o, 1, (m.reg_read(o, 1) & ~mask & 0xFF) | (v & mask))


def scenario(m):
    """The register accesses; returns the trace of the values read."""
    trace = []

    def rd(o, size=1):
        v = m.reg_read(o, size)
        trace.append(v)
        return v

    m.reg_write(0x60, 1, 0x00)
    m.reg_write(0x63, 1, 0x01)
    m.reg_write(0xFFD8, 4, 1 << 24 | 0x1D << 16 | 0)        # echo, chip select PD5 active high
    setbits(m, 0x59E, 0x20, 0x20)                             # PD5: GPIO, output, low
    setbits(m, 0x59A, 0x20, 0x00)
    setbits(m, 0x59B, 0x20, 0x00)
    setbits(m, 0x5A8, 0xF0, 0x00)                             # PA2 DO, PA3 DI: function 0
    setbits(m, 0x5A9, 0x03, 0x00)                             # PA4 CK: function 0
    setbits(m, 0x5B6, 0x20, 0x20)                             # PA4 to the SPI
    setbits(m, 0x5B7, 0x11, 0x01)                             # PA3 the SPI's input
    setbits(m, 0x581, 0x08, 0x08)                             # PA3 input enabled
    setbits(m, 0x586, 0x1C, 0x00)                             # PA2-PA4 GPIO off
    m.reg_write(0x0A, 1, 0x85)                                # divider 5, SPI function
    m.reg_write(0x09, 1, 0x02)                                # master
    setbits(m, 0x59B, 0x20, 0x20)                             # chip select active: transaction 1
    m.reg_write(0x08, 1, 0x20)
    rd(0x09)                                                  # busy
    m.cycles += 95
    rd(0x09)                                                  # still busy
    m.cycles += 1
    rd(0x09)                                                  # done after 16 * 6 cycles
    rd(0x08)                                                  # 0xff: the first answer
    m.reg_write(0x08, 1, 0x05)
    busy_error = False
    try:
        m.reg_write(0x08, 1, 0x06)
    except te.EmuError:
        busy_error = True
    trace.append(int(busy_error))
    m.cycles += 96
    rd(0x08)                                                  # 0x20, echoed
    setbits(m, 0x59B, 0x20, 0x00)                             # chip select inactive
    setbits(m, 0x59B, 0x20, 0x20)                             # transaction 2
    m.reg_write(0x08, 1, 0xA5)
    m.cycles += 96
    rd(0x08)                                                  # 0xff: a new transaction
    m.reg_write(0x09, 1, 0x02 | 0x08 | 0x04)                  # read, DO disabled
    rd(0x08)                                                  # still 0xff, clocks one in
    m.cycles += 96
    rd(0x08)                                                  # 0xa5: the device's answer
    m.cycles += 96
    m.reg_write(0x09, 1, 0x02)
    setbits(m, 0x586, 0x10, 0x10)                             # PA4 GPIO again: no CK
    m.reg_write(0x08, 1, 0x77)
    m.cycles += 96
    rd(0x08)                                                  # 0: nothing came in
    rd(0xFFD8, 4)                                             # 2 transactions, 3 octets
    for _ in range(4):
        rd(0xFFDC)                                            # 0x20, 0x05, 0xa5, then 0
    # The ADC's pin codes.
    m.reg_write(0xFFE4, 4, 1 << 24 | 4 << 16 | 0x1234)
    m.analog[0xEB], m.analog[0xFC] = 0x4F, 0x00
    buf = 0x848000
    r = m.regs_mem
    r[0xB08], r[0xB09], r[0xB0A] = buf & 0xFF, (buf >> 8) & 0xFF, 1
    r[0xB10] |= 0x04
    m.update_time()
    trace.append(struct.unpack_from("<I", m.sram, buf - te.SRAM_BASE)[0])
    m.analog[0xEB] = 0x5F                                     # another pin: adc_code
    m.update_time()
    trace.append(struct.unpack_from("<I", m.sram, buf - te.SRAM_BASE)[0])
    m.reg_write(0xFFE4, 4, 0)
    m.analog[0xEB] = 0x4F
    m.update_time()
    trace.append(struct.unpack_from("<I", m.sram, buf - te.SRAM_BASE)[0])
    bad = False
    try:
        m.reg_write(0xFFE4, 4, 1 << 24 | 11 << 16)
    except te.EmuError:
        bad = True
    trace.append(int(bad))
    return trace


def main():
    m = machine()
    t = scenario(m)
    print("trace:", " ".join(f"{v:x}" for v in t))
    check(t[0] & 0x40 and t[1] & 0x40 and not t[2] & 0x40, "0x09 bit 6: busy for 96 cycles at divider 5")
    check(t[3] == 0xFF, "the first answer of a transaction is 0xff")
    check(t[4] == 1, "a write of 0x08 while an octet is going out is an error")
    check(t[5] == 0x20, "the device echoes the octet before")
    check(t[6] == 0xFF, "a new transaction answers 0xff first")
    check(t[7] == 0xFF and t[8] == 0xA5, "a read with 0x09 bit 3 gives the octet in and clocks the next")
    check(t[9] == 0, "with CK's GPIO function on, nothing comes in")
    check(t[10] == (2 << 16 | 3), f"2 transactions and 3 octets received (0x{t[10]:x})")
    check(t[11:15] == [0x20, 0x05, 0xA5, 0], f"the octets in order ({t[11:15]})")
    check(t[15] == 0x1234 and t[16] == 0x0D47 and t[17] == 0x0D47 and t[18] == 1,
          "ADC: the pin input's code, another pin's adc_code, back to adc_code, input 11 refused")
    digest = hashlib.sha256(" ".join(f"{v:x}" for v in t).encode()).hexdigest()
    print("digest:", digest)
    check(digest == DIGEST, "the trace's digest is go/tc32emu/spi_test.go's")
    print(f"spi_check: {'PASS' if not failures else 'FAIL'} ({len(failures)} failure(s))")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())

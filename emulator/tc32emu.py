#!/usr/bin/env python3
"""TC32 (Telink TLSR825x/827x) instruction-set emulator for boot checks.

TC32 is Thumb-1 with the top five opcode bits permuted (tc32isa.TOP), plus a few
instructions of its own in Thumb's undefined space:
  0x6bc0-0x6bdf  tmcsr / tmrcs / tmssr / tmrss   (CPSR/SPSR <-> register)
  0x6800-0x69ff  treti {list}                   (pop, then CPSR = SPSR)
  0xcf00-0xcfff  tserv                           (software interrupt)
isa_check.py checks the permutation and these ranges against Telink's own
tc32-elf-objdump on all 65536 values and on BL pairs.

Semantics are ARMv4T Thumb's. tc32emu stops (EmuError) where ARM leaves the
result UNPREDICTABLE (empty register lists, stm storing its base after a
lower register, bx with bit 7 or bits 2:0 set, high-register add/cmp/mov of
two low registers) and where TC32 may differ from ARM in a way that matters
(PC read by a high-register op at a halfword address; treti without pc).
Telink's code uses none of these. Forms ARM defines but Telink's code never
uses are reported statically by forms_check.py.

Exceptions follow ARM: IRQ mode (0x12) and SVC mode (0x13) bank r13/r14; an
interrupt saves CPSR in SPSR_irq, enters mode 0x92 (I set), puts the address
of the next instruction in r14 and jumps to 0x10. Interrupts are taken when
reg_irq_en (0x643) bit 0 is set, the I bit (CPSR bit 7) is clear, and
reg_irq_src & reg_irq_mask is nonzero.

Memory: flash 0x000000-0x0fffff (XIP), registers 0x800000-0x80ffff, SRAM
0x840000-0x84ffff. Booted from a slot S other than 0, the CPU sees the running
image at 0: an address below S reads the flash at S + address (Telink's
handbook: "the fetch address = 0x20000 + PC pointer value"; an image reads its
own constants by pointer with its link addresses). An address at or above
2 x S reads the flash at that address: the SDK reads the calibration sector by
pointer with the flash's own address in every image, whichever slot runs
(vendor/common/ble_flash.c, flash_sector_calibration + offset). What the
addresses from S to 2 x S - 1 read then is in none of our sources, so an
access there stops the run. The boot ROM
copies the first (header word 0x0c & 0xffff) * 16 bytes of the boot slot to
SRAM 0x840000 and starts at 0 (the copy is executed there on hardware; the
code is position independent, so starting from flash behaves the same).

Clock: the CPU clock follows reg_clk_sel (0x66) and 0x70[0] (DS-TLSR8278 4.2):
0x66[6:5] = 0 RC 24 MHz, 1 FHS, 2 FHS / 0x66[4:0], 3 32 MHz; FHS from
{0x70[0], 0x66[7]} = 0 the 48 MHz crystal doubler, 1 RC 24 MHz, 2-3 the 24 MHz
crystal. The chip boots on RC 24 MHz; the firmware we run switches to 0x20
(48 MHz). Each instruction takes `cpi` cycles (default 1;
real TC32 code needs more). Timer2 counts CPU cycles.

Peripherals modelled, as DS-TLSR8278 (Ver 1.0.4) describes the TLSR8278:
- System timer (5.3, Table 5-2): 16 MHz from the crystal, independent of the
  CPU clock. It is stopped at reset (0x74a = 0xc1) and counts while 0x74a
  bit 1 is set. Bits 2:0 of the count (0x740) and of the compare (0x744)
  read 0. 0x750 reads the 32 kHz count. 0x74c-0x74f (the 32 kHz timer's
  write value) are plain storage: the TLSR8258 control bits there do not
  exist on this chip. Not in the datasheet, from the B87 SDK: a compare
  match sets interrupt source 20 when 0x748 bit 2 is set
  (reg_system_irq_mask), and 0x74b bit 5 toggles at each 32 kHz update (the
  datasheet lists it as reserved).
- Interrupt controller (6.2, Table 6-1): mask 0x640-0x642, enable 0x643,
  sources 0x648-0x64a. Sources 0-15 are level-triggered and follow their
  peripheral's status (level_sources); sources 16-23 are edge-triggered and
  latched until written with a one.
- MSPI flash commands (read, status, write enable, page program, 4 KB/32 KB/
  64 KB erase, chip erase, JEDEC ID). Status register writes are logged,
  not modelled; every command byte is counted.
- Analog register port (0xb8-0xba), never busy. Assumed from the SDK (the
  datasheet does not describe it): analog 0x88 bit
  7 reads 1, the crystal is ready (0 with xtal_ready = False).
- Timer0/1 in mode 0 (5.1.2): count the system clock, set their status bit
  (0x623 bits 0/1, write one to clear; interrupt sources 0/1) at the capture
  and start again from 0 (the datasheet says they stop; the SDK uses them as
  periodic timers).
- Timer2 watchdog (5.1.6), reset cause (0x72, write one to clear), reboot
  (0x6f bit 5), boot slot register (0x63e).
- Not documented: 0x608 loads 16 bytes of flash
  into SRAM at the same offset (bit 24 starts it), and analog 0x7f bit 0 is 1
  after power-on (cleared for a wake from deep retention). The flash answers
  0x9F (JEDEC ID) and 0x4B (a 16-byte unique ID), both settable.
- A hardware divider at 0x660 (mode: UDIV 0, SDIV 1, UMOD 2, SMOD 3; reads
  0 when done), 0x664 (dividend, then quotient) and 0x668 (divisor, then
  remainder), from the Telink SDK's common/div_mod.S; not in the datasheet.
  Division by zero and the time it takes are not known; here quotient -1,
  at once.
- Not documented: a random number generator at 0x804400 (enable
  bit 0), 0x804408 (bit 0: a number is ready, always here) and 0x80440c
  (the number; a fixed pseudo-random sequence here).
- The ADC's DMA (DFIFO2, from the B87 SDK's register.h and dfifo.h): while
  0xb10 bit 2 is set, its buffer holds 32-bit samples of adc_code. The ADC
  itself is not modelled.
  Nothing ties these two to the hardware: a firmware may only need a
  number and a sample to arrive, and no check depends on the values.
- GPIO interrupt (7.1.3): a rising edge of |((input ^ polarity) & irq)
  latches source 18 while 0x5b5 bit 3 is set; board models call
  gpio_irq_update() when they change a pin.
- From the B87 SDK (clock.c): starting an RC calibration (analog 0xc6 or
  0xc7 bit 0) sets its done bit in analog 0xcf (bit 6 or 7) at once.
- GPIO reset values: all outputs disabled (0x582, 0x58a, 0x592, 0x59a,
  0x5a2 = 0xff) and the GPIO function where Table 7-1 lists GPIO as the
  pad's default. The datasheet gives no numbers; a pad that defaulted to a
  driving GPIO would drive at power-on.
- GPIO inputs (0x580 + 8p): the pads' levels as pad_levels() gives them
  (a driven output its bit, a 1 MOhm or 10 kOhm pull-up 1, a 100 kOhm
  pull-down 0), through the input enable (0x581 + 8p; analog 0xc0 for port
  C). A floating pad reads the level it last had while it was driven or
  pulled: its capacitance holds it between the steps of a key scan (a scan
  can precharge floating rows low); 1 after reset. The level
  is taken at each read and at each write to the port's registers or pulls,
  so it does not depend on the firmware reading the pad before releasing it.
  The datasheet gives no number for either. Board and USB models override
  pad_levels() for what is wired to the pads.
- The register block also answers at 0x1000000 + offset: Telink's older SDK
  writes its RF registers there.
Other registers are plain storage.

SPDX-License-Identifier: Apache-2.0
"""
import math
import os
import struct
import sys

sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "common"))
from tc32isa import TOP, to_thumb  # noqa: E402,F401

# Flash: 1 MB on the TLSR8278. TC32EMU_FLASH_SIZE gives a part with less
# (0x80000: 512 KB).
# What lies at or above the part's size is then refused: an XIP access there
# and an SPI read, program or erase with such an address are errors (what a
# real part answers to an address it does not have is not in our documents,
# so the model does not guess). A read is refused when its byte is handed to
# the firmware (the data register 0x0c is read), not when it is clocked: in
# the MSPI's auto mode every read of 0x0c clocks one byte more, so a read
# that ends at the part's last byte clocks the one after it and drops it.
# Flash.mem keeps FLASH_MEM bytes, so a check
# that seeds a byte above the part's size still runs; nothing the firmware
# does can reach that byte. The JEDEC ID's capacity byte follows the size.
# TC32EMU_FLASH_BEYOND says what an SPI read at or above the part's size gives
# when the firmware takes its byte: "stop" (the default) is the error above;
# "ff" gives 0xff; "wrap" gives the byte at the address modulo the size. The
# datasheets say only that the command carries a 24-bit address, so both are
# assumptions for running a firmware that reads such an address and makes
# nothing of the bytes (for example a CRC read of slot A's image by its size
# word, 0xffffffff when the slot is blank); each such read is logged as
# ("read beyond", address, 1). Programs, erases and XIP accesses there stay
# errors.
FLASH_BEYOND = os.environ.get("TC32EMU_FLASH_BEYOND", "stop")
if FLASH_BEYOND not in ("stop", "ff", "wrap"):
    raise ValueError("TC32EMU_FLASH_BEYOND: " + FLASH_BEYOND)
FLASH_MEM = 0x100000
FLASH_SIZE = int(os.environ.get("TC32EMU_FLASH_SIZE", "0x100000"), 0)
# The flash's busy time. Without a model a program or an erase is done at once
# and the status register (0x05) never shows WIP (bit 0). TC32EMU_FLASH_TPP_US,
# TC32EMU_FLASH_TSE_US, TC32EMU_FLASH_TBE32_US and TC32EMU_FLASH_TBE64_US
# (microseconds; 0 or unset: no busy time) keep WIP set that long after a page
# program (0x02), a sector erase (0x20), a 32 KB block erase (0x52) and a
# 64 KB block erase (0xd8) that the part executes; one the protection ignores
# sets nothing (the datasheets give the times of executed ones). The SDK's
# flash_wait_done() polls 0x05 from RAM code with interrupts off, so a busy
# time holds the interrupts off that long, as on the chip: the typical times
# in DS-TLSR8278-E Table 19-13 (19.7, the flash) are 1.6 ms (TPP), 150 ms
# (TSE) and 0.5 s and 0.8 s (TBE, 32 KB and 64 KB); a part of the board's own
# has its own datasheet. The time runs on the machine's clock, through sleep
# and clock changes (Machine keeps it as a cycle count, Flash.busy_until, and
# converts what is left when the CPU clock changes), and goes on through a
# chip reset (the part does not see it); a power-on (Machine()) finds the part
# idle. While the part is busy it takes nothing but status reads (0x05, 0x35):
# any other command, committed or clocked for data, and an XIP read are
# errors (the part answers nothing else before WIP clears).
# Not checked, by decision: the boot ROM's copy at a reset and the 0x608
# flash-to-SRAM loader read the flash whether it is busy or not. What the boot
# ROM does with a busy part is not in our documents, and the model does not
# guess.
FLASH_TPP_US = int(os.environ.get("TC32EMU_FLASH_TPP_US", "0"), 0)
FLASH_TSE_US = int(os.environ.get("TC32EMU_FLASH_TSE_US", "0"), 0)
FLASH_TBE32_US = int(os.environ.get("TC32EMU_FLASH_TBE32_US", "0"), 0)
FLASH_TBE64_US = int(os.environ.get("TC32EMU_FLASH_TBE64_US", "0"), 0)
# Timer0/1 in mode 0 match their capture on equality: a capture written at or
# below the running count (or a count written, or the timer started, at or
# above the capture) matches only when the 32-bit count has wrapped round to
# it, 2^32 cycles later (89.5 s at 48 MHz), with no interrupt from the timer
# meanwhile. The datasheet does not say how the compare works (DS-TLSR8278-E
# 5.1.2 says only that a mode 0 timer stops at its capture); this is what the
# hardware showed: a ZMK image that wrote Timer1's capture below the count after a
# flash write had held interrupts off lost its timer interrupt for 91 s after
# every power-on, and one that never writes the capture while the timer runs
# did not. A write that leaves the count equal
# to the capture is taken as missed too: the careful reading, an assumption
# the hardware did not show either way. TC32EMU_TIMER_CAPTURE_BELOW: "stop"
# (the default) makes such a write an error that stops the run; "wrap" models
# the wait and logs it as an event.
TIMER_CAPTURE_BELOW = os.environ.get("TC32EMU_TIMER_CAPTURE_BELOW", "stop")
if TIMER_CAPTURE_BELOW not in ("stop", "wrap"):
    raise ValueError("TC32EMU_TIMER_CAPTURE_BELOW: " + TIMER_CAPTURE_BELOW)
# SRAM: 64 KB on the TLSR8278. TC32EMU_SRAM_SIZE gives a part with less (the
# TLSR8271 has 32 KB: 0x8000); an access above it is then an error, as any
# access outside the memory map is.
# TC32EMU_SRAM_SEED (not 0) fills the SRAM at power-on (Machine()) with the
# bytes of an xorshift32 generator seeded with it, as a real chip's SRAM holds
# something at power-on rather than zeros; a reset (Machine.reset(), the
# watchdog, a software reset) keeps the SRAM's contents either way. Unset or 0,
# the SRAM is zeros at power-on.
SRAM_BASE, SRAM_SIZE = 0x840000, int(os.environ.get("TC32EMU_SRAM_SIZE", "0x10000"), 0)
SRAM_SEED = int(os.environ.get("TC32EMU_SRAM_SEED", "0"), 0) & 0xFFFFFFFF
# The flash cache. Code and constants beyond the RAM mirror are read from the
# SPI flash through a cache whose tags and data the startup places in SRAM
# (register 0x60c/0x60d; the SDK's link scripts give it 0x100 bytes of tags and
# 0x800 of data: 64 lines of 32 bytes). Without a model every such access
# costs nothing beyond the instruction's cycles. TC32EMU_ICACHE_MISS=N (cycles,
# 0 or unset: off) adds N cycles for each access to a line that is not the one
# its slot holds, the slot being the line number modulo ICACHE_LINES
# (direct-mapped). What the documents give is the sizes; the mapping and the
# cost of a miss (the SPI read of a line: about 36 bytes on the wire at the
# flash clock) are not in them, so the cost is the caller's to choose and a
# run states it. Machine.icache_misses counts them.
ICACHE_MISS = int(os.environ.get("TC32EMU_ICACHE_MISS", "0"), 0)
# The pads' capacitance. Without a model a pad that is only pulled by its
# resistor is at the pull's level at once. TC32EMU_PAD_C_PF=C (pF, an integer;
# 0 or unset: off) gives every pad that capacitance: a pad that was at the
# other level and is now held by its pull resistor alone (its output disabled,
# nothing else driving it) reads the old level until the RC curve has crossed
# the input threshold, 0.3 VDD falling and 0.7 VDD rising from the full rail:
# t = ln(1 / 0.3) x R x C = 1.204 x R x C, R the pull's nominal value (1 MOhm,
# 100 kOhm, 10 kOhm; TC32EMU_PAD_R_PCT, percent, scales it for the tolerance).
# A level that is not the pull's own is taken as driven and is there at once;
# one that is the pull's own is taken as reached through the pull, also when
# something outside drives it there (the model cannot tell the two apart).
# C is a property of the board and in none of our documents: it is the
# caller's assumption. The time runs from the register write (GPIO or pull)
# or the board model's update (hold_pads, gpio_irq_update) that released the
# pad; when it has passed the pad takes its level and the GPIO interrupt sees
# the change then (run() serves it), as the chip's edge comes from the input
# the firmware reads.
PAD_C_PF = int(os.environ.get("TC32EMU_PAD_C_PF", "0"), 0)
PAD_R_PCT = int(os.environ.get("TC32EMU_PAD_R_PCT", "100"), 0)
PAD_R_OHM = {1: 1_000_000, 2: 100_000, 3: 10_000}
ICACHE_LINES, ICACHE_LINE_SHIFT = 64, 5


def sram_fill(seed, size):
    """The power-on contents of an SRAM of size bytes for a seed: xorshift32
    (Marsaglia's 13, 17, 5), one byte (the low one) per step. The Go emulator
    computes the same bytes."""
    out = bytearray(size)
    x = seed & 0xFFFFFFFF
    for i in range(size):
        x ^= (x << 13) & 0xFFFFFFFF
        x ^= x >> 17
        x ^= (x << 5) & 0xFFFFFFFF
        out[i] = x & 0xFF
    return out


# Block protection of the SPI flash: the status register's BP (and, on the
# Puya parts, CMP/SEC/TB) bits and the address range each value protects, as
# the SDK's per-part headers list them (tc_ble_single_sdk drivers/B87/flash/
# flash_mid<mid>.h, and drivers/B85 for the P25Q40; the mid is the JEDEC ID
# read low byte first: (capacity << 16) | (type << 8) | manufacturer). A part
# whose mid is not here has no protection modelled (the status write is kept
# and logged, nothing is protected). A status value that a part's table does
# not list is an error: the SDK does not say what it protects. Programs and
# erases inside the protected range change nothing (the datasheets' behaviour
# for a protected block) and are logged as ("protected", addr, len).
def _low_table(size, mask=0x1C):
    """GigaDevice GD25LD40C/80C and Zbit ZB25WD40B/80B: bits 4:2 protect the
    low part of the array: all but the top 8, 16, 32, 64, 128, 256 KB (0x04 to
    0x18) or all (0x1c), the SDK's LOW_xxxK values."""
    top = size - 1
    ranges = {0x00: None, 0x04: (0, top - 0x2000), 0x08: (0, top - 0x4000), 0x0C: (0, top - 0x8000),
              0x10: (0, top - 0x10000), 0x14: (0, top - 0x20000), 0x18: (0, top - 0x40000), 0x1C: (0, top)}
    return mask, ranges


def _puya_table(size):
    """Puya P25Q40 (mid 0x136085) and P25Q80 (0x146085): 16-bit status, mask
    0x407c (CMP bit 14, SEC bit 6, TB bit 5, BP2:0 bits 4:2). The values and
    ranges are the SDK headers' lists, including the aliases in their comments."""
    top = size - 1
    k = 1024
    r = {0x0000: None, 0x0020: None, 0x407C: None,
         0x0004: (size - 64 * k, top), 0x0008: (size - 128 * k, top), 0x000C: (size - 256 * k, top),
         0x0024: (0, 64 * k - 1), 0x0028: (0, 128 * k - 1), 0x002C: (0, 256 * k - 1),
         0x0044: (size - 4 * k, top), 0x0048: (size - 8 * k, top), 0x004C: (size - 16 * k, top),
         0x0050: (size - 32 * k, top), 0x0054: (size - 32 * k, top),
         0x0064: (0, 4 * k - 1), 0x0068: (0, 8 * k - 1), 0x006C: (0, 16 * k - 1),
         0x0070: (0, 32 * k - 1), 0x0074: (0, 32 * k - 1),
         0x4044: (0, top - 4 * k), 0x4048: (0, top - 8 * k), 0x404C: (0, top - 16 * k),
         0x4050: (0, top - 32 * k), 0x4054: (0, top - 32 * k),
         0x4064: (4 * k, top), 0x4068: (8 * k, top), 0x406C: (16 * k, top),
         0x4070: (32 * k, top), 0x4074: (32 * k, top),
         0x007C: (0, top), 0x4000: (0, top), 0x4040: (0, top), 0x4020: (0, top), 0x4060: (0, top)}
    if size == 0x100000:      # P25Q80
        r.update({0x0010: (0x80000, top), 0x4030: (0x80000, top), 0x0030: (0, 0x7FFFF), 0x4010: (0, 0x7FFFF),
                  0x4004: (0, top - 64 * k), 0x4008: (0, top - 128 * k), 0x400C: (0, top - 256 * k),
                  0x4024: (64 * k, top), 0x4028: (128 * k, top), 0x402C: (256 * k, top)})
    else:                     # P25Q40
        r.update({0x4030: None, 0x4004: (0, top - 64 * k), 0x4008: (0, top - 128 * k),
                  0x4024: (64 * k, top), 0x4028: (128 * k, top)})
    return 0x407C, r


def _zbit_c_table(size):
    """Zbit ZB25WD40C (the SDK's mid 0x0113325e) and ZB25WD80C (0x0114325e): the
    same JEDEC ID as the B parts, told apart by the SFDP signature (0x5a at 0:
    0x53), a 16-bit status register, mask 0x407c; flash_mid0113325e.h and
    flash_mid0114325e.h, with the aliases in their comments."""
    top = size - 1
    k = 1024
    r = {0x0000: None,
         0x0004: (size - 64 * k, top), 0x0008: (size - 128 * k, top), 0x000C: (size - 256 * k, top),
         0x0024: (0, 64 * k - 1), 0x0028: (0, 128 * k - 1), 0x002C: (0, 256 * k - 1),
         0x0044: (size - 4 * k, top), 0x0048: (size - 8 * k, top), 0x004C: (size - 16 * k, top),
         0x0050: (size - 32 * k, top),
         0x0064: (0, 4 * k - 1), 0x0068: (0, 8 * k - 1), 0x006C: (0, 16 * k - 1), 0x0070: (0, 32 * k - 1),
         0x4004: (0, top - 64 * k), 0x4008: (0, top - 128 * k),
         0x4024: (64 * k, top), 0x4028: (128 * k, top),
         0x4044: (0, top - 4 * k), 0x4048: (0, top - 8 * k), 0x404C: (0, top - 16 * k), 0x4050: (0, top - 32 * k),
         0x4064: (4 * k, top), 0x4068: (8 * k, top), 0x406C: (16 * k, top)}
    if size == 0x100000:      # ZB25WD80C
        r.update({0x0010: (0x80000, top), 0x001C: (0, top), 0x003C: (0, top), 0x005C: (0, top), 0x007C: (0, top),
                  0x0030: (0, 0x7FFFF), 0x0054: (size - 32 * k, top), 0x0074: (0, 32 * k - 1),
                  0x400C: (0, top - 256 * k), 0x402C: (256 * k, top), 0x4054: (0, top - 32 * k),
                  0x4070: (32 * k, top), 0x4074: (32 * k, top)})
    else:                     # ZB25WD40C
        r.update({0x0010: (0, top), 0x0030: (0, top), 0x005C: (0, top), 0x007C: (0, top),
                  0x0058: (size - 32 * k, top), 0x0078: (0, 32 * k - 1), 0x4058: (0, top - 32 * k),
                  0x4078: (32 * k, top)})
    return 0x407C, r


# Keyed by the part as the SDK names it: the JEDEC mid, or 0x01000000 | mid for a
# Zbit C part (its flash_read_mid() returns 0x0113325e / 0x0114325e for those).
FLASH_PROTECTION = {0x1360C8: _low_table(0x80000), 0x13325E: _low_table(0x80000),
                    0x1460C8: _low_table(0x100000), 0x14325E: _low_table(0x100000),
                    0x136085: _puya_table(0x80000), 0x146085: _puya_table(0x100000),
                    0x0113325E: _zbit_c_table(0x80000), 0x0114325E: _zbit_c_table(0x100000)}
SFDP_SIGNATURE = b"SFDP"   # what a part with SFDP answers to 0x5a at address 0 (JESD216)
REG_BASE, REG_SIZE = 0x800000, 0x10000
REG_ALIAS = 0x1000000          # the register block again (see the module docstring)
MODE_IRQ, MODE_SVC = 0x12, 0x13
CPU_HZ, STIMER_HZ = 24_000_000, 16_000_000  # CPU_HZ: the boot clock
CLK_16M = 0x43
CLK_48M = 0x20   # what our SoC code selects
IRQ_STIMER = 20
STIMER_CTRL_RESET = 0xC1   # 0x74a: 32 kHz calibration mode 0xc, write mode; timer stopped
STIMER_EN = 0x02           # 0x74a bit 1
STIMER_IRQ = 0x04          # 0x748 bit 2
M32 = 0xFFFFFFFF


class EmuError(Exception):
    pass


class Stop(Exception):
    """Raised by hooks to end a run."""


class Flash:
    """SPI NOR flash behind the MSPI registers; also the XIP backing store."""

    track = False  # when True, every Flash made is kept in created, so checks can read all their logs
    created = []

    def __init__(self, size=FLASH_SIZE, jedec=(0x85, 0x60, 0x14), uid=bytes(range(0xA0, 0xB0)), sfdp=None):
        self.size = size
        self.mem = bytearray(b"\xff" * max(size, FLASH_MEM))
        if size < FLASH_MEM and jedec[2] == 0x14:
            jedec = (jedec[0], jedec[1], size.bit_length() - 1)   # 0x13 for 512 KB
        self.jedec = jedec
        self.uid = bytes(uid)  # 0x4B: 3 address bytes and 1 dummy byte, then 16 bytes
        self.mid = (jedec[2] << 16) | (jedec[1] << 8) | jedec[0]
        # SFDP (0x5a, 3 address bytes, 1 dummy byte): the bytes at those addresses,
        # 0xff beyond them; None for a part without (0xff throughout: what the SDK
        # calls a Zbit B part when the signature is not 0x53). The part, as the SDK
        # names it, follows: a Zbit ID with the signature is the C part.
        self.sfdp = bytes(sfdp) if sfdp is not None else None
        self.part = self.mid
        if self.mid in (0x13325E, 0x14325E) and self.sfdp and self.sfdp[0] == 0x53:
            self.part = 0x01000000 | self.mid
        # The status register (16 bits: 0x05 reads the low byte, 0x35 the high one),
        # non-volatile as on the part; its protection bits per FLASH_PROTECTION.
        self.status = 0
        self.wel = False
        self.cs_low = False
        self.tx = []
        self.rx = 0xFF
        self.beyond = None  # the address of the byte last clocked out, if the part does not have it
        self.beyond_mode = FLASH_BEYOND   # "stop", "ff" or "wrap": see FLASH_BEYOND
        self.read_addr = None
        self.log = []  # (op, addr, len) of programs, erases and other writes (status register, chip erase)
        # on_write(op, addr, n), when set, is called as a page program or an
        # erase is carried out ("program", "erase") or refused by the block
        # protection ("protected"), with the log entry's values, at that
        # moment of the machine's time: for a check of what else runs then
        # (a radio on air, an interrupt held off). None: nothing is called.
        self.on_write = None
        self.commands = {}  # command byte -> count, every command the flash was sent
        self.dpd = False    # deep power-down (0xb9) until released (0xab): it answers nothing else
        self.busy_until = 0     # WIP (0x05 bit 0) until the machine's cycles reach this; 0: idle
        self.machine = None     # whose cycles and cpu_hz busy_until counts (Machine sets it); None: never busy
        if Flash.track:
            Flash.created.append(self)

    def select(self, low):
        if self.cs_low and not low:
            self._commit()
        if low and not self.cs_low:
            self.tx, self.read_addr = [], None
        self.cs_low = low

    # Commands whose address or data phase follows the command byte (the status
    # reads 0x05/0x35 included: their byte is clocked next).
    PHASED = frozenset((0x01, 0x02, 0x03, 0x05, 0x0B, 0x11, 0x20, 0x31, 0x35, 0x4B, 0x52, 0x5A, 0x9F, 0xD8))

    def spi_busy(self):
        """A transaction that an XIP access would cut: more than the command
        byte sent, or a command whose address/data phase is next."""
        return len(self.tx) > 1 or (len(self.tx) == 1 and self.tx[0] in Flash.PHASED)

    def abort(self):
        """A chip reset: the MSPI's chip select goes high without a commit (the
        transaction, if any, is cut; the flash's own state is not modelled,
        except that a busy time goes on)."""
        self.cs_low, self.tx, self.read_addr, self.beyond = False, [], None, None

    def busy(self):
        """WIP: a program or erase with a busy time (FLASH_TPP_US ...) still
        running. busy_until is cleared once passed, so an idle part costs one
        test of it."""
        if not self.busy_until:
            return False
        if self.machine.cycles < self.busy_until:
            return True
        self.busy_until = 0
        return False

    def busy_left_us(self):
        """The busy time left, in microseconds (for messages)."""
        return (self.busy_until - self.machine.cycles) * 1_000_000 / self.machine.cpu_hz

    def _start_busy(self, us):
        if us and self.machine is not None:
            # Rounded up to a whole cycle; at least 1, so busy_until is not 0.
            self.busy_until = self.machine.cycles + -(-us * self.machine.cpu_hz // 1_000_000)

    def _refuse_busy(self, cmd):
        """While busy the part takes nothing but status reads (0x05, 0x35)."""
        if cmd not in (0x05, 0x35) and self.busy():
            self.tx = []
            raise EmuError(f"flash command 0x{cmd:02x} while the flash is busy with a program or erase (WIP, "
                           f"{self.busy_left_us():.0f} us left): the part takes only status reads until WIP clears")

    def clock(self, byte):
        """One byte out; returns the byte clocked in."""
        if not self.cs_low:
            return 0xFF
        self.tx.append(byte)
        cmd, n = self.tx[0], len(self.tx)
        self.beyond = None
        if n > 1 and self.busy_until:
            self._refuse_busy(cmd)
        if self.dpd:
            return 0xFF
        if cmd in (0x03, 0x0B):
            skip = 5 if cmd == 0x0B else 4
            if n > skip:
                a = ((self.tx[1] << 16) | (self.tx[2] << 8) | self.tx[3]) + (n - skip - 1)
                if self.size < FLASH_MEM and a >= self.size:
                    self.beyond = a         # the firmware taking this byte is an error or logged (Machine.reg_read)
                    return self.mem[a % self.size] if self.beyond_mode == "wrap" else 0xFF
                return self.mem[a % len(self.mem)]
            return 0xFF
        if cmd == 0x05:
            return (self.status & 0xFC) | (0x02 if self.wel else 0x00) | (0x01 if self.busy() else 0x00)
        if cmd == 0x35:
            return (self.status >> 8) & 0xFF
        if cmd == 0x9F and 1 < n <= 4:
            return self.jedec[n - 2]
        if cmd == 0x5A and n > 5:
            a = ((self.tx[1] << 16) | (self.tx[2] << 8) | self.tx[3]) + (n - 6)
            return self.sfdp[a] if self.sfdp is not None and a < len(self.sfdp) else 0xFF
        if cmd == 0x4B and n > 5:
            return self.uid[(n - 6) % len(self.uid)]
        return 0xFF

    def _commit(self):
        if not self.tx:
            return
        cmd = self.tx[0]
        addr = (self.tx[1] << 16 | self.tx[2] << 8 | self.tx[3]) if len(self.tx) >= 4 else None
        self.commands[cmd] = self.commands.get(cmd, 0) + 1
        if self.busy_until:
            self._refuse_busy(cmd)
        if self.dpd:
            self.dpd = cmd != 0xAB
            self.tx = []
            return
        if (self.size < FLASH_MEM and addr is not None and cmd in (0x02, 0x20, 0x52, 0xD8)
                and addr + (len(self.tx) - 4 if cmd == 0x02 else 1) > self.size):
            self.tx = []
            raise EmuError(f"flash {'program' if cmd == 0x02 else 'erase'} (0x{cmd:02x}) at 0x{addr:x}: "
                           f"the part has 0x{self.size:x} bytes")
        if cmd == 0xB9:
            self.dpd = True
        elif cmd in (0x01, 0x31, 0x11) and self.wel:
            # Write status register: 0x01 with one byte (the low byte) or two (low,
            # high), 0x31 the high byte; 0x11 (a third register) is only logged.
            data = self.tx[1:]
            self.log.append(("status", cmd, bytes(data).hex()))
            self.wel = False
            if cmd == 0x01 and len(data) >= 1:
                self.status = (self.status & 0xFF00) | (data[0] & 0xFC)
                if len(data) >= 2:
                    self.status = (self.status & 0x00FF) | (data[1] << 8)
            elif cmd == 0x31 and len(data) >= 1:
                self.status = (self.status & 0x00FF) | (data[0] << 8)
            if self.protected_range() is Ellipsis:
                self.tx = []
                raise EmuError(f"flash status 0x{self.status:04x} (0x{cmd:02x}): the SDK's table for part "
                               f"0x{self.part:06x} does not list its protection")
        elif cmd in (0x60, 0xC7) and self.wel:
            self.mem[:] = b"\xff" * len(self.mem)
            self.log.append(("chip erase", 0, len(self.mem)))
            self.wel = False
        elif cmd == 0x06:
            self.wel = True
        elif cmd == 0x04:
            self.wel = False
        elif cmd == 0x02 and self.wel and addr is not None:
            data = self.tx[4:]
            page = addr & ~0xFF
            self.wel = False
            if self.protected(addr, len(data)):
                self._wrote("protected", addr, len(data))
            else:
                for i, b in enumerate(data):
                    a = page | ((addr + i) & 0xFF)
                    self.mem[a] &= b
                self._wrote("program", addr, len(data))
                self._start_busy(FLASH_TPP_US)
        elif cmd in (0x20, 0x52, 0xD8) and self.wel and addr is not None:
            size = {0x20: 0x1000, 0x52: 0x8000, 0xD8: 0x10000}[cmd]
            base = addr & ~(size - 1)
            self.wel = False
            if self.protected(base, size):
                self._wrote("protected", base, size)
            else:
                self.mem[base:base + size] = b"\xff" * size
                self._wrote("erase", base, size)
                self._start_busy({0x20: FLASH_TSE_US, 0x52: FLASH_TBE32_US, 0xD8: FLASH_TBE64_US}[cmd])
        self.tx = []

    def _wrote(self, op, addr, n):
        self.log.append((op, addr, n))
        if self.on_write is not None:
            self.on_write(op, addr, n)

    def protected_range(self):
        """(first, last) of the protected addresses, None when nothing is protected
        (or the part's protection is not modelled), Ellipsis when the status value
        is one the SDK's table does not list."""
        table = FLASH_PROTECTION.get(self.part)
        if table is None:
            return None
        mask, ranges = table
        return ranges.get(self.status & mask, Ellipsis)

    def protected(self, addr, n):
        """True when any of the n bytes at addr lies in the protected range: the
        program or erase then changes nothing (the datasheets: ignored)."""
        r = self.protected_range()
        if not r:
            return False
        first, last = r
        return addr <= last and addr + n - 1 >= first


class Machine:
    gpio_oen_reset = 0xFF  # OEN of every GPIO port at reset (1: output disabled)

    def __init__(self, flash, boot_slot=None, symbols=None, max_log=200, cpi=1):
        self.flash = flash
        flash.machine = self
        self.cpi = cpi
        self.sym = symbols or {}      # the property below keeps addr_sym in step
        self.hooks = {}          # pc -> callable(machine)
        self.core_wake_sources = []  # callables: a core (digital) wake event now, e.g. USB resume
        self.wakes = []          # (ms, analog 0x44 status) of every wake from suspend
        self.float_wakes = set()  # (port, bits): pad wake enabled on floating pads
        self.on_treti = None     # callable(machine), after a treti
        self.events = []         # (cycle, text)
        self.max_log = max_log
        self.reg_log = None      # set(): (offset, size, 'r'/'w', pc) of every register access
        self.xtal_ready = True   # analog 0x88 bit 7
        # Interrupt sources 0-15 are level-triggered (DS-TLSR8278 Table 6-1):
        # callables returning the source bits their peripheral's status holds.
        # Sources 16-23 are edge-triggered: irq_latch, cleared by writing 0x648-0x64a.
        # Timer0-2 status (0x623 bits 0-2) drives sources 0-2.
        self.level_sources = [lambda: self.regs_mem[0x623] & 0x07]
        self.stimer_stuck = False  # a system timer that never counts (a fault to test against)
        # ADC samples the DFIFO2 DMA writes: the code for about 3.9 V with the
        # SDK's 1175 mV reference and 1/8 prescaler (3900 / (1175 * 8) * 8192)
        self.adc_code = 0x0D47
        self.reset_count = 0
        self.icache_miss = ICACHE_MISS     # cycles per flash cache miss, 0: no cache model
        self.icache_tags = [-1] * ICACHE_LINES
        self.icache_misses = 0
        # Power-on: the SRAM's contents (SRAM_SEED, see sram_fill); a reset keeps them.
        self.sram = sram_fill(SRAM_SEED, SRAM_SIZE) if SRAM_SEED else bytearray(SRAM_SIZE)
        self.reset(boot_slot, cause_wd=False)

    # ------------------------------------------------------------ reset/boot
    def reset(self, boot_slot=None, cause_wd=False):
        f = self.flash.mem
        if boot_slot is None:
            boot_slot = next((s for s in (0x0, 0x20000, 0x40000, 0x80000)
                              if s < self.flash.size and f[s + 8] == 0x4B), None)
            if boot_slot is None:
                raise EmuError("no bootable slot (byte 8 = 0x4b)")
        self.boot_slot = boot_slot
        self.flash.abort()
        # A program or erase goes on through a reset (the part does not see
        # it); the clock starts again at 0 and at CPU_HZ below, so the cycles
        # left are kept, converted to the boot clock. At power-on (Machine())
        # the part is idle.
        busy_left = self.flash.busy_until - self.cycles if self.reset_count and self.flash.busy() else 0
        busy_hz = self.cpu_hz if self.reset_count else CPU_HZ
        self.icache_tags = [-1] * ICACHE_LINES
        copy = (struct.unpack_from("<I", f, boot_slot + 0x0C)[0] & 0xFFFF) * 16
        # An erased header word asks for a 1 MB copy: no more than the SRAM (a
        # slice assignment past its end would grow the bytearray).
        copy = min(copy, SRAM_SIZE)
        self.sram[:copy] = f[boot_slot:boot_slot + copy]
        # Analog 0x3a-0x3c keep their value across a watchdog or software reset
        # and are cleared at power-on, to 0x00, 0x00 and 0x0f (DS-TLSR8278
        # Table 2-2: afe_0x3c default 0x0f; the B87 SDK's pm.h, DEEP_ANA_REG2
        # "initial value =0x0f").
        keep_analog = getattr(self, "analog", None)
        if keep_analog is None:
            keep_analog = bytearray(256)
            keep_analog[0x3C] = 0x0F
        self.regs_mem = bytearray(REG_SIZE)
        self.analog = bytearray(256)
        # 0x35-0x39 are kept in deep sleep only: every reset puts them at their
        # defaults, 0x20, 0, 0, 0 and 0xff (the same Table 2-2).
        self.analog[0x35], self.analog[0x39] = 0x20, 0xFF
        self.analog[0x3A:0x3D] = keep_analog[0x3A:0x3D]  # kept across watchdog/software reset
        self.pad_hold = [0xFF] * 4    # a floating pad's last level (GPIO inputs, reg_read)
        self.pad_c_pf, self.pad_r_pct = PAD_C_PF, PAD_R_PCT
        self.pad_rc_since = {}        # (port, bit) -> cycles when the pad was left to its pull
        # Analog 0x7f bit 0 is 1 after power-on and resets; the B87 sleep code
        # clears it for a wake from deep retention. Assumed: a startup can copy
        # its RAM code only when the bit is 1.
        self.analog[0x7F] = 0x01
        self.regs_mem[0x72] = 1 if cause_wd else 0
        self.regs_mem[0x74A] = STIMER_CTRL_RESET
        # GPIO at reset: every output disabled (OEN = 1; a pad whose default is
        # GPIO would drive otherwise), and the GPIO function set where
        # DS-TLSR8278 Table 7-1 gives GPIO as the default: not PA5/PA6 (DM/DP),
        # PA7 (SWS), PB6/PB7 (SPI_DI/SPI_DO), PD2 (SPI_CN), PD7 (SPI_CK).
        # The datasheet gives no reset value for OEN; gpio_oen_reset = 0 tests
        # the other case (reg_evidence.py --oen-reset 0).
        for oen in (0x582, 0x58A, 0x592, 0x59A, 0x5A2):
            self.regs_mem[oen] = self.gpio_oen_reset
        self.regs_mem[0x586], self.regs_mem[0x58E] = 0x1F, 0x3F
        self.regs_mem[0x596], self.regs_mem[0x59E] = 0xFF, 0x7B
        self.regs_mem[0x63E] = 0 if boot_slot == 0 else 0x02
        self.r = [0] * 16
        self.bank = {MODE_IRQ: [0, 0], MODE_SVC: [0, 0]}
        self.spsr = {MODE_IRQ: 0, MODE_SVC: 0}
        self.mode = MODE_SVC
        self.i_bit = 1
        self.n = self.z = self.c = self.v = 0
        self.r[15] = 0
        self.cycles = 0
        self.idle_skipped = 0    # of cycles, those run()'s idle skip jumped over (the idle loop's share)
        self.cpu_hz = CPU_HZ
        self.ms_base = 0.0
        self.ms_base_cycles = 0
        self.flash.busy_until = self.flash_busy_until(busy_left, busy_hz)
        self.analog_writes = []
        self.analog_log = []     # ('r'|'w', addr, value) in order
        self.tick_base_cycles = 0
        self.tick_base = 0
        self.stimer_on = False
        self.stimer_cmp = 0
        self.irq_latch = 0
        self.gpio_req = False
        self.last_tick = 0
        self.tick2_base = 0
        self.tmr_base = [None, None]     # Timer0/1: the cycle at which their tick was 0
        self.tmr_wrap = [False, False]   # Timer0/1: the next match only after the count wraps (TIMER_CAPTURE_BELOW)
        self.wd_fed_cycles = 0
        self.mspi_auto = False
        self.halted = None
        self.boot_copy = copy
        self.asleep = None       # the system timer value when suspend began
        self.sleep_since = 0.0
        self.k32_wake = None     # 32 kHz timer wake value (0x74c, set by 0x74b bit 3)
        self.reset_count += 1
        for f in getattr(self, "reset_hooks", ()):
            f()
        self.event(f"reset #{self.reset_count}: boot slot 0x{boot_slot:05x}"
                   f"{' (watchdog)' if cause_wd else ''}")

    def event(self, text):
        self.events.append((self.cycles, text))
        if len(self.events) <= self.max_log:
            print(f"[{self.ms():9.3f} ms] {text}")

    def ms(self):
        return self.ms_base + (self.cycles - self.ms_base_cycles) * 1000 / self.cpu_hz

    def sysclk(self):
        """System clock from reg_clk_sel (0x66) and 0x70[0], DS-TLSR8278 4.2."""
        sel, fhs_sel = self.regs_mem[0x66], ((self.regs_mem[0x70] & 1) << 1) | (self.regs_mem[0x66] >> 7)
        fhs = 48_000_000 if fhs_sel == 0 else 24_000_000
        src = (sel >> 5) & 3
        if src == 0:
            return 24_000_000
        if src == 1:
            return fhs
        if src == 2:
            return fhs // max(sel & 0x1F, 2)
        return 32_000_000

    def set_cpu_hz(self, hz):
        if hz == self.cpu_hz:
            return
        now_tick = self.stimer_raw()
        self.ms_base, self.ms_base_cycles = self.ms(), self.cycles
        self.tick_base, self.tick_base_cycles = now_tick, self.cycles
        busy_left, old_hz = (self.flash.busy_until - self.cycles if self.flash.busy() else 0), self.cpu_hz
        self.cpu_hz = hz
        self.flash.busy_until = self.flash_busy_until(busy_left, old_hz)
        self.event(f"CPU clock {hz // 1_000_000} MHz")

    def flash_busy_until(self, left, hz):
        """The flash's busy deadline (Flash.busy_until) for a busy time of
        left cycles at hz, on this clock: the same time in cycles of cpu_hz,
        rounded up to a whole cycle; 0 when nothing is left. A clock change and
        a reset convert what is left this way; sleep and the idle skip advance
        the cycles, so the time goes on through them."""
        if left <= 0:
            return 0
        return self.cycles + -(-left * self.cpu_hz // hz)

    @property
    def sym(self):
        """The symbols (name -> address) that symbolize() names addresses with."""
        return self._sym

    @sym.setter
    def sym(self, symbols):
        # Assigned after construction too (a whole-keyboard runner sets the
        # running image's symbols at each boot): the sorted table follows, as
        # the Go SetSymbols() rebuilds its own.
        self._sym = symbols or {}
        self.addr_sym = sorted((a, n) for n, a in self._sym.items())

    def set_symbols(self, symbols):
        """The Go SetSymbols(): the same as assigning sym."""
        self.sym = symbols

    def symbolize(self, addr):
        import bisect
        i = bisect.bisect_right(self.addr_sym, (addr, "￿")) - 1
        if i < 0:
            return hex(addr)
        a, name = self.addr_sym[i]
        return f"{name}+0x{addr - a:x}"

    # ------------------------------------------------------------ CPSR
    def cpsr(self):
        return (self.n << 31) | (self.z << 30) | (self.c << 29) | (self.v << 28) | (self.i_bit << 7) | self.mode

    def set_cpsr(self, val):
        new_mode = val & 0x1F
        if new_mode not in self.bank:
            raise EmuError(f"mode 0x{new_mode:x} at {self.symbolize(self.r[15])}")
        if new_mode != self.mode:
            self.bank[self.mode] = [self.r[13], self.r[14]]
            self.r[13], self.r[14] = self.bank[new_mode]
            self.mode = new_mode
        self.n, self.z, self.c, self.v = (val >> 31) & 1, (val >> 30) & 1, (val >> 29) & 1, (val >> 28) & 1
        self.i_bit = (val >> 7) & 1

    # ------------------------------------------------------------ time
    def stimer_raw(self):
        if not self.stimer_on or self.stimer_stuck:
            return self.tick_base
        return (self.tick_base + (self.cycles - self.tick_base_cycles) * STIMER_HZ // self.cpu_hz) & M32

    def stimer_now(self):
        return self.stimer_raw() & ~7

    def k32_now(self):
        return int(self.ms() * 32.768) & M32

    def tick2_now(self):
        return ((self.cycles - self.tick2_base) & M32)

    def update_time(self):
        if self.regs_mem[0xB10] & 0x04:
            self.dfifo2_fill()
        now = self.stimer_now()
        if self.stimer_on and self.regs_mem[0x748] & STIMER_IRQ:
            # Compare reached between the last check and now (wrapping arithmetic).
            if ((now - self.stimer_cmp) & M32) < 0x80000000 and ((self.last_tick - self.stimer_cmp) & M32) >= 0x80000000:
                self.irq_latch |= 1 << IRQ_STIMER
        self.last_tick = now
        self.timers_update()
        ctrl = struct.unpack_from("<I", self.regs_mem, 0x620)[0]
        if ctrl & (1 << 23) and ctrl & (1 << 6):
            capture = (ctrl >> 9) & 0x3FFF
            if capture and (self.tick2_now() >> 18) >= capture:
                raise WatchdogReset()

    def tmr_enabled(self, n):
        ctrl = self.regs_mem[0x620]
        return bool(ctrl & (0x01 if n == 0 else 0x08)) and ((ctrl >> (1 if n == 0 else 4)) & 3) == 0

    def tmr_tick(self, n):
        if self.tmr_base[n] is None:
            return int.from_bytes(self.regs_mem[0x630 + 4 * n:0x634 + 4 * n], "little")
        return (self.cycles - self.tmr_base[n]) & M32

    def timers_update(self):
        """Timer0/1 in mode 0 (DS-TLSR8278 5.1.2): the tick counts the system
        clock; when it reaches the capture, the timer's status bit (0x623) is
        set. The tick then starts again from 0, as the SDK's periodic use of the
        timers needs (the datasheet says the timer stops). A tick that is past
        the capture reaches it only after the 32-bit wrap (TIMER_CAPTURE_BELOW).
        A write to Timer0/1's control, capture or count, and a read of
        0x620-0x637, call this first, so a match the count made since the last
        update is taken before the write is judged and is in what the read
        returns. Two things follow that the chip does as well: a 32-bit write
        of 0x620 that clears the status (write one to clear) a few cycles
        after a match no update had seen clears that match (the status was
        set at the match, the word write clears it); and a capture raised a
        few cycles after such a match reports that match. Other modes are not
        modelled."""
        for n in (0, 1):
            if self.tmr_base[n] is None:
                continue
            cap = int.from_bytes(self.regs_mem[0x624 + 4 * n:0x628 + 4 * n], "little")
            if not cap:
                continue
            elapsed = self.cycles - self.tmr_base[n]
            if self.tmr_wrap[n]:
                # The count wraps round to 0 and counts up to the capture as
                # from a start; a capture written after the wrap is judged
                # against the wrapped count.
                if elapsed < 1 << 32:
                    continue
                self.tmr_wrap[n] = False
                self.tmr_base[n] += 1 << 32
                elapsed -= 1 << 32
                self.event(f"timer {n}: the count wrapped round to 0 (capture 0x{cap:x})")
            if elapsed >= cap:
                self.regs_mem[0x623] |= 1 << n
                self.tmr_base[n] += (elapsed // cap) * cap

    def tmr_check_past(self, n, what):
        """After a capture or count write, or a start: a running count at or
        past a non-zero capture waits for the wrap (TIMER_CAPTURE_BELOW)."""
        cap = int.from_bytes(self.regs_mem[0x624 + 4 * n:0x628 + 4 * n], "little")
        if self.tmr_base[n] is not None:
            # The count is 32 bits: fold the base to it, so a timer that ran
            # 2^32 cycles or more with capture 0 (never updated) counts on
            # from its 32-bit count, not from the cycles since its start.
            self.tmr_base[n] = self.cycles - self.tmr_tick(n)
        if self.tmr_base[n] is None or not cap:
            self.tmr_wrap[n] = False
            return
        tick = self.tmr_tick(n)
        if tick < cap:
            self.tmr_wrap[n] = False
            return
        wait_s = ((1 << 32) - tick + cap) / self.cpu_hz
        text = (f"timer {n}: {what} leaves the count 0x{tick:x} at or past the capture 0x{cap:x}: the chip "
                f"matches on equality, so the next match comes when the count has wrapped, {wait_s:.1f} s on, "
                f"at {self.symbolize(self.r[15])}")
        if TIMER_CAPTURE_BELOW == "stop":
            raise EmuError(text)
        self.tmr_wrap[n] = True
        self.event(text)

    def tmr_ctrl_written(self):
        for n in (0, 1):
            on = self.tmr_enabled(n)
            if on and self.tmr_base[n] is None:
                start = int.from_bytes(self.regs_mem[0x630 + 4 * n:0x634 + 4 * n], "little")
                self.tmr_base[n] = self.cycles - start
                self.tmr_check_past(n, "a start")
            elif not on and self.tmr_base[n] is not None:
                self.regs_mem[0x630 + 4 * n:0x634 + 4 * n] = self.tmr_tick(n).to_bytes(4, "little")
                self.tmr_base[n] = None
                self.tmr_wrap[n] = False

    def dfifo2_fill(self):
        """The ADC's DMA (DFIFO2, the misc channel): while enabled (0xb10 bit
        2) it keeps its buffer (0xb08 address, 0xb0b high byte, 0xb0a size in
        16-byte units - 1) full of 32-bit samples. Samples arrive at once here."""
        m = self.regs_mem
        hi = m[0xB0B] or 0x04
        addr = 0x800000 | (hi << 16) | m[0xB08] | (m[0xB09] << 8)
        size = (m[0xB0A] + 1) * 16
        o = addr - SRAM_BASE
        if 0 <= o and o + size <= SRAM_SIZE:
            self.sram[o:o + size] = self.adc_code.to_bytes(4, "little") * (size // 4)

    def cycles_to_stimer(self, target):
        if not self.stimer_on:
            return 0
        delta = (target - self.stimer_now()) & M32
        if delta >= 0x80000000:
            return 0
        return delta * self.cpu_hz // STIMER_HZ + 1

    # ------------------------------------------------------------ memory
    def xip(self, a):
        """The flash address an XIP address reads; see "Memory" in the module's text."""
        s = self.boot_slot
        if a < s:
            return a + s
        if a < 2 * s:
            raise EmuError(f"XIP access 0x{a:x} with boot slot 0x{s:x}: what 0x{s:x}-0x{2 * s - 1:x} "
                           f"reads then is not documented, at {self.symbolize(self.r[15])}")
        return a

    def read(self, a, size):
        if REG_ALIAS <= a < REG_ALIAS + REG_SIZE:
            return self.reg_read(a - REG_ALIAS, size)
        if a < FLASH_SIZE:
            if self.flash.dpd and a >= self.sram_code_end():
                raise EmuError(f"flash read 0x{a:x} while the flash is in deep power-down (0xb9)"
                               f" at {self.symbolize(self.r[15])}")
            if self.flash.busy_until and a >= self.sram_code_end() and self.flash.busy():
                raise EmuError(f"flash read 0x{a:x} while the flash is busy with a program or erase (WIP, "
                               f"{self.flash.busy_left_us():.0f} us left) at {self.symbolize(self.r[15])}")
            if self.flash.cs_low and self.flash.spi_busy() and a >= self.sram_code_end():
                # A command with an address or data phase is underway on the SPI:
                # the flash cannot serve XIP as well, and a fetch here would cut
                # the transaction. The SDK: a program's data "must not reside at
                # flash"; its code that talks to the flash runs from RAM. A startup
                # may fetch from flash with the chip select low and nothing
                # or a bare one-byte command (0xab) sent, so those are allowed.
                raise EmuError(f"XIP read 0x{a:x} while SPI command 0x{self.flash.tx[0]:02x} is underway"
                               f" ({len(self.flash.tx)} bytes sent, chip select low) at {self.symbolize(self.r[15])}")
            p = self.xip(a)
            if p + size > self.flash.size:
                raise EmuError(f"XIP read 0x{a:x} past the end of flash (boot slot 0x{self.boot_slot:x})"
                               f" at {self.symbolize(self.r[15])}")
            if self.icache_miss and a >= self.sram_code_end():
                line = a >> ICACHE_LINE_SHIFT
                last = (a + size - 1) >> ICACHE_LINE_SHIFT
                while True:
                    slot = line % ICACHE_LINES
                    if self.icache_tags[slot] != line:
                        self.icache_tags[slot] = line
                        self.icache_misses += 1
                        self.cycles += self.icache_miss
                    if line == last:
                        break
                    line += 1
            return int.from_bytes(self.flash.mem[p:p + size], "little")
        if SRAM_BASE <= a < SRAM_BASE + SRAM_SIZE:
            o = a - SRAM_BASE
            if o + size > SRAM_SIZE:
                raise EmuError(f"read{size * 8} 0x{a:08x} crosses the end of SRAM at {self.symbolize(self.r[15])}")
            return int.from_bytes(self.sram[o:o + size], "little")
        if REG_BASE <= a < REG_BASE + REG_SIZE:
            return self.reg_read(a - REG_BASE, size)
        raise EmuError(f"read{size * 8} 0x{a:08x} at {self.symbolize(self.r[15])}")

    def write(self, a, size, val):
        if REG_ALIAS <= a < REG_ALIAS + REG_SIZE:
            self.reg_write(a - REG_ALIAS, size, val & ((1 << (size * 8)) - 1))
            return
        if SRAM_BASE <= a < SRAM_BASE + SRAM_SIZE:
            o = a - SRAM_BASE
            if o + size > SRAM_SIZE:
                # A slice assignment past the end would grow the bytearray silently.
                raise EmuError(f"write{size * 8} 0x{a:08x} crosses the end of SRAM at {self.symbolize(self.r[15])}")
            self.sram[o:o + size] = (val & ((1 << (size * 8)) - 1)).to_bytes(size, "little")
            return
        if REG_BASE <= a < REG_BASE + REG_SIZE:
            self.reg_write(a - REG_BASE, size, val & ((1 << (size * 8)) - 1))
            return
        raise EmuError(f"write{size * 8} 0x{a:08x} = 0x{val:x} at {self.symbolize(self.r[15])}")

    # ------------------------------------------------------------ registers
    def reg_read(self, o, size):
        m = self.regs_mem
        if self.reg_log is not None:
            self.reg_log.add((o, size, "r", self.r[15]))
        if o == 0x740 and size == 4:
            return self.stimer_now()
        if o == 0x638 and size == 4:
            return self.tick2_now()
        if o < 0x638 and o + size > 0x620:
            # Timer0/1's control, status, captures and counts: the matches the
            # counts made since the last update are in what a read returns.
            self.timers_update()
        if o in (0x630, 0x634) and size == 4:
            return self.tmr_tick((o - 0x630) // 4)
        if 0x648 <= o <= 0x64B:
            self.update_time()
            val = (self.irq_sources() & 0xFFFFFF).to_bytes(4, "little")
            return int.from_bytes(val[o - 0x648:o - 0x648 + size], "little")
        if o == 0x0C and size == 1:
            if self.flash.beyond is not None:
                if self.flash.beyond_mode == "stop":
                    raise EmuError(f"flash read of 0x{self.flash.beyond:x}: the part has 0x{self.flash.size:x} bytes"
                                   f" at {self.symbolize(self.r[15])}")
                self.flash.log.append(("read beyond", self.flash.beyond, 1))
            if self.mspi_auto:
                v = self.flash.rx
                self.flash.rx = self.flash.clock(0x00)
                return v
            return self.flash.rx
        if o == 0x0D and size == 1:
            return m[0x0D] & ~0x10  # never busy
        if o == 0xBA and size == 1:
            return m[0xBA] & ~0x01  # analog port never busy
        if o == 0x4408 and size == 1:
            return m[0x4408] | 0x01   # random number ready
        if o == 0x440C and size == 4:
            self.rng = (getattr(self, "rng", 0x12345678) * 1103515245 + 12345) & M32
            return self.rng
        if o == 0x74B and size == 1:
            return (self.k32_now() & 1) << 5
        if o == 0x750 and size == 4:
            return self.k32_now()
        if o == 0x754 and size == 4:
            # reg_system_32k_tick_cal: system timer ticks per 32 kHz tick, times
            # 16 (the B87 SDK's cpu_sleep_wakeup scales by it >> 4); measured by
            # the 32 kHz tracking (0x74a bit 3), here the exact 16 MHz / 32768 Hz.
            return STIMER_HZ * 16 // 32768
        if o in (0x580, 0x588, 0x590, 0x598) and size == 1:
            # GPIO inputs: the pads (self.pad_levels, so board and USB models
            # apply), a floating pad at its last level, through the input
            # enable (port C's is analog 0xc0).
            port = (o - 0x580) // 8
            v = self.pad_inputs(port)
            return v & (self.analog[0xC0] if port == 2 else m[o + 1])
        return int.from_bytes(m[o:o + size], "little")

    def reg_write(self, o, size, val):
        m = self.regs_mem
        if self.reg_log is not None:
            self.reg_log.add((o, size, "w", self.r[15]))
        if o == 0x740 and size == 4:
            self.tick_base, self.tick_base_cycles = val, self.cycles
            self.last_tick = val & ~7
            return
        if o == 0x744 and size == 4:
            val &= ~7
            self.stimer_cmp = val
            self.last_tick = self.stimer_now()
            m[o:o + 4] = val.to_bytes(4, "little")
            return
        if o == 0x660 and size == 1:
            # Hardware divider, as the Telink SDK's common/div_mod.S drives it: 0x664
            # dividend, 0x668 divisor; writing the mode to 0x660 starts it and
            # 0x660 reads 0 when done (at once here). UDIV 0 and SDIV 1 leave
            # the quotient in 0x664, UMOD 2 and SMOD 3 the remainder in 0x668.
            a = int.from_bytes(m[0x664:0x668], "little")
            b = int.from_bytes(m[0x668:0x66C], "little")
            if val & 1:
                a = a - (1 << 32) if a & 0x80000000 else a
                b = b - (1 << 32) if b & 0x80000000 else b
            if b == 0:
                q, r = -1, a
            else:
                q = abs(a) // abs(b) * (1 if (a < 0) == (b < 0) else -1)
                r = a - q * b
            m[0x664:0x668] = (q & M32).to_bytes(4, "little")
            m[0x668:0x66C] = (r & M32).to_bytes(4, "little")
            m[0x660] = 0
            self.div_ops = getattr(self, "div_ops", 0) + 1
            return
        if o == 0x608 and size == 4:
            # Load 16 bytes of flash into SRAM at the same offset: bit 24 starts
            # it, bits 23:0 are the offset. Not in the datasheet. It is the
            # loader of Telink's startup for the TLSR8271 and TLSR8251
            # (b85_ble_sdk V3.4.2.2 boot/8271/cstartup_8271_RET_32K.S,
            # "copy flash ram code part to SRAM", register IC_IA): that startup
            # loads its RAM code this way on a cold boot and waits for the top byte to
            # read 0 (here at once).
            if val & (1 << 24):
                off = val & 0xFFFFFF
                src = self.xip(off)
                if off + 16 > SRAM_SIZE:
                    # A slice assignment past the end would grow the bytearray silently.
                    raise EmuError(f"load of flash into SRAM offset 0x{off:x}, past the end of SRAM"
                                   f" at {self.symbolize(self.r[15])}")
                if src + 16 > self.flash.size:
                    raise EmuError(f"load of flash 0x{src:x} into SRAM: the part has 0x{self.flash.size:x} bytes"
                                   f" at {self.symbolize(self.r[15])}")
                self.sram[off:off + 16] = self.flash.mem[src:src + 16]
            m[o:o + 4] = (val & 0x00FFFFFF).to_bytes(4, "little")
            return
        if o == 0x74A and size == 1:
            on = bool(val & STIMER_EN)
            if on != self.stimer_on:
                self.tick_base, self.tick_base_cycles = self.stimer_raw(), self.cycles
                self.stimer_on = on
                self.last_tick = self.stimer_now()
                self.event(f"system timer {'started' if on else 'stopped'}")
            m[o] = val
            return
        if o == 0x638 and size == 4:
            self.tick2_base = (self.cycles - val) & M32
            return
        if 0x648 <= o <= 0x64B:
            mask = val << (8 * (o - 0x648))
            self.irq_latch &= ~mask
            return
        if o in (0x630, 0x634) and size == 4:
            n = (o - 0x630) // 4
            self.timers_update()
            m[o:o + 4] = val.to_bytes(4, "little")
            if self.tmr_base[n] is not None:
                self.tmr_base[n] = self.cycles - val
                self.tmr_check_past(n, "a count write")
            return
        if 0x624 <= o < 0x62C:
            # Judged after each write: a capture written a byte at a time,
            # low byte first, can stop the run where the chip reaches the
            # whole word (the SDK and ZMK write it as one word).
            self.timers_update()
            m[o:o + size] = val.to_bytes(size, "little")
            for n in {(o - 0x624) // 4, (o + size - 1 - 0x624) // 4}:
                if n < 2:
                    self.tmr_check_past(n, "a capture write")
            return
        if o == 0x623 and size == 1:
            m[0x623] &= ~(val & 0x07) & 0xFF    # timer status, write one to clear
            if val & 0x08:
                self.feed()
            return
        if o == 0x620 and size in (1, 2, 4):
            self.timers_update()
            if size == 4:
                m[0x620:0x623] = (val & 0xFFFFFF).to_bytes(3, "little")
                m[0x623] &= ~((val >> 24) & 0x07) & 0xFF
                if val & (1 << 27):
                    self.feed()
            else:
                m[0x620:0x620 + size] = val.to_bytes(size, "little")
            self.tmr_ctrl_written()
            return
        if o == 0x72 and size == 1:
            m[0x72] &= ~val
            return
        if o == 0x6F and size == 1 and val & 0x20:
            raise SoftwareReset()
        if o == 0x6F and size == 1 and val & 0x80:
            m[o] = val
            self.enter_suspend(val)
            return
        if o == 0x74B and size == 1 and val & 0x08:
            self.k32_wake = int.from_bytes(m[0x74C:0x750], "little")
        if o in (0x66, 0x70) and size == 1:
            m[o] = val
            self.set_cpu_hz(self.sysclk())
            return
        if o == 0x0D and size == 1:
            self.flash.select(not (val & 0x01))
            self.mspi_auto = bool(val & 0x08)
            m[0x0D] = val
            return
        if o == 0x0C and size == 1:
            self.flash.rx = self.flash.clock(val)
            return
        if o == 0xBA and size == 1:
            if val & 0x40:
                addr = m[0xB8]
                if val & 0x20:
                    old = self.analog[addr]
                    self.analog[addr] = (old & ~m[0xB9] & 0xFF) if addr == 0x44 else m[0xB9]
                    if addr in (0xC6, 0xC7):
                        # RC calibration: starting it (bit 0) completes at once;
                        # done is analog 0xcf bit 6 (32 kHz, 0xc6) or bit 7
                        # (24 MHz, 0xc7), as the B87 SDK's rc_32k_cal/rc_24m_cal poll.
                        done = 0x40 if addr == 0xC6 else 0x80
                        self.analog[0xCF] = (self.analog[0xCF] | done) if m[0xB9] & 1 else (self.analog[0xCF] & ~done)
                    if 0x0E <= addr <= 0x15:
                        self.hold_pads((addr - 0x0E) // 2)     # a pull changed
                    self.analog_writes.append((addr, m[0xB9], self.symbolize(self.r[15])))
                    self.analog_log.append(("w", addr, m[0xB9]))
                    if addr == 0x0B and (old ^ m[0xB9]) & 0x80:
                        self.event(f"analog 0x0b bit 7 (USB DP pull-up) -> {m[0xB9] >> 7}")
                    if addr == 0xC0:
                        self.gpio_irq_update()     # PC input enable
                else:
                    m[0xB9] = self.analog[addr]
                    if addr == 0x88:
                        m[0xB9] = (m[0xB9] | 0x80) if self.xtal_ready else (m[0xB9] & 0x7F)
                    self.analog_log.append(("r", addr, m[0xB9]))
            m[0xBA] = val
            return
        m[o:o + size] = val.to_bytes(size, "little")
        if o < 0x5A0 and o + size > 0x580:
            for port in range(max(o - 0x580, 0) // 8, min((o + size - 1 - 0x580) // 8, 3) + 1):
                self.hold_pads(port)
        if o < 0x5A0 and o + size > 0x580 or o <= 0x5B5 < o + size:
            self.gpio_irq_update()

    def hold_pads(self, port):
        """Take the levels of a port's driven or pulled pads into pad_hold: a
        pad keeps the level it had when it stops being either. With the
        capacitance model every port is looked at: a pad of another port may
        have been released by this write (a matrix row by its column)."""
        for p in (range(4) if self.pad_c_pf else (port,)):
            self.pad_inputs(p)

    def pad_inputs(self, port):
        """The levels of a port's pads as the input buffers see them: driven or
        pulled pads at their level, a floating pad at its last one, and with
        the capacitance model (PAD_C_PF) a pad left to its pull resistor at its
        old level until the RC time has passed."""
        lvl, flt = self.pad_levels(port)
        v = (lvl & ~flt | self.pad_hold[port] & flt) & 0xFF
        if self.pad_c_pf:
            r, b = self.regs_mem, 0x580 + 8 * port
            for bit in range(8):
                key, mask = (port, bit), 1 << bit
                pull = (self.analog[0x0E + 2 * port + (bit >> 2)] >> (2 * (bit & 3))) & 3
                own = (r[b + 6] >> bit) & 1 and not (r[b + 2] >> bit) & 1
                rest = mask if pull in (1, 3) else 0
                if flt & mask or own or not pull or (v & mask) != rest or (self.pad_hold[port] & mask) == rest:
                    self.pad_rc_since.pop(key, None)
                    continue
                since = self.pad_rc_since.setdefault(key, self.cycles)
                if self.cycles - since >= self.pad_rc_cycles(pull):
                    del self.pad_rc_since[key]
                else:
                    v = v & ~mask | self.pad_hold[port] & mask
        self.pad_hold[port] = v
        return v

    def pad_rc_cycles(self, pull):
        """The cycles a pad takes to reach its pull's level at the present clock."""
        need_ns = 1204 * PAD_R_OHM[pull] * self.pad_c_pf * self.pad_r_pct // 100 // 1_000_000
        return -(-need_ns * self.cpu_hz // 1_000_000_000)

    def pad_rc_service(self):
        """The pads that are on their way to their pull's level: those whose time
        has passed take it, and the GPIO interrupt sees the change (on the chip
        the edge comes when the pad crosses the threshold). Called by run()."""
        before = list(self.pad_hold)
        for port in range(4):
            self.pad_inputs(port)
        if self.pad_hold != before:
            self.gpio_irq_update()

    def pad_rc_due(self):
        """Cycles until the first pad on its way reaches its pull's level."""
        due = None
        for (port, bit), since in self.pad_rc_since.items():
            pull = (self.analog[0x0E + 2 * port + (bit >> 2)] >> (2 * (bit & 3))) & 3
            left = since + self.pad_rc_cycles(pull) - self.cycles if pull else 0
            due = left if due is None else min(due, left)
        return due

    def gpio_irq_update(self):
        """GPIO interrupt (DS-TLSR8278 7.1.3): the request is
        |((input ^ polarity) & irq) over the four ports (registers 0x580+8p,
        +4, +7), enabled by 0x5b5 bit 3; irq_gpio (source 18) is
        edge-triggered (Table 6-1), so a rising request latches it. Call this
        when a pin level changes (board models do)."""
        r, req = self.regs_mem, 0
        if r[0x5B5] & 0x08:
            for p in range(4):
                b = 0x580 + 8 * p
                req |= (self.reg_read(b, 1) ^ r[b + 4]) & r[b + 7]
        if req and not self.gpio_req:
            self.irq_latch |= 1 << 18
        self.gpio_req = bool(req)

    def feed(self):
        # Assumed: clearing the watchdog status (0x623 bit 3) restarts the count,
        # as the Telink SDK's wd_clear() is used to keep the watchdog off.
        self.tick2_base = self.cycles
        self.wd_fed_cycles = self.cycles

    # ------------------------------------------------------------ IRQ
    def irq_sources(self):
        level = 0
        for f in self.level_sources:
            level |= f()
        return self.irq_latch | (level & 0xFFFF)

    def irq_pending(self):
        m = self.regs_mem
        if not (m[0x643] & 1) or self.i_bit:
            return False
        mask = m[0x640] | m[0x641] << 8 | m[0x642] << 16
        return (self.irq_sources() & mask) != 0

    def take_irq(self):
        ret = self.r[15]
        cpsr = self.cpsr()
        self.set_cpsr((cpsr & ~0x9F) | 0x80 | MODE_IRQ)
        self.spsr[MODE_IRQ] = cpsr
        self.r[14] = ret
        self.r[15] = 0x10

    # ------------------------------------------------------------ flags
    def nz(self, x):
        self.n = (x >> 31) & 1
        self.z = 1 if x == 0 else 0

    def add(self, a, b, carry=0):
        s = a + b + carry
        r = s & M32
        self.nz(r)
        self.c = 1 if s > M32 else 0
        self.v = 1 if (~(a ^ b) & (a ^ r)) & 0x80000000 else 0
        return r

    def sub(self, a, b, borrow_in=1):
        # a - b as a + ~b + carry, like ARM.
        return self.add(a, (~b) & M32, borrow_in)

    def cond(self, cc):
        n, z, c, v = self.n, self.z, self.c, self.v
        return [z, not z, c, not c, n, not n, v, not v, c and not z, (not c) or z,
                n == v, n != v, (not z) and n == v, z or n != v, True, False][cc]

    # ------------------------------------------------------------ execution
    def step(self):
        if self.asleep is not None:
            return
        pc = self.r[15]
        hook = self.hooks.get(pc)
        if hook is not None:
            hook(self)
            if self.r[15] != pc:
                return
        hw = self.read(pc, 2)
        self.r[15] = pc + 2
        self.cycles += self.cpi

        # TC32-only instructions.
        if hw & 0xFFE0 == 0x6BC0:
            op, rd = (hw >> 3) & 3, hw & 7
            if op == 0:
                self.set_cpsr(self.r[rd])
            elif op == 1:
                self.r[rd] = self.cpsr()
            elif op == 2:
                self.spsr[self.mode] = self.r[rd]
            else:
                self.r[rd] = self.spsr[self.mode]
            return
        if hw & 0xFE00 == 0x6800:  # treti {list[, pc]}
            if not hw & 0x100:
                # Telink's code returns with treti {..., pc} only.
                raise EmuError(f"treti without pc (0x{hw:04x}) at {self.symbolize(pc)}: not modelled")
            sp = self.r[13]
            for i in range(8):
                if hw & (1 << i):
                    self.r[i] = self.read(sp, 4)
                    sp += 4
            newpc = None
            if hw & 0x100:
                newpc = self.read(sp, 4)
                sp += 4
            self.r[13] = sp
            self.set_cpsr(self.spsr[self.mode])
            if newpc is not None:
                self.r[15] = newpc & ~1
            if self.on_treti is not None:
                self.on_treti(self)
            return
        if hw & 0xFF00 == 0xCF00:
            raise EmuError(f"tserv {hw & 0xff} at {self.symbolize(pc)}")

        t = to_thumb(hw)
        top = t >> 11
        r = self.r
        if top <= 2:  # shift by immediate
            imm, rm, rd = (t >> 6) & 31, (t >> 3) & 7, t & 7
            x = r[rm]
            if top == 0:
                if imm:
                    self.c = (x >> (32 - imm)) & 1
                    x = (x << imm) & M32
            elif top == 1:
                imm = imm or 32
                self.c = (x >> (imm - 1)) & 1
                x = (x >> imm) if imm < 32 else 0
            else:
                imm = imm or 32
                self.c = (x >> (imm - 1)) & 1 if imm < 32 else x >> 31
                sx = x - (1 << 32) if x & 0x80000000 else x
                x = (sx >> min(imm, 31)) & M32
            r[rd] = x
            self.nz(x)
        elif top == 3:  # add/sub register or imm3
            rd, rn, f = t & 7, (t >> 3) & 7, (t >> 6) & 7
            b = f if t & 0x400 else r[f]
            r[rd] = self.sub(r[rn], b) if t & 0x200 else self.add(r[rn], b)
        elif top <= 7:  # mov/cmp/add/sub imm8
            rd, imm = (t >> 8) & 7, t & 0xFF
            op = top - 4
            if op == 0:
                r[rd] = imm
                self.nz(imm)
            elif op == 1:
                self.sub(r[rd], imm)
            elif op == 2:
                r[rd] = self.add(r[rd], imm)
            else:
                r[rd] = self.sub(r[rd], imm)
        elif t >> 10 == 0x10:  # ALU
            self.alu((t >> 6) & 15, t & 7, (t >> 3) & 7)
        elif t >> 10 == 0x11:  # hi register ops, bx
            op = (t >> 8) & 3
            rd = (t & 7) | ((t >> 4) & 8)
            rm = (t >> 3) & 15
            # ARMv4T leaves these UNPREDICTABLE, and no Telink code uses them.
            if op == 3 and t & 0x87:
                raise EmuError(f"bx with bit 7 or bits 2:0 set (0x{hw:04x}) at {self.symbolize(pc)}: "
                               "UNPREDICTABLE in ARMv4T")
            if op < 3 and rd < 8 and rm < 8:
                raise EmuError(f"high-register add/cmp/mov with two low registers (0x{hw:04x}) at "
                               f"{self.symbolize(pc)}: UNPREDICTABLE in ARMv4T")
            # PC as an operand of these: ARM Thumb reads the instruction's
            # address + 4; the LLVM TC32 backend's jump tables ("tadd rX, pc;
            # tloadr rX, [rX, #8]") assume it word-aligned. Nothing shows which
            # the TC32 does (Telink's code never reads PC this way), and
            # the two differ only at a halfword address: stop there.
            reads_pc = rm == 15 or (rd == 15 and op in (0, 1))
            if reads_pc and (pc + 4) & 2:
                raise EmuError(f"PC read by a high-register add/cmp/mov at 0x{pc:x}, a halfword address: "
                               "unknown on TC32 (ARM gives pc+4, LLVM TC32 assumes it word-aligned)")
            vm = (pc + 4) if rm == 15 else r[rm]
            if op == 0:
                vd = (pc + 4) if rd == 15 else r[rd]
                self.setreg(rd, (vd + vm) & M32)
            elif op == 1:
                vd = (pc + 4) if rd == 15 else r[rd]
                self.sub(vd, vm)
            elif op == 2:
                self.setreg(rd, vm)
            else:
                r[15] = vm & ~1
        elif top == 9:  # ldr pc-relative
            r[(t >> 8) & 7] = self.read(((pc + 4) & ~3) + (t & 0xFF) * 4, 4)
        elif top in (0x0A, 0x0B):  # load/store register offset
            op, ro, rb, rd = (t >> 9) & 7, (t >> 6) & 7, (t >> 3) & 7, t & 7
            a = (r[rb] + r[ro]) & M32
            if op == 0:
                self.write(self.chk(a, 4), 4, r[rd])
            elif op == 1:
                self.write(self.chk(a, 2), 2, r[rd])
            elif op == 2:
                self.write(a, 1, r[rd])
            elif op == 3:
                x = self.read(a, 1)
                r[rd] = (x - 0x100) & M32 if x & 0x80 else x
            elif op == 4:
                r[rd] = self.read(self.chk(a, 4), 4)
            elif op == 5:
                r[rd] = self.read(self.chk(a, 2), 2)
            elif op == 6:
                r[rd] = self.read(a, 1)
            else:
                x = self.read(self.chk(a, 2), 2)
                r[rd] = (x - 0x10000) & M32 if x & 0x8000 else x
        elif top in (0x0C, 0x0D):  # ldr/str word imm5
            imm, rb, rd = (t >> 6) & 31, (t >> 3) & 7, t & 7
            a = (r[rb] + imm * 4) & M32
            if top == 0x0D:
                r[rd] = self.read(self.chk(a, 4), 4)
            else:
                self.write(self.chk(a, 4), 4, r[rd])
        elif top in (0x0E, 0x0F):  # ldrb/strb imm5
            imm, rb, rd = (t >> 6) & 31, (t >> 3) & 7, t & 7
            a = (r[rb] + imm) & M32
            if top == 0x0F:
                r[rd] = self.read(a, 1)
            else:
                self.write(a, 1, r[rd])
        elif top in (0x10, 0x11):  # ldrh/strh imm5
            imm, rb, rd = (t >> 6) & 31, (t >> 3) & 7, t & 7
            a = (r[rb] + imm * 2) & M32
            if top == 0x11:
                r[rd] = self.read(self.chk(a, 2), 2)
            else:
                self.write(self.chk(a, 2), 2, r[rd])
        elif top in (0x12, 0x13):  # sp-relative
            rd, a = (t >> 8) & 7, (r[13] + (t & 0xFF) * 4) & M32
            if top == 0x13:
                r[rd] = self.read(self.chk(a, 4), 4)
            else:
                self.write(self.chk(a, 4), 4, r[rd])
        elif top in (0x14, 0x15):  # add rd, pc/sp, imm8*4
            rd = (t >> 8) & 7
            base = r[13] if top == 0x15 else (pc + 4) & ~3
            r[rd] = (base + (t & 0xFF) * 4) & M32
        elif top in (0x16, 0x17):  # misc
            if t & 0xFF00 == 0xB000:
                imm = (t & 0x7F) * 4
                r[13] = (r[13] - imm) & M32 if t & 0x80 else (r[13] + imm) & M32
            elif t & 0xF600 == 0xB400:  # push/pop
                if not t & 0x1FF:
                    raise EmuError(f"push/pop of no registers (0x{hw:04x}) at {self.symbolize(pc)}: "
                                   "UNPREDICTABLE in ARMv4T")
                if t & 0x0800:  # pop
                    sp = r[13]
                    for i in range(8):
                        if t & (1 << i):
                            r[i] = self.read(self.chk(sp, 4), 4)
                            sp += 4
                    if t & 0x100:
                        r[15] = self.read(self.chk(sp, 4), 4) & ~1
                        sp += 4
                    r[13] = sp
                else:
                    regs = [i for i in range(8) if t & (1 << i)] + ([14] if t & 0x100 else [])
                    sp = (r[13] - 4 * len(regs)) & M32
                    r[13] = sp
                    for i in regs:
                        self.write(self.chk(sp, 4), 4, r[i])
                        sp += 4
            else:
                raise EmuError(f"undefined 0x{hw:04x} at {self.symbolize(pc)}")
        elif top in (0x18, 0x19):  # stmia/ldmia
            rb = (t >> 8) & 7
            if not t & 0xFF:
                raise EmuError(f"ldm/stm of no registers (0x{hw:04x}) at {self.symbolize(pc)}: "
                               "UNPREDICTABLE in ARMv4T")
            if top == 0x18 and t & (1 << rb) and t & ((1 << rb) - 1):
                raise EmuError(f"stm storing its base register after a lower one (0x{hw:04x}) at "
                               f"{self.symbolize(pc)}: the stored value is UNPREDICTABLE in ARMv4T")
            a = r[rb]
            for i in range(8):
                if t & (1 << i):
                    if top == 0x19:
                        r[i] = self.read(self.chk(a, 4), 4)
                    else:
                        self.write(self.chk(a, 4), 4, r[i])
                    a += 4
            if not (top == 0x19 and t & (1 << rb)):
                r[rb] = a & M32
        elif top in (0x1A, 0x1B):  # conditional branch
            cc = (t >> 8) & 15
            if cc >= 14:
                raise EmuError(f"undefined 0x{hw:04x} at {self.symbolize(pc)}")
            if self.cond(cc):
                off = t & 0xFF
                off = off - 0x100 if off & 0x80 else off
                r[15] = (pc + 4 + off * 2) & M32
        elif top == 0x1C:  # b
            off = t & 0x7FF
            off = off - 0x800 if off & 0x400 else off
            r[15] = (pc + 4 + off * 2) & M32
        elif top == 0x1E:  # bl, first half
            off = t & 0x7FF
            off = off - 0x800 if off & 0x400 else off
            r[14] = (pc + 4 + (off << 12)) & M32
        elif top == 0x1F:  # bl, second half
            target = (r[14] + (t & 0x7FF) * 2) & M32
            r[14] = (pc + 2) | 0
            r[15] = target
        else:
            raise EmuError(f"undefined 0x{hw:04x} at {self.symbolize(pc)}")

    def chk(self, a, size):
        if a % size:
            raise EmuError(f"unaligned {size * 8}-bit access 0x{a:08x} at {self.symbolize(self.r[15] - 2)}")
        return a

    def setreg(self, rd, val):
        if rd == 15:
            self.r[15] = val & ~1
        else:
            self.r[rd] = val

    def alu(self, op, rd, rs):
        r = self.r
        a, b = r[rd], r[rs]
        if op == 0:
            r[rd] = a & b
            self.nz(r[rd])
        elif op == 1:
            r[rd] = a ^ b
            self.nz(r[rd])
        elif op in (2, 3, 4, 7):  # lsl, lsr, asr, ror by register
            s = b & 0xFF
            x = a
            if op == 2:
                if s:
                    self.c = (x >> (32 - s)) & 1 if s <= 32 else 0
                    x = (x << s) & M32 if s < 32 else 0
            elif op == 3:
                if s:
                    self.c = (x >> (s - 1)) & 1 if s <= 32 else 0
                    x = x >> s if s < 32 else 0
            elif op == 4:
                if s:
                    sx = x - (1 << 32) if x & 0x80000000 else x
                    self.c = (sx >> (min(s, 32) - 1)) & 1
                    x = (sx >> min(s, 31)) & M32
            else:
                if s:
                    k = s & 31
                    x = ((x >> k) | (x << (32 - k))) & M32 if k else x
                    self.c = x >> 31
            r[rd] = x
            self.nz(x)
        elif op == 5:
            r[rd] = self.add(a, b, self.c)
        elif op == 6:
            r[rd] = self.sub(a, b, self.c)
        elif op == 8:
            self.nz(a & b)
        elif op == 9:
            r[rd] = self.sub(0, b)
        elif op == 10:
            self.sub(a, b)
        elif op == 11:
            self.add(a, b)
        elif op == 12:
            r[rd] = a | b
            self.nz(r[rd])
        elif op == 13:
            r[rd] = (a * b) & M32
            self.nz(r[rd])
        elif op == 14:
            r[rd] = a & ~b & M32
            self.nz(r[rd])
        else:
            r[rd] = ~b & M32
            self.nz(r[rd])

    # ------------------------------------------------------------ suspend
    def enter_suspend(self, val):
        """0x6f bit 7 (the B87 SDK's sleep_start writes 0x81): the chip stops
        until a wake source enabled in analog 0x26 fires (PM_WAKEUP_PAD 0x08,
        CORE 0x10, TIMER 0x20), then runs on after the store with the cause in
        analog 0x44 (WAKEUP_STATUS_TIMER 0x02, CORE 0x04, PAD 0x08; write one
        to clear). Only suspend (analog 0x7e = 0, as the SDK's
        cpu_sleep_wakeup() sets it for SUSPEND_MODE) is modelled; the deep modes
        power the chip down and boot again."""
        mode = self.analog[0x7E]
        if mode != 0:
            raise EmuError(f"sleep with analog 0x7e = 0x{mode:02x}: only suspend (0) is modelled"
                           f" at {self.symbolize(self.r[15])}")
        self.asleep = self.stimer_raw()
        self.sleep_since = self.ms()
        self.event(f"suspend (0x6f = 0x{val:02x}, wake sources analog 0x26 = 0x{self.analog[0x26]:02x})")

    def sram_code_end(self):
        """End of the low addresses the CPU reads from SRAM rather than
        flash: the boot ROM's copy, or up to 0x60c * 256 once the firmware has
        set 0x60c (the instruction cache's tag location). Assumed: a startup
        can load 0xd0-0x5000 into SRAM itself, set 0x60c = 0x50 and run its
        sleep code (flash powered down) from there."""
        return max(self.boot_copy, self.regs_mem[0x60C] << 8)

    def pad_levels(self, port):
        """(levels, floating) of a port's pads as they are electrically, not
        through the input enable: a GPIO pad with its output enabled drives its
        output bit; otherwise its pull resistor decides (analog 0x0e + 2 * port,
        2 bits a pin: 1 is 1 MOhm up, 2 100 kOhm down, 3 10 kOhm up) and with
        none it floats. Board and USB models override this for what is wired
        to the pads."""
        r, b = self.regs_mem, 0x580 + 8 * port
        lvl = flt = 0
        for bit in range(8):
            if (r[b + 6] >> bit) & 1 and not (r[b + 2] >> bit) & 1:
                lvl |= ((r[b + 3] >> bit) & 1) << bit
                continue
            pull = (self.analog[0x0E + 2 * port + (bit >> 2)] >> (2 * (bit & 3))) & 3
            if pull in (1, 3):
                lvl |= 1 << bit
            elif pull == 0:
                flt |= 1 << bit
        return lvl, flt

    def wake_status(self):
        """The analog 0x44 bits of the enabled wake sources that fire now. Pad
        wake (cpu_set_gpio_wakeup(): enable analog 0x27 + port, polarity 0x21
        + port with 1 waking on low) on a floating pad is recorded in
        float_wakes: its level is undefined on the chip."""
        src, st = self.analog[0x26], 0
        if src & 0x08:
            for port in range(4):
                en = self.analog[0x27 + port]
                if not en:
                    continue
                lvl, flt = self.pad_levels(port)
                if en & flt:
                    self.float_wakes.add((port, en & flt))
                pol = self.analog[0x21 + port]
                if en & ~flt & ((~lvl & pol) | (lvl & ~pol)) & 0xFF:
                    st |= 0x08
        if src & 0x10 and any(f() for f in self.core_wake_sources):
            st |= 0x04
        if src & 0x20 and self.k32_wake is not None and ((self.k32_now() - self.k32_wake) & M32) < 0x80000000:
            st |= 0x02
        return st

    def sleep_until(self, max_cycles):
        st = self.wake_status()
        if st:
            self.analog[0x44] |= st
            self.asleep = None
            self.wakes.append((self.ms(), st))
            self.event(f"wake: analog 0x44 = 0x{st:02x} after {self.ms() - self.sleep_since:.1f} ms")
            return
        chunk = max(1, min(max_cycles - self.cycles, self.cpu_hz // 1000))
        if self.analog[0x26] & 0x20 and self.k32_wake is not None:
            # The timer wake comes at the 32 kHz tick that reaches the wake
            # value, not at the next check: the SDK wakes 0x24b0 system ticks
            # (0.59 ms) early and clamps the system timer it restores to just
            # before its wakeup time, so a wake taken later than that margin
            # would drop the difference from the firmware's clock.
            d = (self.k32_wake - self.k32_now()) & M32
            if d < 0x80000000:
                now = self.ms()
                need = math.ceil(((int(now * 32.768) + d) / 32.768 - now) * self.cpu_hz / 1000)
                chunk = max(1, min(chunk, need))
        # The clocks are off: the system timer and Timer0-2 (so the watchdog) stand still.
        self.cycles += chunk
        self.tick_base_cycles += chunk
        self.tick2_base += chunk
        self.wd_fed_cycles += chunk
        for n in (0, 1):
            if self.tmr_base[n] is not None:
                self.tmr_base[n] += chunk

    # ------------------------------------------------------------ run loop
    def run_ms(self, target_ms, idle_ranges=()):
        """Run until the simulated time reaches target_ms (the clock may change on the way)."""
        while self.ms() < target_ms:
            left = int((target_ms - self.ms()) * self.cpu_hz / 1000) + 1
            self.run(self.cycles + min(left, self.cpu_hz // 100), idle_ranges)

    def run(self, max_cycles, idle_ranges=(), check_every=32):
        """Run until max_cycles or a Stop; returns the reason."""
        while self.cycles < max_cycles:
            if self.asleep is not None:
                self.sleep_until(max_cycles)
                continue
            for _ in range(check_every):
                self.step()
            if self.asleep is not None:
                continue
            try:
                self.update_time()
            except WatchdogReset:
                raise
            if self.pad_rc_since:
                self.pad_rc_service()
            if self.irq_pending():
                self.take_irq()
            elif (idle_ranges and self.regs_mem[0x643] & 1 and not self.i_bit
                  and any(lo <= self.r[15] < hi for lo, hi in idle_ranges)):
                # Idle loop: jump to the next compare or watchdog deadline. Only
                # while an interrupt could be taken: one that became pending while
                # they are off is taken when the code turns them on, not at the
                # next compare (checks/idle_skip_check.py).
                skip = self.cycles_to_stimer(self.stimer_cmp) if self.regs_mem[0x748] & STIMER_IRQ else 0
                skip = min(skip, max_cycles - self.cycles)   # the caller's time limit stands
                if self.pad_rc_since:
                    skip = min(skip, max(self.pad_rc_due(), 0) + 32)   # a pad reaching its level is an event
                if skip > 64:
                    self.cycles += skip - 32
                    self.idle_skipped += skip - 32
        return "cycles"


class WatchdogReset(Exception):
    pass


class SoftwareReset(Exception):
    pass


def load_symbols(nm_output):
    syms = {}
    for line in nm_output.splitlines():
        parts = line.split()
        if len(parts) == 3 and parts[1] in "TtWw":
            syms[parts[2]] = int(parts[0], 16)
    return syms

"""How much of each stack a Zephyr image uses in tc32emu.

The stacks are the ELF's data objects in the noinit section whose names
contain "stack" (llvm-nm -S --format=sysv): the interrupt stack, the main,
idle and work queue threads' and those of K_THREAD_DEFINE threads, which
Zephyr places there. Objects of other sections are left alone even when
their names contain "stack": the image relies on their contents, such as a
kernel object type (obj_type_stack) or a zero-initialized k_stack.
The startup code clears them, so paint() fills them with 0xaa when z_cstart
starts (as CONFIG_INIT_STACKS would), except for the part at and above a stack pointer
that points into one (lo < sp <= top; the startup runs on the interrupt
stack, its sp at the top). used() then takes the lowest byte that no longer holds 0xaa as the deepest use: stacks
grow down. Bytes written with 0xaa themselves look unused, so a frame whose
deepest word is 0xaaaaaaaa would read up to 4 bytes short.

The emulator does not nest interrupts deeper than the image lets them, and
its runs do not reach every path; hence the margin MARGIN.

SPDX-License-Identifier: Apache-2.0
"""
import subprocess

import run_boot
import tc32emu as te

PAINT = 0xAA
MARGIN = 0.25  # fraction of each stack that must stay unused
SECTION = "noinit"  # Zephyr's output section for thread and kernel stacks


def stacks(elf):
    """[(name, address, size)] of the image's stacks."""
    out = subprocess.run([run_boot.NM, "-S", "--format=sysv", elf], check=True, capture_output=True,
                         text=True).stdout
    found = []
    for line in out.splitlines():
        # name | value | class | type | size | line | section
        p = [f.strip() for f in line.split("|")]
        if (len(p) == 7 and len(p[2]) == 1 and p[2] in "BbDd" and p[6] == SECTION and "stack" in p[0]
                and p[4] and int(p[4], 16) >= 64):
            found.append((p[0], int(p[1], 16), int(p[4], 16)))
    return sorted(found, key=lambda s: s[1])


def paint(m, elf, addr):
    """Hook z_cstart to paint the stacks; returns them."""
    found = stacks(elf)

    def at_cstart(mm):
        sps = [mm.r[13]] + [b[0] for b in mm.bank.values()]
        for _name, lo, size in found:
            # A full-descending stack is in use below sp: lo < sp <= top.
            hi = min([lo + size] + [sp for sp in sps if lo < sp <= lo + size])
            o = lo - te.SRAM_BASE
            mm.sram[o:o + (hi - lo)] = bytes([PAINT]) * (hi - lo)
        painted.append(mm.ms())
    painted = []
    m.hooks[addr["z_cstart"]] = at_cstart
    return found, painted


def used(m, found):
    """[(name, size, bytes used)]."""
    out = []
    for name, lo, size in found:
        o = lo - te.SRAM_BASE
        mem = m.sram[o:o + size]
        first = next((i for i, b in enumerate(mem) if b != PAINT), size)
        out.append((name, size, size - first))
    return out


def verdict(results):
    """(ok, text) for check(): every stack keeps MARGIN of it unused."""
    over = [f"{n} {u}/{s}" for n, s, u in results if u > s * (1 - MARGIN)]
    text = ", ".join(f"{n} {u} of {s} B" for n, s, u in results)
    return not over, (f"stack use within {100 - int(MARGIN * 100)}% of each stack: {text}"
                      + (f"; over: {over}" if over else ""))

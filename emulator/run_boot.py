#!/usr/bin/env python3
"""Boot a TC32 Zephyr/ZMK image in tc32emu and report what it reaches.

Usage:
  run_boot.py --elf zmk.elf --bin zmk.ota.bin [--layout direct|installed]
              [--slot-a original.bin] [--ms 300] [--hang SYMBOL] [--resets N]
              [--usb]

Layouts:
  direct     the image alone in slot A (0x00000)
  installed  as an OTA update leaves it: the image in slot B (0x20000) and
             bootable, the original firmware (--slot-a) in slot A with its
             flag word cleared

--ms is the simulated time per boot. --hang SYMBOL makes every call of
SYMBOL spin forever (a hang for the watchdog to catch). Watchdog and software
resets restart the machine with the flash kept, up to --resets times; the boot
ROM model then starts the slot whose byte 8 is 0x4b (0x00000 first).

--usb attaches the USB device controller model with an idle host, for images
whose USB driver must find the controller's registers as on the chip.

SPDX-License-Identifier: Apache-2.0
"""
import argparse
import os
import subprocess
import sys

import tc32emu as te

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(os.path.dirname(HERE), "common"))
import toolchain  # noqa: E402
NM = toolchain.tool("llvm-nm")

MILESTONES = ["z_cstart", "z_sys_init_run_level", "tlsr_boot_guard_boot", "usb_dc_attach", "main",
              "zmk_keymap_init", "z_tc32_handle_irqs", "idle", "tlsr_slot_revert", "tlsr_reboot",
              "sys_arch_reboot"]
FATAL = ["z_fatal_error", "z_irq_spurious", "arch_system_halt", "k_sys_fatal_error_handler",
         "__assert_post_action", "assert_post_action", "z_tc32_fatal_error"]


def symbols(elf):
    """Code symbols' addresses and sizes. In an ELF linked as ARM Thumb
    (TC32_THUMB builds) code symbols have bit 0 set; the address is even."""
    out = subprocess.run([NM, "-S", elf], check=True, capture_output=True, text=True).stdout
    addr, size = {}, {}
    for line in out.splitlines():
        p = line.split()
        if len(p) == 4 and p[2] in "TtWw":
            addr[p[3]], size[p[3]] = int(p[0], 16) & ~1, int(p[1], 16)
        elif len(p) == 3 and p[1] in "TtWw":
            addr[p[2]] = int(p[0], 16) & ~1
    return addr, size


def idle_ranges(addr, size):
    """The idle functions' address ranges: Machine.run() skips ahead from there
    to the next system timer compare while an interrupt could be taken
    (0x800643 bit 0 set, the CPSR's I bit clear)."""
    return [(addr[n], addr[n] + size.get(n, 0)) for n in ("arch_cpu_idle", "arch_cpu_atomic_idle", "idle")
            if n in addr]


def data_symbols(elf):
    out = subprocess.run([NM, elf], check=True, capture_output=True, text=True).stdout
    return {p[2]: int(p[0], 16) for p in (line.split() for line in out.splitlines())
            if len(p) == 3 and p[1] in "BbDd"}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--elf", required=True)
    ap.add_argument("--bin", required=True)
    ap.add_argument("--layout", choices=("direct", "installed"), default="direct")
    ap.add_argument("--slot-a", help="installed layout: the image to put in slot A (the original firmware)")
    ap.add_argument("--ms", type=float, default=300)
    ap.add_argument("--hang")
    ap.add_argument("--resets", type=int, default=0)
    ap.add_argument("--cpi", type=int, default=1, help="cycles per instruction")
    ap.add_argument("--stacks", action="store_true", help="paint the stacks at z_cstart and report their use (stack_use.py)")
    ap.add_argument("--reg-audit", action="store_true",
                    help="log every register access and print reg_audit.py's rows (TLSR8258 vs TLSR8278 names)")
    ap.add_argument("--reviewed", help="the reviewed list for --reg-audit (offset, note CSV)")
    ap.add_argument("--console", type=lambda s: int(s, 0), nargs="?", const=0xFFF0, default=None,
                    help="capture the bytes the image writes to this register offset (default 0xfff0) as its console")
    ap.add_argument("--console-out", help="write the captured console text to this file (else print it after the summary)")
    ap.add_argument("--console-stream", action="store_true",
                    help="with --console: print each console line on stdout when the image completes it, among the "
                         "run's events, instead of the whole text after the summary (for a test runner reading the "
                         "output as it comes)")
    ap.add_argument("--stop-at", help="end the run when this symbol is reached (e.g. _exit), before --ms is up")
    ap.add_argument("--fatal-continue", action="store_true",
                    help="log the fatal-error symbols (asserts, faults) but let the image handle them instead of stopping")
    ap.add_argument("--stop-line", action="append", default=[],
                    help="with --console: end the run when a console line contains this text (may repeat)")
    ap.add_argument("--usb", action="store_true",
                    help="attach the USB device controller model (usb_model.UsbModel, interrupt lines on) with its "
                         "host idle: no bus reset and no requests, the controller's registers as on the chip")
    a = ap.parse_args()

    addr, size = symbols(a.elf)
    img = open(a.bin, "rb").read()
    fl = te.Flash()
    if a.layout == "direct":
        fl.mem[:len(img)] = img
    else:
        fl.mem[0x20000:0x20000 + len(img)] = img
        if a.slot_a:
            orig = open(a.slot_a, "rb").read()[:0x20000]
            fl.mem[:len(orig)] = orig
            fl.mem[8:12] = b"\0\0\0\0"
    m = te.Machine(fl, symbols=addr, cpi=a.cpi)
    if a.usb:
        import usb_model
        usb_model.UsbModel(m, irq_lines=True)
    if a.reg_audit:
        m.reg_log = set()
    console = bytearray()
    streamed = [0]   # with --console-stream: how much of the console is on stdout

    def stream_console(end):
        """Write console[streamed:end] to stdout after the events printed so far."""
        if end > streamed[0]:
            sys.stdout.flush()
            sys.stdout.buffer.write(bytes(console[streamed[0]:end]))
            sys.stdout.buffer.flush()
            streamed[0] = end

    if a.console is not None:
        # The image's console: one byte per write to the register (a log
        # backend the emulator reads; the register is plain storage otherwise).
        base_write = m.reg_write

        def reg_write(o, size, val, _w=base_write):
            if o == a.console and size == 1:
                console.append(val & 0xFF)
                if val & 0xFF == 10 and a.console_stream:
                    stream_console(len(console))
                if val & 0xFF == 10 and a.stop_line:
                    line = console[console.rfind(b"\n", 0, len(console) - 1) + 1:-1].decode("utf-8", "replace")
                    hit = next((t for t in a.stop_line if t in line), None)
                    if hit is not None:
                        stopped_line.append((m.ms(), hit))
                        m.event(f"--stop-line: console line contains {hit!r}")
                        _w(o, size, val)
                        raise te.Stop("stop-line")
            return _w(o, size, val)
        m.reg_write = reg_write
    stopped_line = []
    seen = {}
    fatal = []
    emu_stop = False

    def milestone(name):
        def hook(mm):
            if name not in seen:
                seen[name] = mm.ms()
                mm.event(f"reached {name}")
        return hook

    def fatal_hook(name):
        def hook(mm):
            caller = mm.symbolize(mm.r[14])
            fatal.append(f"{name} (r0={mm.r[0]:#x}, lr={caller})")
            mm.event(f"FATAL {name}: r0={mm.r[0]:#x} r1={mm.r[1]:#x} lr={caller}")
            if not a.fatal_continue:
                raise te.Stop()
        return hook

    def hang_hook(mm):
        mm.event(f"--hang: {a.hang} spins")
        mm.r[15] = addr[a.hang]  # stays here; the hook runs again each step
        mm.cycles += 1
        raise te.Stop("hang")

    for n in MILESTONES:
        if n in addr:
            m.hooks[addr[n]] = milestone(n)
    stopped_at = []
    if a.stop_at and a.stop_at in addr:
        def stop_hook(mm):
            stopped_at.append(mm.ms())
            mm.event(f"--stop-at: reached {a.stop_at}")
            raise te.Stop("stop-at")
        m.hooks[addr[a.stop_at]] = stop_hook
    found = []
    if a.stacks and "z_cstart" in addr:
        import stack_use
        found, _painted = stack_use.paint(m, a.elf, addr)
        cstart = milestone("z_cstart")
        paint_hook = m.hooks[addr["z_cstart"]]

        def both(mm, paint_hook=paint_hook, cstart=cstart):
            paint_hook(mm)
            cstart(mm)
        m.hooks[addr["z_cstart"]] = both
    for n in FATAL:
        if n in addr:
            m.hooks[addr[n]] = fatal_hook(n)
    idle = [(addr[n], addr[n] + size.get(n, 0)) for n in ("arch_cpu_idle", "arch_cpu_atomic_idle", "idle")
            if n in addr]

    resets = 0
    hanging = False
    while True:
        if a.hang and a.hang in addr and not hanging:
            m.hooks[addr[a.hang]] = hang_hook
        try:
            if hanging:
                # A hang: burn time until the watchdog fires or the limit is reached.
                while m.ms() < a.ms:
                    m.cycles += 10000
                    m.update_time()
            else:
                m.run_ms(a.ms, idle_ranges=idle)
            break
        except te.Stop as s:
            if str(s) == "hang":
                hanging = True
                continue
            break
        except (te.WatchdogReset, te.SoftwareReset) as e:
            kind = "watchdog" if isinstance(e, te.WatchdogReset) else "software"
            m.event(f"{kind} reset")
            if resets >= a.resets:
                break
            resets += 1
            hanging = False
            m.reset(cause_wd=isinstance(e, te.WatchdogReset))
            seen.clear()
        except te.EmuError as e:
            m.event(f"EMULATOR STOP: {e}")
            fatal.append(str(e))
            emu_stop = True
            break

    if a.console is not None and a.console_stream and len(console) > streamed[0]:
        # A last line the image did not end: out as it is, ended here.
        stream_console(len(console))
        print()
    r = m.regs_mem
    print("\n== summary")
    print(f"simulated {m.ms():.1f} ms, boot slot 0x{m.boot_slot:05x}, pc {m.symbolize(m.r[15])}")
    if m.icache_miss:
        print(f"flash cache model: {m.icache_misses} misses at {m.icache_miss} cycles each, "
              f"{m.icache_misses * m.icache_miss} of {m.cycles} cycles")
    print("milestones:", ", ".join(f"{k} @{v:.2f}ms" for k, v in seen.items()) or "none")
    if a.stop_at:
        print(f"stop-at {a.stop_at}: " + (f"reached at {stopped_at[0]:.2f} ms" if stopped_at else "not reached"))
    if a.stop_line:
        print("stop-line: " + (f"{stopped_line[0][1]!r} at {stopped_line[0][0]:.2f} ms" if stopped_line else "not reached"))
    print(f"USB DP pull-up (analog 0x0b bit 7): {m.analog[0x0B] >> 7}")
    wd = int.from_bytes(r[0x620:0x623], "little")
    print(f"watchdog: enabled={wd >> 23 & 1} timer2={wd >> 6 & 1} capture={wd >> 9 & 0x3fff}")
    print("flash writes:", [(op, hex(ad), n) for op, ad, n in fl.log][:12], "..." if len(fl.log) > 12 else "")
    print(f"slot flags: A={bytes(fl.mem[8:12])} B={bytes(fl.mem[0x20008:0x2000c])}")
    if a.stacks and found:
        import stack_use
        print("stacks:", stack_use.verdict(stack_use.used(m, found))[1])
    if a.console is not None:
        text = console.decode("utf-8", errors="replace")
        if a.console_out:
            with open(a.console_out, "w") as f:
                f.write(text)
            print(f"console: {len(console)} bytes to {a.console_out}")
        elif a.console_stream:
            print(f"console: {len(console)} bytes, streamed above")
        else:
            print(f"console: {len(console)} bytes")
            sys.stdout.write(text)
    if a.reg_audit:
        import reg_audit
        reg_audit.REVIEWED = a.reviewed
        rows = reg_audit.audit(m.reg_log, m.symbolize)
        print(f"registers: {len(rows)} bytes touched, {len(reg_audit.unreviewed(rows))} not reviewed")
        print(reg_audit.format_rows(rows))
    print("fatal:", fatal or "none")
    # With --fatal-continue the image's own handling decides; only an emulator stop fails the run.
    sys.exit(1 if (fatal and not a.fatal_continue) or emu_stop else 0)


if __name__ == "__main__":
    main()

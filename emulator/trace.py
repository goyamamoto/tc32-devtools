#!/usr/bin/env python3
"""Run an image in tc32emu and print one line per instruction: the lockstep
trace that the Go port (go/cmd/tc32emu-trace) must reproduce byte for byte
(checks/lockstep.sh).

The machine runs through Machine.run() (blocks of 32 instructions, then
update_time() and one interrupt if pending; a sleeping machine advances in
sleep_until() chunks), with a subclass that prints after each step. Lines:
  <pc:06x> <hw:04x> r0..r15 (8 hex each) <cpsr:08x>    after each instruction
  irq <cycles>                                          an interrupt was taken
  wake <analog 0x44:02x> <cycles>                       the machine woke up
  reset wd|sw <cycles>                                  a watchdog or software reset (a plain run goes on from reset)
  stop emuerror                                         the emulator stopped (the reason goes to stderr)
  usb ...                                               the USB scenario's events (--usb)
  end cycles=<n> sram=<sha256> regs=<sha256> analog=<sha256>

Modes:
  plain (default): run --cycles cycles.
  --usb: attach the USB model (usb_model.py) and run a fixed host scenario:
    wait for the DP pull-up, bus reset, GET_DESCRIPTOR device, SET_ADDRESS 1,
    GET_DESCRIPTOR configuration, SET_CONFIGURATION 1, then 200 ms polling
    the IN endpoints 1-4 every millisecond.

Usage: trace.py <image.bin> [--slot 0x0] [--cycles N] [--cpi 1] [--usb] [--check-every 32]
                [--sparse N] [--idle-elf zmk.elf] [--usb-suspend MS]

SPDX-License-Identifier: Apache-2.0
"""
import argparse
import hashlib
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import tc32emu as te  # noqa: E402
import usb_model as um  # noqa: E402


class TraceMachine(te.Machine):
    out = None
    sparse = 0      # > 0: no per-instruction lines; a state line every sparse instructions
    executed = 0

    def step(self):
        if self.asleep is not None:
            return
        pc = self.r[15]
        hw = self.read(pc, 2)
        super().step()
        r = self.r
        self.executed += 1
        if self.sparse:
            if self.executed % self.sparse == 0:
                self.out.write(f"at {self.executed} {self.cycles} {pc:06x} {hw:04x} "
                               + " ".join(f"{x:08x}" for x in r) + f" {self.cpsr():08x}\n")
            return
        self.out.write(
            f"{pc:06x} {hw:04x} {r[0]:08x} {r[1]:08x} {r[2]:08x} {r[3]:08x} {r[4]:08x} {r[5]:08x} {r[6]:08x} "
            f"{r[7]:08x} {r[8]:08x} {r[9]:08x} {r[10]:08x} {r[11]:08x} {r[12]:08x} {r[13]:08x} {r[14]:08x} "
            f"{r[15]:08x} {self.cpsr():08x}\n")

    def take_irq(self):
        super().take_irq()
        self.out.write(f"irq {self.cycles}\n")

    def sleep_until(self, max_cycles):
        super().sleep_until(max_cycles)
        if self.asleep is None:
            self.out.write(f"wake {self.analog[0x44]:02x} {self.cycles}\n")


def guarded(m, w, fn):
    """Run fn; on a reset print it and reset the machine (True: go on); on an
    emulator stop print it (False)."""
    try:
        fn()
        return True
    except te.SoftwareReset:
        w(f"reset sw {m.cycles}\n")
        m.reset()
        return True
    except te.WatchdogReset:
        w(f"reset wd {m.cycles}\n")
        m.reset(cause_wd=True)
        return True
    except te.EmuError as e:
        w("stop emuerror\n")
        print(f"trace: {e}", file=sys.stderr)
        return False


def usb_scenario(m, w, suspend_ms=0, idle=(), resume_ms=0, resume_k_ms=20):
    u = um.UsbModel(m, irq_lines=False)
    ok = u.run_until(u.dp_pullup, 100)
    w(f"usb attach {int(ok)} {m.cycles}\n")
    if not ok:
        return
    u.bus_reset()
    w(f"usb reset {m.cycles}\n")
    try:
        dev = u.get_descriptor(1, length=18)
        w(f"usb device {dev.hex()}\n")
        u.control(bytes([0, 5, 1, 0, 0, 0, 0, 0]))
        w(f"usb address {u.address} {m.cycles}\n")
        cfg = u.get_descriptor(2, length=9)
        total = cfg[2] | cfg[3] << 8
        cfg = u.get_descriptor(2, length=total)
        w(f"usb config {cfg.hex()}\n")
        u.control(bytes([0, 9, 1, 0, 0, 0, 0, 0]))
        w(f"usb configured {m.cycles}\n")
        for _ in range(200):
            m.run_ms(m.ms() + 1)
            for ep in (1, 2, 3, 4):
                pkt = u.in_ep(ep)
                if pkt is not None:
                    w(f"usb in{ep} {pkt.hex()} {m.cycles}\n")
        if suspend_ms:
            u.suspend()
            w(f"usb suspend {m.cycles}\n")
            m.run_ms(m.ms() + suspend_ms, idle_ranges=idle)
            w(f"usb suspend-end {m.cycles} wakes={len(m.wakes)} remote={len(u.remote_wakeups)}\n")
            if resume_ms:
                # The host resumes: K for resume_k_ms (20 ms at least, by the USB specification), then the bus runs for resume_ms.
                u.resume(resume_k_ms)
                w(f"usb resume-k-end {m.cycles} wakes={len(m.wakes)} asleep={int(m.asleep is not None)}\n")
                m.run_ms(m.ms() + resume_ms, idle_ranges=idle)
                w(f"usb resume-end {m.cycles} wakes={len(m.wakes)} asleep={int(m.asleep is not None)}\n")
    except um.UsbError as e:
        w("usb error\n")
        print(f"trace: usb: {e}", file=sys.stderr)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("image")
    ap.add_argument("--slot", type=lambda s: int(s, 0), default=None)
    ap.add_argument("--cycles", type=int, default=1_000_000)
    ap.add_argument("--cpi", type=int, default=1)
    ap.add_argument("--usb", action="store_true")
    ap.add_argument("--check-every", type=int, default=32, help="instructions per interrupt check (Machine.run)")
    ap.add_argument("--sparse", type=int, default=0, help="print a state line every N instructions instead of every one")
    ap.add_argument("--idle-elf", help="ELF whose idle functions Machine.run() skips ahead from (run_boot.idle_ranges)")
    ap.add_argument("--usb-suspend", type=float, default=0, help="--usb: after the polling, suspend the bus and run this many ms")
    ap.add_argument("--usb-resume", type=float, default=0, help="--usb-suspend: then the host resumes (K, see --usb-resume-k) and the bus runs this many ms")
    ap.add_argument("--usb-resume-k", type=float, default=20, help="--usb-resume: how long the host drives K, ms")
    a = ap.parse_args()
    idle = ()
    if a.idle_elf:
        import run_boot
        addr, size = run_boot.symbols(a.idle_elf)
        idle = run_boot.idle_ranges(addr, size)
    fl = te.Flash()
    data = open(a.image, "rb").read()
    fl.mem[:len(data)] = data
    m = TraceMachine(fl, boot_slot=a.slot, max_log=0, cpi=a.cpi)
    m.out = sys.stdout
    m.sparse = a.sparse
    w = sys.stdout.write
    if a.usb:
        guarded(m, w, lambda: usb_scenario(m, w, a.usb_suspend, idle, a.usb_resume, a.usb_resume_k))
    else:
        while m.cycles < a.cycles:
            if not guarded(m, w, lambda: m.run(a.cycles, idle_ranges=idle, check_every=a.check_every)):
                break
    w(f"end cycles={m.cycles} sram={hashlib.sha256(m.sram).hexdigest()} "
      f"regs={hashlib.sha256(m.regs_mem).hexdigest()} analog={hashlib.sha256(m.analog).hexdigest()}\n")


if __name__ == "__main__":
    main()

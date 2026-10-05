#!/usr/bin/env python3
"""Run sem/sem_*.bin in tc32emu and compare the results with the host build.

Usage: sem_check.py <llvm-nm> <host results file> <sem_*.elf>... [--engine python|go]

SPDX-License-Identifier: Apache-2.0
"""
import os
import subprocess
import sys

sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "emulator"))
import tc32emu  # noqa: E402

N_RESULTS = 64


GO_RUN = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "go", "bin", "tc32emu-run")


def run(nm, elf, engine="python"):
    syms = {}
    for line in subprocess.run([nm, elf], check=True, capture_output=True, text=True).stdout.splitlines():
        p = line.split()
        if len(p) == 3:
            syms[p[2]] = int(p[0], 16)
    if engine == "go":
        r = subprocess.run([GO_RUN, elf[:-4] + ".bin", "--done", hex(syms["done"]), "--results", hex(syms["results"]),
                            "--count", str(N_RESULTS), "--limit", "200000000"], capture_output=True, text=True)
        p = r.stdout.split()
        if r.returncode != 0 or not p or p[0] != "ok":
            raise SystemExit(f"{elf}: tc32emu-run: {r.stdout.strip()} {r.stderr.strip()}")
        return [int(x, 16) for x in p[1:1 + N_RESULTS]], int(p[1 + N_RESULTS])
    img = open(elf[:-4] + ".bin", "rb").read()
    fl = tc32emu.Flash()
    fl.mem[:len(img)] = img
    m = tc32emu.Machine(fl, boot_slot=0, symbols=syms, max_log=0)
    done = syms["done"] - tc32emu.SRAM_BASE
    m.sram[done:done + 4] = bytes(4)   # the boot copy could leave the flag's value there
    while int.from_bytes(m.sram[done:done + 4], "little") != 0x600DC0DE:
        for _ in range(10000):
            m.step()
        if m.cycles > 200_000_000:
            raise SystemExit(f"{elf}: no result after {m.cycles} instructions")
    base = syms["results"] - tc32emu.SRAM_BASE
    return [int.from_bytes(m.sram[base + 4 * i:base + 4 * i + 4], "little") for i in range(N_RESULTS)], m.cycles


def main():
    argv = sys.argv[1:]
    engine = "python"
    if "--engine" in argv:
        i = argv.index("--engine")
        engine = argv[i + 1]
        argv = argv[:i] + argv[i + 2:]
    nm, host_file, elves = argv[0], argv[1], argv[2:]
    want = [int(x, 16) for x in open(host_file).read().split()]
    bad = 0
    for elf in elves:
        got, n = run(nm, elf, engine)
        diff = [i for i in range(N_RESULTS) if got[i] != want[i]]
        print(f"{elf}: {n} instructions, {N_RESULTS - len(diff)}/{N_RESULTS} results match"
              + (f"; differ at {diff}" if diff else ""))
        bad += len(diff)
    sys.exit(1 if bad else 0)


if __name__ == "__main__":
    main()

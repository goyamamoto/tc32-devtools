#!/usr/bin/env python3
"""Zephyr's kernel test suites (tests/kernel, ztest) built for the TC32 with
the mainstream-clang path and run in the emulator.

Each variant of checks/zephyr_kernel_tests.txt (name, directory under
tests/kernel, Kconfig settings) is built for the tlsr8278_generic board with
clang for ARMv4T Thumb (-DTC32_THUMB=ON, the port's assembly through
tc32asm2thumb.py), re-encoded by thumb2tc32.py and checked by image_check.py,
with the log backend that writes to the emulator console (register 0xfff0)
and no UART console. run-boot --console runs it until ztest's last line,
"PROJECT EXECUTION SUCCESSFUL" or "... FAILED" (--stop-line), letting the
image handle the faults some cases raise on purpose (--fatal-continue). The
outcome is that line plus ztest's own PASS/FAIL/SKIP counts; a run that hits
the time cap or an emulator stop is a failure.

Usage: zephyr_kernel_tests_tc32.py --zephyr <workspace with zephyr/, toolchains/thumb-llvm, .venv-zephyr>
           [--list checks/zephyr_kernel_tests.txt] [--builds <dir>] [--build] [--jobs N]
           [--engine go|python] [--ms 120000] [--filter name] [--out <dir for the logs>]
--build runs the builds (cmake, one at a time) before the runs; without it
the images in --builds/<name>/zephyr/zephyr.{elf,bin} are used as they are.
Exit status 1 if a suite failed, did not finish, or has no build.

SPDX-License-Identifier: Apache-2.0
"""
import argparse
import concurrent.futures
import os
import re
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
CONSOLE_CONF = """CONFIG_LOG=y
CONFIG_LOG_MODE_IMMEDIATE=y
CONFIG_LOG_BACKEND_TC32EMU=y
CONFIG_LOG_BACKEND_UART=n
CONFIG_UART_CONSOLE=n
CONFIG_LOG_IMMEDIATE_CLEAN_OUTPUT=y
"""


def variants(path, flt):
    out = []
    for ln in open(path):
        ln = ln.strip()
        if not ln or ln.startswith("#"):
            continue
        name, src, *cfg = ln.split()
        if not flt or flt in name:
            out.append((name, src, cfg))
    return out


def build(name, src, cfg, a):
    w = os.path.abspath(a.zephyr)
    b = os.path.join(a.builds, name)
    os.makedirs(b, exist_ok=True)
    conf = os.path.join(b, "tc32emu_console.conf")
    with open(conf, "w") as f:
        f.write(CONSOLE_CONF + "".join(c + "\n" for c in cfg))
    env = dict(os.environ, PATH=os.path.join(w, ".venv-zephyr", "bin") + os.pathsep + os.environ["PATH"],
               ZEPHYR_BASE=os.path.join(w, "zephyr"))
    llvm = os.path.join(w, "toolchains", "thumb-llvm")
    cmds = [
        ["cmake", "-S", os.path.join(w, "zephyr", "tests", "kernel", src), "-B", b, "-GNinja",
         "-DBOARD=tlsr8278_generic", "-DZEPHYR_TOOLCHAIN_VARIANT=host/llvm", f"-DLLVM_TOOLCHAIN_PATH={llvm}",
         "-DTC32_THUMB=ON", f"-DTC32_THUMB_TOOLS={os.path.join(ROOT, 'compiler')}",
         f"-DCMAKE_READELF={os.path.join(llvm, 'bin', 'llvm-readelf')}",
         f"-DPython3_EXECUTABLE={os.path.join(w, '.venv-zephyr', 'bin', 'python')}",
         f"-DUSER_CACHE_DIR={os.path.join(w, 'build', 'cache')}", f"-DEXTRA_CONF_FILE={conf}"],
        ["cmake", "--build", b],
        [sys.executable, os.path.join(ROOT, "compiler", "thumb2tc32.py"),
         os.path.join(b, "zephyr", "zephyr.elf"), os.path.join(b, "zephyr", "zephyr.bin")],
        [sys.executable, os.path.join(ROOT, "compiler", "image_check.py"),
         os.path.join(b, "zephyr", "zephyr.elf"), os.path.join(b, "zephyr", "zephyr.bin")],
    ]
    with open(os.path.join(b, "build.log"), "w") as log:
        for c in cmds:
            r = subprocess.run(c, env=env, stdout=log, stderr=subprocess.STDOUT)
            if r.returncode:
                return False
    return True


def run_one(name, a):
    bdir = os.path.join(a.builds, name, "zephyr")
    elf, img = os.path.join(bdir, "zephyr.elf"), os.path.join(bdir, "zephyr.bin")
    odir = os.path.join(a.out, name)
    os.makedirs(odir, exist_ok=True)
    if not (os.path.exists(elf) and os.path.exists(img)):
        return "NOBUILD", name, "no zephyr.elf/zephyr.bin"
    console = os.path.join(odir, "console.txt")
    tool = ([os.path.join(ROOT, "go", "bin", "run-boot")] if a.engine == "go"
            else [sys.executable, "-B", os.path.join(ROOT, "emulator", "run_boot.py")])
    cmd = tool + ["--elf", elf, "--bin", img, "--ms", str(a.ms), "--console", "--console-out", console,
                  "--fatal-continue", "--stacks", "--stop-line", "PROJECT EXECUTION SUCCESSFUL",
                  "--stop-line", "PROJECT EXECUTION FAILED"]
    r = subprocess.run(cmd, capture_output=True, text=True)
    with open(os.path.join(odir, "run.log"), "w") as f:
        f.write(r.stdout + r.stderr)
    text = open(console, errors="replace").read() if os.path.exists(console) else ""
    counts = {k: len(re.findall(rf"^ {k} - ", text, re.M)) for k in ("PASS", "FAIL", "SKIP")}
    tally = f"pass {counts['PASS']} fail {counts['FAIL']} skip {counts['SKIP']}"
    m = re.search(r"^stop-line: '(PROJECT EXECUTION \w+)' at ([\d.]+) ms", r.stdout, re.M)
    if r.returncode != 0:
        return "FAILED", name, f"run-boot exit {r.returncode} ({tally}; see run.log)"
    if not m:
        return "FAILED", name, f"did not finish within {a.ms:g} ms ({tally})"
    if m.group(1) != "PROJECT EXECUTION SUCCESSFUL" or counts["FAIL"]:
        return "FAILED", name, f"{m.group(1)} ({tally})"
    return "PASS", name, f"{tally}, {float(m.group(2)):.0f} ms simulated"


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--zephyr", required=True)
    ap.add_argument("--list", default=os.path.join(ROOT, "checks", "zephyr_kernel_tests.txt"))
    ap.add_argument("--builds", default=os.path.join(ROOT, "build", "zephyr-kernel-tests"))
    ap.add_argument("--build", action="store_true")
    ap.add_argument("--jobs", type=int, default=max(1, (os.cpu_count() or 2) - 1))
    ap.add_argument("--engine", choices=("go", "python"), default="go")
    ap.add_argument("--ms", type=float, default=120000)
    ap.add_argument("--filter", default="")
    ap.add_argument("--out", default=None)
    a = ap.parse_args()
    a.out = a.out or os.path.join(ROOT, "build", "zephyr-kernel-tests-" + a.engine)
    vs = variants(a.list, a.filter)
    if not vs:
        sys.exit("no variants")
    if a.build:
        for name, src, cfg in vs:
            ok = build(name, src, cfg, a)
            print(("BUILT " if ok else "NOBUILD ") + name + ("" if ok else f"  (see {a.builds}/{name}/build.log)"),
                  flush=True)
    results = []
    with concurrent.futures.ThreadPoolExecutor(a.jobs) as ex:
        for res in ex.map(lambda v: run_one(v[0], a), vs):
            results.append(res)
            print(f"{res[0]}: {res[1]}  {res[2]}", flush=True)
    n = {k: sum(1 for r in results if r[0] == k) for k in ("PASS", "FAILED", "NOBUILD")}
    print(f"\nPASS {n['PASS']}  FAIL {n['FAILED']}  NOBUILD {n['NOBUILD']}  of {len(results)} suites "
          f"({a.engine} engine, cap {a.ms:g} ms each)")
    os.makedirs(a.out, exist_ok=True)
    with open(os.path.join(a.out, "pass-fail.log"), "w") as f:
        for kind, name, info in results:
            f.write(f"{kind}: {name}  {info}\n")
    return 1 if n["FAILED"] or n["NOBUILD"] else 0


if __name__ == "__main__":
    sys.exit(main())

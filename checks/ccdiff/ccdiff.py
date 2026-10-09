#!/usr/bin/env python3
"""Differential test of a TC32 compiler: random C programs from Csmith,
built for the host and for TC32 at several optimization levels, run on the
host and in tc32emu; every TC32 build must print the host's checksum.

Csmith programs are free of undefined behaviour and print a checksum of their
global state. Their long literals are made int (ilp32_literals) so that the
LP64 host computes what ILP32 TC32 does. On TC32 they are built freestanding
(shim.c, include/), linked with the semantics test's start code and linker
script (../sem) and this repository's runtime helpers (../../compiler/runtime);
shim.c's printf() keeps the printed checksum, which is read once main() has
returned (exactly one print is expected).

Two compilers (--compiler):
- thumb (default): a mainstream clang and ld.lld building Thumb-1 (ARMv4T),
  re-encoded as TC32 by ../../compiler/thumb2tc32.py (TC32_LLVM, TC32_LLD or
  --cc); its "zmk" level uses the flags of the ZMK firmware build this was
  made for (ZMK_FLAGS);
- direct: llvm-tc32 (--cc) on the direct path, the thumb compiler's flags
  with -mcpu=tc32 (and -noarm on the C compiles, as the thumb build
  returns), linked by its ld.lld and written by ../../compiler/elf2bin.py;
  each program is also built the thumb way with the same clang, and the two
  images must be byte-identical (outcome IMAGE otherwise) before the direct
  image runs;
- telink: Telink's own toolchain (tc32-elf-gcc 4.5.1, binutils 2.20, its
  libgcc; x86-64 Linux binaries, TELINK_TC32 or --telink <dir>, run in
  Docker), at its own levels (TELINK_LEVELS). Its code has run on shipped
  devices, so it checks tc32emu: a Telink build that runs wrong here points
  at the emulator.
The thumb and direct builds use the code generation flags a firmware must use
(FIRMWARE_FLAGS). --hw-divider builds the helpers for the TLSR8278 divider
(tc32emu models it). --keep-elf <dir> keeps each build's ELF, or for telink
the program's object (forms_check.py evidence --elf).

Outcomes per program and level: ok, DIFF (wrong checksum), COMPILE (the
compiler failed or crashed), LINK, EMU (the emulator stopped: an undecodable
or unmodelled instruction, a bad access), SLOW (over the instruction limit;
not a failure). A program that does not finish on the host within the time
limit is skipped.

Usage: ccdiff.py [--seeds 1-200] [--jobs 8] [--compiler thumb|direct|telink] [--cc <bin dir>]
                 [--telink <Telink toolchain dir>] [--limit N] [--out <dir for failing programs>]
                 [--levels O0,O2,Os,Oz(,zmk for thumb)] [--keep-elf <dir>] [--hw-divider]
                 [--engine python|go]   (go: the Go emulator, go/bin/tc32emu-run; same outcomes expected)

SPDX-License-Identifier: Apache-2.0
"""
import argparse
import multiprocessing
import os
import re
import shutil
import subprocess
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))
sys.path[:0] = [os.path.join(ROOT, "common"), os.path.join(ROOT, "emulator")]
import tc32emu  # noqa: E402
import toolchain  # noqa: E402

SEM = os.path.join(ROOT, "checks", "sem")
COMPILER = os.path.join(ROOT, "compiler")
RUNTIME = os.path.join(COMPILER, "runtime")
THUMB_FLAGS = ["--target=thumbv4t-none-eabi", "-mcpu=arm7tdmi", "-mthumb", "-mfloat-abi=soft", "-ffreestanding",
               "-fno-builtin"]
# The flags that shape code in the ZMK firmware build this was made for
# (compile_commands.json of that build), for the thumb compiler's "zmk"
# level. Most files build without -fno-builtin, so it is left out here too.
ZMK_FLAGS = ["--target=thumbv4t-none-eabi", "-mcpu=arm7tdmi", "-mthumb", "-mfloat-abi=soft", "-ffreestanding",
             "-Oz", "-std=c17", "-fno-strict-aliasing", "-fno-common", "-ffunction-sections", "-fdata-sections",
             "-fno-pic", "-fno-pie", "-fno-asynchronous-unwind-tables", "--rtlib=libgcc"]
LEVELS = {
    "O0": ["-O0"],
    "O2": ["-O2"],
    "Os": ["-Os"],
    # The firmware's flags.
    "Oz": ["-Oz", "-fno-strict-aliasing", "-fno-common", "-ffunction-sections",
           "-fdata-sections"],
}
# Telink's SDK projects (project/tlsr_tc32/B87/.cproject) build with -O2,
# -fshort-enums, -fpack-struct, -finline-small-functions, -std=gnu99,
# -fshort-wchar and -fms-extensions: that is the configuration shipped code
# comes from. "sdk" is it without -fpack-struct (which packs every struct:
# Csmith then takes packed members' addresses into plain pointers, see below).
# Telink's gcc refuses -O3, and at -Os it miscompiles switch tables (a table
# of byte offsets dispatched as one of word addresses), so -Os is not a
# default level.
TELINK_LEVELS = {
    "O0": ["-O0"],
    "O2": ["-O2"],
    "sdk": ["-O2", "-std=gnu99", "-fshort-enums", "-finline-small-functions", "-fshort-wchar", "-fms-extensions",
            "-ffunction-sections", "-fdata-sections"],
    "Os": ["-Os"],
}
TELINK_IMAGE = os.environ.get("TELINK_IMAGE", "debian:bookworm-slim")
LIBGCC = os.path.join("lib", "gcc", "tc32-elf", "4.5.1.tc32-elf-1.5", "libgcc.a")
# No packed structs: Csmith then takes addresses of packed members into plain
# pointers (e.g. uint32_t *p = &s.f1), undefined behaviour that x86 hosts
# forgive and a strict-alignment CPU does not (it showed as unaligned 32-bit
# accesses). Accesses to packed members themselves are split into byte
# accesses, which the targeted tests check.
CSMITH_OPTS = ["--max-funcs", "4", "--max-block-depth", "3", "--max-expr-complexity", "6", "--no-packed-struct"]
DONE = 0x600DC0DE
# As a firmware must build (README): no jump tables, no ldm/stm merging after
# register allocation (tc32emu stops on the PC reads jump tables need;
# forms_check.py on ldm with the base register in the list; nothing shows how
# TC32 runs either).
FIRMWARE_FLAGS = ["-fno-jump-tables", "-mllvm", "-arm-load-store-opt=false"]


def csmith_include():
    inc = os.environ.get("CSMITH_INCLUDE")
    if inc:
        return inc
    prefix = subprocess.run(["brew", "--prefix", "csmith"], capture_output=True, text=True).stdout.strip()
    found = [d for d in os.listdir(os.path.join(prefix, "include")) if d.startswith("csmith")]
    return os.path.join(prefix, "include", found[0])


def ilp32_literals(text):
    """Drop a single L suffix from integer literals (not LL): Csmith writes
    them as long, which is 64-bit on the host (LP64) and 32-bit on TC32
    (ILP32), so the host would compute some expressions in 64 bits. Without
    the suffix both compute in int, as long (32-bit) would on TC32."""
    return re.sub(r"\b(0[xX][0-9A-Fa-f]+|\d+)([uU]?)[lL]\b", r"\1\2", text)


def run(cmd, timeout=120, **kw):
    return subprocess.run(cmd, capture_output=True, text=True, timeout=timeout, **kw)


def host_checksum(src, work, inc):
    exe = os.path.join(work, "host")
    r = run(["cc", "-O0", "-w", "-I" + inc, src, "-o", exe])
    if r.returncode != 0:
        return None, "host compile failed"
    try:
        r = run([exe], timeout=5)
    except subprocess.TimeoutExpired:
        return None, "host timeout"
    m = re.search(r"checksum = ([0-9A-F]+)", r.stdout)
    return (int(m.group(1), 16), None) if m else (None, "no host checksum")


def divider_flags(hw_divider):
    return ["-DTC32_TLSR8278_DIVIDER=1"] if hw_divider else []


DIRECT_C = ["-Xclang", "-target-feature", "-Xclang", "-noarm"]


def direct_flags(flags):
    """The thumb compiler's flags for the direct path: -mcpu=tc32."""
    return ["-mcpu=tc32" if f == "-mcpu=arm7tdmi" else f for f in flags]


def thumb_runtime(cc, work, hw_divider, direct=False):
    """Start code, shim, memory functions and this repository's helpers for
    the thumb compiler (direct: on the direct path), built once per run into
    work; returns the objects (the start object first)."""
    objs = []
    base = direct_flags(THUMB_FLAGS) if direct else THUMB_FLAGS
    for src, flags in [(os.path.join(SEM, "start_thumb.S"), []),
                       (os.path.join(HERE, "shim.c"), ["-O2"]),
                       (os.path.join(SEM, "mem.c"), ["-O1"]),
                       (os.path.join(RUNTIME, "compiler_builtins.c"), ["-Oz"] + FIRMWARE_FLAGS + divider_flags(hw_divider)),
                       (os.path.join(RUNTIME, "aeabi_thumb.S"), [])]:
        obj = os.path.join(work, os.path.basename(src) + (".direct.o" if direct else ".o"))
        extra = DIRECT_C if direct and src.endswith(".c") else []
        r = run([os.path.join(cc, "clang")] + base + extra + flags + ["-c", src, "-o", obj])
        if r.returncode != 0:
            raise SystemExit(f"thumb runtime: {src}: {r.stderr[-300:]}")
        objs.append(obj)
    return objs


def in_docker(telink, script, mounts, timeout=600):
    """Run a shell script in the x86-64 Linux container with Telink's tools."""
    cmd = ["docker", "run", "--rm", "--platform", "linux/amd64", "--network", "none"]
    for m in sorted({telink, *mounts}):
        cmd += ["-v", f"{m}:{m}"]
    return run(cmd + [TELINK_IMAGE, "sh", "-c", script], timeout=timeout)


def telink_gcc(telink):
    return f"{telink}/bin/tc32-elf-gcc -B{telink}/bin/tc32-elf- -ffreestanding"


def telink_runtime(telink, work, inc):
    """Start code, shim and memory functions built by Telink's gcc into work,
    and a copy of Csmith's headers there (the container sees only mounted
    folders); returns (objects, include folder)."""
    shutil.copytree(inc, os.path.join(work, "csmith"))
    g = telink_gcc(telink)
    r = in_docker(telink, f"set -e; cd {work}; {g} -c {SEM}/start.S -o start.o; "
                          f"{g} -Os -c {HERE}/shim.c -o shim.o; {g} -O1 -fno-builtin -c {SEM}/mem.c -o mem.o",
                  [work, ROOT])
    if r.returncode != 0:
        raise SystemExit(f"telink runtime: {r.stderr[-400:]}")
    return [os.path.join(work, x) for x in ("start.o", "shim.o", "mem.o")], os.path.join(work, "csmith")


def telink_build(src, work, levels, telink, runtime):
    """Build every level in one container: {level}.bin and {level}.syms, or
    {level}.failed with the error in {level}.err. The program goes through
    assembly, where mark_data.awk marks the data in its code for
    forms_check.py; {level}.o is the program alone."""
    objs, inc = runtime
    g, t = telink_gcc(telink), f"{telink}/bin/tc32-elf-"
    steps = [f"cd {work}"]
    for lv in levels:
        flags = " ".join(TELINK_LEVELS[lv])
        steps.append(f"( {g} -w {flags} -I{HERE}/include -I{inc} -S {src} -o {lv}.s 2> {lv}.err"
                     f" && awk -f {HERE}/mark_data.awk {lv}.s > {lv}.m.s && {g} -c {lv}.m.s -o {lv}.o 2>> {lv}.err"
                     f" && {t}ld -T {SEM}/sem.ld {objs[0]} {lv}.o {' '.join(objs[1:])} {telink}/{LIBGCC}"
                     f" -o {lv}.elf 2>> {lv}.err && {t}objcopy -O binary {lv}.elf {lv}.bin"
                     f" && {t}nm {lv}.elf > {lv}.syms ) || touch {lv}.failed")
    return in_docker(telink, "; ".join(steps), [work, ROOT, os.path.dirname(objs[0])], timeout=900)


def telink_result(work, level, limit, engine="python"):
    base = os.path.join(work, level)
    if os.path.exists(base + ".failed") or not os.path.exists(base + ".bin"):
        err = open(base + ".err").read().strip().splitlines() if os.path.exists(base + ".err") else []
        return ("COMPILE" if not os.path.exists(base + ".o") else "LINK"), (err or ["?"])[-1][:200]
    syms = {}
    for line in open(base + ".syms"):
        p = line.split()
        if len(p) == 3:
            syms[p[2]] = int(p[0], 16)
    return emulate(base + ".bin", syms, limit, engine)


def direct_checksum(src, work, level, cc, inc, limit, runtime, engine="python"):
    """The direct path: the program built with -mcpu=tc32 and written by
    elf2bin.py, and the thumb build of it with the same clang; the images
    must be equal, then the direct one runs."""
    rt_direct, rt_thumb = runtime
    got = tc32_checksum(src, work, level, cc, inc, limit, "thumb", rt_thumb, engine, run_image=False)
    if got is not None:
        return got
    thumb_img = os.path.join(work, f"{level}.bin")
    os.rename(thumb_img, thumb_img + ".thumb")
    obj, elf, img = (os.path.join(work, f"{level}.direct.{x}") for x in ("o", "elf", "bin"))
    flags = ["-w"] + FIRMWARE_FLAGS + ["-I" + os.path.join(HERE, "include"), "-I" + inc, "-c", src, "-o", obj]
    if level == "zmk":
        cmd = [os.path.join(cc, "clang")] + direct_flags(ZMK_FLAGS) + DIRECT_C + flags
    else:
        cmd = [os.path.join(cc, "clang")] + direct_flags(THUMB_FLAGS) + DIRECT_C + flags \
            + [f for f in LEVELS[level] if f != "-mcpu=tc32"]
    try:
        r = run(cmd)
    except subprocess.TimeoutExpired:
        return "COMPILE", "compiler timeout"
    if r.returncode != 0:
        return "COMPILE", (r.stderr.strip().splitlines() or ["?"])[-1][:200]
    r = run([os.path.join(cc, "ld.lld"), "-T", os.path.join(SEM, "thumb.ld"), rt_direct[0], obj] + rt_direct[1:]
            + ["-o", elf])
    if r.returncode != 0:
        return "LINK", (r.stderr.strip().splitlines() or ["?"])[-1][:200]
    r = run([sys.executable, "-B", os.path.join(COMPILER, "elf2bin.py"), elf, img])
    if r.returncode != 0:
        return "LINK", "elf2bin: " + (r.stderr.strip().splitlines() or ["?"])[-1][:200]
    a, b = open(thumb_img + ".thumb", "rb").read(), open(img, "rb").read()
    if a != b:
        at = next((i for i in range(min(len(a), len(b))) if a[i] != b[i]), min(len(a), len(b)))
        return "IMAGE", f"the direct image differs from the thumb path's at 0x{at:x} ({len(a)} / {len(b)} B)"
    syms = {}
    for line in run([os.path.join(cc, "llvm-nm"), elf]).stdout.splitlines():
        p = line.split()
        if len(p) == 3:
            syms[p[2]] = int(p[0], 16)
    return emulate(img, syms, limit, engine)


def tc32_checksum(src, work, level, cc, inc, limit, compiler, runtime, engine="python", run_image=True):
    obj, elf, img = (os.path.join(work, f"{level}.{x}") for x in ("o", "elf", "bin"))
    flags = ["-w"] + FIRMWARE_FLAGS + ["-I" + os.path.join(HERE, "include"), "-I" + inc, "-c", src, "-o", obj]
    if compiler == "thumb" and level == "zmk":
        cmd = [os.path.join(cc, "clang")] + ZMK_FLAGS + flags
    else:
        cmd = [os.path.join(cc, "clang")] + THUMB_FLAGS + flags + LEVELS[level]
    try:
        r = run(cmd)
    except subprocess.TimeoutExpired:
        return "COMPILE", "compiler timeout"
    if r.returncode != 0:
        return "COMPILE", (r.stderr.strip().splitlines() or ["?"])[-1][:200]
    r = run([toolchain.tool("ld.lld") if cc == toolchain.llvm_bin() else os.path.join(cc, "ld.lld"),
             "-T", os.path.join(SEM, "thumb.ld"), runtime[0], obj] + runtime[1:] + ["-o", elf])
    if r.returncode != 0:
        return "LINK", (r.stderr.strip().splitlines() or ["?"])[-1][:200]
    r = run([sys.executable, "-B", os.path.join(COMPILER, "thumb2tc32.py"), elf, img])
    if r.returncode != 0:
        return "LINK", "thumb2tc32: " + (r.stderr.strip().splitlines() or ["?"])[-1][:200]
    if not run_image:
        return None
    syms = {}
    for line in run([os.path.join(cc, "llvm-nm"), elf]).stdout.splitlines():
        p = line.split()
        if len(p) == 3:
            syms[p[2]] = int(p[0], 16)
    return emulate(img, syms, limit, engine)


GO_RUN = os.path.join(ROOT, "go", "bin", "tc32emu-run")   # --engine go: the Go emulator, same outcomes expected


def emulate_go(img, syms, limit):
    """emulate() through the Go emulator."""
    r = run([GO_RUN, img, "--done", hex(syms["done"]), "--checksum", hex(syms["checksum"]),
             "--printed", hex(syms["printed"]), "--limit", str(limit)])
    out = r.stdout.strip().split()
    if r.returncode != 0 or not out:
        return "EMU", "tc32emu-run: " + (r.stderr.strip() or "no output")[:200]
    if out[0] == "ok":
        return int(out[1], 16), int(out[2])
    return out[0], " ".join(out[1:])[:240]


def emulate(img, syms, limit, engine="python"):
    """Run a linked image in tc32emu to the end of main(): (checksum, cycles)
    or (outcome, detail)."""
    if "checksum" not in syms or "done" not in syms or "printed" not in syms:
        return "LINK", "no checksum, done or printed symbol"
    if engine == "go":
        return emulate_go(img, syms, limit)
    fl = tc32emu.Flash()
    data = open(img, "rb").read()
    fl.mem[:len(data)] = data
    m = tc32emu.Machine(fl, boot_slot=0, symbols=syms, max_log=0)
    done = syms["done"] - tc32emu.SRAM_BASE
    # The boot copy puts flash bytes in SRAM; the flag must start clear, or a
    # copy of the constant could end the run before it starts.
    m.sram[done:done + 4] = bytes(4)
    try:
        while int.from_bytes(m.sram[done:done + 4], "little") != DONE:
            for _ in range(20000):
                m.step()
            if m.cycles > limit:
                return "SLOW", f"over {limit} instructions"
    except Exception as e:  # noqa: BLE001 - any stop of the emulator is a finding
        return "EMU", f"{type(e).__name__}: {e} at pc 0x{m.r[15]:x} ({m.symbolize(m.r[15])})"[:240]
    word = lambda name: int.from_bytes(m.sram[syms[name] - tc32emu.SRAM_BASE:][:4], "little")  # noqa: E731
    if word("printed") != 1:
        return "DIFF", f"checksum printed {word('printed')} times"
    return word("checksum"), m.cycles


def one(args):
    seed, levels, cc, inc, limit, out, compiler, runtime, telink, keep, engine = args
    work = os.path.realpath(tempfile.mkdtemp(prefix=f"ccdiff{seed}_"))
    try:
        src = os.path.join(work, f"p{seed}.c")
        with open(src, "w") as f:
            r = run(["csmith", "--seed", str(seed)] + CSMITH_OPTS, cwd=work)   # it writes platform.info
            f.write(ilp32_literals(r.stdout))
        want, why = host_checksum(src, work, inc)
        if want is None:
            return seed, None, {"skip": why}
        res = {}
        if compiler == "telink":
            telink_build(src, work, levels, telink, runtime)
        for level in levels:
            if compiler == "telink":
                got, info = telink_result(work, level, limit, engine)
            elif compiler == "direct":
                got, info = direct_checksum(src, work, level, cc, inc, limit, runtime, engine)
            else:
                got, info = tc32_checksum(src, work, level, cc, inc, limit, compiler, runtime, engine)
            kept = {"telink": f"{level}.o", "direct": f"{level}.direct.elf"}.get(compiler, f"{level}.elf")  # Telink: the program alone
            if keep and os.path.exists(os.path.join(work, kept)):
                os.makedirs(keep, exist_ok=True)
                shutil.copy(os.path.join(work, kept), os.path.join(keep, f"p{seed}_{kept}"))
            if isinstance(got, int):
                res[level] = ("ok", info) if got == want else ("DIFF", f"0x{got:08X} vs host 0x{want:08X}")
            else:
                res[level] = (got, info)
        if out and any(v[0] not in ("ok", "SLOW") for v in res.values()):
            os.makedirs(out, exist_ok=True)
            shutil.copy(src, os.path.join(out, f"p{seed}.c"))
        return seed, want, res
    finally:
        shutil.rmtree(work, ignore_errors=True)


def parse_seeds(text):
    a, _, b = text.partition("-")
    return range(int(a), int(b or a) + 1)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--seeds", default="1-50")
    ap.add_argument("--jobs", type=int, default=max(1, os.cpu_count() - 2))
    ap.add_argument("--cc", help="toolchain bin folder for thumb (default: TC32_LLVM, see toolchain.py)")
    ap.add_argument("--limit", type=int, default=100_000_000)
    ap.add_argument("--levels")
    ap.add_argument("--out")
    ap.add_argument("--compiler", choices=("thumb", "direct", "telink"), default="thumb")
    ap.add_argument("--telink", default=os.environ.get("TELINK_TC32", ""))
    ap.add_argument("--keep-elf")
    ap.add_argument("--hw-divider", action="store_true", help="helpers for the TLSR8278 hardware divider")
    ap.add_argument("--engine", choices=("python", "go"), default="python",
                    help="the emulator: tc32emu.py, or the Go port (go/bin/tc32emu-run)")
    args = ap.parse_args()
    if not args.cc:
        args.cc = {"thumb": toolchain.llvm_bin(), "direct": os.environ.get("TC32_LLVM_DIRECT", "")}.get(
            args.compiler, "")
    if args.compiler != "telink" and not args.cc:
        sys.exit("no toolchain: set TC32_LLVM (thumb) or TC32_LLVM_DIRECT (direct), or pass --cc")
    if args.compiler == "telink" and not args.telink:
        sys.exit("no Telink toolchain: set TELINK_TC32 or pass --telink")
    levels = (args.levels or {"telink": "O0,O2,sdk", "thumb": "O0,O2,Os,Oz,zmk", "direct": "O0,O2,Os,Oz,zmk"}[
        args.compiler]).split(",")
    inc = csmith_include()
    runtime = []
    telink = os.path.realpath(args.telink) if args.telink else ""
    keep = os.path.realpath(args.keep_elf) if args.keep_elf else None
    rt_dir = tempfile.mkdtemp(prefix="ccdiff_rt_")
    if args.compiler == "thumb":
        runtime = thumb_runtime(args.cc, rt_dir, args.hw_divider)
    elif args.compiler == "direct":
        runtime = (thumb_runtime(args.cc, rt_dir, args.hw_divider, direct=True),
                   thumb_runtime(args.cc, rt_dir, args.hw_divider))
    else:
        runtime = telink_runtime(telink, os.path.realpath(rt_dir), inc)
    jobs = [(s, levels, args.cc, inc, args.limit, args.out, args.compiler, runtime, telink, keep, args.engine)
            for s in parse_seeds(args.seeds)]
    counts, bad = {}, []
    with multiprocessing.Pool(args.jobs) as pool:
        for seed, want, res in pool.imap_unordered(one, jobs):
            if want is None:
                counts["skipped"] = counts.get("skipped", 0) + 1
                continue
            line = " ".join(f"{lv}:{res[lv][0]}" for lv in levels)
            print(f"seed {seed}: {line}", flush=True)
            for lv in levels:
                counts[res[lv][0]] = counts.get(res[lv][0], 0) + 1
                if res[lv][0] not in ("ok", "SLOW"):
                    bad.append((seed, lv, res[lv][0], res[lv][1]))
    print("\nsummary (program x level):", ", ".join(f"{k} {v}" for k, v in sorted(counts.items())))
    for seed, lv, kind, info in sorted(bad):
        print(f"  seed {seed} {lv}: {kind} {info}")
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())

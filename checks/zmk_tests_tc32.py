#!/usr/bin/env python3
"""ZMK's own snapshot tests (app/tests) run on the TC32 image in the emulator.

Upstream runs each test on native_sim (app/run-test.sh): a mock key scan
presses keys, the log lines matching events.patterns (sed -n) must equal
keycode_events.snapshot, and a differing test marked "pending" counts as
pending. Here each test is a TC32 build (zmk.elf, zmk.bin in a directory per
test) whose log backend writes its characters to register 0xfff0; run-boot
--console collects them, the run ends at _exit (the mock's exit-after), and
the same patterns and comparison follow. The patterns are applied by sed_n()
below, a Python reading of the GNU "sed -n" scripts the tests use (s/RE/REPL/p
with BRE groups and \\| alternation), because macOS's sed has no \\| and
matches nothing there; --sed CMD applies them with that (GNU) sed instead.
The emulator is the Go one (go/bin/run-boot) unless --engine python.

Some snapshots hold absolute k_uptime values or depend on the mock's key
events and a timer landing in one order, which a real tick clock changes (the
mock reschedules from its handler, one tick later each time). --expected FILE
lists such tests, one "<test> <reason>" per line; a listed test whose output
differs is reported EXPECTED with the reason and does not fail the run, and a
listed test that passes is reported as PASS (the list is then stale).

Usage: zmk_tests_tc32.py --builds <dir with <test>/zephyr/zmk.{elf,bin}> --tests <zmk/app/tests>
                         [--ms 60000] [--jobs N] [--engine go|python] [--filter path-under-tests]
                         [--out <dir for the logs>] [--sed gsed] [--expected FILE]
Exit status 1 if a test failed or its build is missing (as upstream).

SPDX-License-Identifier: Apache-2.0
"""
import argparse
import concurrent.futures
import os
import re
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def cases(tests, flt):
    out = []
    for dirpath, _dirs, files in os.walk(os.path.join(tests, flt)):
        if "native_sim.keymap" in files:  # as upstream's run-test.sh (find -name native_sim.keymap)
            out.append(os.path.relpath(dirpath, tests))
    return sorted(out)


def bre_to_python(bre):
    """A POSIX basic regular expression as GNU sed reads it, as a Python one:
    \\( \\) \\| \\{ \\} are the operators; the bare characters are literal."""
    out, i = [], 0
    while i < len(bre):
        c = bre[i]
        if c == "\\" and i + 1 < len(bre):
            n = bre[i + 1]
            out.append(n if n in "(){}|" else "\\" + n)
            i += 2
            continue
        out.append("\\" + c if c in "(){}|+?" else c)
        i += 1
    return "".join(out)


def parse_sed_script(script):
    cmds = []
    for ln in script.splitlines():
        ln = ln.strip()
        if not ln:
            continue
        m = re.fullmatch(r"s/((?:[^/\\]|\\.)*)/((?:[^/\\]|\\.)*)/p", ln)
        if not m:
            raise ValueError(f"unsupported sed command: {ln!r}")
        repl = []
        j, r = 0, m.group(2)
        while j < len(r):
            if r[j] == "\\" and j + 1 < len(r):
                repl.append("\\g<%s>" % r[j + 1] if r[j + 1].isdigit() else r[j + 1].replace("\\", "\\\\"))
                j += 2
            elif r[j] == "&":
                repl.append("\\g<0>")
                j += 1
            else:
                repl.append(r[j].replace("\\", "\\\\"))
                j += 1
        cmds.append((re.compile(bre_to_python(m.group(1))), "".join(repl)))
    return cmds


def sed_n(script, text):
    """GNU 'sed -n -f script' on text, for scripts of s/RE/REPL/p lines: each
    input line goes through the commands in order; a successful substitution
    (first match only, no g flag) prints the line, and later commands see the
    modified line."""
    cmds = parse_sed_script(script)
    out = []
    for line in text.splitlines():
        for rx, repl in cmds:
            line, n = rx.subn(repl, line, count=1)
            if n:
                out.append(line)
    return "".join(ln + "\n" for ln in out)


def run_one(name, a):
    bdir = os.path.join(a.builds, name, "zephyr")
    elf, img = os.path.join(bdir, "zmk.elf"), os.path.join(bdir, "zmk.bin")
    tdir = os.path.join(a.tests, name)
    odir = os.path.join(a.out, name)
    os.makedirs(odir, exist_ok=True)
    if not (os.path.exists(elf) and os.path.exists(img)):
        return "NOBUILD", name, "no zmk.elf/zmk.bin"
    full = os.path.join(odir, "keycode_events_full.log")
    if a.engine == "go":
        cmd = [os.path.join(ROOT, "go", "bin", "run-boot"), "--elf", elf, "--bin", img, "--ms", str(a.ms),
               "--console", "--console-out", full, "--stop-at", "_exit"]
    else:
        cmd = [sys.executable, "-B", os.path.join(ROOT, "emulator", "run_boot.py"), "--elf", elf, "--bin", img,
               "--ms", str(a.ms), "--console", "--console-out", full, "--stop-at", "_exit"]
    r = subprocess.run(cmd, capture_output=True, text=True)
    with open(os.path.join(odir, "run.log"), "w") as f:
        f.write(r.stdout + r.stderr)
    if r.returncode != 0:
        return "FAILED", name, f"run-boot exit {r.returncode} (a fatal error or an emulator stop; see run.log)"
    # As run-test.sh: strip the log prefix up to "> ", then the patterns.
    text = open(full, errors="replace").read()
    stripped = "".join((ln.split("> ", 1)[1] if "> " in ln else ln) + "\n" for ln in text.splitlines())
    if a.sed:
        got = subprocess.run([a.sed, "-n", "-f", os.path.join(tdir, "events.patterns")], input=stripped,
                             capture_output=True, text=True, check=True).stdout
    else:
        got = sed_n(open(os.path.join(tdir, "events.patterns")).read(), stripped)
    with open(os.path.join(odir, "keycode_events.log"), "w") as f:
        f.write(got)
    want = open(os.path.join(tdir, "keycode_events.snapshot")).read()
    same = [ln.rstrip() for ln in got.splitlines()] == [ln.rstrip() for ln in want.splitlines()]
    capped = "" if "stop-at _exit: reached" in r.stdout else " (the image did not reach _exit; the cap ended the run)"
    if same:
        return "PASS", name, capped.strip()
    if os.path.exists(os.path.join(tdir, "pending")):
        return "PENDING", name, capped.strip()
    if name in a.expected_map:
        return "EXPECTED", name, a.expected_map[name] + capped
    return "FAILED", name, f"diff {os.path.join(tdir, 'keycode_events.snapshot')} {os.path.join(odir, 'keycode_events.log')}{capped}"


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--builds", required=True)
    ap.add_argument("--tests", required=True)
    ap.add_argument("--ms", type=float, default=60000, help="cap on the simulated time; a test ends at _exit")
    ap.add_argument("--jobs", type=int, default=max(1, (os.cpu_count() or 2) - 1))
    ap.add_argument("--engine", choices=("go", "python"), default="go")
    ap.add_argument("--filter", default="")
    ap.add_argument("--out", default=os.path.join(ROOT, "build", "zmk-tests-tc32"))
    ap.add_argument("--sed", help="apply events.patterns with this GNU sed instead of the built-in reading")
    ap.add_argument("--expected", help="file of '<test> <reason>' lines: tests whose difference is expected")
    a = ap.parse_args()
    a.expected_map = {}
    if a.expected:
        for ln in open(a.expected):
            ln = ln.strip()
            if ln and not ln.startswith("#"):
                t, _, why = ln.partition(" ")
                a.expected_map[t] = why.strip()
    names = cases(a.tests, a.filter)
    if not names:
        sys.exit("no tests found")
    results = []
    with concurrent.futures.ThreadPoolExecutor(a.jobs) as ex:
        for res in ex.map(lambda n: run_one(n, a), names):
            results.append(res)
            print(f"{res[0]}: {res[1]}" + (f"  {res[2]}" if res[2] else ""), flush=True)
    counts = {k: sum(1 for r in results if r[0] == k) for k in ("PASS", "FAILED", "NOBUILD", "PENDING", "EXPECTED")}
    print(f"\nPASS {counts['PASS']}  FAIL {counts['FAILED']}  NOBUILD {counts['NOBUILD']}  PENDING {counts['PENDING']}"
          f"  EXPECTED {counts['EXPECTED']}  of {len(results)} tests ({a.engine} engine, cap {a.ms:g} ms each)")
    stale = [t for t in a.expected_map if any(r[1] == t and r[0] == "PASS" for r in results)]
    if stale:
        print("passing tests still listed in --expected: " + ", ".join(stale))
    os.makedirs(a.out, exist_ok=True)
    with open(os.path.join(a.out, "pass-fail.log"), "w") as f:
        for kind, name, info in sorted(results, key=lambda r: r[1]):
            f.write(f"{kind}: {name}\n")
    return 1 if counts["FAILED"] or counts["NOBUILD"] else 0


if __name__ == "__main__":
    sys.exit(main())

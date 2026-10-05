#!/usr/bin/env python3
"""zmk_tests_tc32.sed_n against GNU sed: every events.patterns script of ZMK's
tests is applied to every captured console log (the stripped text, as the
runner does) by both, and the outputs must be equal. GNU sed is run in a
Docker container (debian:stable-slim) unless --sed names one on this host.

Usage: sed_n_check.py --tests <zmk/app/tests> --logs <runner --out dir> [--sed gsed]
SPDX-License-Identifier: Apache-2.0
"""
import argparse
import os
import subprocess
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from zmk_tests_tc32 import cases, sed_n  # noqa: E402


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--tests", required=True)
    ap.add_argument("--logs", required=True)
    ap.add_argument("--sed", help="a GNU sed on this host (else Docker debian:stable-slim)")
    a = ap.parse_args()
    scripts = {}
    for n in cases(a.tests, ""):
        scripts.setdefault(open(os.path.join(a.tests, n, "events.patterns")).read(), n)
    logs = []
    for n in cases(a.tests, ""):
        p = os.path.join(a.logs, n, "keycode_events_full.log")
        if os.path.exists(p):
            text = open(p, errors="replace").read()
            logs.append("".join((ln.split("> ", 1)[1] if "> " in ln else ln) + "\n" for ln in text.splitlines()))
    if not logs:
        sys.exit("no logs")
    # One sed process per script over all logs, separated by a marker line.
    marker = "=== sed_n_check log boundary ===\n"
    joined = marker.join(logs)
    bad = 0
    for script, example in scripts.items():
        mine = marker.join(sed_n(script, ln) for ln in logs)
        if a.sed:
            cmd = [a.sed, "-n", "-e", script]
        else:
            cmd = ["docker", "run", "--rm", "-i", "debian:stable-slim", "sed", "-n", "-e", script]
        # The marker must pass through both: add a command printing it.
        cmd[-1] = script + "\n/^=== sed_n_check log boundary ===$/p\n"
        theirs = subprocess.run(cmd, input=joined, capture_output=True, text=True, check=True).stdout
        if mine != theirs:
            bad += 1
            print(f"DIFFERS: {example} ({script.strip()!r})")
    print(f"sed_n: {len(scripts)} distinct scripts x {len(logs)} logs, {len(scripts) - bad} equal to GNU sed, {bad} differ")
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())

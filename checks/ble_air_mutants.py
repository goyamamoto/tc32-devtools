#!/usr/bin/env python3
"""Controls for ble_air_check.py: each of these changes to the air model, the
radio's use of it and the central's must make that check fail. Every mutant
is one replaced piece of text in a copy of emulator/, checks/ and common/
in a temporary folder; the repository's files are not touched. The copy
without a change must pass the check first, so that a copy which cannot run
does not count every mutant as caught.

Usage: ble_air_mutants.py

SPDX-License-Identifier: Apache-2.0
"""
import os
import shutil
import subprocess
import sys
import tempfile

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
MUTANTS = [
    ("the central's interval ignores the clock", "emulator/ble_central.py",
     'self.interval_ms = self.p["interval"] * 1.25 * clk', 'self.interval_ms = self.p["interval"] * 1.25'),
    ("central_tx does not ask the air", "emulator/ble_radio.py",
     'if self.air is not None and self.air.lost("keyboard", ch, t):', 'if False:'),
    ("the reply is not asked of the air", "emulator/ble_radio.py",
     'if c and self.air is not None and self.air.lost("host", b["ch"], self.m.ms()):', 'if False:'),
    ("both ways draw from one seed", "emulator/ble_air.py",
     '"host": random.Random(4 * seed + 1)', '"host": random.Random(4 * seed)'),
    ("a blackout draws", "emulator/ble_air.py",
     "if now_ms < self.blackout_until:\n            gone = True",
     "if now_ms < self.blackout_until:\n            gone = self.rng[way].randrange(MILLION) >= 0"),
    ("the jitter moves the next event's time", "emulator/ble_central.py",
     'self.radio.at(nxt + self.jitter_ms(), lambda: self.conn_event(k + 1), owner="central")',
     'j = self.jitter_ms(); self.base_t += j; self.radio.at(nxt + j, lambda: self.conn_event(k + 1), owner="central")'),
    ("establish gives up one event late", "emulator/ble_central.py",
     "if self.establish and k >= 6 and not self.heard:", "if self.establish and k >= 7 and not self.heard:"),
    ("a lost CONNECT_IND is not told to its central", "emulator/ble_radio.py",
     "if rsp[0] & 0xF == 5:                            # CONNECT_IND: its central cannot know", "if False:"),
    ("a lost advertising packet is still offered", "emulator/ble_radio.py",
     "            centrals = []\n        if cmd == 0x87", "            pass\n        if cmd == 0x87"),
    ("mic_ends does not end the connection", "emulator/ble_central.py",
     'if self.mic_ends and note.endswith("MIC FAILED)"):', "if False:"),
    ("ll_rules: the queue is not held during the encryption start", "emulator/ble_central.py",
     "        if self.enc_busy:\n            for i, item", "        if False:\n            for i, item"),
    ("ll_rules: the update's instant stays as given", "emulator/ble_central.py",
     'self.update["instant"] = (self.event + self.update_lead) & 0xFFFF', "pass"),
    ("ll_rules: an update not yet sent is applied at its instant", "emulator/ble_central.py",
     '(self.update_sent or not self.ll_rules) and k + 1 == u["instant"]', 'k + 1 == u["instant"]'),
    ("ll_rules: a rejection does not end the wait", "emulator/ble_central.py",
     'self.records.setdefault("peripheral_reject", []).append(p.hex())\n            self.enc_busy = False',
     'self.records.setdefault("peripheral_reject", []).append(p.hex())'),
    ("ll_rules: an update sent late in the event before its instant is not applied", "emulator/ble_central.py",
     'and self.update_sent and k == u["instant"]:', 'and self.update_sent and False:'),
    ("param_update_lead: the update's instant is 12 events on", "emulator/ble_central.py",
     "self.event + (self.param_update_lead or 12))", "self.event + 12)"),
    ("reclock keeps the old interval", "emulator/ble_central.py",
     "        self.interval_ms = self.p[\"interval\"] * 1.25 * self.clock()", "        pass"),
]


def copy_tree(tree):
    os.makedirs(tree)
    for d in ("emulator", "checks", "common"):
        shutil.copytree(os.path.join(ROOT, d), os.path.join(tree, d), ignore=shutil.ignore_patterns("__pycache__"))
    for f in os.listdir(ROOT):
        if f.endswith(".py"):
            shutil.copy(os.path.join(ROOT, f), tree)


def main():
    bad = 0
    with tempfile.TemporaryDirectory() as tmp:
        tree = os.path.join(tmp, "unchanged")
        copy_tree(tree)
        r = subprocess.run([sys.executable, "-B", os.path.join(tree, "checks", "ble_air_check.py")],
                           capture_output=True, text=True)
        if r.returncode != 0 or any(ln.startswith("FAIL") for ln in r.stdout.splitlines()):
            print("FAIL the unchanged copy does not pass the check:\n" + r.stdout[-2000:] + r.stderr[-2000:])
            return 1
        print("ok   the unchanged copy passes the check")
        for n, (name, path, old, new) in enumerate(MUTANTS):
            tree = os.path.join(tmp, str(n))
            copy_tree(tree)
            p = os.path.join(tree, path)
            text = open(p).read()
            if text.count(old) != 1:
                print(f"FAIL the mutant does not apply ({text.count(old)} places): {name}")
                bad += 1
                continue
            open(p, "w").write(text.replace(old, new))
            r = subprocess.run([sys.executable, "-B", os.path.join(tree, "checks", "ble_air_check.py")],
                               capture_output=True, text=True)
            fails = [ln for ln in r.stdout.splitlines() if ln.startswith("FAIL")]
            caught = bool(fails) or r.returncode != 0
            print(("ok   caught: " if caught else "FAIL not caught: ") + f"{name}: {len(fails)} FAIL line(s)"
                  + ("" if fails or not caught else " (the check stopped with an error)"))
            bad += not caught
    print(f"\n{len(MUTANTS)} mutants, {bad} failure(s)")
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())

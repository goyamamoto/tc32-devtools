#!/usr/bin/env python3
"""The SRAM's contents at power-on (TC32EMU_SRAM_SEED), in both engines.
A hand-assembled image reads the word at 0x841000, which nothing wrote, stores
it at 0x841004 and sets 0x841008 to 0x600dc0de (tc32emu-run's done value). Without a seed the word is 0 (the SRAM is
zeros at power-on); with a seed it is the fill's bytes at that offset, the
same in the Python and the Go emulator: an image that counts on a variable
being zero before it is written fails under the fill. A reset keeps the SRAM.
Each Python half runs in its own interpreter (the seed is read at import).
Usage: sram_fill_check.py            (both halves, both engines)
       sram_fill_check.py --half     (the Python emulator with the environment as it is)
SPDX-License-Identifier: Apache-2.0
"""
import os
import struct
import subprocess
import sys
ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path[:0] = [os.path.join(ROOT, "emulator"), os.path.join(ROOT, "common")]
GO_RUN = os.path.join(ROOT, "go", "bin", "tc32emu-run")
SEED = 0x2A

failures = []


def check(cond, text):
    print(("ok   " if cond else "FAIL ") + text)
    if not cond:
        failures.append(text)


def image():
    """The test image: a branch over the header, then the program at 0x20 (Thumb, re-encoded)."""
    from tc32isa import to_tc32
    code = [0x4803,  # ldr r0, =0x841000
            0x6801,  # ldr r1, [r0]
            0x4A03,  # ldr r2, =0x841004
            0x6011,  # str r1, [r2]
            0x4B03,  # ldr r3, =0x841008
            0x4C04,  # ldr r4, =0x600dc0de
            0x601C,  # str r4, [r3]
            0xE7FE]  # b .
    img = bytearray(0x40)
    struct.pack_into("<H", img, 0, to_tc32(0xE00E))          # b 0x20
    img[8:12] = b"KNLT"                                       # bootable; the word at 0xc (0): nothing copied to SRAM
    for i, hw in enumerate(code):
        struct.pack_into("<H", img, 0x20 + 2 * i, to_tc32(hw))
    struct.pack_into("<IIII", img, 0x30, 0x841000, 0x841004, 0x841008, 0x600DC0DE)
    return bytes(img)


def half():
    import tc32emu as te
    fl = te.Flash()
    fl.mem[:0x40] = image()
    m = te.Machine(fl, max_log=0)
    for _ in range(100):
        if struct.unpack_from("<I", m.sram, 0x1008)[0] == 0x600DC0DE:
            break
        m.step()
    else:
        print("FAIL the image did not finish")
        return 1
    result = struct.unpack_from("<I", m.sram, 0x1004)[0]
    before = bytes(m.sram[0x1000:0x1010])
    m.reset()
    kept = bytes(m.sram[0x1000:0x1010]) == before
    print(f"python {te.SRAM_SEED:#x} {result:#010x} {'kept' if kept else 'CLEARED'}")
    return 0


def go(env):
    path = os.path.join(ROOT, "build", "sram_fill.bin")
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "wb") as f:
        f.write(image())
    r = subprocess.run([GO_RUN, path, "--done", "0x841008", "--results", "0x841004", "--count", "1", "--limit", "100000"],
                       env=env, capture_output=True, text=True)
    p = r.stdout.split()
    if len(p) < 2 or p[0] != "ok":
        return None, r.stdout + r.stderr
    return int(p[1], 16), None


def main():
    if "--half" in sys.argv:
        return half()
    import tc32emu as te
    expected = struct.unpack_from("<I", te.sram_fill(SEED, 0x1004), 0x1000)[0]
    for seed in (None, SEED):
        env = dict(os.environ)
        env.pop("TC32EMU_SRAM_SEED", None)
        if seed is not None:
            env["TC32EMU_SRAM_SEED"] = hex(seed)
        want = expected if seed is not None else 0
        r = subprocess.run([sys.executable, "-B", os.path.abspath(__file__), "--half"], env=env, capture_output=True, text=True)
        p = r.stdout.split()
        py = int(p[2], 16) if len(p) >= 4 and p[0] == "python" else None
        label = f"seed {seed:#x}" if seed is not None else "no seed"
        check(py == want, f"{label}, Python: the unwritten word at 0x841000 reads {py:#010x}, expected {want:#010x}"
              if py is not None else f"{label}, Python: {r.stdout}{r.stderr}")
        check(len(p) >= 4 and p[3] == "kept", f"{label}, Python: a reset keeps the SRAM")
        g, err = go(env)
        check(g == want, f"{label}, Go: reads {g:#010x}, expected {want:#010x}" if g is not None else f"{label}, Go: {err}")
    check(expected != 0, f"the fill's word at 0x841000 for seed {SEED:#x} is {expected:#010x}: an image counting on zero there fails under the fill")
    print(f"{len(failures)} failure(s)")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())

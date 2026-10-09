#!/usr/bin/env python3
"""The public key engine's model (DS-TLSR8278 16, registers 0x802000-, operand
RAM A at 0x802400 and B at 0x803000), on a machine that runs no code, with
the operands in the slots the engine's routines take: P-256 point
multiplication (MC_PTR 0x10)
of the Bluetooth Core specification's debug private key gives the debug public
key (Vol 3 Part H 2.3.5.6.1), a second key pair and the ECDH shared secret
both ways equal what the cryptography package computes, the point check (0x0c)
accepts a point on the curve and refuses one off it (STOP_LOG 3), a scalar of
0 gives STOP_LOG 2, CAL_PRE_MON (0x28) gives R^2 mod p and -p^-1 mod 2^32,
Done comes after the time asked for and a write to STAT clears it, Stop ends a
running operation (STOP_LOG 1), and a start without the clock, in reset, with
another radix or operand form, or with wrong Montgomery constants stops the
emulation. The Go port (go/tc32emu/pke_test.go) checks the same values.

SPDX-License-Identifier: Apache-2.0
"""
import os
import struct
import sys

sys.path[:0] = [os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "emulator")]
import tc32emu as te  # noqa: E402

failures = []

P = 0xffffffff00000001000000000000000000000000ffffffffffffffffffffffff
A = P - 3
B = 0x5ac635d8aa3a93e7b3ebbd55769886bc651d06b0cc53b0f63bce3c3e27d2604b
GX = 0x6b17d1f2e12c4247f8bce6e563a440f277037d812deb33a0f4a13945d898c296
GY = 0x4fe342e2fe1a7f9b8ee7eb4a7c0f9e162bce33576b315ececbb6406837bf51f5
R2 = pow(1 << 256, 2, P)
N1 = -pow(P, -1, 1 << 32) % (1 << 32)
# The debug key pair of the Core specification, a second pair and their shared secret (the
# cryptography package, independently of this model).
DA = 0x3f49f6d4a3c55f3874c9b3e3d2103f504aff607beb40b7995899b8a6cd3c1abd
AX = 0x20b003d2f297be2c5e2c83a7e9f9a5b9eff49111acf4fddbcc0301480e359de6
AY = 0xdc809c49652aeb6d63329abf5a52155c766345c28fed3024741c8ed01589d28b
DB = 0x55188b3d32f6bb9a900afcfbeed4e72a59cb9ac2f19d7cfb6b4fdd49f47fc5fd
BX = 0x1ea1f0f01faf1d9609592284f19e4c0047b58afd8615a69f559077b22faaa190
BY = 0x4c55f33e429dad377356703a9ab85160472d1130e28e36765f89aff915b1214a
DHKEY = 0xec0234a357c8ad05341010a60a397d9b99796b13b4f866f1868d34f373bfa698

SLOT = te.PKE_STEP


def check(cond, text):
    print(("ok   " if cond else "FAIL ") + text)
    if not cond:
        failures.append(text)


def machine():
    fl = te.Flash()
    fl.mem[8] = 0x4B
    m = te.Machine(fl, max_log=0)
    m.regs_mem[0x61] &= ~0x80   # reset released
    m.regs_mem[0x64] |= 0x80    # clock on
    m.reg_write(te.PKE_CONF, 4, (2 << 24) | (8 << 16))
    m.reg_write(te.PKE_EXE_CONF, 4, 0x15)
    return m


def put(m, off, value, words=9):
    m.regs_mem[off:off + 4 * words] = value.to_bytes(4 * words, "little")


def get(m, off, words=8):
    return int.from_bytes(m.regs_mem[off:off + 4 * words], "little")


def a_slot(i):
    return te.PKE_RAM_A + i * SLOT


def b_slot(i):
    return te.PKE_RAM_B + i * SLOT


def curve(m):
    put(m, b_slot(3), P)
    put(m, a_slot(3), R2)
    put(m, b_slot(4), N1)
    put(m, a_slot(5), A)


def run(m, mc):
    """Starts the microcode and advances the clock to its end; the stop reason."""
    m.reg_write(te.PKE_MC_PTR, 4, mc)
    m.reg_write(te.PKE_STAT, 4, 0)
    m.reg_write(te.PKE_CTRL, 4, 1)
    m.cycles += m.pke_done_at - m.cycles
    return m.reg_read(te.PKE_RT_CODE, 4)


def mul(m, k, x, y):
    curve(m)
    put(m, b_slot(0), x)
    put(m, b_slot(1), y)
    put(m, a_slot(4), k)
    rt = run(m, te.PKE_PMUL)
    return rt, get(m, a_slot(0)), get(m, a_slot(1))


def verify(m, x, y):
    curve(m)
    put(m, b_slot(0), x)
    put(m, b_slot(1), y)
    put(m, a_slot(4), B)
    return run(m, te.PKE_PVER)


def expect_error(m, what, fn):
    try:
        fn()
        check(False, what + ": stops the emulation")
    except te.EmuError as e:
        check(True, f"{what}: stops the emulation ({str(e).split(' at ')[0]})")


def main():
    m = machine()
    rt, x, y = mul(m, DA, GX, GY)
    check((rt, x, y) == (0, AX, AY), "PMUL: the debug private key times G is the debug public key")
    rt, x, y = mul(m, DB, GX, GY)
    check((rt, x, y) == (0, BX, BY), "PMUL: a second key pair as the cryptography package computes it")
    rt, x, y = mul(m, DA, BX, BY)
    check(rt == 0 and x == DHKEY, "PMUL: dA * B gives the shared secret's x")
    rt, x2, y2 = mul(m, DB, AX, AY)
    check(rt == 0 and x2 == DHKEY and y2 == y, "PMUL: dB * A gives the same point")
    check(get(m, a_slot(0), 9) == x2 and get(m, a_slot(1), 9) == y2, "PMUL: the ninth word of a result is 0")
    # A multiplication by 0 never finishes on the chip: Done stays 0 however long, until Stop.
    curve(m)
    put(m, b_slot(0), GX)
    put(m, b_slot(1), GY)
    put(m, a_slot(4), 0)
    m.reg_write(te.PKE_MC_PTR, 4, te.PKE_PMUL)
    m.reg_write(te.PKE_STAT, 4, 0)
    m.reg_write(te.PKE_CTRL, 4, 1)
    m.cycles += 100 * te.PKE_US * m.cpu_hz // 1_000_000
    check(m.reg_read(te.PKE_STAT, 4) == 0, "PMUL: a scalar of 0 is never done (Done 0 after 100 times the operation's time)")
    m.reg_write(te.PKE_CTRL, 4, 1 << 16)
    check(m.reg_read(te.PKE_STAT, 4) == 1 and m.reg_read(te.PKE_RT_CODE, 4) == te.PKE_RT_STOPPED,
          "PMUL by 0: Stop ends it with STOP_LOG 1")
    t0 = m.cycles
    check(verify(m, AX, AY) == 0, "PVER: the debug public key is on the curve")
    check(m.cycles - t0 == -(-te.PKE_PVER_US * m.cpu_hz // 1_000_000),
          f"PVER takes PKE_PVER_US ({te.PKE_PVER_US} us), not PKE_US ({te.PKE_US} us)")
    check(verify(m, AX, AY + 1) == te.PKE_RT_NOT_ON_CURVE, "PVER: a point off the curve gives STOP_LOG 3")
    check(verify(m, AX, P) == te.PKE_RT_NOT_ON_CURVE, "PVER: a coordinate not below p is off the curve")
    put(m, b_slot(3), P)
    put(m, a_slot(3), 0)
    put(m, b_slot(4), 0)
    check(run(m, te.PKE_CAL_PRE_MON) == 0 and get(m, a_slot(3)) == R2 and get(m, b_slot(4), 1) == N1,
          "CAL_PRE_MON: R^2 mod p in A3 and -p^-1 mod 2^32 in B4")

    # Done, its clearing, the time and Stop.
    m = machine()
    curve(m)
    put(m, b_slot(0), GX)
    put(m, b_slot(1), GY)
    put(m, a_slot(4), DA)
    m.reg_write(0xFFEC, 4, 1234)
    m.reg_write(te.PKE_MC_PTR, 4, te.PKE_PMUL)
    m.reg_write(te.PKE_CTRL, 4, 1)
    t0 = m.cycles
    check(m.reg_read(te.PKE_STAT, 4) == 0 and m.reg_read(te.PKE_RT_CODE, 4) == 0, "Done reads 0 while the engine runs")
    check(get(m, a_slot(0)) != AX, "the result is not in the operand RAM before Done")
    m.cycles = t0 + 1234 * m.cpu_hz // 1_000_000 - 1
    check(m.reg_read(te.PKE_STAT, 4) == 0, "Done reads 0 one cycle before the time asked for (0xffec)")
    m.cycles += 1
    check(m.reg_read(te.PKE_STAT, 1) == 1 and get(m, a_slot(0)) == AX, "Done reads 1 at the time, the result is in A0")
    m.reg_write(te.PKE_STAT, 4, 0)
    check(m.reg_read(te.PKE_STAT, 4) == 0, "a write to STAT clears Done")
    m.reg_write(te.PKE_CTRL, 4, 1)
    m.cycles += 10
    m.reg_write(te.PKE_CTRL, 4, 0x10000)
    check(m.reg_read(te.PKE_STAT, 4) == 1 and m.reg_read(te.PKE_RT_CODE, 4) == te.PKE_RT_STOPPED,
          "Stop ends a running operation with STOP_LOG 1")

    # What is not modelled stops the emulation.
    m = machine()
    curve(m)
    put(m, b_slot(0), GX)
    put(m, b_slot(1), GY)
    put(m, a_slot(4), DA)
    m.regs_mem[0x64] &= ~0x80
    expect_error(m, "a start without the clock", lambda: run(m, te.PKE_PMUL))
    m.regs_mem[0x64] |= 0x80
    m.regs_mem[0x61] |= 0x80
    expect_error(m, "a start in reset", lambda: run(m, te.PKE_PMUL))
    m.regs_mem[0x61] &= ~0x80
    m.reg_write(te.PKE_CONF, 4, (2 << 24) | (6 << 16))
    expect_error(m, "192-bit operands", lambda: run(m, te.PKE_PMUL))
    m.reg_write(te.PKE_CONF, 4, (2 << 24) | (8 << 16))
    m.reg_write(te.PKE_EXE_CONF, 4, 0x2A)
    expect_error(m, "Montgomery operands", lambda: run(m, te.PKE_PMUL))
    m.reg_write(te.PKE_EXE_CONF, 4, 0x15)
    expect_error(m, "microcode 0x18 (MODMUL)", lambda: run(m, 0x18))
    put(m, a_slot(3), R2 ^ 1)
    expect_error(m, "a wrong R^2 mod p", lambda: run(m, te.PKE_PMUL))
    put(m, a_slot(3), R2)
    m.reg_write(te.PKE_MC_PTR, 4, te.PKE_PMUL)
    m.reg_write(te.PKE_CTRL, 4, 1)
    expect_error(m, "a start while running", lambda: m.reg_write(te.PKE_CTRL, 4, 1))

    print(f"pke_check: {len(failures)} failure(s)")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())

"""The NIST P-256 curve in plain Python integers (affine coordinates, None
for the point at infinity), for the emulator's public key engine model
(tc32emu.py) and the scripted central's Secure Connections (ble_sc.py).
Neither speed nor constant time matter here; correctness is checked by
checks/pke_check.py and checks/ble_models_check.py against the Bluetooth
Core specification's debug key pair and a recorded pairing.

SPDX-License-Identifier: Apache-2.0
"""
P = 0xffffffff00000001000000000000000000000000ffffffffffffffffffffffff
A = P - 3
B = 0x5ac635d8aa3a93e7b3ebbd55769886bc651d06b0cc53b0f63bce3c3e27d2604b
N = 0xffffffff00000000ffffffffffffffffbce6faada7179e84f3b9cac2fc632551
GX = 0x6b17d1f2e12c4247f8bce6e563a440f277037d812deb33a0f4a13945d898c296
GY = 0x4fe342e2fe1a7f9b8ee7eb4a7c0f9e162bce33576b315ececbb6406837bf51f5
G = (GX, GY)


def add(P1, Q, p=P, a=A):
    if P1 is None:
        return Q
    if Q is None:
        return P1
    x1, y1 = P1
    x2, y2 = Q
    if x1 == x2:
        if (y1 + y2) % p == 0:
            return None
        lam = (3 * x1 * x1 + a) * pow(2 * y1, -1, p) % p
    else:
        lam = (y2 - y1) * pow(x2 - x1, -1, p) % p
    x3 = (lam * lam - x1 - x2) % p
    return x3, (lam * (x1 - x3) - y1) % p


def mul(k, P1, p=P, a=A):
    R = None
    while k:
        if k & 1:
            R = add(R, P1, p, a)
        P1 = add(P1, P1, p, a)
        k >>= 1
    return R


def on_curve(P1, p=P, a=A, b=B):
    x, y = P1
    return x < p and y < p and (y * y - (x * x * x + a * x + b)) % p == 0


# The octet forms the SMP PDUs use: 32 octets, least significant first.
def scalar_from_le(b):
    return int.from_bytes(b, "little")


def point_to_le(P1):
    return P1[0].to_bytes(32, "little") + P1[1].to_bytes(32, "little")


def point_from_le(b):
    return int.from_bytes(b[:32], "little"), int.from_bytes(b[32:64], "little")


def public_key(priv):
    """The public key of a private key given as an integer in [1, n - 1]."""
    return mul(priv, G)


def dhkey(priv, pub):
    """The x coordinate of priv * pub, 32 octets least significant first."""
    return mul(priv, pub)[0].to_bytes(32, "little")

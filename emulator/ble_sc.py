"""The LE Secure Connections functions of Bluetooth Core Vol 3 Part H 2.2:
AES-CMAC (RFC 4493) on aes128.encrypt(), f4, f5, f6 and g2, in the octet
order the SMP PDUs carry their values (least significant octet first) as
the scripted central (ble_central.py) uses them. checks/ble_models_check.py
tests them against the specification's vectors and a recorded pairing.

SPDX-License-Identifier: Apache-2.0
"""
import aes128

SALT = bytes.fromhex("6C888391AAF5A53860370BDB5A6083BE")
KEY_ID = b"btle"


def _shift(k):
    v = int.from_bytes(k, "big") << 1
    if v >> 128:
        v ^= 0x87
    return (v & ((1 << 128) - 1)).to_bytes(16, "big")


def aes_cmac(key, m):
    """RFC 4493: key and message as written, the MAC most significant octet first."""
    k1 = _shift(aes128.encrypt(key, bytes(16)))
    k2 = _shift(k1)
    blocks = max(1, (len(m) + 15) // 16)
    whole = len(m) > 0 and len(m) % 16 == 0
    last = m[(blocks - 1) * 16:]
    if not whole:
        last = last + b"\x80" + bytes(15 - len(last))
    last = bytes(a ^ b for a, b in zip(last, k1 if whole else k2))
    x = bytes(16)
    for i in range(blocks - 1):
        x = aes128.encrypt(key, bytes(a ^ b for a, b in zip(x, m[i * 16:(i + 1) * 16])))
    return aes128.encrypt(key, bytes(a ^ b for a, b in zip(x, last)))


def f4(u, v, x, z):
    """Confirm value: u, v the 32-octet x coordinates, x the 16-octet random, z one octet (all on-air order)."""
    return aes_cmac(x[::-1], u[::-1] + v[::-1] + bytes([z]))[::-1]


def addr7(addr_type, addr):
    """A1 or A2: the type, then the 6 on-air octets most significant first."""
    return bytes([addr_type]) + addr[::-1]


def f5(w, n1, n2, a1, a2):
    """MacKey and LTK (on-air order) from the DHKey w, the randoms and addr7() of both sides."""
    t = aes_cmac(SALT, w[::-1])
    body = KEY_ID + n1[::-1] + n2[::-1] + a1 + a2 + bytes([1, 0])
    return aes_cmac(t, bytes([0]) + body)[::-1], aes_cmac(t, bytes([1]) + body)[::-1]


def f6(w, n1, n2, r, iocap, a1, a2):
    """A DHKey check (on-air order): w the MacKey, iocap the three octets AuthReq, OOB flag, IO capability."""
    return aes_cmac(w[::-1], n1[::-1] + n2[::-1] + r[::-1] + bytes(iocap) + a1 + a2)[::-1]


def g2(u, v, x, y):
    """The numeric comparison value: an integer below 1000000."""
    return int.from_bytes(aes_cmac(x[::-1], u[::-1] + v[::-1] + y[::-1])[12:], "big") % 1000000


def iocap_of(pairing_pdu):
    """AuthReq, OOB flag, IO capability of a Pairing Request or Response as sent."""
    return bytes([pairing_pdu[3], pairing_pdu[2], pairing_pdu[1]])

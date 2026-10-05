"""Pure-Python AES-128 (FIPS-197), CCM (RFC 3610, NIST SP 800-38C) and the
Bluetooth LE security functions a scripted central needs: e(), c1(), s1()
(Core Vol 3 Part H 2.2) and the link layer's AES-CCM with a 4-octet MIC (Core
Vol 6 Part E 2.1). Byte strings are in the standard AES orientation (first
byte = most significant octet) unless a name says _le. No external crypto
library is used. checks/ble_models_check.py tests it against the published
known answers.

SPDX-License-Identifier: Apache-2.0
"""
SBOX = [0] * 256
INV = [0] * 256


def _init():
    p = q = 1
    while True:
        p = p ^ ((p << 1) & 0xFF) ^ (0x1B if p & 0x80 else 0)
        q ^= q << 1
        q ^= q << 2
        q ^= q << 4
        q &= 0xFF
        if q & 0x80:
            q ^= 0x09
        x = q ^ ((q << 1) | (q >> 7)) ^ ((q << 2) | (q >> 6)) ^ ((q << 3) | (q >> 5)) ^ ((q << 4) | (q >> 4))
        x = (x ^ 0x63) & 0xFF
        SBOX[p] = x
        INV[x] = p
        if p == 1:
            break
    SBOX[0] = 0x63
    INV[0x63] = 0


_init()
assert SBOX[0x53] == 0xED and INV[0xED] == 0x53


def _xt(a):
    return ((a << 1) ^ 0x1B) & 0xFF if a & 0x80 else a << 1


def _mul(a, b):
    r = 0
    while b:
        if b & 1:
            r ^= a
        a = _xt(a)
        b >>= 1
    return r


def expand(key):
    w = [list(key[i:i + 4]) for i in range(0, 16, 4)]
    rcon = 1
    for i in range(4, 44):
        t = list(w[i - 1])
        if i % 4 == 0:
            t = t[1:] + t[:1]
            t = [SBOX[x] for x in t]
            t[0] ^= rcon
            rcon = _xt(rcon)
        w.append([w[i - 4][j] ^ t[j] for j in range(4)])
    return [sum(w[4 * r:4 * r + 4], []) for r in range(11)]


def encrypt(key, block):
    rk = expand(bytes(key))
    s = [b ^ k for b, k in zip(block, rk[0])]
    for rnd in range(1, 11):
        s = [SBOX[x] for x in s]
        s = [s[(i + 4 * (i % 4)) % 16] for i in range(16)]          # ShiftRows (column-major state)
        if rnd != 10:
            n = []
            for c in range(4):
                a = s[4 * c:4 * c + 4]
                n += [_mul(a[0], 2) ^ _mul(a[1], 3) ^ a[2] ^ a[3],
                      a[0] ^ _mul(a[1], 2) ^ _mul(a[2], 3) ^ a[3],
                      a[0] ^ a[1] ^ _mul(a[2], 2) ^ _mul(a[3], 3),
                      _mul(a[0], 3) ^ a[1] ^ a[2] ^ _mul(a[3], 2)]
            s = n
        s = [b ^ k for b, k in zip(s, rk[rnd])]
    return bytes(s)


def decrypt(key, block):
    rk = expand(bytes(key))
    s = [b ^ k for b, k in zip(block, rk[10])]
    for rnd in range(9, -1, -1):
        s = [s[(i - 4 * (i % 4)) % 16] for i in range(16)]          # InvShiftRows
        s = [INV[x] for x in s]
        s = [b ^ k for b, k in zip(s, rk[rnd])]
        if rnd:
            n = []
            for c in range(4):
                a = s[4 * c:4 * c + 4]
                n += [_mul(a[0], 14) ^ _mul(a[1], 11) ^ _mul(a[2], 13) ^ _mul(a[3], 9),
                      _mul(a[0], 9) ^ _mul(a[1], 14) ^ _mul(a[2], 11) ^ _mul(a[3], 13),
                      _mul(a[0], 13) ^ _mul(a[1], 9) ^ _mul(a[2], 14) ^ _mul(a[3], 11),
                      _mul(a[0], 11) ^ _mul(a[1], 13) ^ _mul(a[2], 9) ^ _mul(a[3], 14)]
            s = n
    return bytes(s)


# FIPS-197 Appendix C.1
_K = bytes(range(16))
_P = bytes.fromhex("00112233445566778899aabbccddeeff")
assert encrypt(_K, _P).hex() == "69c4e0d86a7b0430d8cdb78070b4c55a"
assert decrypt(_K, encrypt(_K, _P)) == _P


def e(k, p):
    """Security function e with 128-bit integers."""
    return int.from_bytes(encrypt(k.to_bytes(16, "big"), p.to_bytes(16, "big")), "big")


def c1(k, r, preq, pres, iat, ia, rat, ra):
    """Legacy confirm value. preq/pres: the 7-octet SMP commands as sent;
    ia/ra: 6-octet addresses as sent (LSO first); returns an integer."""
    p1 = (int.from_bytes(pres, "little") << 72) | (int.from_bytes(preq, "little") << 16) | (rat << 8) | iat
    p2 = (int.from_bytes(ia, "little") << 48) | int.from_bytes(ra, "little")
    return e(k, e(k, r ^ p1) ^ p2)


def s1(k, r1, r2):
    return e(k, ((r1 & (1 << 64) - 1) << 64) | (r2 & (1 << 64) - 1))


def ah(k, r):
    """Random address hash (Core Vol 3 Part H 2.2.2): the low 24 bits of e(k, r)
    for a 24-bit r, integers."""
    return e(k, r & 0xFFFFFF) & 0xFFFFFF


def rpa(irk, prand):
    """A resolvable private address, 6 octets LSO first: hash in the low three
    octets, prand (bits 23:22 = 01) in the high three. irk: 16 octets LSO
    first, as SMP's Identity Information carries it."""
    prand = (prand & 0x3FFFFF) | 0x400000
    h = ah(int.from_bytes(irk, "little"), prand)
    return h.to_bytes(3, "little") + prand.to_bytes(3, "little")


# Core Vol 3 Part H 2.2.3/2.2.4 sample data
assert c1(0, 0x5783D52156AD6F0E6388274EC6702EE0, bytes.fromhex("01010000100707"), bytes.fromhex("02030000080005"),
          1, bytes.fromhex("a6a5a4a3a2a1"), 0, bytes.fromhex("b6b5b4b3b2b1")) == 0x1E1E3FEF878988EAD2A74DC5BEF13B86
assert s1(0, 0x000F0E0D0C0B0A091122334455667788, 0x010203040506070899AABBCCDDEEFF00) == \
    0x9A1FE1F0E8B0F49B5B4216AE796DA062
# Core Vol 3 Part H D.7 sample data
assert ah(0xEC0234A357C8AD05341010A60A397D9B, 0x708194) == 0x0DFBAA


def ccm(key, nonce, aad, data, mic_len):
    """CCM encryption (RFC 3610): returns data' || MIC. The length field has
    15 - len(nonce) octets; aad shorter than 0xff00 octets."""
    n, lf = len(data), 15 - len(nonce)
    flags = (0x40 if aad else 0) | ((mic_len - 2) // 2) << 3 | (lf - 1)
    x = encrypt(key, bytes([flags]) + nonce + n.to_bytes(lf, "big"))
    blocks = b""
    if aad:
        a = len(aad).to_bytes(2, "big") + aad
        blocks += a + bytes(-len(a) % 16)
    blocks += data + bytes(-n % 16)
    for i in range(0, len(blocks), 16):
        x = encrypt(key, bytes(p ^ q for p, q in zip(x, blocks[i:i + 16])))

    def ctr(i):
        return encrypt(key, bytes([lf - 1]) + nonce + i.to_bytes(lf, "big"))
    out = bytearray()
    for i in range(0, n, 16):
        out += bytes(p ^ q for p, q in zip(data[i:i + 16], ctr(i // 16 + 1)))
    return bytes(out) + bytes(p ^ q for p, q in zip(x[:mic_len], ctr(0)))


def ccm_encrypt(sk, nonce, hdr0, payload):
    """BLE LL AES-CCM: returns payload' || MIC (4 octets). sk: 16 key octets
    (standard orientation); nonce: 13 octets; AAD = hdr0 with NESN, SN, MD
    masked (& 0xe3)."""
    return ccm(sk, nonce, bytes([hdr0 & 0xE3]), payload, 4)


def ccm_decrypt(sk, nonce, hdr0, data):
    """Inverse of ccm_encrypt: returns (payload, mic_ok). Fewer than 4 octets
    cannot hold the MIC: (b"", False)."""
    if len(data) < 4:
        return b"", False
    n = len(data) - 4
    lf = 15 - len(nonce)
    out = bytearray()
    for i in range(0, n, 16):
        s = encrypt(sk, bytes([lf - 1]) + nonce + (i // 16 + 1).to_bytes(lf, "big"))
        out += bytes(a ^ b for a, b in zip(data[i:min(i + 16, n)], s))
    return bytes(out), ccm_encrypt(sk, nonce, hdr0, bytes(out))[n:] == data[n:]


# Core Vol 6 Part C 1 (LE encryption sample data): session key and the first
# central-to-peripheral packet (LL_START_ENC_RSP, counter 0, direction 1).
_LTK = 0x4C68384139F574D836BCF34E9DFB01BF
_SKD = 0x0213243546576879ACBDCEDFE0F10213  # SKDs || SKDm
_SK = e(_LTK, _SKD)
assert _SK == 0x99AD1B5226A37E3E058E3B8E27C2C666, hex(_SK)
_IV = bytes.fromhex("24abdcbabebaafde")    # IVm || IVs as octets on air: IVm = babcab24, IVs = deafbabe
_nonce = (0).to_bytes(4, "little") + bytes([0x80]) + _IV
# Data PDU of the sample: 0F 05 9F CD A7 F4 48 (header, length, payload, MIC)
assert ccm_encrypt(_SK.to_bytes(16, "big"), _nonce, 0x0F, bytes([0x06])).hex() == "9fcda7f448"
assert ccm_decrypt(_SK.to_bytes(16, "big"), _nonce, 0x0F, bytes.fromhex("9fcda7f448")) == (bytes([6]), True)

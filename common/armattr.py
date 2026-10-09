"""The core an ARM ELF file names in its build attributes (Tag_CPU_name).

An object or link built with -mcpu=tc32 (llvm-tc32) holds its Thumb-1
instructions in TC32's encoding and names the core "tc32"; one built for an
ARM core (-mcpu=arm7tdmi) holds Thumb's encoding and names that core. The
tools that write or check an image read this to tell the two apart: a linked
ELF keeps the attributes of its first input (ld.lld).

SPDX-License-Identifier: Apache-2.0
"""
import struct

SHT_ARM_ATTRIBUTES = 0x70000003
TAG_FILE = 1
TAG_CPU_NAME = 5


def _uleb(buf, i):
    value = shift = 0
    while True:
        b = buf[i]
        i += 1
        value |= (b & 0x7F) << shift
        shift += 7
        if not b & 0x80:
            return value, i


def _ntbs(buf, i):
    end = buf.index(b"\0", i)
    return buf[i:end].decode("ascii", "replace"), end + 1


def _attributes_section(data):
    """The bytes of the .ARM.attributes section of an ELF32 little-endian file, or None."""
    if data[:4] != b"\x7fELF" or data[4] != 1 or data[5] != 1:
        raise ValueError("not a little-endian ELF32 file")
    shoff, = struct.unpack_from("<I", data, 0x20)
    shentsize, shnum = struct.unpack_from("<HH", data, 0x2E)
    for k in range(shnum):
        _name, typ, _flags, _addr, off, size = struct.unpack_from("<IIIIII", data, shoff + k * shentsize)
        if typ == SHT_ARM_ATTRIBUTES:
            return data[off:off + size]
    return None


def cpu_name(data):
    """Tag_CPU_name of the file-wide aeabi attributes ("" when absent, None
    when the file has no attributes section)."""
    sec = _attributes_section(data)
    if sec is None:
        return None
    if not sec or sec[0] != 0x41:            # format version 'A'
        return ""
    i = 1
    while i + 4 <= len(sec):
        length, = struct.unpack_from("<I", sec, i)
        if length < 5:
            break
        end = i + length
        vendor, j = _ntbs(sec, i + 4)
        while vendor == "aeabi" and j < end:
            tag, k = _uleb(sec, j)
            size, = struct.unpack_from("<I", sec, k)
            sub_end = j + size
            k += 4
            if tag == TAG_FILE:
                while k < sub_end:
                    t, k = _uleb(sec, k)
                    if t == TAG_CPU_NAME:
                        return _ntbs(sec, k)[0]
                    if t == 32:                       # Tag_compatibility: ULEB, then NTBS
                        _, k = _uleb(sec, k)
                        _, k = _ntbs(sec, k)
                    elif t in (4, 5, 65, 67) or (t > 32 and t & 1):
                        _, k = _ntbs(sec, k)          # strings
                    else:
                        _, k = _uleb(sec, k)          # numbers
            j = sub_end
        i = end
    return ""


def is_tc32(data):
    """True when the file names the core tc32 (its code is in TC32's encoding)."""
    return (cpu_name(data) or "").lower() == "tc32"

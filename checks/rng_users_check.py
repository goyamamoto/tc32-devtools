#!/usr/bin/env python3
"""Who in a linked TC32 image can reach the random bit generator or Zephyr's
random number APIs; for images built with zephyr-tc32's tc32_rng, which
must be the only reader of the generator's raw output.
1. Every word in the code and constant sections whose value lies in the
   random bit generator's registers (0x804400-0x804487), with the function
   that holds it and that function's object file (from the linker map): all
   must be in tc32_rng.c. An address the code computes instead of loading is
   not seen.
2. Every symbol of Zephyr's random number or entropy APIs (sys_rand*,
   sys_csrand*, z_impl_sys_*rand*, entropy driver API tables, xoshiro) other
   than the linker's markers of an empty section, and every caller of them.
Exit 0 when both show nothing outside tc32_rng.
Usage: rng_users_check.py LLVM_BIN_DIR zephyr.elf zephyr.map

SPDX-License-Identifier: Apache-2.0
"""
import re
import struct
import subprocess
import sys

bin_dir, elf, mapf = sys.argv[1:4]
nm = subprocess.run([bin_dir + "/llvm-nm", "-S", "-n", elf], capture_output=True, text=True).stdout
funcs = []
names = {}
addrs = {}
for ln in nm.splitlines():
    p = ln.split()
    if len(p) == 4 and p[2] in "TtWw":
        a, s = int(p[0], 16) & ~1, int(p[1], 16)
        funcs.append((a, a + s, p[3]))
    if len(p) >= 3:
        names[p[-1]] = p[-2]
        addrs[p[-1]] = int(p[0], 16)
objs = {}
for ln in open(mapf):
    m = re.match(r"^\s+([0-9a-f]+)\s+[0-9a-f]+\s+[0-9a-f]+\s+\d+\s+\S*\((\S+\.obj)\):\(\.text\.(\S+)\)", ln)
    if m:
        objs[m.group(3)] = m.group(2)

# The loadable sections' bytes.
hdr = subprocess.run([bin_dir + "/llvm-readelf", "-S", "-W", elf], capture_output=True, text=True).stdout
data = open(elf, "rb").read()
bad = []
hits = []
for ln in hdr.splitlines():
    m = re.match(r"\s*\[\s*\d+\]\s+(\S+)\s+(PROGBITS)\s+([0-9a-f]+)\s+([0-9a-f]+)\s+([0-9a-f]+)\s+\S+\s+(\S*)", ln)
    if not m or "X" not in m.group(6) and "A" not in m.group(6):
        continue
    addr, off, size = int(m.group(3), 16), int(m.group(4), 16), int(m.group(5), 16)
    if addr >= 0x800000:
        continue
    for i in range(0, size - 3, 2):
        w = struct.unpack_from("<I", data, off + i)[0]
        if 0x804400 <= w < 0x804488:
            at = addr + i
            fn = next((f for f in funcs if f[0] <= at < f[1]), None)
            name = fn[2] if fn else "?"
            obj = objs.get(name, "?")
            hits.append((hex(at), hex(w), name, obj))
            if obj != "tc32_rng.c.obj":
                bad.append(hits[-1])
print("words in the RBG's register range:", hits or "none")

api = [n for n in names if re.match(r"(z_impl_)?sys_c?s?rand|.*entropy|random_entropy|xoshiro", n)]
# The linker's boundaries of an iterable section (the entropy driver API
# list exists in every image): harmless when the list is empty.
markers = [n for n in api if re.match(r"_\w+_list_(start|end)$|_\w+_ext_end$", n)]
for n in markers:
    base = re.sub(r"_(list_start|list_end|ext_end)$", "", n)
    if addrs.get(base + "_list_start") == addrs.get(base + "_list_end"):
        api.remove(n)
print("empty section markers:", markers or "none")
print("Zephyr random/entropy symbols:", api or "none")
callers = []
if api:
    dis = subprocess.run([bin_dir + "/llvm-objdump", "-d", "--no-show-raw-insn", elf],
                         capture_output=True, text=True).stdout
    cur = None
    for ln in dis.splitlines():
        m = re.match(r"^[0-9a-f]+ <(.+)>:$", ln)
        if m:
            cur = m.group(1)
            continue
        m = re.search(r"\tbl\t0x[0-9a-f]+ <([^>+]+)", ln)
        if m and m.group(1) in api:
            callers.append((cur, m.group(1)))
    print("callers:", sorted(set(callers)) or "none")
for b in bad:
    print("NOT tc32_rng:", b)
sys.exit(1 if bad or api else 0)

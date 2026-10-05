#!/bin/sh
# Build hello.c as a TC32 image with the sem start code: $OUT/hello.elf and hello.bin.
# Environment: TC32_LLVM, TC32_LLD (see toolchain.py), OUT (default <repo>/build/console).
# SPDX-License-Identifier: Apache-2.0
set -eu
HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/../.." && pwd)
tool() { python3 -c "import sys; sys.path.insert(0, sys.argv[1]); import toolchain; print(toolchain.tool(sys.argv[2]))" "$ROOT/common" "$1"; }
CLANG=$(tool clang)
LLD=$(tool ld.lld)
OUT=${OUT:-$ROOT/build/console}
mkdir -p "$OUT"
F="--target=thumbv4t-none-eabi -mcpu=arm7tdmi -mthumb -mfloat-abi=soft -ffreestanding -fno-builtin -fno-jump-tables -mllvm -arm-load-store-opt=false"
$CLANG $F -c "$ROOT/checks/sem/start_thumb.S" -o "$OUT/start.o"
$CLANG $F -Oz -c "$HERE/hello.c" -o "$OUT/hello.o"
$CLANG $F -c "$HERE/test_blob.S" -o "$OUT/test_blob.o"
$LLD -T "$ROOT/checks/sem/thumb.ld" "$OUT/start.o" "$OUT/hello.o" "$OUT/test_blob.o" -o "$OUT/hello.elf"
python3 -B "$ROOT/compiler/thumb2tc32.py" "$OUT/hello.elf" "$OUT/hello.bin" > /dev/null
# The test blob's manifest, and a wrong one.
python3 - "$OUT" <<'PYEOF'
import hashlib, json, sys
out = sys.argv[1]
blob = bytes([0x70, 0xb5, 0x04, 0x46, 0x0d, 0x46, 0x16, 0x46, 0x00, 0x20, 0x70, 0xbd, 0x00, 0xbf, 0x00, 0xbf])
json.dump({"blob_sha256": hashlib.sha256(blob).hexdigest(), "size": 16, "symbol": "test_blob"}, open(out + "/test_blob.json", "w"))
json.dump({"blob_sha256": hashlib.sha256(blob[:-1] + b"\x01").hexdigest(), "size": 16, "symbol": "test_blob"},
          open(out + "/test_blob_wrong.json", "w"))
PYEOF
echo "built $OUT/hello.elf and hello.bin (with the test blob)"

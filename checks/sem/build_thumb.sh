#!/bin/sh
# Build the semantics test (sem.c) with a mainstream clang for ARMv4T Thumb,
# link it with ld.lld and this repository's runtime helpers
# (compiler/runtime), and re-encode the result as TC32 with thumb2tc32.py:
# $OUT/thumb_{O0,O2,Os,Oz}.elf and .bin, plus the host build's results in
# $OUT/host.txt, for sem_check.py.
#
# Environment: TC32_LLVM, TC32_LLD (where toolchain.py looks for clang and
# ld.lld), OUT (default <repo>/build/sem),
# DIVIDER=1 to build the helpers for the TLSR8278 hardware divider.
# SPDX-License-Identifier: Apache-2.0
set -eu
HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/../.." && pwd)
tool() { python3 -c "import sys; sys.path.insert(0, sys.argv[1]); import toolchain; print(toolchain.tool(sys.argv[2]))" "$ROOT" "$1"; }
CLANG=$(tool clang)
LLD=$(tool ld.lld)
OUT=${OUT:-$ROOT/build/sem}
RT=$ROOT/compiler/runtime
mkdir -p "$OUT"
# The code generation flags a firmware built this way must use (see README).
F="--target=thumbv4t-none-eabi -mcpu=arm7tdmi -mthumb -mfloat-abi=soft -ffreestanding -fno-builtin -fno-jump-tables -mllvm -arm-load-store-opt=false"
DIV=""
[ "${DIVIDER:-0}" = 1 ] && DIV="-DTC32_TLSR8278_DIVIDER=1"
$CLANG $F -c "$HERE/start_thumb.S" -o "$OUT/start.o"
$CLANG $F -c "$RT/aeabi_thumb.S" -o "$OUT/aeabi_thumb.o"
$CLANG $F -Oz $DIV -c "$RT/compiler_builtins.c" -o "$OUT/builtins.o"
$CLANG $F -O1 -c "$HERE/mem.c" -o "$OUT/mem.o"
for O in O0 O2 Os Oz; do
	$CLANG $F -$O -c "$HERE/sem.c" -o "$OUT/sem_$O.o"
	$LLD -T "$HERE/thumb.ld" "$OUT/start.o" "$OUT/sem_$O.o" "$OUT/builtins.o" "$OUT/aeabi_thumb.o" "$OUT/mem.o" -o "$OUT/thumb_$O.elf"
	python3 -B "$ROOT/compiler/thumb2tc32.py" "$OUT/thumb_$O.elf" "$OUT/thumb_$O.bin"
done
cc -DSEM_HOST -O2 -o "$OUT/sem_host" "$HERE/sem.c"
"$OUT/sem_host" > "$OUT/host.txt"
echo "built $OUT/thumb_{O0,O2,Os,Oz}.{elf,bin} and host.txt"

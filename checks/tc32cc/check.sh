#!/bin/sh
# tc32-cc (go/bin/tc32-cc) against what it stands for:
# - its built-in copies of compiler/runtime and compiler/vendor_forms.txt are
#   those files;
# - its images of the semantics test (checks/sem) equal, byte for byte, the
#   images of the same steps run by hand (clang, ld.lld, thumb2tc32.py), at
#   -O0, -O2, -Os and -Oz;
# - those images, one with the start file in Telink's syntax (-x tc32-asm)
#   and one with the helpers for the hardware divider, give the host's
#   results in the Go emulator;
# - options that would change the code generation are refused (status 2),
#   and an image with a form Telink's code never uses is refused (status 1)
#   and removed, its ELF kept.
# Needs build/sem_div0/host.txt and go/bin/tc32emu-run (run_checks.sh makes
# them first). Exit status 0 when everything holds.
# SPDX-License-Identifier: Apache-2.0
set -u
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
CC=$ROOT/go/bin/tc32-cc
S=$ROOT/checks/sem
OUT=$ROOT/build/tc32cc
tool() { python3 -c "import sys; sys.path.insert(0, sys.argv[1]); import toolchain; print(toolchain.tool(sys.argv[2]))" "$ROOT/common" "$1"; }
CLANG=$(tool clang)
LLD=$(tool ld.lld)
NM=$(tool llvm-nm)
F=$("$CC" --print-flags)
rc=0
rm -rf "$OUT"
mkdir -p "$OUT/cc" "$OUT/hand"

for pair in compiler/runtime/compiler_builtins.c:go/tc32cc/embed/compiler_builtins.c \
	compiler/runtime/aeabi_thumb.S:go/tc32cc/embed/aeabi_thumb.S \
	compiler/vendor_forms.txt:go/tc32cc/embed/vendor_forms.txt; do
	if cmp -s "$ROOT/${pair%%:*}" "$ROOT/${pair#*:}"; then echo "${pair#*:}: equals ${pair%%:*}"
	else echo "${pair#*:}: DIFFERS from ${pair%%:*}"; rc=1; fi
done

# The semantics test through tc32-cc, and the same steps by hand in the same order.
(cd "$OUT/cc" && "$CC" -c -O1 -ffreestanding -fno-builtin "$S/mem.c" -o mem.o) || rc=1
(cd "$OUT/hand" &&
	$CLANG $F -ffreestanding -fno-builtin -Oz -c "$ROOT/compiler/runtime/compiler_builtins.c" -o builtins.o &&
	$CLANG $F -c "$ROOT/compiler/runtime/aeabi_thumb.S" -o aeabi.o &&
	$CLANG $F -O1 -ffreestanding -fno-builtin -c "$S/mem.c" -o mem.o) || rc=1
for O in O0 O2 Os Oz; do
	(cd "$OUT/cc" && "$CC" -$O -ffreestanding -fno-builtin "$S/start_thumb.S" "$S/sem.c" mem.o -T "$S/thumb.ld" \
		-o thumb_$O.bin > report_$O.txt) || rc=1
	tail -1 "$OUT/cc/report_$O.txt"
	(cd "$OUT/hand" &&
		$CLANG $F -$O -ffreestanding -fno-builtin -c "$S/start_thumb.S" -o start.o &&
		$CLANG $F -$O -ffreestanding -fno-builtin -c "$S/sem.c" -o sem.o &&
		$LLD -T "$S/thumb.ld" start.o sem.o mem.o builtins.o aeabi.o -o thumb_$O.elf &&
		python3 -B "$ROOT/compiler/thumb2tc32.py" thumb_$O.elf thumb_$O.bin > /dev/null) || rc=1
	if cmp -s "$OUT/cc/thumb_$O.bin" "$OUT/hand/thumb_$O.bin"; then echo "-$O: the tc32-cc image equals the image of the steps by hand"
	else echo "-$O: the tc32-cc image DIFFERS from the image of the steps by hand"; rc=1; fi
done
(cd "$OUT/cc" && "$CC" -O2 -ffreestanding -fno-builtin -x tc32-asm "$S/start.S" -x none "$S/sem.c" mem.o -T "$S/thumb.ld" \
	-o tc32asm_O2.bin > /dev/null) || rc=1
(cd "$OUT/cc" && "$CC" -O2 -ffreestanding -fno-builtin --tlsr8278-divider "$S/start_thumb.S" "$S/sem.c" mem.o -T "$S/thumb.ld" \
	-o divider_O2.bin > /dev/null) || rc=1
python3 -B "$ROOT/checks/sem_check.py" "$NM" "$ROOT/build/sem_div0/host.txt" "$OUT"/cc/thumb_O0.elf "$OUT"/cc/thumb_O2.elf \
	"$OUT"/cc/thumb_Os.elf "$OUT"/cc/thumb_Oz.elf "$OUT"/cc/tc32asm_O2.elf "$OUT"/cc/divider_O2.elf --engine go || rc=1

# Refusals.
for opt in -marm -mcpu=cortex-m0 --target=armv7-none-eabi -mfloat-abi=hard -fjump-tables -flto; do
	st=0; "$CC" "$opt" -c "$S/mem.c" -o "$OUT/refused.o" 2> "$OUT/refused.txt" || st=$?
	if [ $st = 2 ] && [ ! -e "$OUT/refused.o" ]; then echo "$opt: refused ($(cat "$OUT/refused.txt"))"
	else echo "$opt: NOT refused (status $st)"; rc=1; fi
done
st=0; (cd "$OUT/cc" && "$CC" "$ROOT/checks/tc32cc/bad_form.S" --no-runtime -T "$S/thumb.ld" -o bad.bin > bad.txt 2>&1) || st=$?
if [ $st = 1 ] && [ ! -e "$OUT/cc/bad.bin" ] && [ -e "$OUT/cc/bad.elf" ] && grep -q "ldm base-in-list" "$OUT/cc/bad.txt"; then
	echo "bad_form.S: the forms check refuses it (ldm base-in-list); the image is removed, the ELF kept"
else echo "bad_form.S: NOT refused as it should be (status $st)"; rc=1; fi
exit $rc

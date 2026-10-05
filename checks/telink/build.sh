#!/bin/sh
# Build rot.c with Telink's tc32-elf-gcc 4.5.1 (x86-64 Linux binaries from Telink's IDE, run in Docker)
# at -O0, -O2 and with the SDK's flags, and disassemble the objects with Telink's objdump:
# out/rot_{O0,O2,sdk}.o and .dis. Then the objects go into the evidence table:
#   python3 compiler/forms_check.py evidence --telink-dis /dev/null --base compiler/vendor_forms.txt \
#       --elf checks/telink/out/rot_O0.o checks/telink/out/rot_O2.o checks/telink/out/rot_sdk.o > vendor_forms.new
# Environment: TELINK_TC32 (the toolchain directory, with bin/tc32-elf-gcc), TELINK_IMAGE (default debian:bookworm-slim).
# SPDX-License-Identifier: Apache-2.0
set -eu
HERE=$(cd "$(dirname "$0")" && pwd)
T=${TELINK_TC32:?set TELINK_TC32 to Telink's toolchain directory}
T=$(cd "$T" && pwd)
mkdir -p "$HERE/out"
docker run --rm --platform linux/amd64 --network none -v "$T:$T" -v "$HERE:$HERE" "${TELINK_IMAGE:-debian:bookworm-slim}" sh -c "
  cd $HERE && G=\"$T/bin/tc32-elf-gcc -B$T/bin/tc32-elf- -ffreestanding -std=gnu99\" &&
  \$G -O0 -c rot.c -o out/rot_O0.o && $T/bin/tc32-elf-objdump -d out/rot_O0.o > out/rot_O0.dis &&
  \$G -O2 -c rot.c -o out/rot_O2.o && $T/bin/tc32-elf-objdump -d out/rot_O2.o > out/rot_O2.dis &&
  \$G -O2 -fshort-enums -finline-small-functions -fshort-wchar -fms-extensions -c rot.c -o out/rot_sdk.o &&
  $T/bin/tc32-elf-objdump -d out/rot_sdk.o > out/rot_sdk.dis"
for f in O0 O2 sdk; do echo "rot_$f: trotrs at $(grep -c trotr "$HERE/out/rot_$f.dis") sites"; done

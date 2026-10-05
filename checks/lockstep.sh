#!/bin/sh
# Lockstep comparison of the Go emulator with the Python one: both run the
# same image for the same number of instructions and print emulator/trace.py's
# trace; the two streams must be identical (cmp). On a difference the first
# differing line of each side is shown.
#
# Usage: checks/lockstep.sh <image.bin> [cycles] [--slot 0x0] [--cpi N] [--usb]
# Environment: none beyond Python 3 and Go (go/bin/tc32emu-trace is built here).
# SPDX-License-Identifier: Apache-2.0
set -u
ROOT=$(cd "$(dirname "$0")/.." && pwd)
img=${1:?image}; shift
steps=1000000
case "${1:-}" in ''|-*) ;; *) steps=$1; shift ;; esac
(cd "$ROOT/go" && mkdir -p bin && go build -o bin/tc32emu-trace ./cmd/tc32emu-trace) || exit 1
tmp=$(mktemp -d)
python3 -B "$ROOT/emulator/trace.py" "$img" --cycles "$steps" "$@" > "$tmp/py.txt" 2> "$tmp/py.err" &
"$ROOT/go/bin/tc32emu-trace" "$img" --cycles "$steps" "$@" > "$tmp/go.txt" 2> "$tmp/go.err"
wait
py_lines=$(wc -l < "$tmp/py.txt" | tr -d ' ')
go_lines=$(wc -l < "$tmp/go.txt" | tr -d ' ')
if cmp -s "$tmp/py.txt" "$tmp/go.txt"; then
	echo "$(basename "$img"): $steps steps, $py_lines trace lines equal; $(tail -1 "$tmp/py.txt")"
	rm -rf "$tmp"
	exit 0
fi
first=$(cmp "$tmp/py.txt" "$tmp/go.txt" 2>&1 | sed -n 's/.*line \([0-9]*\).*/\1/p')
[ -z "$first" ] && first=$(( (py_lines < go_lines ? py_lines : go_lines) + 1 ))
echo "$(basename "$img"): DIFFERENT at trace line $first (Python $py_lines lines, Go $go_lines lines)"
echo "  python: $(sed -n "${first}p" "$tmp/py.txt")"
echo "  go:     $(sed -n "${first}p" "$tmp/go.txt")"
[ -s "$tmp/py.err" ] && echo "  python stderr: $(tail -1 "$tmp/py.err")"
[ -s "$tmp/go.err" ] && echo "  go stderr: $(tail -1 "$tmp/go.err")"
echo "  traces kept in $tmp"
exit 1

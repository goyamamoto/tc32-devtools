#!/bin/sh
# The checks that need only a mainstream clang, ld.lld, Python 3 and (for the
# last step) Csmith: build the semantics test both ways the helpers can be
# built, run it in tc32emu against the host, check the written images against
# their ELFs and their instruction forms against Telink's use, and run a
# small differential test. Exit status 0 when everything passes.
#
# Environment: TC32_LLVM, TC32_LLD (see toolchain.py), SEEDS (default 1-20),
# JOBS (default 4). The checks that need Telink's tools are not here:
# checks/isa_check.py, compiler/asm_check.py.
# SPDX-License-Identifier: Apache-2.0
set -eu
ROOT=$(cd "$(dirname "$0")/.." && pwd)
NM=$(python3 -c "import sys; sys.path.insert(0, '$ROOT'); import toolchain; print(toolchain.tool('llvm-nm'))")
rc=0
for div in 0 1; do
	OUT=$ROOT/build/sem_div$div
	echo "== sem, helpers with DIVIDER=$div"
	DIVIDER=$div OUT=$OUT sh "$ROOT/checks/sem/build_thumb.sh"
	python3 -B "$ROOT/checks/sem_check.py" "$NM" "$OUT/host.txt" "$OUT"/thumb_O0.elf "$OUT"/thumb_O2.elf "$OUT"/thumb_Os.elf "$OUT"/thumb_Oz.elf || rc=1
	for O in O0 O2 Os Oz; do
		python3 -B "$ROOT/compiler/image_check.py" "$OUT/thumb_$O.elf" "$OUT/thumb_$O.bin" || rc=1
		# Every level, -O0 included: its rotate (alu ror) is a form Telink's gcc emits too (checks/telink).
		out=$(python3 -B "$ROOT/compiler/forms_check.py" check "$OUT/thumb_$O.elf" --thumb) || rc=1
		echo "$out" | tail -1
	done
done
if command -v go >/dev/null 2>&1; then
	echo "== Go: build, unit tests, and the same images as the Python tools"
	(cd "$ROOT/go" && go vet ./... && go test ./... && mkdir -p bin && go build -o bin/thumb2tc32 ./cmd/thumb2tc32 && go build -o bin/image-check ./cmd/image-check) || rc=1
	for elf in "$ROOT"/build/sem_div0/thumb_*.elf "$ROOT"/build/sem_div1/thumb_*.elf; do
		gobin="${elf%.elf}.go.bin"
		"$ROOT/go/bin/thumb2tc32" "$elf" "$gobin" > /dev/null || rc=1
		if cmp -s "${elf%.elf}.bin" "$gobin"; then echo "$(basename "$elf"): Go image equals the Python image"
		else echo "$(basename "$elf"): Go image DIFFERS from the Python image"; rc=1; fi
		out=$("$ROOT/go/bin/image-check" "$elf" "$gobin") || rc=1
		echo "$out" | tail -1
	done
	echo "== Go tc32asm2thumb against the Python one on checks/asm/sample.S"
	(cd "$ROOT/go" && go build -o bin/tc32asm2thumb ./cmd/tc32asm2thumb) || rc=1
	python3 -B "$ROOT/compiler/tc32asm2thumb.py" "$ROOT/checks/asm/sample.S" "$ROOT/build/sample.py.S" || rc=1
	"$ROOT/go/bin/tc32asm2thumb" "$ROOT/checks/asm/sample.S" "$ROOT/build/sample.go.S" || rc=1
	if cmp -s "$ROOT/build/sample.py.S" "$ROOT/build/sample.go.S"; then echo "sample.S: Go output equals the Python output"
	else echo "sample.S: Go output DIFFERS from the Python output"; rc=1; fi
	echo "== Go forms-check against the Python one (report and exit status)"
	(cd "$ROOT/go" && go build -o bin/forms-check ./cmd/forms-check) || rc=1
	for elf in "$ROOT"/build/sem_div0/thumb_O0.elf "$ROOT"/build/sem_div1/thumb_Oz.elf; do
		# Compare the exit status too, whatever it is (a form without vendor use gives 1 on both).
		prc=0; python3 -B "$ROOT/compiler/forms_check.py" check "$elf" --thumb > "$ROOT/build/forms.py.txt" || prc=$?
		grc=0; "$ROOT/go/bin/forms-check" check "$elf" --thumb --evidence "$ROOT/compiler/vendor_forms.txt" > "$ROOT/build/forms.go.txt" || grc=$?
		if cmp -s "$ROOT/build/forms.py.txt" "$ROOT/build/forms.go.txt" && [ "$prc" = "$grc" ]; then
			echo "$(basename "$elf"): Go report equals the Python report (exit $prc)"
		else echo "$(basename "$elf"): Go report DIFFERS from the Python report (exit py=$prc go=$grc)"; rc=1; fi
	done
	echo "== sem through the Go emulator (sem_check.py --engine go, go/bin/tc32emu-run)"
	(cd "$ROOT/go" && go build -o bin/tc32emu-run ./cmd/tc32emu-run) || rc=1
	for div in 0 1; do
		OUT=$ROOT/build/sem_div$div
		python3 -B "$ROOT/checks/sem_check.py" "$NM" "$OUT/host.txt" "$OUT"/thumb_O0.elf "$OUT"/thumb_O2.elf "$OUT"/thumb_Os.elf "$OUT"/thumb_Oz.elf --engine go || rc=1
	done
	echo "== tc32-cc: its built-in files, its images against the steps by hand, sem through it, refusals"
	(cd "$ROOT/go" && go build -o bin/tc32-cc ./cmd/tc32-cc) || rc=1
	sh "$ROOT/checks/tc32cc/check.sh" || rc=1
	echo "== Go run-boot against run_boot.py (the boot driver: hooks, events, idle skipping, the summary)"
	(cd "$ROOT/go" && go build -o bin/run-boot ./cmd/run-boot) || rc=1
	elf=$ROOT/build/sem_div1/thumb_Oz.elf
	prc=0; python3 -B "$ROOT/emulator/run_boot.py" --elf "$elf" --bin "${elf%.elf}.bin" --ms 20 > "$ROOT/build/runboot.py.txt" 2>&1 || prc=$?
	grc=0; "$ROOT/go/bin/run-boot" --elf "$elf" --bin "${elf%.elf}.bin" --ms 20 > "$ROOT/build/runboot.go.txt" 2>&1 || grc=$?
	if cmp -s "$ROOT/build/runboot.py.txt" "$ROOT/build/runboot.go.txt" && [ "$prc" = "$grc" ]; then
		echo "run-boot: Go output equals the Python output (exit $prc)"
	else echo "run-boot: Go output DIFFERS from the Python output (exit py=$prc go=$grc)"; rc=1; fi
	prc=0; python3 -B "$ROOT/emulator/run_boot.py" --elf "$elf" --bin "${elf%.elf}.bin" --ms 20 --reg-audit > "$ROOT/build/runboot_audit.py.txt" 2>&1 || prc=$?
	grc=0; "$ROOT/go/bin/run-boot" --elf "$elf" --bin "${elf%.elf}.bin" --ms 20 --reg-audit --tables "$ROOT/emulator" > "$ROOT/build/runboot_audit.go.txt" 2>&1 || grc=$?
	if cmp -s "$ROOT/build/runboot_audit.py.txt" "$ROOT/build/runboot_audit.go.txt" && [ "$prc" = "$grc" ]; then
		echo "run-boot --reg-audit: Go output equals the Python output (reg_audit.py rows)"
	else echo "run-boot --reg-audit: Go output DIFFERS from the Python output"; rc=1; fi
	echo "== the emulator console (register 0xfff0): run_boot.py --console and run-boot --console on checks/console/hello.c"
	OUT=$ROOT/build/console sh "$ROOT/checks/console/build.sh" > /dev/null || rc=1
	python3 -B "$ROOT/emulator/run_boot.py" --elf "$ROOT/build/console/hello.elf" --bin "$ROOT/build/console/hello.bin" --ms 1 --console --stop-at _exit > "$ROOT/build/console/py.txt" 2>&1 || rc=1
	"$ROOT/go/bin/run-boot" --elf "$ROOT/build/console/hello.elf" --bin "$ROOT/build/console/hello.bin" --ms 1 --console --stop-at _exit > "$ROOT/build/console/go.txt" 2>&1 || rc=1
	if cmp -s "$ROOT/build/console/py.txt" "$ROOT/build/console/go.txt" && grep -q "^hello from tc32$" "$ROOT/build/console/go.txt" \
	   && grep -q "^stop-at _exit: reached at" "$ROOT/build/console/go.txt"; then
		echo "console: both collect the same text and stop at _exit ($(grep -c . "$ROOT/build/console/go.txt") lines)"
	else echo "console: DIFFERS or the text is missing"; rc=1; fi
	python3 -B "$ROOT/emulator/run_boot.py" --elf "$ROOT/build/console/hello.elf" --bin "$ROOT/build/console/hello.bin" --ms 1 --console --stop-line "from tc32" > "$ROOT/build/console/py_line.txt" 2>&1 || rc=1
	"$ROOT/go/bin/run-boot" --elf "$ROOT/build/console/hello.elf" --bin "$ROOT/build/console/hello.bin" --ms 1 --console --stop-line "from tc32" > "$ROOT/build/console/go_line.txt" 2>&1 || rc=1
	if cmp -s "$ROOT/build/console/py_line.txt" "$ROOT/build/console/go_line.txt" && grep -q "^stop-line: 'from tc32' at" "$ROOT/build/console/go_line.txt" \
	   && ! grep -q "hid_listener" "$ROOT/build/console/go_line.txt"; then
		echo "console: both stop on the first line with --stop-line, before the second"
	else echo "console: --stop-line DIFFERS or did not stop"; rc=1; fi
	echo "== a blob in an image: image_check --blob on the console image's test blob, both engines"
	(cd "$ROOT/go" && go build -o bin/image-check ./cmd/image-check) || rc=1
	C=$ROOT/build/console
	python3 -B "$ROOT/compiler/image_check.py" "$C/hello.elf" "$C/hello.bin" --blob "$C/test_blob.json" > "$C/blob_py.txt" 2>&1 || rc=1
	"$ROOT/go/bin/image-check" "$C/hello.elf" "$C/hello.bin" --blob "$C/test_blob.json" > "$C/blob_go.txt" 2>&1 || rc=1
	nrc=0; python3 -B "$ROOT/compiler/image_check.py" "$C/hello.elf" "$C/hello.bin" --blob "$C/test_blob_wrong.json" > "$C/blobw_py.txt" 2>&1 || nrc=$?
	grc=0; "$ROOT/go/bin/image-check" "$C/hello.elf" "$C/hello.bin" --blob "$C/test_blob_wrong.json" > "$C/blobw_go.txt" 2>&1 || grc=$?
	if cmp -s "$C/blob_py.txt" "$C/blob_go.txt" && grep -q "0 mismatch(es).*; blob 16 B at" "$C/blob_go.txt" \
	   && [ "$nrc" = 1 ] && [ "$grc" = 1 ] && cmp -s "$C/blobw_py.txt" "$C/blobw_go.txt" && grep -q "blob: image bytes sha256" "$C/blobw_go.txt"; then
		echo "blob rule: both engines accept the test blob and reject the wrong manifest with the same report"
	else echo "blob rule: DIFFERS or the negative control passed"; rc=1; fi
	echo "== the startup in an image: image_check --startup, both engines (TC32_STARTUP_DIR: zmk.elf, zmk.bin, tlsr8278_startup.json and tlsr_spi_flash_io.json of a TC32 ZMK build)"
	D=${TC32_STARTUP_DIR:-}; SM=$D/tlsr8278_startup.json; BM=$D/tlsr_spi_flash_io.json
	if [ -n "$D" ] && [ -f "$SM" ]; then
		mkdir -p "$ROOT/build/ss"
		python3 - "$SM" "$ROOT/build/ss/hdr.json" <<'PYEOF'
import json, sys
m = json.load(open(sys.argv[1])); m["header_copy_word"] = 0x00880150; json.dump(m, open(sys.argv[2], "w"))
PYEOF
		ssok=1
		for man in "$SM" "$ROOT/build/ss/hdr.json"; do
			prc=0; python3 -B "$ROOT/compiler/image_check.py" "$D/zmk.elf" "$D/zmk.bin" --blob "$BM" --startup "$man" > "$ROOT/build/ss/py.txt" 2>&1 || prc=$?
			grc=0; "$ROOT/go/bin/image-check" "$D/zmk.elf" "$D/zmk.bin" --blob "$BM" --startup "$man" > "$ROOT/build/ss/go.txt" 2>&1 || grc=$?
			cmp -s "$ROOT/build/ss/py.txt" "$ROOT/build/ss/go.txt" && [ "$prc" = "$grc" ] || ssok=0
		done
		if [ $ssok = 1 ] && [ "$grc" = 1 ]; then
			echo "startup rule: both engines give the same report on the image and reject a wrong header word the same way"
		else echo "startup rule: DIFFERS between the engines"; rc=1; fi
	else
		echo "startup rule: skipped (TC32_STARTUP_DIR not set)"
	fi
	echo "== Go tc32-flow against tc32_flow.py on the sem images"
	(cd "$ROOT/go" && go build -o bin/tc32-flow ./cmd/tc32-flow) || rc=1
	python3 -B "$ROOT/compiler/tc32_flow.py" "$ROOT"/build/sem_div0/thumb_*.bin "$ROOT"/build/sem_div1/thumb_*.bin --forms-out "$ROOT/build/flow.py.tsv" > "$ROOT/build/flow.py.txt" || rc=1
	"$ROOT/go/bin/tc32-flow" "$ROOT"/build/sem_div0/thumb_*.bin "$ROOT"/build/sem_div1/thumb_*.bin --forms-out "$ROOT/build/flow.go.tsv" > "$ROOT/build/flow.go.txt" || rc=1
	if cmp -s "$ROOT/build/flow.py.txt" "$ROOT/build/flow.go.txt" && cmp -s "$ROOT/build/flow.py.tsv" "$ROOT/build/flow.go.tsv"; then
		echo "tc32-flow: Go table and forms equal the Python ones on 8 images"
	else echo "tc32-flow: Go output DIFFERS from the Python output"; rc=1; fi
	echo "== Go emulator in lockstep with the Python one (emulator/trace.py)"
	for img in "$ROOT"/build/sem_div0/thumb_Oz.bin "$ROOT"/build/sem_div1/thumb_O0.bin; do
		out=$(sh "$ROOT/checks/lockstep.sh" "$img" "${LOCKSTEP_STEPS:-1000000}") || rc=1
		echo "$out" | cut -c1-120
	done
	echo "== the same in lockstep with the flash cache model (TC32EMU_ICACHE_MISS=288)"
	out=$(TC32EMU_ICACHE_MISS=288 sh "$ROOT/checks/lockstep.sh" "$ROOT"/build/sem_div1/thumb_O0.bin "${LOCKSTEP_STEPS:-1000000}") || rc=1
	echo "$out" | cut -c1-120
	echo "== the same in lockstep with the SRAM filled at power-on (TC32EMU_SRAM_SEED=0x2a), and sem through both engines with it"
	out=$(TC32EMU_SRAM_SEED=0x2a sh "$ROOT/checks/lockstep.sh" "$ROOT"/build/sem_div1/thumb_O0.bin "${LOCKSTEP_STEPS:-1000000}") || rc=1
	echo "$out" | cut -c1-120
	TC32EMU_SRAM_SEED=0x2a python3 -B "$ROOT/checks/sem_check.py" "$NM" "$ROOT/build/sem_div1/host.txt" "$ROOT"/build/sem_div1/thumb_O2.elf --engine go || rc=1
	TC32EMU_SRAM_SEED=0x2a python3 -B "$ROOT/checks/sem_check.py" "$NM" "$ROOT/build/sem_div1/host.txt" "$ROOT"/build/sem_div1/thumb_Oz.elf || rc=1
else
	echo "== Go skipped: go not installed"
fi
echo "== BLE models: known answers (AES, CCM, SMP, LL encryption, radio, channel selection)"
out=$(python3 -B "$ROOT/checks/ble_models_check.py") || rc=1
echo "$out" | tail -1
echo "== The air model: lost packets, the central's clock, jitter, a connection not established"
out=$(python3 -B "$ROOT/checks/ble_air_check.py") || rc=1
echo "$out" | tail -1
out=$(python3 -B "$ROOT/checks/ble_air_mutants.py") || rc=1
echo "$out" | tail -1
echo "== GPIO input reads from the pads, the floating hold, the input-enable mask, the register alias"
out=$(python3 -B "$ROOT/checks/gpio_check.py") || rc=1
echo "$out" | tail -1
echo "== flash_size_check.py: the flash as a 512 KB part (TC32EMU_FLASH_SIZE), and as 1 MB without it"
out=$(python3 -B "$ROOT/checks/flash_size_check.py") || rc=1
printf '%s\n' "$out" | tail -1
echo "== flash_lock_check.py: the status register and block protection per the SDK's tables"
out=$(python3 -B "$ROOT/checks/flash_lock_check.py") || rc=1
printf '%s\n' "$out" | tail -1
echo "== power_on_check.py: the analog registers a reset keeps (0x3a-0x3c) at power-on"
out=$(python3 -B "$ROOT/checks/power_on_check.py") || rc=1
printf '%s\n' "$out" | tail -1
echo "== timer_capture_check.py: Timer0/1 match on equality; a capture below the count (TC32EMU_TIMER_CAPTURE_BELOW)"
out=$(python3 -B "$ROOT/checks/timer_capture_check.py") || rc=1
printf '%s\n' "$out" | tail -1
echo "== idle_skip_check.py: the idle skip only while an interrupt could be taken"
out=$(python3 -B "$ROOT/checks/idle_skip_check.py") || rc=1
printf '%s\n' "$out" | tail -1
echo "== flash_busy_check.py: the optional flash busy time (TC32EMU_FLASH_TPP_US, _TSE_US, _TBE32_US, _TBE64_US)"
out=$(python3 -B "$ROOT/checks/flash_busy_check.py") || rc=1
printf '%s\n' "$out" | tail -1
echo "== xip_slot_check.py: what an XIP address reads when booted from slot B"
out=$(python3 -B "$ROOT/checks/xip_slot_check.py") || rc=1
printf '%s\n' "$out" | tail -1
echo "== pad_rc_check.py: the optional pad capacitance model (TC32EMU_PAD_C_PF)"
out=$(python3 -B "$ROOT/checks/pad_rc_check.py") || rc=1
printf '%s\n' "$out" | tail -1
echo "== icache_check.py: the optional flash cache model (TC32EMU_ICACHE_MISS)"
out=$(python3 -B "$ROOT/checks/icache_check.py") || rc=1
printf '%s\n' "$out" | tail -1
echo "== usb_core_wake_check.py: the USB core wake as a level or an edge (TC32EMU_USB_CORE_WAKE)"
out=$(python3 -B "$ROOT/checks/usb_core_wake_check.py") || rc=1
printf '%s\n' "$out" | tail -1
echo "== sram_fill_check.py: the SRAM's contents at power-on (TC32EMU_SRAM_SEED), both engines"
out=$(python3 -B "$ROOT/checks/sram_fill_check.py") || rc=1
printf '%s\n' "$out" | tail -1
echo "== timer_wake_check.py: the 32 kHz timer wake from suspend comes at its tick"
out=$(python3 -B "$ROOT/checks/timer_wake_check.py") || rc=1
printf '%s\n' "$out" | tail -1
echo "== symbolize_check.py: symbols given at construction and assigned afterwards"
out=$(python3 -B "$ROOT/checks/symbolize_check.py") || rc=1
printf '%s\n' "$out" | tail -1
echo "== the register cases of the Python emulator all have a Go case, and the other way round"
python3 -B "$ROOT/checks/reg_cases_check.py" || rc=1
echo "== the ISA table is one object for the compiler and the emulator"
python3 -B -c "import sys; sys.path[:0] = ['$ROOT', '$ROOT/emulator']; import tc32isa, tc32emu; assert tc32emu.TOP is tc32isa.TOP; assert sorted(tc32isa.TOP) == list(range(32)); print('ok')" || rc=1
if command -v csmith >/dev/null 2>&1; then
	echo "== ccdiff, Csmith seeds ${SEEDS:-1-20}"
	python3 -B "$ROOT/checks/ccdiff/ccdiff.py" --compiler thumb --seeds "${SEEDS:-1-20}" --jobs "${JOBS:-4}" --levels O2,Oz,zmk --hw-divider > "$ROOT/build/ccdiff.py.txt" || rc=1
	grep summary "$ROOT/build/ccdiff.py.txt"
	echo "== the same seeds through the Go emulator (--engine go): the per-seed outcomes must be equal"
	python3 -B "$ROOT/checks/ccdiff/ccdiff.py" --compiler thumb --seeds "${SEEDS:-1-20}" --jobs "${JOBS:-4}" --levels O2,Oz,zmk --hw-divider --engine go > "$ROOT/build/ccdiff.go.txt" || rc=1
	grep summary "$ROOT/build/ccdiff.go.txt"
	grep "^seed" "$ROOT/build/ccdiff.py.txt" | sort > "$ROOT/build/ccdiff.py.seeds"
	grep "^seed" "$ROOT/build/ccdiff.go.txt" | sort > "$ROOT/build/ccdiff.go.seeds"
	if cmp -s "$ROOT/build/ccdiff.py.seeds" "$ROOT/build/ccdiff.go.seeds"; then
		echo "ccdiff: Go and Python engines give the same outcome for every seed and level"
	else echo "ccdiff: Go and Python engines DIFFER"; rc=1; fi
else
	echo "== ccdiff skipped: csmith not installed"
fi
echo "run_checks: $([ $rc -eq 0 ] && echo PASS || echo FAIL)"
exit $rc

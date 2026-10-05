# tc32-devtools

Open tools to build, check and emulate firmware for Telink's TC32 CPU (TLSR825x, TLSR827x), without Telink's closed toolchain.

- **A compiler path built on a mainstream clang.** TC32 runs the ARMv4T Thumb-1 instruction set with the top five bits of every instruction assigned differently. clang and ld.lld build for Thumb-1; `thumb2tc32.py` re-encodes the linked image for TC32, and every byte and every instruction form of the result is checked.
- **`tc32-cc`, the compiler path as one command.** A single Go binary runs clang and ld.lld with the fixed flags, re-encodes, and checks the image, with the runtime helpers and the evidence table built in. It needs an LLVM install and nothing else.
- **An emulator of TC32 and the TLSR8278**, `tc32emu`. It boots whole images from reset with the chip's timers, interrupt controller, flash, GPIO, USB device controller, suspend, and a radio model for Bluetooth LE and Telink's 2.4 GHz format. Each model states its source, and behaviour that no document states is an explicit option, never a silent guess.
- **The evidence** that both are right: checks against Telink's own disassembler, assembler and compiler, differential tests against the host, and Zephyr's and ZMK's own test suites run on TC32.
- **A Go port** of the build tools and the emulator, as single static binaries, held to the Python tools byte for byte and instruction for instruction.

Firmware built this way runs in daily use: the ZMK ports for three TLSR8278 keyboards (Cidoo V21, V75 Pro and V65 V3) are built with this compiler path, and their checks run in this emulator.

## Contents

| Path | What it holds |
|---|---|
| `common/` | What the compiler tools, the emulator and the checks share: `tc32isa.py`, the TC32 ↔ Thumb opcode mapping (one table), and `toolchain.py`, where the LLVM tools are found (`TC32_LLVM`, `TC32_LLD`) |
| `compiler/` | `thumb2tc32.py`, `tc32asm2thumb.py`, the checks of a written image (`image_check.py`, `forms_check.py`, `asm_check.py`), `tc32_flow.py`, and `runtime/` (the helpers clang calls) |
| `emulator/` | `tc32emu.py` (CPU and TLSR8278 peripherals), `usb_model.py` (USB device controller and a host), `ble_radio.py`, `ble_central.py`, `ble_air.py`, `aes128.py` (radio, a scripted central, the air between them), `run_boot.py`, `stack_use.py`, `reg_audit.py`, `trace.py` |
| `checks/` | `run_checks.sh` and the checks it runs; `isa_check.py`, `sem/`, `ccdiff/`, `telink/`, and the Zephyr and ZMK test drivers |
| `go/` | The Go port (see [Go port](#go-port)) |

## Requirements

- clang and ld.lld from LLVM 16 or later. The evidence was made with Homebrew LLVM 23.1.2 and lld 23.1.2. `common/toolchain.py` looks in `TC32_LLVM` and `TC32_LLD`, then in Homebrew's paths, then on `PATH`.
- Python 3, with no packages beyond the standard library.
- Go 1.22 or later, for the Go port (optional).
- Csmith, for the differential test (optional; `run_checks.sh` skips that step without it).

Telink's closed tools are never needed for a build. Only two checks use them (`isa_check.py`, `asm_check.py`).

## Quick start

```
cd go && go build -o bin/tc32-cc ./cmd/tc32-cc && cd ..
go/bin/tc32-cc -O2 -ffreestanding start.S app.c -T your.ld -o firmware.bin   # firmware.bin and firmware.elf, checked
```

The same, step by hand (what `tc32-cc` runs):

```
sh checks/run_checks.sh                  # every check that needs only clang, ld.lld, Python (and Go, Csmith if present)

clang --target=thumbv4t-none-eabi -mcpu=arm7tdmi -mthumb -mfloat-abi=soft \
      -fno-jump-tables -mllvm -arm-load-store-opt=false -c app.c -o app.o
ld.lld -T your.ld app.o -o firmware.elf
python3 compiler/thumb2tc32.py firmware.elf firmware.bin
python3 compiler/image_check.py firmware.elf firmware.bin     # every byte against the ELF
python3 compiler/forms_check.py check firmware.elf --thumb    # every instruction form against Telink's own use
python3 emulator/run_boot.py --elf firmware.elf --bin firmware.bin --ms 300
```

`checks/sem/` is a complete small example: a C test, a start file, a linker script and the build script `build_thumb.sh`.

## The compiler path

There is no TC32 compiler in this repository, and none is needed. A mainstream clang does all the compiling and ld.lld all the linking, for the ARMv4T Thumb-1 target (`thumbv4t-none-eabi`). The tools here come only before and after them:

```
C source ─────────────────────────────── clang ──┐
Telink-syntax assembly ─ tc32asm2thumb.py ─ clang ─┼─▶ Thumb objects ─ ld.lld ─▶ Thumb ELF ─ thumb2tc32.py ─▶ TC32 image
compiler/runtime/ (division, memcpy, ...) ─ clang ─┘                                          image_check.py, forms_check.py
```

Telink's own toolchain is built the same way: a Thumb gcc 4.5 with an assembler that writes TC32 opcodes. Here the compiler and linker never see TC32. The last step re-encodes every code halfword by a fixed permutation, which is checked value by value against Telink's tools.

**Flags.** Build with `--target=thumbv4t-none-eabi -mcpu=arm7tdmi -mthumb -mfloat-abi=soft -fno-jump-tables -mllvm -arm-load-store-opt=false`.

- `-fno-jump-tables`: a jump table is dispatched by reading PC in a high-register add. No Telink code reads PC that way, so nothing shows what TC32 returns at a halfword address. Without jump tables, PC is read only by literal loads, which Telink's code uses throughout, and by `adr` (`add rd, pc, #imm`), which clang emits for a table lookup and which Telink's own libgcc uses the same way (`__clzsi2`); both compute the word-aligned PC + 4, as on ARM.
- `-mllvm -arm-load-store-opt=false`: the load/store optimizer emits `ldm rN, {..., rN, ...}` (base register in the list, no writeback), which Telink's compiler never emits.

**Linking.** Use ld.lld with a linker script that keeps read-only data out of executable sections (`checks/sem/thumb.ld`). `thumb2tc32.py` tells code from data by the ELF's mapping symbols and sized data objects, and refuses an executable section that has none.

**What `thumb2tc32.py` does.** It rewrites the code of the linked image into TC32's encoding, instruction by instruction, and copies data unchanged. It does not compile, optimize or link anything.

- It refuses ARM code, BLX and Thumb-2 other than a BL pair.
- It rewrites two Thumb instructions into forms Telink's code uses.
  - `lsls rd, rm, #0` becomes `adds rd, rm, #0`. This is clang's register move on ARMv4T. The two differ only in C and V, and clang emits the move only where the flags are dead (Thumb1InstrInfo.cpp, copyPhysReg). Hand-written Thumb assembly must not rely on `movs` keeping C or V.
  - `udf`, clang's trap, becomes a branch to itself.

**Assembly in Telink's syntax** (`tloadr`, `tjl`, `tmcsr`, `treti`, ...) goes through `tc32asm2thumb.py`. It writes the Thumb instruction with the same encoding, and TC32's own instructions become `.inst.n` values that `thumb2tc32.py` re-encodes like the rest. `asm_check.py` compares the result with Telink's assembler byte for byte.

**Runtime helpers.** `compiler/runtime/compiler_builtins.c` and `aeabi_thumb.S` provide what clang calls: `__aeabi_uidiv`, `__aeabi_uldivmod`, `__aeabi_lmul`, the 64-bit shifts, `__aeabi_memcpy` and the unaligned-access helpers. With `-DTC32_TLSR8278_DIVIDER=1`, 32-bit division uses the TLSR8278's hardware divider at 0x800660, as Telink's SDK does. It runs with interrupts off, from `.ram_code`, which the linker script must place.

**Checking the image.**

- `image_check.py` compares every byte of the written image with the ELF, and the address the startup copies `.data` from with the ELF's data segment. With `--blob` and `--startup` it also holds a blob of hand-written code and the startup to the manifests of the versions proven on the chip, so that code which boots the chip stays byte for byte as it was proven.
- `forms_check.py` compares every instruction form with the forms Telink's code uses (`compiler/vendor_forms.txt`: Telink's SDK libraries and libgcc, the executed code of an existing TC32 binary, and programs built by Telink's gcc). It fails on a form that has no vendor use. It reads the functions and the code outside them that a `$t` mapping symbol marks (hand-written assembly without `.type`/`.size`), so it reads every instruction `thumb2tc32.py` re-encodes: the counts are equal for the semantics test and for the ZMK images of three keyboards.
- `tc32_flow.py` follows the control flow of a raw image without symbols, such as a vendor image, and reports the instruction forms it reaches.

### tc32-cc: the compiler path as one command

`go/cmd/tc32-cc` is one program for the whole path. It takes C and assembly like a C compiler, and on a link it writes the TC32 image and the ELF next to it:

```
tc32-cc [-c|-S|-E] [-O...] [-D...] [-I...] [-x tc32-asm] <inputs> -T <script> -o <image>
```

1. clang compiles each input with the fixed flags (`tc32-cc --print-flags`). Assembly in Telink's syntax (`-x tc32-asm`) goes through the Go port of `tc32asm2thumb.py` first.
2. The runtime helpers are compiled and linked after the inputs, unless `-nostdlib`. With `--tlsr8278-divider`, they use the hardware divider.
3. ld.lld links. thumb2tc32 re-encodes the image.
4. The image check compares every byte with the ELF. The forms check compares every instruction form with Telink's use.
5. A failed check removes the image (status 1). The ELF stays for inspection.

**Refused options.** Options that would change the code generation are refused with status 2: another target, CPU or float ABI, `-marm`, `-fjump-tables`, `-mllvm -arm-load-store-opt=true`, `-flto`.

**What it needs.** An LLVM install: clang, ld.lld, llvm-objcopy and llvm-readelf, found as `common/toolchain.py` finds them (`TC32_LLVM`, `TC32_LLD`, Homebrew, `PATH`). The runtime helpers and `vendor_forms.txt` are built into the binary, so neither Python nor a checkout of this repository is needed. `tc32-cc --version` shows the tools it found. A link warns when clang is not the LLVM release the checks here were made with.

**How it is held to the rest.** `checks/tc32cc/check.sh` checks four things:

- The built-in files equal `compiler/runtime` and `compiler/vendor_forms.txt`.
- Its images of the semantics test at -O0, -O2, -Os and -Oz equal, byte for byte, the images of the same steps run by hand with `thumb2tc32.py`.
- Those images, and one each with a Telink-syntax start file and the divider helpers, give the host's results in the emulator.
- The refusals hold, and an image with a form Telink never uses is removed.

`tc32-cc` is Go only. It adds no behaviour of its own: it runs clang and ld.lld and the Go ports, which are held equal to the Python tools.

## The emulator

`emulator/tc32emu.py` runs Thumb-1 semantics (ARMv4T) under the TC32 encoding, plus TC32's own instructions (`tmcsr`/`tmrcs`/`tmssr`/`tmrss`, `treti`, `tserv`). It has ARM-style IRQ and SVC modes with banked r13/r14, and the vector at 0x10.

It stops with an error wherever ARM leaves a result UNPREDICTABLE. It also stops wherever TC32 could differ from ARM in a way that matters: a high-register op reading PC at a halfword address, or `treti` without pc.

**Peripherals of the TLSR8278.** The module docstrings give the source of each model, the DS-TLSR8278 datasheet or Telink's public SDKs, and mark what no document describes.

- **Core and memory:** the boot ROM's copy and start; booting from slot B, with the address remap the handbook describes; flash XIP; 64 KB SRAM.
- **Clocks and timers:** the clock select (RC 24 MHz, 48 MHz, 32 MHz); the system timer and its compare interrupt; Timer0-2 and the watchdog; the reset cause and reboot.
- **Interrupts and analog:** the interrupt controller (level and edge sources); the analog register port; RC calibration.
- **Flash:** the MSPI flash, with its status register and block protection per the SDK's tables for each part.
- **GPIO:** inputs from pad levels, pulls, a floating pad holding its level, and the GPIO interrupt.
- **Other blocks:** the hardware divider, the random number generator and the ADC's DMA.
- **USB and suspend:** the USB device controller with a scripted host that enumerates the image and polls its endpoints (`usb_model.py`); suspend and its wake sources.
- **Radio:** `ble_radio.py` models the state machine, DMA, TX FIFO, SN/NESN and the AES block, as far as a BLE peripheral's link layer drives them, and Telink's private 2.4 GHz packet format (TPLL) with a scripted peer.
- **Bluetooth central:** `ble_central.py` is a scripted central. It covers the link layer (channel selection #1, encryption, LL control), L2CAP, an ATT client and SMP legacy pairing. It can also connect from a resolvable private address and distribute an identity key, and several centrals can hear one advertiser.
- **Air:** `ble_air.py` puts the air between the radio and its centrals: packets lost each way, bad channels, a blackout, a central's clock drift, and jitter of its connection events.

**Timing.** Each instruction takes a fixed number of cycles (`cpi`). By default an access to the flash costs nothing more, and there is no model of wait states. Interrupts are looked at every `check_every` instructions (32 by default) in `Machine.run`. `TC32EMU_ICACHE_MISS=N` turns on a model of the flash cache: 64 lines of 32 bytes, direct-mapped, with N cycles for each miss. The mapping and the cost of a miss are not documented, so N is the caller's choice and every run states it. `run_boot` prints the misses and their share of the cycles. Idle CPU shares measured on TLSR8278 keyboards fall near the model's figures at about 190 cycles per miss.

**Behaviour no document states is an option.** Where the chip's behaviour is in none of the sources, the model either stops the run or takes a value from the environment. It never picks a value silently:

| Variable | What it sets | Default |
|---|---|---|
| `TC32EMU_ICACHE_MISS` | Cycles per flash cache miss | off (0) |
| `TC32EMU_SRAM_SIZE` | SRAM below 64 KB (`0x8000` for a 32 KB part); an access above it stops the run | 64 KB |
| `TC32EMU_SRAM_SEED` | SRAM filled at power-on with xorshift32 bytes instead of zeros (both engines give the same bytes) | zeros |
| `TC32EMU_FLASH_SIZE` | Flash below 1 MB (`0x80000` for 512 KB); the JEDEC capacity byte follows it; XIP, reads, programs and erases at or above it stop the run | 1 MB |
| `TC32EMU_FLASH_BEYOND` | What a taken SPI read beyond the part gives: `stop`, `ff`, or `wrap` | `stop` |
| `TC32EMU_FLASH_TPP_US`, `_TSE_US`, `_TBE32_US`, `_TBE64_US` | Busy time after a page program, sector erase, 32 KB and 64 KB block erase. While busy the part takes only status reads; anything else, an XIP read included, stops the run. (The datasheet's typical values are 1.6 ms, 150 ms, 0.5 s and 0.8 s.) | none |
| `TC32EMU_TIMER_CAPTURE_BELOW` | A Timer0/1 capture written at or below the running count: the match comes only after the 32-bit count wraps (hardware shows this). `stop`, or `wrap` to model the wait | `stop` |
| `TC32EMU_PAD_C_PF`, `TC32EMU_PAD_R_PCT` | Pad capacitance, and a scaling of the pull resistors; a pad left to its pull reaches the new level after the RC time | instant |
| `TC32EMU_USB_CORE_WAKE` | The USB core wake source as a `level` or an `edge` | `level` |
| `TC32EMU_SUSPEND_RF` | `lost`: the radio's setup is lost in a suspend, and the radio refuses commands until the firmware has set it up again | `keep` |
| `TC32EMU_TXFIFO_CLEAR` | Where a TX FIFO clear leaves its pointers: `position` (empty where it is) or `zero` | `position` |
| `TC32EMU_RX_BUSY` | The "receiving" bit a link layer may wait on: `off`, `air` (while a packet is on air), `stuck` | `off` |
| `TC32EMU_CENTRAL_SKIP_EVERY` | The scripted central skips every Nth connection event, so the peripheral's own timer ends it | 0 (never) |

Other undocumented cases are logged and recorded so a check can fail on them, rather than given a guessed outcome. Examples are an advertising packet sent while the TX FIFO still holds packets, a receive command written after its start tick, and a USB control stage slower than 50 ms. The XIP read during an SPI command, which the flash cannot serve, stops the run.

**Running images.**

- `run_boot.py` boots an image and reports the milestones it reaches. It runs the image alone or installed beside another image in the other slot, through watchdog and software resets, and it can make a symbol hang so the watchdog has something to catch.
- `--console` collects what the firmware writes to register 0xfff0. That is a free offset, so a Zephyr log backend writing there makes the emulator its console. `--stop-at SYMBOL` and `--stop-line TEXT` end the run.
- `stack_use.py` (`--stacks`) measures stack use from painted stacks.
- `reg_audit.py` lists the registers an image touches whose names differ between the TLSR8258 and TLSR8278 SDKs.
- Machine hooks let a board model sit around the emulator: register and memory hooks, pad levels, reset, time update, idle skip, trace, interrupt, wake, and flash write.

**Reading TC32 code.** Do not read TC32 code with the `llvm-objdump` of the LLVM TC32 toolchain (`elf32-littletc32`). It mis-decodes whole instruction classes, so in a shipped image hundreds of sites read wrong:

- NEG (`tnegs`) and TST (`tnand`) print as shifts.
- BX (`tjex`) prints as a shift.
- `treti`, `tmrcs`, `tserv` and `tjlex` print as loads or stores.

Two ways agree with each other and with the emulator:

- Telink's `tc32-elf-objdump -D -b binary -m tc32`, an x86-64 Linux binary, run in Docker.
- The halfwords mapped to Thumb with `tc32isa.to_thumb` and read by a mainstream `llvm-objdump --triple=thumbv4t`. Here the TC32-only `tmcsr`/`tmrcs`/`tmssr`/`tmrss`/`treti` show as undefined.

## What the checks show

| Check | Result |
|---|---|
| `isa_check.py`: every 16-bit value as TC32 through Telink's `tc32-elf-objdump`, and, re-encoded, as Thumb through llvm-objdump; 65,536 BL pairs | 56,174 values decode to the same instruction and operands; 8,254 are undefined in both; all BL targets are equal. The only one-sided decodes are TC32's own instructions and encodings ARMv4T leaves undefined. Swapping two table entries gives 4,096 mismatches |
| `asm_check.py`: the Zephyr TC32 port's 7 assembly files (1,781 halfwords) through Telink's assembler, and through `tc32asm2thumb.py` + clang + `thumb2tc32.py` | Byte for byte equal, apart from alignment fill after a return |
| `image_check.py`: a 130 KB ZMK image (the V75 Pro's) against its ELF | 51,455 code and 13,564 data halfwords, 0 mismatches. Two flipped bits are found, and so is an image whose code was not converted |
| `forms_check.py`: that image's instruction forms against Telink's SDK libraries and libgcc, the executed code of an existing TC32 binary, and 866 objects built by Telink's gcc | 51,050 instructions in 1,200 functions, 0 forms without vendor use |
| `sem/`: 64 arithmetic results at -O0/-O2/-Os/-Oz, run in the emulator | 64/64 equal to the host at every level, with software division and with the hardware divider |
| `ccdiff/`: Csmith programs built with clang + `thumb2tc32.py`, run in the emulator | 3,000 programs at 5 levels (315 skipped: their host run took over 5 s): 13,424 builds equal to the host, 1 over the instruction limit, 0 wrong; 25 more with the divider helpers: 125 equal, 0 wrong |
| The emulator itself: Telink's gcc, whose code runs on shipped devices, building the same Csmith programs and `sem` | 450 programs at -O0, -O2 and the SDK's flags (1,350 builds) and 64/64, equal to the host. A wrong result here would point at the emulator; none did |
| `zephyr_kernel_tests_tc32.py`: Zephyr's kernel test suites built for TC32 (scheduler with and without time slicing, timer, work queue, mutex, message queue, semaphore, common, sleep, thread APIs) | 10 suites: 374 ztest cases pass, 0 fail, 27 skipped (userspace-only and per-thread-slice cases) |
| `zmk_tests_tc32.py`: ZMK's 248 snapshot tests (`app/tests`), built for TC32 and run with a mock key scan | 239 pass, 2 pending (as on native_sim), and 7 expected differences from timing baked into snapshots (`checks/zmk_tests_expected.txt`) |
| BLE models (`ble_models_check.py`, `ble_air_check.py`) | Known answers from FIPS-197, RFC 3610 and the Bluetooth Core specification's SMP, LL encryption and `ah()` samples; the radio's FIFO, DMA and ack cases; channel selection. `ble_air_mutants.py` changes the air model in 14 places, one at a time, and the check fails for each |

**How far the evidence reaches.** The evidence that TC32 behaves as ARMv4T Thumb is only as wide as what Telink's code uses, which is why `forms_check.py` fails on any form outside that.

- Csmith programs never contain a rotate. `checks/telink/rot.c` was built by Telink's gcc to show that it emits `alu ror` (`trotrs`) for the usual rotate idioms, and those objects are in the table.
- Where the LLVM TC32 backend's passes assume the hardware differs from Thumb (load hazards, GE/PL/LS branches, immediate carries, three-register add/sub), Telink's own code uses all of those forms freely.

`checks/run_checks.sh` runs everything that needs only clang, ld.lld, Python, Go and Csmith:

- sem both ways and the image and forms checks;
- the Go tools against the Python ones;
- emulator lockstep, also with the cache model and a seeded SRAM;
- the BLE and air models;
- the peripheral checks;
- a small ccdiff.

The other checks need more:

- `isa_check.py` and `asm_check.py` need Telink's `tc32-elf-objdump` and `tc32-elf-as`, x86-64 Linux binaries from Telink's IDE, run in Docker. So does `ccdiff.py --compiler telink`, with Telink's `tc32-elf-gcc`.
- `zephyr_kernel_tests_tc32.py` and `zmk_tests_tc32.py` need a Zephyr workspace with the TC32 Zephyr port, and for the ZMK tests the TC32 ZMK port, as their docstrings describe.

## Go port

The Python tools are the canonical implementation, and `go/` is a port for speed and for single static binaries.

- A change of behaviour (an encoding rule, a refusal, an emulator model) lands in Python first, with its check.
- The Go side follows, and `checks/run_checks.sh` must pass before a push.
- Go never gets behaviour Python lacks.

| Go | Python | How they are held equal |
|---|---|---|
| `go/tc32isa` | `common/tc32isa.py` | The table is a permutation and equals the Python file's |
| `go/cmd/thumb2tc32` | `compiler/thumb2tc32.py` | The same image, byte for byte, from every sem build |
| `go/cmd/image-check` (incl. `--blob`, `--startup`) | `compiler/image_check.py` | Same rules. Both read the layout through llvm-objcopy and llvm-readelf, so neither trusts its own ELF reader |
| `go/cmd/tc32asm2thumb` | `compiler/tc32asm2thumb.py` | Same output on `checks/asm/sample.S` (every form) and on a Zephyr port's assembly files |
| `go/cmd/forms-check` | `compiler/forms_check.py check` | Same report and exit status. The `evidence` subcommand, which needs Telink's disassembly, stays in Python |
| `go/cmd/tc32-cc` | none: the steps by hand | The one Go-only tool, and it runs only the ports above. Its images equal those of the steps by hand with `thumb2tc32.py` (`checks/tc32cc/check.sh`) |
| `go/cmd/tc32-flow` | `compiler/tc32_flow.py` | Same table and forms file |
| `go/tc32emu` (incl. `usbmodel.go`) | `emulator/tc32emu.py`, `usb_model.py` | Lockstep: `checks/lockstep.sh` requires the two engines' instruction traces (pc, opcode, r0-r15, CPSR, every interrupt, wake and reset) to be identical, over millions of instructions of sem builds and ZMK firmware, a USB enumeration and a suspend with timer wakes |
| `go/cmd/tc32emu-run` | the stepping loop of `ccdiff.py` and `sem_check.py` | `--engine go`: the same result for every sem build and every Csmith seed and level |
| `go/cmd/run-boot`, `go/regaudit` | `emulator/run_boot.py`, `stack_use.py`, `reg_audit.py` | Same output, line for line |
| `go/ble`, `go/ble/aes128` | `emulator/ble_radio.py`, `ble_central.py`, `ble_air.py`, `aes128.py` | The same known answers as Go tests. Python's `random.Random` is reproduced (MT19937), so the central's random bytes and the air's draws are identical |

The Go emulator runs about 74 million instructions per second, against the Python one's 0.65 million, so long soaks and large Csmith runs take minutes instead of hours.

```
cd go && go build -o bin/thumb2tc32 ./cmd/thumb2tc32 && go build -o bin/run-boot ./cmd/run-boot
GOOS=linux GOARCH=arm64 go build -o thumb2tc32-linux-arm64 ./cmd/thumb2tc32     # for a build box or a Pi
```

Still Python only: the checks that drive external toolchains (`isa_check.py`, `asm_check.py`), `forms_check.py evidence`, and the Zephyr and ZMK test drivers.

## Limits

- Thumb-1 only. Whatever needs Thumb-2, ARM state or BLX is refused.
- The re-encoding is checked against Telink's tools, and the semantics against Telink's compiler in emulation. Instruction forms outside Telink's own use are refused, not assumed.
- The emulator's timing is a fixed number of cycles per instruction, plus an optional cache cost. There are no wait states and no bus contention.
- Peripherals that vendor firmware does not exercise are modelled from the datasheet and the SDK only. The radio model covers what a BLE peripheral's link layer and the 2.4 GHz format drive. It is not an RF model.
- Another LLVM than the one named above should be run through `checks/run_checks.sh` first. The `lsls #0` rewrite in particular depends on when clang emits that move.

## Related work

- [rgov/Ghidra_TELink_TC32](https://github.com/rgov/Ghidra_TELink_TC32): a Ghidra processor specification for TC32, bootstrapped from Telink's objdump. It documents the same relation to Thumb.
- [modern-tc32/llvm-project](https://github.com/modern-tc32/llvm-project): an LLVM backend for TC32. Its fixup passes assume hardware differences from Thumb that Telink's own code contradicts, and the differential test found a miscompile in it (a 1-byte-aligned union passed by value, read with 32-bit loads). This repository does not use it.
- [devbis/zephyr-tlsr8258](https://github.com/devbis/zephyr-tlsr8258): Zephyr for the TLSR8258 and Zigbee. Its architecture layer for the TC32 CPU is the base of zephyr-tc32, this project's Zephyr.

## License

Apache-2.0 (see `LICENSE`).

`compiler/vendor_forms.txt` is a table of instruction-form counts inferred from existing TC32 binaries. The binaries themselves are not included. `checks/telink/out/` holds objects and disassembly built by Telink's gcc from `checks/telink/rot.c`. `emulator/b85_registers.csv` and `b87_registers.csv` are generated by `emulator/reg_tables.py` from the `register.h` files of Telink's [tc_ble_single_sdk](https://github.com/telink-semi/tc_ble_single_sdk) (Apache-2.0).

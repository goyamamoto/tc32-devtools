/*
 * checks/sem/thumb.ld with a noinit section in RAM after .bss, where a
 * Zephyr image keeps its thread stacks (stacks.c).
 * SPDX-License-Identifier: Apache-2.0
 */
ENTRY(_start)
MEMORY { FLASH (rx) : ORIGIN = 0, LENGTH = 1M  RAM (rw) : ORIGIN = 0x840000, LENGTH = 64K }
SECTIONS {
	.text : { KEEP(*(.vectors)) *(.text*) *(.ram_code*) . = ALIGN(4); } > FLASH
	.rodata : { *(.rodata*) . = ALIGN(4); } > FLASH
	.ARM.exidx : { *(.ARM.exidx*) } > FLASH
	.data : { __data_start = .; *(.data*) *(.sdata*) . = ALIGN(4); __data_end = .; } > RAM AT > FLASH
	__data_lma = LOADADDR(.data);
	.bss (NOLOAD) : { __bss_start = .; *(.bss*) *(.sbss*) *(COMMON) . = ALIGN(4); __bss_end = .; } > RAM
	noinit (NOLOAD) : { *(noinit) . = ALIGN(4); } > RAM
}

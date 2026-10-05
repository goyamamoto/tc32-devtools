/*
 * Minimal start for Thumb-1 builds run as TC32 (thumb2tc32.py re-encodes
 * the instructions): the TC32 boot header, stack, .data, .bss, sem_main().
 *
 * SPDX-License-Identifier: Apache-2.0
 */
	.syntax	unified
	.thumb
	.section .vectors, "ax"
	.global	_start
_start:
	b	reset
	.hword	0
	.word	0
	.word	0x544c4e4b		/* "KNLT": bootable */
	.word	0x00880000
	.thumb_func
reset:
	ldr	r0, =0x850000
	mov	sp, r0
	ldr	r0, =__data_lma
	ldr	r1, =__data_start
	ldr	r2, =__data_end
1:	cmp	r1, r2
	bhs	2f
	ldr	r3, [r0]
	str	r3, [r1]
	adds	r0, r0, #4
	adds	r1, r1, #4
	b	1b
2:	ldr	r1, =__bss_start
	ldr	r2, =__bss_end
	movs	r3, #0
3:	cmp	r1, r2
	bhs	4f
	str	r3, [r1]
	adds	r1, r1, #4
	b	3b
4:	bl	sem_main
5:	b	5b
	.ltorg

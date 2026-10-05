/*
 * Minimal TC32 start for the semantics test: stack, .data, .bss, sem_main().
 *
 * SPDX-License-Identifier: Apache-2.0
 */
	.section .vectors, "ax"
	.global _start
_start:
	tj	reset
	.word	0
	.word	0x544c4e4b
	.word	0x00880000
reset:
	tloadr	r0, .Lstack
	tmov	sp, r0
	tloadr	r0, .Ldata_lma
	tloadr	r1, .Ldata_start
	tloadr	r2, .Ldata_end
1:	tcmp	r1, r2
	tjhs	2f
	tloadr	r3, [r0]
	tstorer	r3, [r1]
	tadd	r0, r0, #4
	tadd	r1, r1, #4
	tj	1b
2:	tloadr	r1, .Lbss_start
	tloadr	r2, .Lbss_end
	tmov	r3, #0
3:	tcmp	r1, r2
	tjhs	4f
	tstorer	r3, [r1]
	tadd	r1, r1, #4
	tj	3b
4:	tjl	sem_main
5:	tj	5b
	.align 2
.Lstack:	.word	0x850000
.Ldata_lma:	.word	__data_lma
.Ldata_start:	.word	__data_start
.Ldata_end:	.word	__data_end
.Lbss_start:	.word	__bss_start
.Lbss_end:	.word	__bss_end

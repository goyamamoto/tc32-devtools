/*
 * Every instruction form tc32asm2thumb.py translates, in Telink's syntax, so
 * that the Go build can be compared with it line for line (run_checks.sh).
 * The file is not meant to run. SPDX-License-Identifier: Apache-2.0
 */
#include <zephyr/toolchain.h>
#define TC32_MODE_IRQ 0x92

	.section .vectors, "ax"
	.global __start
__start:
	tj __reset
	.word 0
	.org 0x10
	tj __irq
__reset:
	tloadr r0, .Lirq_mode
	tmcsr r0
	tmrcs r1
	tmssr r2
	tmrss r3
	tloadr r0, .Lstack_end  /* the stack */
	tmov sp, r0
	tmov r1, #0		@ zero
	tmov r2, r1		// a low-register move
	tmov r8, r2
	tmov r3, r12
	tstorerb r1, [r0]
	tstorer r1, [r0, #4]
	tstorerh r1, [r0, #2]
	tloadrb r4, [r0, #1]
	tloadrh r4, [r0, #2]
	tloadrsb r4, [r0, r1]
	tloadrsh r4, [r0, r1]
	tloadm r0!, {r1-r3}
	tstorem r0!, {r1, r2}
	tpush {r0-r7}
	tpush {r14}
	tpop {r0-r5}
	tpop {r15}
	tadd r0, r0, r3
	tadd r1, r1, #4
	tadd r0, sp, #8
	tadd sp, #16
	tsub sp, #16
	tsub r2, r2, r3
	tsub r3, r3, #1
	tadd r4, #1
	tadd r8, r2
	tadd r13, r5
	tcmp r0, #0
	tcmp r0, r1
	tcmpn r0, r1
	tand r0, r2
	tor r0, r2
	txor r0, r2
	tbclr r2, r0
	tnand r0, r1
	tmovn r0, r1
	tneg r0, r1
	tmul r0, r1
	taddc r0, r1
	tsubc r0, r1
	trotr r0, r1
	tshftl r0, r0, #23
	tshftr r0, r0, #16
	tasr r0, r0, #2
	tshftl r0, r1
	tshftr r0, r1
	tasr r0, r1
	tjeq .Ldone
	tjne 1b
	tjcs 2f
	tjcc 2f
	tjhs 2f
	tjlo 2f
	tjmi 2f
	tjpl 2f
	tjvs 2f
	tjvc 2f
	tjhi 2f
	tjls 2f
	tjge 2f
	tjlt 2f
	tjgt 2f
	tjle 2f
	tjl z_prep_c
	tjex r0
	tjex lr
	nop
	nop
.Ldone:
	tj .Ldone
	.align 2
.Lirq_mode:
	.word 0x12
.Lstack_end:
	.word z_interrupt_stacks + __z_interrupt_stack_SIZEOF
__irq:
	tpush {r14}
	tpush {r0-r7}
	tmrss r0
	tmov r5, #TC32_MODE_IRQ
	tmcsr r5
	tjl z_tc32_handle_irqs
	tpop {r0-r7}
	tmssr r0
	treti {r15}
	treti {r0-r3, r15}

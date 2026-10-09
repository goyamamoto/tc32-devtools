/*
 * The standard-syntax twin of twin.S (unified Thumb syntax, the TC32-only
 * instructions as .inst.n with the Telink instruction in the comment), for
 * asm_twin_check.py's test in run_checks.sh. The file is not meant to run.
 *
 * SPDX-License-Identifier: Apache-2.0
 */
	.syntax unified
	.thumb
	.section .text.twin, "ax", %progbits
	.global twin_entry
	.type twin_entry, %function
twin_entry:
	push {r4, lr}
	adds r4, r0, #0
	movs r0, #0x93
	.inst.n 0xbbc0 @ tmcsr r0
	ldr r1, .Lpool
	ldr r2, [r1, #4]
	adds r2, r2, #1
	str r2, [r1, #4]
	bl twin_callee
	cmp r4, #0
	beq .Ldone
	mov r8, r8
.Ldone:
	adds r0, r4, #0
	pop {r4, pc}
	.balign 4
.Lpool:
	.word twin_data
	.size twin_entry, . - twin_entry

	.global twin_irq_return
	.type twin_irq_return, %function
twin_irq_return:
	.inst.n 0xbbd0 @ tmssr r0
	.inst.n 0xb900 @ treti {r15}
	.size twin_irq_return, . - twin_irq_return

	.section .data.twin, "aw", %progbits
	.global twin_data
twin_data:
	.word 0, 1, twin_entry

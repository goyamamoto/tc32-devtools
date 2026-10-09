/*
 * A source in Telink's syntax that assembles on its own, and its twin in
 * standard syntax (twin.ual.S): asm_twin_check.py must find the translation
 * (tc32asm2thumb.py) and the twin the same object, and a twin with one
 * operand changed different (run_checks.sh). The file is not meant to run.
 *
 * SPDX-License-Identifier: Apache-2.0
 */
	.section .text.twin, "ax", %progbits
	.global twin_entry
	.type twin_entry, %function
twin_entry:
	tpush {r4, r14}
	tmov r4, r0
	tmov r0, #0x93
	tmcsr r0
	tloadr r1, .Lpool
	tloadr r2, [r1, #4]
	tadd r2, r2, #1
	tstorer r2, [r1, #4]
	tjl twin_callee
	tcmp r4, #0
	tjeq .Ldone
	tmov r8, r8
.Ldone:
	tmov r0, r4
	tpop {r4, r15}
	.balign 4
.Lpool:
	.word twin_data
	.size twin_entry, . - twin_entry

	.global twin_irq_return
	.type twin_irq_return, %function
twin_irq_return:
	tmssr r0
	treti {r15}
	.size twin_irq_return, . - twin_irq_return

	.section .data.twin, "aw", %progbits
	.global twin_data
twin_data:
	.word 0, 1, twin_entry

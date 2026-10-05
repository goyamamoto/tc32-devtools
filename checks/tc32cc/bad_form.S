/*
 * An image with an instruction form Telink's code never uses (ldm with its
 * base register in the list and no writeback), outside any function: the
 * forms check must refuse it, and tc32-cc must then remove the image.
 * SPDX-License-Identifier: Apache-2.0
 */
	.syntax	unified
	.thumb
	.section .vectors, "ax"
	.global	_start
_start:
	ldm	r0, {r0, r1}
	b	_start

/*
 * ARM EABI 64-bit division helpers for a mainstream compiler building
 * ARMv4T Thumb (TC32_THUMB; thumb2tc32.py re-encodes the link as TC32):
 * the quotient in r0:r1 and the remainder in r2:r3, over
 * __tc32_udivmoddi4() and __tc32_divmoddi4() in compiler_builtins.c.
 * Thumb syntax, not translated by tc32asm2thumb.py.
 *
 * SPDX-License-Identifier: Apache-2.0
 */
	.syntax	unified
	.thumb
	.text

	.global	__aeabi_uldivmod
	.type	__aeabi_uldivmod, %function
	.thumb_func
__aeabi_uldivmod:
	push	{r4, lr}
	sub	sp, #16
	add	r4, sp, #8
	str	r4, [sp]
	bl	__tc32_udivmoddi4
	ldr	r2, [sp, #8]
	ldr	r3, [sp, #12]
	add	sp, #16
	pop	{r4, pc}
	.size	__aeabi_uldivmod, . - __aeabi_uldivmod

	.global	__aeabi_ldivmod
	.type	__aeabi_ldivmod, %function
	.thumb_func
__aeabi_ldivmod:
	push	{r4, lr}
	sub	sp, #16
	add	r4, sp, #8
	str	r4, [sp]
	bl	__tc32_divmoddi4
	ldr	r2, [sp, #8]
	ldr	r3, [sp, #12]
	add	sp, #16
	pop	{r4, pc}
	.size	__aeabi_ldivmod, . - __aeabi_ldivmod

/*
 * Unaligned accesses (packed structures), a byte at a time as Telink's
 * compiler does them. In assembly: in C the compiler could merge the byte
 * accesses back into a call of the same helper.
 */

/* uint32_t __aeabi_uread4(void *p) */
	.global	__aeabi_uread4
	.type	__aeabi_uread4, %function
	.thumb_func
__aeabi_uread4:
	ldrb	r1, [r0, #0]
	ldrb	r2, [r0, #1]
	lsls	r2, r2, #8
	orrs	r1, r2
	ldrb	r2, [r0, #2]
	lsls	r2, r2, #16
	orrs	r1, r2
	ldrb	r2, [r0, #3]
	lsls	r2, r2, #24
	orrs	r1, r2
	adds	r0, r1, #0
	bx	lr
	.size	__aeabi_uread4, . - __aeabi_uread4

/* uint32_t __aeabi_uwrite4(uint32_t v, void *p) */
	.global	__aeabi_uwrite4
	.type	__aeabi_uwrite4, %function
	.thumb_func
__aeabi_uwrite4:
	strb	r0, [r1, #0]
	lsrs	r2, r0, #8
	strb	r2, [r1, #1]
	lsrs	r2, r0, #16
	strb	r2, [r1, #2]
	lsrs	r2, r0, #24
	strb	r2, [r1, #3]
	bx	lr
	.size	__aeabi_uwrite4, . - __aeabi_uwrite4

/* uint64_t __aeabi_uread8(void *p) */
	.global	__aeabi_uread8
	.type	__aeabi_uread8, %function
	.thumb_func
__aeabi_uread8:
	push	{r4, lr}
	adds	r4, r0, #0
	bl	__aeabi_uread4
	adds	r3, r0, #0
	adds	r0, r4, #4
	bl	__aeabi_uread4
	adds	r1, r0, #0
	adds	r0, r3, #0
	pop	{r4, pc}
	.size	__aeabi_uread8, . - __aeabi_uread8

/* uint64_t __aeabi_uwrite8(uint64_t v, void *p) */
	.global	__aeabi_uwrite8
	.type	__aeabi_uwrite8, %function
	.thumb_func
__aeabi_uwrite8:
	push	{r0, r1, r4, lr}
	adds	r4, r2, #0
	adds	r1, r2, #0
	bl	__aeabi_uwrite4
	ldr	r0, [sp, #4]
	adds	r1, r4, #4
	bl	__aeabi_uwrite4
	pop	{r0, r1, r4, pc}
	.size	__aeabi_uwrite8, . - __aeabi_uwrite8

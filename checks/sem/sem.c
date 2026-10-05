/*
 * Instruction semantics test for tc32emu: the same computations built for
 * TC32 (run in the emulator) and for the host must give the same results.
 * Inputs come from a volatile seed so the compiler cannot fold them.
 *
 * SPDX-License-Identifier: Apache-2.0
 */

#include <stdint.h>

#define N_RESULTS 64

volatile uint32_t seed = 0x12345678u;
uint32_t results[N_RESULTS];
volatile uint32_t done;

static uint32_t lcg(uint32_t *s)
{
	*s = *s * 1664525u + 1013904223u;
	return *s;
}

static uint32_t mix(uint32_t h, uint32_t v)
{
	h ^= v;
	h *= 0x01000193u;
	return h ^ (h >> 15);
}

struct blob {
	uint32_t a[7];
	uint8_t b[5];
	int16_t c;
};

static int32_t __attribute__((noinline)) pick(int k, int32_t x)
{
	switch (k & 7) {
	case 0: return x + 17;
	case 1: return x - 1000;
	case 2: return x * 3;
	case 3: return -x;
	case 4: return x >> 3;
	case 5: return x ^ 0x5a5a;
	case 6: return ~x;
	default: return x / 7;
	}
}

static uint32_t op_add(uint32_t a, uint32_t b) { return a + b; }
static uint32_t op_sub(uint32_t a, uint32_t b) { return a - b; }
static uint32_t op_bic(uint32_t a, uint32_t b) { return a & ~b; }
static uint32_t (*const ops[3])(uint32_t, uint32_t) = {op_add, op_sub, op_bic};

static uint32_t __attribute__((noinline)) fib(uint32_t n)
{
	return n < 2u ? n : fib(n - 1u) + fib(n - 2u);
}

volatile uint16_t vu16[8] = {1, 0x8001, 3, 0xfffe, 5, 0x7fff, 7, 0x8000};
volatile int16_t vs16[8] = {-1, 2, -32768, 4, 32767, -6, 7, -8};
volatile int8_t vs8[8] = {-1, 2, -128, 4, 127, -6, 7, -8};

/* Many values live across calls: spills to sp-relative slots. */
static uint32_t __attribute__((noinline)) spill(uint32_t a, uint32_t b)
{
	uint32_t v0 = a + 1u, v1 = a * 3u, v2 = b ^ a, v3 = b - 7u, v4 = a >> 3, v5 = b << 2;
	uint32_t v6 = a | b, v7 = a & b, v8 = ~a, v9 = b + a * b;

	v0 += fib(v0 & 7u);
	v1 += fib(v1 & 7u);
	v2 += fib(v2 & 7u);
	return v0 ^ v1 ^ v2 ^ v3 ^ v4 ^ v5 ^ v6 ^ v7 ^ v8 ^ v9;
}

/* More than four arguments and a struct by value: stack arguments. */
struct pair {
	uint32_t x;
	uint32_t y;
	uint16_t z;
};

static uint32_t __attribute__((noinline)) six(uint32_t a, uint32_t b, uint32_t c, uint32_t d,
					       uint32_t e, uint32_t f)
{
	return a + b * 3u + (c ^ d) + (e << 1) - f;
}

static uint32_t __attribute__((noinline)) by_value(struct pair p, uint32_t k)
{
	return p.x * k + p.y - p.z;
}

static uint32_t crc32(const uint8_t *p, int n)
{
	uint32_t c = 0xffffffffu;

	for (int i = 0; i < n; i++) {
		c ^= p[i];
		for (int k = 0; k < 8; k++) {
			c = (c >> 1) ^ (0xedb88320u & -(c & 1u));
		}
	}
	return ~c;
}

void sem_run(void)
{
	uint32_t s = seed;
	uint32_t h[N_RESULTS] = {0};
	int8_t sb[16];
	int16_t sh[16];
	struct blob x, y;

	for (int i = 0; i < 200; i++) {
		uint32_t a = lcg(&s), b = lcg(&s);
		int32_t sa = (int32_t)a, sbv = (int32_t)b;
		uint32_t sh_n = b & 31u;
		uint64_t a64 = ((uint64_t)a << 32) | lcg(&s), b64 = ((uint64_t)b << 32) | lcg(&s);
		int64_t sa64 = (int64_t)a64;

		h[0] = mix(h[0], a + b);
		h[1] = mix(h[1], a - b);
		h[2] = mix(h[2], a * b);
		h[3] = mix(h[3], b ? a / b : 0u);
		h[4] = mix(h[4], b ? a % b : 0u);
		h[5] = mix(h[5], (b & 0xffffu) ? a / (b & 0xffffu) : 0u);
		h[6] = mix(h[6], sbv != 0 && !(sa == INT32_MIN && sbv == -1) ? (uint32_t)(sa / sbv) : 0u);
		h[7] = mix(h[7], sbv != 0 && !(sa == INT32_MIN && sbv == -1) ? (uint32_t)(sa % sbv) : 0u);
		h[8] = mix(h[8], (uint32_t)((sa >> 16) / ((sbv >> 24) | 1)));
		h[9] = mix(h[9], a << sh_n);
		h[10] = mix(h[10], a >> sh_n);
		h[11] = mix(h[11], (uint32_t)(sa >> sh_n));
		h[12] = mix(h[12], (a >> sh_n) | (a << ((32u - sh_n) & 31u)));
		h[13] = mix(h[13], (uint32_t)(a < b) | (uint32_t)(sa < sbv) << 1 | (uint32_t)(a <= b) << 2 |
				   (uint32_t)(sa >= sbv) << 3 | (uint32_t)(a == b) << 4 |
				   (uint32_t)(sa > 0) << 5 | (uint32_t)(a > 0x80000000u) << 6);
		h[14] = mix(h[14], (uint32_t)(a64 + b64) ^ (uint32_t)((a64 + b64) >> 32));
		h[15] = mix(h[15], (uint32_t)(a64 - b64) ^ (uint32_t)((a64 - b64) >> 32));
		h[16] = mix(h[16], (uint32_t)(a64 * b64) ^ (uint32_t)((a64 * b64) >> 32));
		h[17] = mix(h[17], (uint32_t)(a64 / (b64 | 1u)) ^ (uint32_t)((a64 % (b64 | 1u)) >> 3));
		h[18] = mix(h[18], (uint32_t)(sa64 / ((int64_t)(sbv | 1))));
		h[19] = mix(h[19], (uint32_t)(a64 << (sh_n + 7u)) ^ (uint32_t)(a64 >> (sh_n + 9u)));
		h[20] = mix(h[20], (uint32_t)(sa64 >> (sh_n + 20u)));
		h[21] = mix(h[21], (uint32_t)(a64 < b64) | (uint32_t)(sa64 < (int64_t)b64) << 1);
		h[22] = mix(h[22], (uint32_t)pick((int)b, sa));
		h[23] = mix(h[23], ops[b % 3u](a, b));
		h[24] = mix(h[24], (uint32_t)__builtin_popcount(a) | (uint32_t)(a ? __builtin_clz(a) : 32) << 8);
		h[25] = mix(h[25], (uint32_t)(int32_t)(int8_t)a + (uint32_t)(int32_t)(int16_t)b);
		h[26] = mix(h[26], (uint32_t)(uint8_t)(a + b) * (uint16_t)(a ^ b));
		h[27] = mix(h[27], (uint32_t)((int64_t)sa * (int64_t)sbv >> 20));
		h[28] = mix(h[28], (uint32_t)(((uint64_t)a * b) >> 32));
		for (int k = 0; k < 16; k++) {
			sb[k] = (int8_t)(a >> k);
			sh[k] = (int16_t)(b >> k);
		}
		int32_t acc = 0;

		for (int k = 0; k < 16; k++) {
			acc += sb[k] * sh[15 - k];
		}
		h[29] = mix(h[29], (uint32_t)acc);
		for (int k = 0; k < 7; k++) {
			x.a[k] = a + (uint32_t)k * b;
		}
		for (int k = 0; k < 5; k++) {
			x.b[k] = (uint8_t)(b >> k);
		}
		x.c = (int16_t)(a >> 3);
		y = x;
		h[30] = mix(h[30], y.a[6] ^ y.b[4] ^ (uint32_t)y.c);
		h[31] = mix(h[31], a > b ? a - b : b - a);
		unsigned int j = b & 7u;
		int32_t lo = (int32_t)(int8_t)a, hi = (int32_t)(int8_t)b;
		uint32_t steps = 0;

		h[35] = mix(h[35], vu16[j] + (uint32_t)vs16[j] + (uint32_t)vs8[j] + (uint32_t)vs16[7u - j]);
		for (int32_t k = lo; k <= hi; k += 3) {
			steps++;
		}
		h[36] = mix(h[36], steps);
		h[37] = mix(h[37], spill(a, b));
		h[38] = mix(h[38], six(a, b, a ^ b, a + b, b >> 5, a >> 7));
		h[39] = mix(h[39], by_value((struct pair){a, b, (uint16_t)(a ^ b)}, b | 1u));
	}
	h[32] = fib(18);
	h[33] = crc32((const uint8_t *)h, 32 * 4);
	h[34] = (uint32_t)pick(3, INT32_MIN + 1) ^ (uint32_t)(-(int32_t)seed);
	for (int i = 0; i < N_RESULTS; i++) {
		results[i] = h[i];
	}
}

#ifdef SEM_HOST
#include <stdio.h>
int main(void)
{
	sem_run();
	for (int i = 0; i < N_RESULTS; i++) {
		printf("%08x\n", results[i]);
	}
	return 0;
}
#else
void sem_main(void)
{
	sem_run();
	done = 0x600dc0deu;
	for (;;) {
	}
}
#endif

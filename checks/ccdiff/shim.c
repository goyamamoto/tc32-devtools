/*
 * Freestanding support for Csmith programs on TC32 (ccdiff.py): the entry
 * that start.S calls, and the few C library functions the programs use
 * (printf, strcmp, fabs, fabsf).
 * The program ends by printing "checksum = %X"; printf() here keeps that
 * value in `checksum` (an external call, so the optimizer cannot drop the
 * computation), and `done` tells the emulator that main() returned.
 *
 * SPDX-License-Identifier: Apache-2.0
 */
#include <stdarg.h>
#include <stddef.h>
#include <stdint.h>

volatile uint32_t done;
volatile uint32_t checksum;
volatile uint32_t printed;
int main(int argc, char *argv[]);

void sem_main(void)
{
	main(1, 0);
	done = 0x600DC0DEu;
}

int printf(const char *fmt, ...)
{
	static const char want[] = "checksum = ";
	va_list ap;
	size_t i;

	for (i = 0; want[i] != '\0' && fmt[i] == want[i]; i++) {
	}
	if (want[i] == '\0') {
		va_start(ap, fmt);
		checksum = va_arg(ap, unsigned int);
		va_end(ap);
		printed++;
	}
	return 0;
}

/* Csmith's float helpers call these; builds at -O0 do not inline them.
 * Clearing the sign bit needs no floating-point helper. */
double fabs(double x)
{
	union { double d; uint64_t u; } v = { x };

	v.u &= ~(1ull << 63);
	return v.d;
}

float fabsf(float x)
{
	union { float f; uint32_t u; } v = { x };

	v.u &= ~(1u << 31);
	return v.f;
}

int strcmp(const char *a, const char *b)
{
	while (*a != '\0' && *a == *b) {
		a++;
		b++;
	}
	return (unsigned char)*a - (unsigned char)*b;
}

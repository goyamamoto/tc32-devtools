/* memset/memcpy/memmove for the freestanding semantics test and ccdiff. SPDX-License-Identifier: Apache-2.0 */
#include <stddef.h>

void *memset(void *d, int c, size_t n)
{
	unsigned char *p = d;

	while (n-- > 0) {
		*p++ = (unsigned char)c;
	}
	return d;
}

void *memcpy(void *d, const void *s, size_t n)
{
	unsigned char *p = d;
	const unsigned char *q = s;

	while (n-- > 0) {
		*p++ = *q++;
	}
	return d;
}

void *memmove(void *d, const void *s, size_t n)
{
	unsigned char *p = d;
	const unsigned char *q = s;

	if (p < q) {
		while (n-- > 0) {
			*p++ = *q++;
		}
	} else {
		while (n-- > 0) {
			p[n] = q[n];
		}
	}
	return d;
}

/* The string functions Csmith programs use (ccdiff). SPDX-License-Identifier: Apache-2.0 */
#ifndef CCDIFF_STRING_H
#define CCDIFF_STRING_H
#include <stddef.h>
void *memcpy(void *d, const void *s, size_t n);
void *memset(void *d, int c, size_t n);
int strcmp(const char *a, const char *b);
#endif

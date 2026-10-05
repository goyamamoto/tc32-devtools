/*
 * Rotates as cryptographic and checksum code writes them: SHA-256's sigma
 * function, an MD5 step, a variable-amount rotate and a rotating checksum
 * loop. Csmith never generates the rotate idiom, so the 866 Csmith programs
 * that make the "Telink gcc" column of compiler/vendor_forms.txt contain no
 * rotate; this file, built by Telink's own gcc (build.sh), shows what that
 * compiler emits for them: trotrs, at -O0, -O2 and with the SDK's flags
 * (out/rot_*.dis). Its objects are part of the evidence table.
 *
 * SPDX-License-Identifier: Apache-2.0
 */
#include <stdint.h>
#define ROTR(x, n) (((x) >> (n)) | ((x) << (32 - (n))))
#define ROTL(x, n) (((x) << (n)) | ((x) >> (32 - (n))))
uint32_t sha_sigma0(uint32_t x) { return ROTR(x, 2) ^ ROTR(x, 13) ^ ROTR(x, 22); }
uint32_t md5_step(uint32_t a, uint32_t b, uint32_t k, unsigned s) { return b + ROTL(a + k, s); }
uint32_t rotv(uint32_t x, unsigned n) { return ROTR(x, n & 31); }
uint32_t checksum(const uint8_t *p, int n) { uint32_t c = 0; for (int i = 0; i < n; i++) c = ROTL(c, 5) ^ p[i]; return c; }

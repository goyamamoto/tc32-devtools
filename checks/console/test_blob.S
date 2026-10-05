/*
 * A blob for the --blob check of image_check.py and the Go image-check: 16
 * bytes of data in an executable section under the object symbol
 * test_blob. checks/console/build.sh writes its manifest (SHA-256 of these
 * bytes) and a wrong one. The $d is written out: LLVM's assembler emits no
 * mapping symbol for a section that holds only data, and without one the
 * bytes would be classified by whatever mapping symbol the linker placed
 * before them.
 * SPDX-License-Identifier: Apache-2.0
 */
	.section .text.test_blob, "ax", %progbits
	.balign 4
	.global test_blob
	.type test_blob, %object
$d:
test_blob:
	.byte 0x70, 0xb5, 0x04, 0x46, 0x0d, 0x46, 0x16, 0x46, 0x00, 0x20, 0x70, 0xbd, 0x00, 0xbf, 0x00, 0xbf
	.size test_blob, 16

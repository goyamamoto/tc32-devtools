// Package tc32isa holds the TC32 instruction encoding as far as it differs
// from ARMv4T Thumb.
//
// TC32 (Telink TLSR825x/827x and others) executes the Thumb-1 instruction
// set with the top five bits of each 16-bit instruction assigned differently;
// the other eleven bits, and so every register, immediate and branch field,
// are the same. TOP maps the TC32 value of those five bits to the Thumb
// value. It is the table of ../../common/tc32isa.py (the reference implementation),
// which was checked against Telink's own tc32-elf-objdump on all 65,536
// values and 65,536 BL pairs (checks/isa_check.py); the test in this package
// reads that file and requires the two tables to be equal.
//
// TC32 also has instructions of its own in Thumb's undefined space:
//
//	0x6bc0-0x6bdf  tmcsr / tmrcs / tmssr / tmrss   (CPSR/SPSR <-> register)
//	0x6800-0x69ff  treti {list}                   (pop, then CPSR = SPSR)
//	0xcf00-0xcfff  tserv                           (software interrupt)
//
// SPDX-License-Identifier: Apache-2.0
package tc32isa

// TOP maps the TC32 top five bits to the Thumb top five bits.
var TOP = [32]uint16{
	0x08, 0x09, 0x0a, 0x0b, 0x10, 0x11, 0x12, 0x13, 0x0e, 0x0f, 0x0c, 0x0d, 0x16, 0x17, 0x14, 0x15,
	0x1c, 0x1d, 0x1e, 0x1f, 0x04, 0x05, 0x06, 0x07, 0x1a, 0x1b, 0x18, 0x19, 0x02, 0x03, 0x00, 0x01,
}

// INV maps the Thumb top five bits to the TC32 top five bits.
var INV [32]uint16

func init() {
	for tc, th := range TOP {
		INV[th] = uint16(tc)
	}
}

// ToThumb returns the Thumb halfword with the same meaning as TC32 halfword hw.
func ToThumb(hw uint16) uint16 { return TOP[hw>>11]<<11 | hw&0x7FF }

// ToTC32 returns the TC32 halfword with the same meaning as Thumb halfword hw.
func ToTC32(hw uint16) uint16 { return INV[hw>>11]<<11 | hw&0x7FF }

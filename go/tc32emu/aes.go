// SPDX-License-Identifier: Apache-2.0
package tc32emu

// The AES block (DS-TLSR8278 15.4): the Go port of the Python machine's,
// which is the BLE radio model's (go/ble/radio.go keeps its own when
// attached). 0x540 bit 0 decrypt (1) or encrypt (0); a write to 0x540
// starts a block (bit 1: data wanted); the key at 0x550-0x55f in standard
// AES order; four u32 written to 0x548 are the input, after the fourth bit 2
// reads 1 and four reads of 0x548 give the output.

import (
	"encoding/binary"

	"github.com/goyamamoto/tc32-devtools/go/ble/aes128"
)

func (m *Machine) aesWrite(o int, val uint32) {
	r := m.Regs
	if o == 0x540 {
		r[0x540] = byte(val&0x01) | 0x02
		m.aesIn, m.aesOut = nil, nil
		return
	}
	var w [4]byte
	binary.LittleEndian.PutUint32(w[:], val)
	m.aesIn = append(m.aesIn, w[:]...)
	if len(m.aesIn) == 16 {
		key := append([]byte(nil), r[0x550:0x560]...)
		if r[0x540]&1 != 0 {
			m.aesOut = aes128.Decrypt(key, m.aesIn)
		} else {
			m.aesOut = aes128.Encrypt(key, m.aesIn)
		}
		m.aesIn = nil
		r[0x540] = (r[0x540] & 0x01) | 0x04
	}
}

func (m *Machine) aesRead() uint32 {
	var w [4]byte
	copy(w[:], m.aesOut)
	if len(m.aesOut) > 4 {
		m.aesOut = m.aesOut[4:]
	} else {
		m.aesOut = nil
	}
	return binary.LittleEndian.Uint32(w[:])
}

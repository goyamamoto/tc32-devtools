// Package armattr reads the core an ARM ELF file names in its build
// attributes (Tag_CPU_name).
//
// It is the Go implementation of ../../common/armattr.py. An object or link
// built with -mcpu=tc32 (llvm-tc32) holds its Thumb-1 instructions in TC32's
// encoding and names the core "tc32"; one built for an ARM core holds Thumb's
// encoding and names that core. A linked ELF keeps the attributes of its first
// input (ld.lld).
//
// SPDX-License-Identifier: Apache-2.0
package armattr

import (
	"bytes"
	"debug/elf"
	"encoding/binary"
	"errors"
	"strings"
)

const (
	shtARMAttributes = 0x70000003
	tagFile          = 1
	tagCPUName       = 5
)

func uleb(b []byte, i int) (uint64, int, error) {
	var v uint64
	var shift uint
	for {
		if i >= len(b) {
			return 0, i, errors.New("truncated ULEB128")
		}
		c := b[i]
		i++
		v |= uint64(c&0x7F) << shift
		shift += 7
		if c&0x80 == 0 {
			return v, i, nil
		}
	}
}

func ntbs(b []byte, i int) (string, int, error) {
	j := bytes.IndexByte(b[i:], 0)
	if j < 0 {
		return "", i, errors.New("unterminated string")
	}
	return string(b[i : i+j]), i + j + 1, nil
}

// CPUName returns Tag_CPU_name of the file-wide aeabi attributes: ok is false
// when the file has no attributes section, and name is "" when the tag is
// absent.
func CPUName(data []byte) (name string, ok bool, err error) {
	f, err := elf.NewFile(bytes.NewReader(data))
	if err != nil {
		return "", false, err
	}
	if f.Class != elf.ELFCLASS32 || f.Data != elf.ELFDATA2LSB {
		return "", false, errors.New("not a little-endian ELF32 file")
	}
	for _, s := range f.Sections {
		if uint32(s.Type) != shtARMAttributes {
			continue
		}
		sec, err := s.Data()
		if err != nil {
			return "", true, err
		}
		return cpuName(sec), true, nil
	}
	return "", false, nil
}

func cpuName(sec []byte) string {
	if len(sec) == 0 || sec[0] != 0x41 {
		return ""
	}
	i := 1
	for i+4 <= len(sec) {
		length := int(binary.LittleEndian.Uint32(sec[i:]))
		if length < 5 || i+length > len(sec) {
			break
		}
		end := i + length
		vendor, j, err := ntbs(sec, i+4)
		if err != nil {
			return ""
		}
		for vendor == "aeabi" && j < end {
			tag, k, err := uleb(sec, j)
			if err != nil || k+4 > len(sec) {
				return ""
			}
			subEnd := j + int(binary.LittleEndian.Uint32(sec[k:]))
			k += 4
			if tag == tagFile {
				for k < subEnd {
					var t uint64
					if t, k, err = uleb(sec, k); err != nil {
						return ""
					}
					switch {
					case t == tagCPUName:
						s, _, err := ntbs(sec, k)
						if err != nil {
							return ""
						}
						return s
					case t == 32: // Tag_compatibility: ULEB, then NTBS
						if _, k, err = uleb(sec, k); err == nil {
							_, k, err = ntbs(sec, k)
						}
					case t == 4 || t == 65 || t == 67 || (t > 32 && t&1 == 1):
						_, k, err = ntbs(sec, k)
					default:
						_, k, err = uleb(sec, k)
					}
					if err != nil {
						return ""
					}
				}
			}
			if subEnd <= j {
				return ""
			}
			j = subEnd
		}
		i = end
	}
	return ""
}

// IsTC32 reports whether the file names the core tc32 (its code is in TC32's
// encoding).
func IsTC32(data []byte) bool {
	name, _, err := CPUName(data)
	return err == nil && strings.EqualFold(name, "tc32")
}

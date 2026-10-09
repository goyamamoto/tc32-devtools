// Package elf2bin writes the flash image of a linked TC32 ELF: the direct path.
//
// It is the Go implementation of ../../compiler/elf2bin.py, the reference: the
// same layout and refusals, so that the two write the same image from the same
// ELF (checks/run_checks.sh compares them byte for byte). On the direct path
// llvm-tc32 writes TC32 machine code itself (clang -mcpu=tc32 -mthumb) and
// ld.lld links it; the image is each PT_LOAD segment's file bytes at its
// physical address, 0xff between segments, as thumb2tc32 lays out the image
// of the Thumb path, with nothing re-encoded. The ELF must name the core tc32
// (Tag_CPU_name).
//
// SPDX-License-Identifier: Apache-2.0
package elf2bin

import (
	"bytes"
	"debug/elf"
	"fmt"
	"strings"

	"github.com/goyamamoto/tc32-devtools/go/armattr"
)

// Refused is returned for an ELF the tool must not write an image of.
type Refused struct{ Msg string }

func (r *Refused) Error() string { return r.Msg }

// Convert returns the image of the ELF in data and the number of PT_LOAD
// segments written.
func Convert(data []byte) ([]byte, int, error) {
	cpu, _, err := armattr.CPUName(data)
	if err != nil {
		return nil, 0, &Refused{err.Error()}
	}
	if !strings.EqualFold(cpu, "tc32") {
		return nil, 0, &Refused{fmt.Sprintf("Tag_CPU_name is %s, not 'tc32': not built with -mcpu=tc32 "+
			"(a Thumb ELF goes through thumb2tc32.py)", pyRepr(cpu))}
	}
	f, err := elf.NewFile(bytes.NewReader(data))
	if err != nil {
		return nil, 0, &Refused{err.Error()}
	}
	var image []byte
	n := 0
	for _, p := range f.Progs {
		if p.Type != elf.PT_LOAD || p.Filesz == 0 {
			continue
		}
		end := p.Paddr + p.Filesz
		if uint64(len(image)) < end {
			image = append(image, bytes.Repeat([]byte{0xFF}, int(end)-len(image))...)
		}
		if p.Off+p.Filesz > uint64(len(data)) {
			return nil, 0, &Refused{fmt.Sprintf("segment at 0x%x runs past the end of the file", p.Paddr)}
		}
		copy(image[p.Paddr:end], data[p.Off:p.Off+p.Filesz])
		n++
	}
	if n == 0 {
		return nil, 0, &Refused{"no PT_LOAD segment with file bytes"}
	}
	return image, n, nil
}

// pyRepr writes a name as Python's repr() of a str does for the names here.
func pyRepr(s string) string {
	if strings.Contains(s, "'") && !strings.Contains(s, "\"") {
		return "\"" + s + "\""
	}
	return "'" + strings.ReplaceAll(s, "'", "\\'") + "'"
}

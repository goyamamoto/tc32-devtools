// Package thumb2tc32 re-encodes a Thumb-1 (ARMv4T) ELF as TC32 machine code.
//
// It is the Go implementation of ../../compiler/thumb2tc32.py, the reference:
// the same rules, refusals and counts, so that the two write the same image
// from the same ELF (checks/run_checks.sh compares them byte for byte).
//
// The ELF's mapping symbols tell code ($t) from data ($d: literal pools, jump
// tables, the boot header); only code is re-encoded. Data objects (STT_OBJECT
// symbols with a size) inside executable sections are left alone too. Input
// sections without mapping symbols (.rodata) must not be linked into an
// executable output section: the tool cannot tell them from code there. It
// refuses what TC32 cannot run or would read differently: ARM code ($a), BLX,
// and 32-bit Thumb-2 instructions other than a BL pair. Two Thumb instructions
// are written as others, so that the image uses only instruction forms
// Telink's own code uses: lsls rd, rm, #0 (clang's register move on ARMv4T)
// as adds rd, rm, #0 (they differ in C and V only, and clang emits the move
// only where the flags are dead), and udf (clang's trap) as b . (a branch to
// itself). A halfword 0x0000 in code is left as it is (fill from .org or
// .space). Addresses of Thumb code that the link writes as data (function
// pointers) keep bit 0 set, as Telink's toolchain writes them too.
//
// SPDX-License-Identifier: Apache-2.0
package thumb2tc32

import (
	"bytes"
	"debug/elf"
	"encoding/binary"
	"fmt"
	"sort"

	"github.com/goyamamoto/tc32-devtools/go/tc32isa"
)

// Refused is returned for an ELF the tool must not convert.
type Refused struct{ Msg string }

func (r *Refused) Error() string { return r.Msg }

func refuse(format string, a ...any) error { return &Refused{fmt.Sprintf(format, a...)} }

// Kind says what Rewrite did to a halfword.
type Kind int

const (
	KindNone Kind = iota // re-encoded as it is
	KindZero             // 0x0000: fill, kept as 0x0000
	KindMovs             // lsls rd, rm, #0 -> adds rd, rm, #0
	KindUdf              // udf -> b .
)

// Counts is what a conversion did.
type Counts struct {
	Code, Data, Movs, Udf, Zero, TC32Only int
}

// Rewrite returns the Thumb halfword to write for hw and what was rewritten.
func Rewrite(hw uint16) (uint16, Kind) {
	switch {
	case hw == 0x0000:
		return hw, KindZero
	case hw&0xFFC0 == 0x0000:
		return 0x1C00 | hw&0x3F, KindMovs
	case hw&0xFF00 == 0xDE00:
		return 0xE7FE, KindUdf
	}
	return hw, KindNone
}

// Encode returns the TC32 halfword written for a Thumb code halfword (not
// the first half of a BL pair).
func Encode(hw uint16) uint16 {
	hw, kind := Rewrite(hw)
	if kind == KindZero {
		return hw
	}
	return tc32isa.ToTC32(hw)
}

type mark struct {
	addr uint32
	kind byte
}

type span struct{ start, end uint32 }

type segment struct{ vaddr, paddr, filesz uint32 }

// Convert re-encodes the ELF in data and returns the flat image from address
// 0 (as llvm-objcopy -O binary lays it out, with code re-encoded).
func Convert(data []byte) ([]byte, Counts, error) {
	var counts Counts
	f, err := elf.NewFile(bytes.NewReader(data))
	if err != nil {
		return nil, counts, refuse("not an ELF file: %v", err)
	}
	if f.Class != elf.ELFCLASS32 || f.Data != elf.ELFDATA2LSB {
		return nil, counts, refuse("not a little-endian ELF32 file")
	}

	// Mapping symbols and data objects per section index.
	marks := map[elf.SectionIndex][]mark{}
	objects := map[elf.SectionIndex][]span{}
	syms, err := f.Symbols()
	if err != nil && err != elf.ErrNoSymbols {
		return nil, counts, refuse("symbols: %v", err)
	}
	for _, s := range syms {
		n := s.Name
		if len(n) >= 2 && n[0] == '$' && (n[1] == 't' || n[1] == 'd' || n[1] == 'a') && (len(n) == 2 || n[2] == '.') {
			marks[s.Section] = append(marks[s.Section], mark{uint32(s.Value) &^ 1, n[1]})
		} else if elf.ST_TYPE(s.Info) == elf.STT_OBJECT && s.Size != 0 {
			objects[s.Section] = append(objects[s.Section], span{uint32(s.Value), uint32(s.Value + s.Size)})
		}
	}
	for _, v := range marks {
		sort.Slice(v, func(i, j int) bool {
			if v[i].addr != v[j].addr {
				return v[i].addr < v[j].addr
			}
			return v[i].kind < v[j].kind
		})
	}

	// The flash image: PT_LOAD segments at their physical addresses.
	var image []byte
	var segments []segment
	for _, p := range f.Progs {
		if p.Type != elf.PT_LOAD || p.Filesz == 0 {
			continue
		}
		end := p.Paddr + p.Filesz
		if uint64(len(image)) < end {
			image = append(image, bytes.Repeat([]byte{0xFF}, int(end)-len(image))...)
		}
		if p.Off+p.Filesz > uint64(len(data)) {
			return nil, counts, refuse("segment at 0x%x runs past the end of the file", p.Paddr)
		}
		copy(image[p.Paddr:end], data[p.Off:p.Off+p.Filesz])
		segments = append(segments, segment{uint32(p.Vaddr), uint32(p.Paddr), uint32(p.Filesz)})
	}
	at := func(vaddr uint32) (int, error) {
		for _, s := range segments {
			if s.vaddr <= vaddr && vaddr < s.vaddr+s.filesz {
				return int(s.paddr + vaddr - s.vaddr), nil
			}
		}
		return 0, refuse("0x%x is in no loaded segment", vaddr)
	}
	hwAt := func(vaddr uint32) (uint16, int, error) {
		o, err := at(vaddr)
		if err != nil {
			return 0, 0, err
		}
		if o+2 > len(image) {
			return 0, 0, refuse("0x%x is past the end of the image", vaddr)
		}
		return binary.LittleEndian.Uint16(image[o:]), o, nil
	}

	for idx, s := range f.Sections {
		if s.Flags&elf.SHF_ALLOC == 0 || s.Type == elf.SHT_NOBITS || s.Size == 0 || s.Flags&elf.SHF_EXECINSTR == 0 {
			continue
		}
		secIdx := elf.SectionIndex(idx)
		addr, size := uint32(s.Addr), uint32(s.Size)
		mk := marks[secIdx]
		if len(mk) == 0 {
			mk = []mark{{addr, 't'}}
		}
		if mk[0].addr > addr {
			return nil, counts, refuse("%s: no mapping symbol at its start", s.Name)
		}
		objs := objects[secIdx]
		for j, m := range mk {
			end := addr + size
			if j+1 < len(mk) {
				end = mk[j+1].addr
			}
			switch m.kind {
			case 'a':
				return nil, counts, refuse("ARM code in %s at 0x%x: TC32 has no ARM state", s.Name, m.addr)
			case 'd':
				counts.Data += int(end - m.addr)
				continue
			}
			a := m.addr
			for a < end {
				if o, ok := inside(objs, a); ok {
					stop := o.end
					if end < stop {
						stop = end
					}
					counts.Data += int(stop - a)
					a = stop
					continue
				}
				hw, off, err := hwAt(a)
				if err != nil {
					return nil, counts, err
				}
				top := hw >> 11
				if hw&0xFE00 == 0xB800 || (0xBBC0 <= hw && hw <= 0xBBDF) {
					counts.TC32Only++ // .inst.n from tc32asm2thumb.py: treti, tmcsr/tmrcs/tmssr/tmrss
				}
				if hw&0xFF87 == 0x4780 {
					return nil, counts, refuse("0x%x: blx (ARMv5T)", a)
				}
				if top == 0x1E { // BL prefix: its suffix must follow
					nxt, off2, err := hwAt(a + 2)
					if err != nil {
						return nil, counts, err
					}
					if nxt>>11 != 0x1F {
						return nil, counts, refuse("0x%x: BL prefix 0x%04x not followed by a BL suffix (0x%04x)", a, hw, nxt)
					}
					binary.LittleEndian.PutUint16(image[off:], tc32isa.ToTC32(hw))
					binary.LittleEndian.PutUint16(image[off2:], tc32isa.ToTC32(nxt))
					a += 4
					counts.Code += 2
					continue
				}
				if top == 0x1D || top == 0x1F {
					return nil, counts, refuse("0x%x: 0x%04x is a 32-bit Thumb-2 or lone BL half", a, hw)
				}
				w, kind := Rewrite(hw)
				switch kind {
				case KindMovs:
					counts.Movs++
				case KindUdf:
					counts.Udf++
				case KindZero:
					counts.Zero++
				}
				if kind == KindZero {
					binary.LittleEndian.PutUint16(image[off:], w)
				} else {
					binary.LittleEndian.PutUint16(image[off:], tc32isa.ToTC32(w))
				}
				a += 2
				counts.Code++
			}
		}
	}
	return image, counts, nil
}

func inside(objs []span, a uint32) (span, bool) {
	for _, o := range objs {
		if o.start <= a && a < o.end {
			return o, true
		}
	}
	return span{}, false
}

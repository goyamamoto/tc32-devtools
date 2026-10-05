// tc32-flow: find the code of a raw TC32 image by following its control
// flow, and report what that code does. The Go build of
// compiler/tc32_flow.py, with the same table line for line.
//
// Usage: tc32-flow <image>... [--forms-out forms.tsv]
//
// SPDX-License-Identifier: Apache-2.0
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/goyamamoto/tc32-devtools/go/tc32isa"
)

const sram = 0x840000

var alu = strings.Fields("and eor lsl lsr asr adc sbc ror tst neg cmp cmn orr mul bic mvn")

func sext(v uint32, bits uint) int64 {
	if v&(1<<(bits-1)) != 0 {
		return int64(v) - (1 << bits)
	}
	return int64(v)
}

// form is forms_check.form (the same classification as go/cmd/forms-check).
func form(hw uint16) string {
	if hw&0xFFE0 == 0x6BC0 {
		return []string{"tmcsr", "tmrcs", "tmssr", "tmrss"}[(hw>>3)&3]
	}
	if hw&0xFE00 == 0x6800 {
		s := "treti"
		if hw&0x100 != 0 {
			s += " pc"
		}
		if hw&0xFF == 0 {
			s += " no-lo"
		}
		return s
	}
	if hw&0xFF00 == 0xCF00 {
		return "tserv"
	}
	t := tc32isa.ToThumb(hw)
	top := t >> 11
	switch {
	case top <= 2:
		s := []string{"lsl", "lsr", "asr"}[top] + " imm"
		if (t>>6)&31 == 0 {
			s += " #0"
		}
		return s
	case top == 3:
		s := "add"
		if t&0x200 != 0 {
			s = "sub"
		}
		if t&0x400 != 0 {
			return s + " imm3"
		}
		return s + " reg"
	case top <= 7:
		return []string{"mov", "cmp", "add", "sub"}[top-4] + " imm8"
	case t>>10 == 0x10:
		return "alu " + alu[(t>>6)&15]
	case t>>10 == 0x11:
		op, rd, rm := (t>>8)&3, (t&7)|((t>>4)&8), (t>>3)&15
		name := []string{"add", "cmp", "mov", "bx"}[op] + " hi"
		if op == 3 && t&0x87 != 0 {
			return "bx with bit 7 or bits 2:0 set"
		}
		if rm == 15 || (rd == 15 && op < 2) {
			name += " reads pc"
		}
		if rd == 15 && (op == 0 || op == 2) {
			name += " writes pc"
		}
		if op < 3 && rd < 8 && rm < 8 {
			name += " (both low)"
		}
		return name
	case top == 9:
		return "ldr pc-rel"
	case top == 0x0A || top == 0x0B:
		return []string{"str", "strh", "strb", "ldrsb", "ldr", "ldrh", "ldrb", "ldrsh"}[(t>>9)&7] + " reg"
	case 0x0C <= top && top <= 0x11:
		return []string{"str", "ldr", "strb", "ldrb", "strh", "ldrh"}[top-0x0C] + " imm5"
	case top == 0x12 || top == 0x13:
		if top&1 != 0 {
			return "ldr sp"
		}
		return "str sp"
	case top == 0x14 || top == 0x15:
		if top&1 != 0 {
			return "add rd, sp"
		}
		return "add rd, pc"
	case t&0xFF00 == 0xB000:
		return "add/sub sp"
	case t&0xF600 == 0xB400:
		n := "push"
		if t&0x800 != 0 {
			n = "pop"
		}
		s := n
		if t&0x100 != 0 {
			if n == "pop" {
				s += " pc"
			} else {
				s += " lr"
			}
		}
		if t&0x1FF == 0 {
			s += " EMPTY"
		}
		return s
	case top == 0x18 || top == 0x19:
		rb := (t >> 8) & 7
		n := "stm"
		if top&1 != 0 {
			n = "ldm"
		}
		if t&0xFF == 0 {
			return n + " EMPTY"
		}
		if t>>rb&1 != 0 {
			return n + " base-in-list"
		}
		return n
	case top == 0x1A || top == 0x1B:
		return "b<cc> " + strings.Fields("eq ne cs cc mi pl vs vc hi ls ge lt gt le al sv")[(t>>8)&15]
	case top == 0x1C:
		return "b"
	case top == 0x1E:
		return "bl prefix"
	case top == 0x1F:
		return "bl suffix"
	}
	return "undefined"
}

// destOfLoad returns the register a load writes, or -1.
func destOfLoad(t uint16) int {
	top := t >> 11
	if top == 9 || top == 0x13 {
		return int((t >> 8) & 7)
	}
	if top == 0x0D || top == 0x0F || top == 0x11 || ((top == 0x0A || top == 0x0B) && (t>>9)&7 >= 3) {
		return int(t & 7)
	}
	return -1
}

// reads returns the low registers an instruction reads (as a bit set).
func reads(t uint16) uint32 {
	top := t >> 11
	lo := func(s uint) uint32 { return 1 << ((t >> s) & 7) }
	switch {
	case top <= 2:
		return lo(3)
	case top == 3:
		if t&0x400 != 0 {
			return lo(3)
		}
		return lo(3) | lo(6)
	case 5 <= top && top <= 7:
		return lo(8)
	case t>>10 == 0x10:
		op := (t >> 6) & 15
		if op == 9 || op == 15 {
			return lo(3)
		}
		return lo(0) | lo(3)
	case t>>10 == 0x11:
		op, rd, rm := (t>>8)&3, (t&7)|((t>>4)&8), (t>>3)&15
		var s uint32
		if op < 2 && rd < 8 {
			s |= 1 << rd
		}
		if rm < 8 {
			s |= 1 << rm
		}
		return s
	case top == 0x0A || top == 0x0B:
		s := lo(3) | lo(6)
		if (t>>9)&7 <= 2 {
			s |= lo(0)
		}
		return s
	case top == 0x0C || top == 0x0E || top == 0x10:
		return lo(3) | lo(0)
	case top == 0x0D || top == 0x0F || top == 0x11:
		return lo(3)
	case top == 0x12:
		return lo(8)
	case t&0xF600 == 0xB400 && t&0x800 == 0:
		return uint32(t & 0xFF)
	case top == 0x18:
		return lo(8) | uint32(t&0xFF)
	case top == 0x19:
		return lo(8)
	}
	return 0
}

type image struct {
	data   []byte
	mirror int64
}

func (im *image) offset(addr int64) (int64, bool) {
	if 0 <= addr && addr < int64(len(im.data)) {
		return addr, true
	}
	if sram <= addr && addr < sram+im.mirror && addr-sram < int64(len(im.data)) {
		return addr - sram, true
	}
	return 0, false
}

func (im *image) hw(off int64) uint16 { return binary.LittleEndian.Uint16(im.data[off:]) }

type result struct {
	code    map[int64]uint16
	literal map[int64]bool
	funcs   map[int64]bool
	bad     int
}

func walk(im *image) result {
	r := result{map[int64]uint16{}, map[int64]bool{}, map[int64]bool{0: true, 0x10: true}, 0}
	work := []int64{0, 0x10}
	n := int64(len(im.data))
	for len(work) > 0 {
		a := work[len(work)-1]
		work = work[:len(work)-1]
		for {
			if _, seen := r.code[a]; seen {
				break
			}
			if a+2 > n || r.literal[a] {
				if r.literal[a] {
					r.bad++ // code runs into a literal
				}
				break
			}
			hw := im.hw(a)
			r.code[a] = hw
			if hw&0xFFE0 == 0x6BC0 || hw&0xFF00 == 0xCF00 {
				a += 2
				continue
			}
			if hw&0xFE00 == 0x6800 { // treti
				break
			}
			t := tc32isa.ToThumb(hw)
			top := t >> 11
			if top == 9 {
				lit := ((a + 4) &^ 3) + int64(t&0xFF)*4
				r.literal[lit], r.literal[lit+2] = true, true
			}
			if top == 0x1C { // b
				if o, ok := im.offset(a + 4 + sext(uint32(t&0x7FF), 11)*2); ok {
					work = append(work, o)
				}
				break
			}
			if top == 0x1A || top == 0x1B {
				if (t>>8)&15 >= 14 {
					r.bad++ // undefined
					break
				}
				if o, ok := im.offset(a + 4 + sext(uint32(t&0xFF), 8)*2); ok {
					work = append(work, o)
				}
				a += 2
				continue
			}
			if top == 0x1E { // tjl
				if a+4 > n {
					break
				}
				t2 := tc32isa.ToThumb(im.hw(a + 2))
				if t2>>11 != 0x1F {
					r.bad++ // bl prefix without suffix
					break
				}
				r.code[a+2] = im.hw(a + 2)
				tgt := a + 4 + (sext(uint32(t&0x7FF), 11) << 12) + int64(t2&0x7FF)*2
				if o, ok := im.offset(tgt); ok {
					r.funcs[o] = true
					work = append(work, o)
				}
				a += 4
				continue
			}
			if top == 0x1F || top == 0x1D {
				r.bad++ // lone bl half
				break
			}
			if t>>10 == 0x11 {
				op, rd := (t>>8)&3, (t&7)|((t>>4)&8)
				if op == 3 || (rd == 15 && (op == 0 || op == 2)) {
					break
				}
			}
			if t&0xFF00 == 0xBD00 { // pop {.., pc}
				break
			}
			if (top == 0x16 || top == 0x17) && !(t&0xFF00 == 0xB000 || t&0xF600 == 0xB400) {
				r.bad++ // undefined
				break
			}
			a += 2
		}
	}
	return r
}

type analysis struct {
	bytes, code, funcs, loads, loaduse, bad, even, odd int
	cond                                               map[string]int
	forms                                              map[string]int
}

func analyse(path string) (analysis, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return analysis{}, err
	}
	if !bytes.Equal(data[8:12], []byte("KNLT")) {
		if i := bytes.Index(data, []byte("KNLT")); i >= 8 { // e.g. a Zigbee OTA file's header first
			data = data[i-8:]
		}
	}
	im := &image{data, int64(binary.LittleEndian.Uint32(data[0x0C:])&0xFFFF) * 16}
	r := walk(im)
	an := analysis{bytes: len(data), code: len(r.code) * 2, funcs: len(r.funcs), bad: r.bad,
		cond: map[string]int{}, forms: map[string]int{}}
	ccNames := strings.Fields("eq ne cs cc mi pl vs vc hi ls ge lt gt le")
	for a, hw := range r.code {
		f := form(hw)
		if prev, ok := r.code[a-2]; ok && tc32isa.ToThumb(prev)>>11 == 0x1E {
			f = "bl suffix"
		}
		an.forms[f]++
		t := tc32isa.ToThumb(hw)
		if d := destOfLoad(t); d >= 0 {
			if next, ok := r.code[a+2]; ok && tc32isa.ToThumb(next)>>11 != 0x1F {
				an.loads++
				if reads(tc32isa.ToThumb(next))&(1<<uint(d)) != 0 {
					an.loaduse++
				}
			}
		}
		if t>>12 == 0xD && (t>>8)&15 < 14 {
			an.cond[ccNames[(t>>8)&15]]++
		}
	}
	lits := make([]int64, 0, len(r.literal))
	for l := range r.literal {
		lits = append(lits, l)
	}
	sort.Slice(lits, func(i, j int) bool { return lits[i] < lits[j] })
	for _, lit := range lits {
		if lit%4 != 0 || lit+4 > int64(len(data)) {
			continue
		}
		v := int64(binary.LittleEndian.Uint32(data[lit:]))
		for _, base := range []int64{0, sram} {
			o := v - base
			if 0 <= o && o < int64(len(data)) && r.funcs[o&^1] && o&^1 != 0 {
				if v&1 != 0 {
					an.odd++
				} else {
					an.even++
				}
			}
		}
	}
	return an, nil
}

func main() {
	var images []string
	formsOut := ""
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		if args[i] == "--forms-out" {
			i++
			formsOut = args[i]
		} else {
			images = append(images, args[i])
		}
	}
	if len(images) == 0 {
		fmt.Fprintln(os.Stderr, "usage: tc32-flow <image>... [--forms-out forms.tsv]")
		os.Exit(2)
	}
	total := map[string]int{}
	fmt.Println("image\tbytes\tcode bytes reached\tfunctions\tloads followed\tnext uses the load\tGE/PL/LS" +
		"\tcode addresses in literals even/odd\tstops")
	for _, p := range images {
		an, err := analyse(p)
		if err != nil {
			fmt.Fprintln(os.Stderr, "tc32-flow:", err)
			os.Exit(1)
		}
		for k, v := range an.forms {
			total[k] += v
		}
		fmt.Printf("%s\t%d\t%d (%d%%)\t%d\t%d\t%d\t%d/%d/%d\t%d/%d\t%d\n", filepath.Base(p), an.bytes, an.code,
			100*an.code/an.bytes, an.funcs, an.loads, an.loaduse, an.cond["ge"], an.cond["pl"], an.cond["ls"],
			an.even, an.odd, an.bad)
	}
	if formsOut != "" {
		keys := make([]string, 0, len(total))
		for k := range total {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var sb strings.Builder
		for _, k := range keys {
			fmt.Fprintf(&sb, "%s\t%d\n", k, total[k])
		}
		if err := os.WriteFile(formsOut, []byte(sb.String()), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "tc32-flow:", err)
			os.Exit(1)
		}
	}
}

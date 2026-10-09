// Package formscheck is forms-check: instruction forms in an image that
// Telink's own code never uses, in its functions and in the code outside
// them that a $t mapping symbol marks. The Go build of
// compiler/forms_check.py's "check" subcommand, with the same report byte for
// byte (run_checks.sh compares them); the "evidence" subcommand, which
// builds vendor_forms.txt from Telink's disassembly, stays in Python.
//
// Usage: forms-check check <elf> [--thumb] [--evidence vendor_forms.txt]
//
// An ELF whose build attributes name the core (Tag_CPU_name) must agree with
// --thumb: tc32 (clang -mcpu=tc32, the direct path) without it, an ARM core
// (the Thumb path) with it; otherwise it stops with exit status 2.
//
// SPDX-License-Identifier: Apache-2.0
package formscheck

import (
	"bufio"
	"bytes"
	"debug/elf"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/goyamamoto/tc32-devtools/go/armattr"
	"github.com/goyamamoto/tc32-devtools/go/tc32isa"
	"github.com/goyamamoto/tc32-devtools/go/thumb2tc32"
)

var alu = strings.Fields("and eor lsl lsr asr adc sbc ror tst neg cmp cmn orr mul bic mvn")

// rawThumb: Thumb encodings an LLVM TC32 backend can leave untranslated.
var rawThumb = map[uint16]string{
	0xDEFE: "raw Thumb trap 0xdefe (TC32: ldm r6!, {r1-r7})",
	0x46C0: "raw Thumb nop 0x46c0 (TC32: strb r0, [r0, #27])",
}

func endsFlow(hw uint16) bool {
	t := tc32isa.ToThumb(hw)
	return t>>11 == 0x1C || t&0xFF00 == 0xBD00 || t&0xFF87 == 0x4700 || t&0xFF87 == 0x4687
}

// form is the form of a TC32 halfword (forms_check.form).
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

type function struct {
	name       string
	start, end uint32
	body       []byte
	base       uint32
	shndx      int
}

type markKey struct {
	shndx int
	addr  uint32
}

// elfFunctions: the STT_FUNC symbols of executable sections (a function
// without a size runs to the next symbol), and the mapping symbols.
func elfFunctions(data []byte) ([]function, map[markKey]byte, error) {
	f, err := elf.NewFile(bytes.NewReader(data))
	if err != nil {
		return nil, nil, err
	}
	syms, err := f.Symbols()
	if err != nil && err != elf.ErrNoSymbols {
		return nil, nil, err
	}
	type fn struct {
		name  string
		value uint32
		size  uint32
		shndx int
	}
	var funcs []fn
	maps := map[markKey]byte{}
	starts := map[int]map[uint32]bool{}
	for _, s := range syms {
		shndx := int(s.Section)
		if shndx >= len(f.Sections) || f.Sections[shndx].Flags&elf.SHF_EXECINSTR == 0 {
			continue
		}
		n := s.Name
		v := uint32(s.Value) &^ 1
		if len(n) >= 2 && (n[:2] == "$t" || n[:2] == "$d" || n[:2] == "$a") {
			maps[markKey{shndx, v}] = n[1]
		} else if strings.HasPrefix(n, "__cd_d") || strings.HasPrefix(n, "__cd_t") {
			maps[markKey{shndx, v}] = n[5]
		}
		if starts[shndx] == nil {
			starts[shndx] = map[uint32]bool{}
		}
		starts[shndx][v] = true
		if elf.ST_TYPE(s.Info) == elf.STT_FUNC {
			funcs = append(funcs, fn{n, v, uint32(s.Size), shndx})
		}
	}
	var out []function
	for _, fnc := range funcs {
		sec := f.Sections[fnc.shndx]
		end := fnc.value + fnc.size
		if fnc.size == 0 {
			end = uint32(sec.Addr + sec.Size)
			for x := range starts[fnc.shndx] {
				if x > fnc.value && x < end {
					end = x
				}
			}
		}
		body, err := sec.Data()
		if err != nil {
			return nil, nil, err
		}
		out = append(out, function{fnc.name, fnc.value, end, body, uint32(sec.Addr), fnc.shndx})
	}
	return out, maps, nil
}

type addrHw struct {
	addr uint32
	hw   uint16
}

func literals(code []addrHw) map[uint32]bool {
	lit := map[uint32]bool{}
	for _, c := range code {
		t := tc32isa.ToThumb(c.hw)
		if t>>11 == 9 {
			x := ((c.addr + 4) &^ 3) + uint32(t&0xFF)*4
			lit[x], lit[x+2] = true, true
		}
	}
	return lit
}

type object struct {
	shndx  int
	lo, hi uint32
}

// codeSections: the allocated executable sections with content, as entries
// named after the section that span it, and the sized data objects
// (STT_OBJECT) in executable sections.
func codeSections(data []byte) ([]function, []object, error) {
	f, err := elf.NewFile(bytes.NewReader(data))
	if err != nil {
		return nil, nil, err
	}
	var out []function
	for k, s := range f.Sections {
		if s.Type != elf.SHT_NOBITS && s.Flags&(elf.SHF_ALLOC|elf.SHF_EXECINSTR) == elf.SHF_ALLOC|elf.SHF_EXECINSTR && s.Size > 0 {
			body, err := s.Data()
			if err != nil {
				return nil, nil, err
			}
			out = append(out, function{s.Name, uint32(s.Addr), uint32(s.Addr + s.Size), body, uint32(s.Addr), k})
		}
	}
	syms, err := f.Symbols()
	if err != nil && err != elf.ErrNoSymbols {
		return nil, nil, err
	}
	var objects []object
	for _, s := range syms {
		shndx := int(s.Section)
		if elf.ST_TYPE(s.Info) == elf.STT_OBJECT && s.Size > 0 && shndx < len(f.Sections) &&
			f.Sections[shndx].Flags&elf.SHF_EXECINSTR != 0 {
			objects = append(objects, object{shndx, uint32(s.Value), uint32(s.Value + s.Size)})
		}
	}
	return out, objects, nil
}

// imageForms: (counts of forms, first place of each, number of functions,
// number of instructions outside them). With outside, also the code outside
// every function: the halfwords of executable sections that a $t mapping
// symbol marks as code, outside the functions' ranges and the sized data
// objects, other than 0x0000 (fill, as thumb2tc32 takes it).
func imageForms(data []byte, thumb, outside bool) (map[string]int, map[string]string, int, int, error) {
	funcs, maps, err := elfFunctions(data)
	if err != nil {
		return nil, nil, 0, 0, err
	}
	nfuncs := len(funcs)
	isOut := make([]bool, len(funcs))
	covered := map[markKey]bool{}
	var objects []object
	if outside {
		for _, fn := range funcs {
			for x := fn.start; x < fn.end; x += 2 {
				covered[markKey{fn.shndx, x}] = true
			}
		}
		secs, objs, err := codeSections(data)
		if err != nil {
			return nil, nil, 0, 0, err
		}
		objects = objs
		funcs = append(funcs, secs...)
		for range secs {
			isOut = append(isOut, true)
		}
	}
	nOutside := 0
	kinds := make([]markKey, 0, len(maps))
	for k := range maps {
		kinds = append(kinds, k)
	}
	sort.Slice(kinds, func(i, j int) bool {
		if kinds[i].shndx != kinds[j].shndx {
			return kinds[i].shndx < kinds[j].shndx
		}
		return kinds[i].addr < kinds[j].addr
	})
	seen, where := map[string]int{}, map[string]string{}
	done := map[markKey]bool{}
	for fi, fn := range funcs {
		out := isOut[fi]
		var code []addrHw
		hwAt := map[uint32]uint16{}
		for x := fn.start; x+1 < fn.end; x += 2 {
			if done[markKey{fn.shndx, x}] {
				continue
			}
			// The last mapping symbol at or before x in this section.
			i := sort.Search(len(kinds), func(k int) bool {
				return kinds[k].shndx > fn.shndx || (kinds[k].shndx == fn.shndx && kinds[k].addr > x)
			}) - 1
			var kind byte
			if i >= 0 && kinds[i].shndx == fn.shndx {
				kind = maps[kinds[i]]
			}
			if kind == 'd' {
				continue
			}
			off := int(x - fn.base)
			if off+2 > len(fn.body) {
				break
			}
			hw := binary.LittleEndian.Uint16(fn.body[off:])
			if out {
				if covered[markKey{fn.shndx, x}] || kind != 't' || hw == 0 {
					continue
				}
				inObject := false
				for _, o := range objects {
					if o.shndx == fn.shndx && o.lo <= x && x < o.hi {
						inObject = true
						break
					}
				}
				if inObject {
					continue
				}
			}
			if thumb {
				hw = thumb2tc32.Encode(hw)
			}
			code = append(code, addrHw{x, hw})
			hwAt[x] = hw
		}
		lit := literals(code)
		for _, c := range code {
			x, hw := c.addr, c.hw
			if lit[x] {
				continue
			}
			done[markKey{fn.shndx, x}] = true
			prev, hasPrev := hwAt[x-2]
			_, isRaw := rawThumb[hw]
			if isRaw || hw == 0xD4D4 {
				if hasPrev {
					_, prevRaw := rawThumb[prev]
					if endsFlow(prev) || prevRaw || prev == 0xD4D4 {
						continue // padding
					}
				}
				if isRaw && !thumb {
					seen[rawThumb[hw]]++
					if _, ok := where[rawThumb[hw]]; !ok {
						where[rawThumb[hw]] = fmt.Sprintf("%s+0x%x", fn.name, x-fn.start)
					}
					if out {
						nOutside++
					}
					continue
				}
			}
			f := form(hw)
			seen[f]++
			if _, ok := where[f]; !ok {
				where[f] = fmt.Sprintf("%s+0x%x", fn.name, x-fn.start)
			}
			if out {
				nOutside++
			}
		}
	}
	return seen, where, nfuncs, nOutside, nil
}

// ParseEvidence reads an evidence table (vendor_forms.txt): a form, then the
// counts in Telink's libraries, the executed code of an existing TC32 binary
// and Telink gcc's programs, tab-separated; # starts a comment line.
func ParseEvidence(r io.Reader) (map[string][3]int, error) {
	ev := map[string][3]int{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue
		}
		p := strings.Split(strings.TrimRight(line, "\n"), "\t")
		var c [3]int
		for i := 1; i < len(p) && i <= 3; i++ {
			c[i-1], _ = strconv.Atoi(p[i])
		}
		ev[p[0]] = c
	}
	return ev, sc.Err()
}

func loadEvidence(path string) (map[string][3]int, error) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	return ParseEvidence(fh)
}

func defaultEvidence() string {
	exe, err := os.Executable()
	if err == nil {
		p := filepath.Join(filepath.Dir(exe), "..", "..", "compiler", "vendor_forms.txt")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return filepath.Join("compiler", "vendor_forms.txt")
}

// Check writes the report of an ELF's instruction forms against the evidence
// to w and returns the number of forms with no vendor use.
func Check(w io.Writer, elfData []byte, thumb bool, ev map[string][3]int) (int, error) {
	seen, where, nfuncs, nOutside, err := imageForms(elfData, thumb, true)
	if err != nil {
		return 0, err
	}
	bad := map[string]bool{}
	for f := range seen {
		c := ev[f]
		if c[0]+c[1]+c[2] == 0 {
			bad[f] = true
		}
	}
	forms := make([]string, 0, len(seen))
	for f := range seen {
		forms = append(forms, f)
	}
	sort.Slice(forms, func(i, j int) bool {
		bi, bj := bad[forms[i]], bad[forms[j]]
		if bi != bj {
			return bi // the forms without vendor use first
		}
		return forms[i] < forms[j]
	})
	fmt.Fprintf(w, "%-30s %7s %10s %11s %9s\n", "form", "image", "Telink gcc", "Telink libs", "fw ran")
	total := 0
	for _, f := range forms {
		c := ev[f]
		line := fmt.Sprintf("%-30s %7d %10d %11d %9d", f, seen[f], c[2], c[0], c[1])
		if bad[f] {
			line += "   NO VENDOR USE, e.g. " + where[f]
		}
		fmt.Fprintln(w, line)
		total += seen[f]
	}
	badList := make([]string, 0, len(bad))
	for f := range bad {
		badList = append(badList, f)
	}
	sort.Strings(badList)
	summary := fmt.Sprintf("\n%d instructions in %d functions", total, nfuncs)
	if nOutside > 0 {
		summary += fmt.Sprintf(" and %d outside them", nOutside)
	}
	summary += fmt.Sprintf("; %d form(s) Telink's code never uses", len(bad))
	if len(bad) > 0 {
		summary += ": " + strings.Join(badList, ", ")
	}
	fmt.Fprintln(w, summary)
	return len(bad), nil
}

// Main runs forms-check with argv (the arguments after the program name),
// writing its report to w and its errors to ew; it returns the exit status.
func Main(argv []string, w, ew io.Writer) int {
	if len(argv) < 2 || argv[0] != "check" {
		fmt.Fprintln(ew, "usage: forms-check check <elf> [--thumb] [--evidence vendor_forms.txt]")
		return 2
	}
	elfPath, thumb, evidence := "", false, ""
	for i := 1; i < len(argv); i++ {
		switch argv[i] {
		case "--thumb":
			thumb = true
		case "--evidence":
			i++
			evidence = argv[i]
		default:
			elfPath = argv[i]
		}
	}
	data, err := os.ReadFile(elfPath)
	if err != nil {
		fmt.Fprintln(ew, "forms-check:", err)
		return 1
	}
	cpu, _, _ := armattr.CPUName(data)
	if thumb && strings.EqualFold(cpu, "tc32") {
		fmt.Fprintf(ew, "forms-check: %s names the core tc32: its code is TC32 already; check it without --thumb\n", elfPath)
		return 2
	}
	if !thumb && cpu != "" && !strings.EqualFold(cpu, "tc32") {
		fmt.Fprintf(ew, "forms-check: %s names the core %s: its code is Thumb; check it with --thumb\n", elfPath, cpu)
		return 2
	}
	if evidence == "" {
		evidence = defaultEvidence()
	}
	ev, err := loadEvidence(evidence)
	if err != nil {
		fmt.Fprintln(ew, "forms-check:", err)
		return 1
	}
	nbad, err := Check(w, data, thumb, ev)
	if err != nil {
		fmt.Fprintln(ew, "forms-check:", err)
		return 1
	}
	if nbad > 0 {
		return 1
	}
	return 0
}

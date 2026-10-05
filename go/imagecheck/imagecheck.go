// Package imagecheck is image-check: it checks the TC32 image thumb2tc32 wrote against the Thumb ELF
// it came from.
//
// Usage: image-check <firmware.elf> <firmware.bin> [--objcopy <llvm-objcopy>] [--readelf <llvm-readelf>]
//
//	[--blob <manifest.json>] [--startup <manifest.json>]
//
// The Go build of compiler/image_check.py, with the same independence from
// the converter: llvm-objcopy -O binary lays the ELF out by load address, and
// llvm-readelf gives the sections, program headers and symbols, so no ELF
// reader of this repository is trusted here. Each halfword of a loaded
// section is classified from the ELF: code (inside an executable section,
// after a $t mapping symbol, not inside a sized STT_OBJECT) must be
// thumb2tc32.Encode of the ELF's halfword; everything else must be unchanged;
// padding inside a segment must be the ELF's bytes, between segments 0xff;
// the image must be as long as objcopy's output. --blob and --startup check a
// blob and the startup against their manifests, as image_check.py does. Exit
// status 1 on any mismatch.
//
// SPDX-License-Identifier: Apache-2.0
package imagecheck

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/goyamamoto/tc32-devtools/go/internal/llvmtool"
	"github.com/goyamamoto/tc32-devtools/go/tc32isa"
	"github.com/goyamamoto/tc32-devtools/go/thumb2tc32"
)

type section struct {
	index            int
	name, typ, flags string
	vma, off, size   uint32
}

type load struct{ off, vaddr, paddr, filesz uint32 }

type symbol struct {
	value, size uint32
	typ         string
	ndx         int
	name        string
}

type mark struct {
	addr uint32
	kind byte
}

type span struct{ start, end uint32 }

func readelf(tool, flag, elfPath string) []string {
	out, err := exec.Command(tool, flag, "-W", elfPath).Output()
	if err != nil {
		fmt.Fprintf(stderr, "image-check: %s %s: %v\n", tool, flag, err)
		exit(1)
	}
	return strings.Split(string(out), "\n")
}

func hex32(s string) uint32 {
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		fmt.Fprintf(stderr, "image-check: bad hex %q\n", s)
		exit(1)
	}
	return uint32(v)
}

var secLine = regexp.MustCompile(`^\s*\[\s*(\d+)\]\s+(\S+)\s+(\S+)\s+([0-9a-f]+)\s+([0-9a-f]+)\s+([0-9a-f]+)\s+\S+\s+(\S*)`)

func sections(tool, elfPath string) []section {
	var out []section
	for _, line := range readelf(tool, "-S", elfPath) {
		m := secLine.FindStringSubmatch(line)
		if m == nil || m[3] == "NULL" {
			continue
		}
		i, _ := strconv.Atoi(m[1])
		out = append(out, section{i, m[2], m[3], m[7], hex32(m[4]), hex32(m[5]), hex32(m[6])})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].index < out[j].index })
	return out
}

func loads(tool, elfPath string) []load {
	var out []load
	for _, line := range readelf(tool, "-l", elfPath) {
		p := strings.Fields(line)
		if len(p) >= 5 && p[0] == "LOAD" {
			out = append(out, load{hex32(strings.TrimPrefix(p[1], "0x")), hex32(strings.TrimPrefix(p[2], "0x")),
				hex32(strings.TrimPrefix(p[3], "0x")), hex32(strings.TrimPrefix(p[4], "0x"))})
		}
	}
	return out
}

func symbols(tool, elfPath string) []symbol {
	var out []symbol
	for _, line := range readelf(tool, "-s", elfPath) {
		p := strings.Fields(line)
		if len(p) < 8 || !strings.HasSuffix(p[0], ":") {
			continue
		}
		ndx, err := strconv.Atoi(p[6])
		if err != nil {
			if p[6] != "ABS" {
				continue
			}
			ndx = -1
		}
		var size uint64
		if s, err := strconv.ParseUint(p[2], 10, 32); err == nil {
			size = s
		} else if s, err := strconv.ParseUint(strings.TrimPrefix(p[2], "0x"), 16, 32); err == nil {
			size = s
		}
		out = append(out, symbol{hex32(p[1]), uint32(size), p[3], ndx, p[7]})
	}
	return out
}

var mapSym = regexp.MustCompile(`^\$[tad](\..*)?$`)

// Main runs image-check with argv (the arguments after the program name),
// writing its report to w and its errors to ew; it returns the exit status.
func Main(argv []string, w, ew io.Writer) (code int) {
	stdout, stderr = w, ew
	defer func() {
		if e := recover(); e != nil {
			c, ok := e.(exitCode)
			if !ok {
				panic(e)
			}
			code = int(c)
		}
	}()
	args := []string{}
	objcopy, readelfTool := llvmtool.Tool("llvm-objcopy"), llvmtool.Tool("llvm-readelf")
	blobPath := ""
	startupPath := ""
	for i := 0; i < len(argv); i++ {
		switch argv[i] {
		case "--objcopy":
			i++
			objcopy = argv[i]
		case "--readelf":
			i++
			readelfTool = argv[i]
		case "--blob":
			i++
			blobPath = argv[i]
		case "--startup":
			i++
			startupPath = argv[i]
		default:
			args = append(args, argv[i])
		}
	}
	if len(args) != 2 {
		fmt.Fprintln(stderr, "usage: image-check <firmware.elf> <firmware.bin> [--objcopy <llvm-objcopy>] [--readelf <llvm-readelf>] [--blob <manifest.json>] [--startup <manifest.json>]")
		exit(2)
	}
	elfPath, binPath := args[0], args[1]

	tmp, err := os.MkdirTemp("", "image-check")
	if err != nil {
		fmt.Fprintln(stderr, "image-check:", err)
		exit(1)
	}
	defer os.RemoveAll(tmp)
	rawPath := filepath.Join(tmp, "raw.bin")
	if out, err := exec.Command(objcopy, "-O", "binary", elfPath, rawPath).CombinedOutput(); err != nil {
		fmt.Fprintf(stderr, "image-check: %s: %v\n%s", objcopy, err, out)
		exit(1)
	}
	raw, err := os.ReadFile(rawPath)
	if err != nil {
		fmt.Fprintln(stderr, "image-check:", err)
		exit(1)
	}
	img, err := os.ReadFile(binPath)
	if err != nil {
		fmt.Fprintln(stderr, "image-check:", err)
		exit(1)
	}
	elfData, err := os.ReadFile(elfPath)
	if err != nil {
		fmt.Fprintln(stderr, "image-check:", err)
		exit(1)
	}
	secs := sections(readelfTool, elfPath)
	segs := loads(readelfTool, elfPath)
	syms := symbols(readelfTool, elfPath)

	var bad []string
	if len(img) != len(raw) {
		bad = append(bad, fmt.Sprintf("image %d B, objcopy %d B", len(img), len(raw)))
	}

	marks := map[int][]mark{}
	objects := map[int][]span{}
	for _, s := range syms {
		if mapSym.MatchString(s.name) {
			marks[s.ndx] = append(marks[s.ndx], mark{s.value &^ 1, s.name[1]})
		} else if s.typ == "OBJECT" && s.size > 0 {
			objects[s.ndx] = append(objects[s.ndx], span{s.value, s.value + s.size})
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

	type loaded struct {
		lma uint32
		sec section
	}
	var ld []loaded
	for _, s := range secs {
		if !strings.Contains(s.flags, "A") || s.typ == "NOBITS" || s.size == 0 {
			continue
		}
		found := false
		for _, g := range segs {
			if g.off <= s.off && s.off+s.size <= g.off+g.filesz {
				ld = append(ld, loaded{g.paddr + (s.off - g.off), s})
				found = true
				break
			}
		}
		_ = found
	}
	if len(ld) == 0 {
		fmt.Fprintln(stderr, "image-check: no loaded sections")
		exit(1)
	}
	base := ld[0].lma
	for _, l := range ld {
		if l.lma < base {
			base = l.lma
		}
	}
	covered := make([]bool, len(img))
	var nCode, nData, nRewritten int
	for _, l := range ld {
		s := l.sec
		if uint64(s.off)+uint64(s.size) > uint64(len(elfData)) {
			bad = append(bad, fmt.Sprintf("%s: runs past the end of the ELF file", s.name))
			continue
		}
		src := elfData[s.off : s.off+s.size]
		o := int(l.lma - base)
		if o+int(s.size) > len(raw) || string(raw[o:o+int(s.size)]) != string(src) {
			bad = append(bad, fmt.Sprintf("%s: objcopy disagrees with the section's bytes", s.name))
		}
		for i := o; i < o+int(s.size) && i < len(covered); i++ {
			covered[i] = true
		}
		mk := marks[s.index]
		objs := objects[s.index]
		exec := strings.Contains(s.flags, "X")
		for i := uint32(0); i+1 < s.size; i += 2 {
			addr := s.vma + i
			// The last mapping symbol at or before addr.
			j := sort.Search(len(mk), func(k int) bool { return mk[k].addr > addr }) - 1
			kind := byte(0)
			if j >= 0 {
				kind = mk[j].kind
			}
			code := exec && kind == 't'
			if code {
				for _, ob := range objs {
					if ob.start <= addr && addr < ob.end {
						code = false
						break
					}
				}
			}
			hw := binary.LittleEndian.Uint16(src[i:])
			var got int = -1
			if o+int(i)+2 <= len(img) {
				got = int(binary.LittleEndian.Uint16(img[o+int(i):]))
			}
			want := hw
			if code {
				want = thumb2tc32.Encode(hw)
				nCode++
				if _, k := thumb2tc32.Rewrite(hw); k == thumb2tc32.KindMovs || k == thumb2tc32.KindUdf {
					nRewritten++
				}
			} else {
				nData++
			}
			if got != int(want) {
				if len(bad) < 40 {
					gs := "None"
					if got >= 0 {
						gs = fmt.Sprintf("0x%04x", got)
					}
					what := "data"
					if code {
						what = "code"
					}
					bad = append(bad, fmt.Sprintf("%s+0x%x (lma 0x%x, %s): image %s, expected 0x%04x (ELF 0x%04x)",
						s.name, i, l.lma+i, what, gs, want, hw))
				} else {
					bad = append(bad, "...")
					break
				}
			}
		}
		if exec && len(mk) == 0 {
			bad = append(bad, fmt.Sprintf("%s: executable section without mapping symbols", s.name))
		}
	}
	// Padding: inside a segment, the ELF's bytes (the linker's alignment
	// fill); between segments, 0xff (thumb2tc32's fill, erased flash).
	gaps, first := 0, -1
	for i := range img {
		if covered[i] {
			continue
		}
		want := byte(0xFF)
		a := base + uint32(i)
		for _, g := range segs {
			if g.paddr <= a && a < g.paddr+g.filesz {
				if idx := uint64(g.off) + uint64(a-g.paddr); idx < uint64(len(elfData)) {
					want = elfData[idx]
				}
				break
			}
		}
		if img[i] != want {
			if first < 0 {
				first = i
			}
			gaps++
		}
	}
	if gaps > 0 {
		bad = append(bad, fmt.Sprintf("%d padding bytes differ (inside a segment from the ELF, between segments from 0xff), first at 0x%x",
			gaps, int(base)+first))
	}
	// The .data copy source (Zephyr's crt0) against the ELF's data segment.
	sym := map[string]uint32{}
	for _, s := range syms {
		sym[s.name] = s.value
	}
	start, okS := sym["__data_region_start"]
	end, okE := sym["__data_region_end"]
	loadAt, okL := sym["__data_region_load_start"]
	dataCopy := "no __data_region symbols (not a Zephyr image)"
	if okS && okE && okL {
		var seg *load
		for i := range segs {
			if segs[i].vaddr == start && segs[i].filesz > 0 {
				seg = &segs[i]
				break
			}
		}
		switch {
		case seg == nil:
			bad = append(bad, fmt.Sprintf("data copy: no PT_LOAD at __data_region_start 0x%x", start))
		case seg.paddr != loadAt:
			bad = append(bad, fmt.Sprintf("data copy: __data_region_load_start 0x%x, but the data segment is loaded from 0x%x (an orphan section between rodata and data?)", loadAt, seg.paddr))
		case end-start > seg.filesz:
			bad = append(bad, fmt.Sprintf("data copy: region 0x%x B, but the data segment holds 0x%x B", end-start, seg.filesz))
		}
		dataCopy = fmt.Sprintf("data copy source 0x%x checked", loadAt)
	}
	// A blob against its manifest.
	blob := ""
	if blobPath != "" {
		raw, err := os.ReadFile(blobPath)
		if err != nil {
			fmt.Fprintln(stderr, "image-check:", err)
			exit(1)
		}
		var man struct {
			Sha    string `json:"blob_sha256"`
			Size   uint32 `json:"size"`
			Symbol string `json:"symbol"`
		}
		if err := json.Unmarshal(raw, &man); err != nil {
			fmt.Fprintln(stderr, "image-check:", err)
			exit(1)
		}
		bname := man.Symbol
		if bname == "" {
			fmt.Fprintln(stderr, "image-check: the blob's manifest names no symbol")
			exit(1)
		}
		var blobSyms []symbol
		for _, s := range syms {
			if s.name == bname {
				blobSyms = append(blobSyms, s)
			}
		}
		if len(blobSyms) != 1 || (blobSyms[0].typ != "OBJECT" && blobSyms[0].typ != "FUNC" && blobSyms[0].typ != "NOTYPE") {
			bad = append(bad, fmt.Sprintf("blob: %d symbol(s) %s, want one", len(blobSyms), bname))
		} else {
			bs := blobSyms[0]
			bs.value &^= 1 // a Thumb function symbol carries bit 0
			var sec *section
			for i := range secs {
				if secs[i].index == bs.ndx {
					sec = &secs[i]
				}
			}
			var found *loaded
			for i := range ld {
				if ld[i].sec.index == bs.ndx {
					found = &ld[i]
				}
			}
			if bs.size != man.Size {
				bad = append(bad, fmt.Sprintf("blob: size %d, manifest %d", bs.size, man.Size))
			}
			if sec == nil || !strings.Contains(sec.flags, "X") || found == nil {
				bad = append(bad, "blob: not in a loaded executable section")
			} else {
				// Data: one $d at its start and nothing else; code: a $t at its start (hand-written
				// source, re-encoded by thumb2tc32, $d over its literal pools), which the SHA-256 then checks.
				var inside []string
				var kinds []mark
				for _, mk := range marks[bs.ndx] {
					if bs.value <= mk.addr && mk.addr < bs.value+bs.size {
						inside = append(inside, fmt.Sprintf("(%d, '%c')", mk.addr, mk.kind))
						kinds = append(kinds, mk)
					}
				}
				code := len(kinds) > 0 && kinds[0].addr == bs.value && kinds[0].kind == 't'
				data := len(kinds) == 1 && kinds[0].addr == bs.value && kinds[0].kind == 'd'
				if !code && !data {
					bad = append(bad, fmt.Sprintf("blob: mapping symbols inside it [%s], want one $d at its start (data) or a $t at its start (code)", strings.Join(inside, ", ")))
				}
				o := int(found.lma) - int(base) + int(bs.value-found.sec.vma)
				sum := sha256.Sum256(img[o : o+int(bs.size)])
				got := hex.EncodeToString(sum[:])
				if got != man.Sha {
					bad = append(bad, fmt.Sprintf("blob: image bytes sha256 %s..., manifest %s...", got[:16], man.Sha[:16]))
				}
				blob = fmt.Sprintf("; blob %d B at 0x%x checked", bs.size, found.lma+(bs.value-found.sec.vma))
			}
		}
	}
	// The startup against its manifest.
	startup := ""
	if startupPath != "" {
		raw, err := os.ReadFile(startupPath)
		if err != nil {
			fmt.Fprintln(stderr, "image-check:", err)
			exit(1)
		}
		var man struct {
			Code       [][2]int `json:"code"`
			CodeHex    string   `json:"code_hex"`
			HeaderWord uint32   `json:"header_copy_word"`
			Entry      int      `json:"entry"`
			StubEnd    int      `json:"stub_end"`
			Start      int      `json:"start"`
			Call       int      `json:"call"`
			CallSymbol string   `json:"call_symbol"`
			Pool       []struct {
				Offset int             `json:"offset"`
				Kind   string          `json:"kind"`
				Value  json.RawMessage `json:"value"`
			} `json:"pool"`
		}
		if err := json.Unmarshal(raw, &man); err != nil {
			fmt.Fprintln(stderr, "image-check:", err)
			exit(1)
		}
		n0 := len(bad)
		if base != 0 {
			bad = append(bad, fmt.Sprintf("startup: the image starts at 0x%x, not 0", base))
		}
		want, _ := hex.DecodeString(man.CodeHex)
		var code []byte
		for _, r := range man.Code {
			if r[1] <= len(img) {
				code = append(code, img[r[0]:r[1]]...)
			}
		}
		if !bytes.Equal(code, want) {
			first := -1
			pos := 0
			for _, r := range man.Code {
				for i := 0; i < r[1]-r[0] && first < 0; i += 2 {
					if r[0]+i+2 > len(img) || pos+i+2 > len(want) || !bytes.Equal(img[r[0]+i:r[0]+i+2], want[pos+i:pos+i+2]) {
						first = r[0] + i
					}
				}
				pos += r[1] - r[0]
			}
			msg := "startup: the instructions differ from the manifest's"
			if first >= 0 {
				msg += fmt.Sprintf(", first at 0x%x", first)
			}
			bad = append(bad, msg)
		}
		u32 := func(off int) uint32 { return binary.LittleEndian.Uint32(img[off:]) }
		u16 := func(off int) uint16 { return binary.LittleEndian.Uint16(img[off:]) }
		if u32(0xC) != man.HeaderWord {
			bad = append(bad, fmt.Sprintf("startup: header word 0xc is 0x%08x, manifest 0x%08x", u32(0xC), man.HeaderWord))
		}
		jump := func(off int) (int, bool) {
			th := tc32isa.ToThumb(u16(off))
			if th>>11 != 0x1C {
				return 0, false
			}
			d := int(th & 0x7FF)
			if d&0x400 != 0 {
				d -= 0x800
			}
			return off + 4 + 2*d, true
		}
		if t, ok := jump(0); !ok || t != man.Entry {
			bad = append(bad, fmt.Sprintf("startup: the reset vector does not jump to the entry 0x%x", man.Entry))
		}
		out := -1
		for o := man.Entry; o < man.StubEnd; o += 2 {
			if _, ok := jump(o); ok {
				out = o
				break
			}
		}
		if t, ok := 0, false; out < 0 || func() bool { t, ok = jump(out); return !ok || t != man.Start }() {
			_ = t
			bad = append(bad, fmt.Sprintf("startup: the entry does not end in a jump to 0x%x", man.Start))
		} else if out > man.Entry {
			if !bytes.Equal(img[man.Entry:man.Entry+10], want[:10]) {
				bad = append(bad, "startup: the entry does not begin with the startup's first instructions")
			}
		}
		th0, th1 := tc32isa.ToThumb(u16(man.Call)), tc32isa.ToThumb(u16(man.Call+2))
		hi := int(th0 & 0x7FF)
		if hi&0x400 != 0 {
			hi -= 0x800
		}
		target := man.Call + 4 + (hi << 12) + (int(th1&0x7FF) << 1)
		callSym, okCall := sym[man.CallSymbol]
		if th0>>11 != 0x1E || th1>>11 != 0x1F || !okCall || target != int(callSym&^1) {
			bad = append(bad, fmt.Sprintf("startup: 0x%x is not a call to %s", man.Call, man.CallSymbol))
		}
		numRe := regexp.MustCompile(`^(0x)?[0-9a-fA-F]+$`)
		for _, e := range man.Pool {
			got := u32(e.Offset)
			var wantV uint32
			var valueText string
			if e.Kind == "symbol" {
				var expr string
				json.Unmarshal(e.Value, &expr)
				valueText = expr
				var missing []string
				var sum uint64
				for _, t := range strings.Split(expr, "+") {
					t = strings.TrimSpace(t)
					if v, ok := sym[t]; ok {
						sum += uint64(v)
					} else if numRe.MatchString(t) {
						n, _ := strconv.ParseUint(strings.TrimPrefix(strings.ToLower(t), "0x"), map[bool]int{true: 16, false: 10}[strings.HasPrefix(strings.ToLower(t), "0x")], 64)
						sum += n
					} else {
						missing = append(missing, t)
					}
				}
				if len(missing) > 0 {
					bad = append(bad, fmt.Sprintf("startup: pool 0x%x: no symbol %s", e.Offset, pyStrList(missing)))
					continue
				}
				wantV = uint32(sum)
			} else {
				var v uint64
				json.Unmarshal(e.Value, &v)
				wantV = uint32(v)
				valueText = strconv.FormatUint(v, 10)
			}
			if got != wantV {
				bad = append(bad, fmt.Sprintf("startup: pool 0x%x is 0x%08x, expected 0x%08x (%s)", e.Offset, got, wantV, valueText))
			}
		}
		if len(bad) == n0 {
			startup = fmt.Sprintf("; startup checked (%d B of instructions, %d pool words, entry 0x%x", len(code), len(man.Pool), man.Entry)
			if out >= 0 && out > man.Entry {
				startup += " with the watchdog"
			}
			startup += ")"
		}
	}
	fmt.Fprintf(stdout, "%s: %d B; %d loaded sections; %d code halfwords (re-encoded, %d rewritten), %d data halfwords (unchanged); %d mismatch(es); %s%s%s\n",
		binPath, len(img), len(ld), nCode, nRewritten, nData, len(bad), dataCopy, blob, startup)
	for i, b := range bad {
		if i >= 40 {
			break
		}
		fmt.Fprintln(stdout, "  "+b)
	}
	if len(bad) > 0 {
		return 1
	}
	return 0
}

// pyStrList is Python's repr of a list of str.
func pyStrList(xs []string) string {
	var parts []string
	for _, x := range xs {
		parts = append(parts, "'"+strings.ReplaceAll(x, "'", "\\'")+"'")
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// The writers of the running Main, and its way out.
var stdout, stderr io.Writer = os.Stdout, os.Stderr

type exitCode int

func exit(code int) { panic(exitCode(code)) }

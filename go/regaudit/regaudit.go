// Package regaudit is the Go port of ../../emulator/reg_audit.py: the
// registers an image touches, named by the Telink SDK for the TLSR8258 (B85)
// and the TLSR8278 (B87), with the rows formatted as the Python version
// prints them. The tables are emulator/b85_registers.csv and
// b87_registers.csv (reg_tables.py); the reviewed list belongs to the
// firmware under audit.
//
// SPDX-License-Identifier: Apache-2.0
package regaudit

import (
	"encoding/csv"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// Entry is one named register of a table.
type Entry struct {
	Name string
	Addr int
	Size int
}

// Access is one register access, as the Python reg_log set holds it.
type Access struct {
	Offset int
	Size   int
	RW     byte // 'r' or 'w'
	PC     uint32
}

// Row is one audited register byte.
type Row struct {
	Offset int
	RW     string
	Funcs  []string
	B85    []string
	B87    []string
	Status string
	Note   string
}

// Load reads a name,address,size CSV.
func Load(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	var out []Entry
	for {
		rec, err := r.Read()
		if err != nil {
			break
		}
		if len(rec) < 3 || strings.HasPrefix(rec[0], "#") {
			continue
		}
		a, err := strconv.ParseInt(strings.TrimPrefix(strings.TrimSpace(rec[1]), "0x"), 16, 32)
		if err != nil {
			return nil, fmt.Errorf("%s: %v", path, err)
		}
		s, err := strconv.Atoi(strings.TrimSpace(rec[2]))
		if err != nil {
			return nil, fmt.Errorf("%s: %v", path, err)
		}
		out = append(out, Entry{rec[0], int(a), s})
	}
	return out, nil
}

// LoadReviewed reads an offset,note CSV; an empty path gives no entries.
func LoadReviewed(path string) (map[int]string, error) {
	out := map[int]string{}
	if path == "" {
		return out, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	for {
		rec, err := r.Read()
		if err != nil {
			break
		}
		if len(rec) < 1 || strings.HasPrefix(rec[0], "#") {
			continue
		}
		o, err := strconv.ParseInt(strings.TrimPrefix(strings.TrimSpace(rec[0]), "0x"), 16, 32)
		if err != nil {
			return nil, fmt.Errorf("%s: %v", path, err)
		}
		note := ""
		if len(rec) > 1 {
			note = rec[1]
		}
		out[int(o)] = note
	}
	return out, nil
}

func namesAt(table []Entry, off int) []string {
	set := map[string]bool{}
	for _, e := range table {
		if e.Addr <= off && off < e.Addr+e.Size {
			set[e.Name] = true
		}
	}
	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func subset(a, b []string) bool {
	set := map[string]bool{}
	for _, x := range b {
		set[x] = true
	}
	for _, x := range a {
		if !set[x] {
			return false
		}
	}
	return true
}

// Audit is reg_audit.audit(): rows sorted by offset.
func Audit(log []Access, symbolize func(uint32) string, b85, b87 []Entry, rev map[int]string) []Row {
	type acc struct {
		rw    map[byte]bool
		funcs map[string]bool
	}
	byOff := map[int]*acc{}
	for _, a := range log {
		for o := a.Offset; o < a.Offset+a.Size; o++ {
			e := byOff[o]
			if e == nil {
				e = &acc{map[byte]bool{}, map[string]bool{}}
				byOff[o] = e
			}
			e.rw[a.RW] = true
			e.funcs[strings.SplitN(symbolize(a.PC), "+", 2)[0]] = true
		}
	}
	offs := make([]int, 0, len(byOff))
	for o := range byOff {
		offs = append(offs, o)
	}
	sort.Ints(offs)
	var rows []Row
	for _, o := range offs {
		n85, n87 := namesAt(b85, o), namesAt(b87, o)
		var status string
		switch {
		case equal(n85, n87):
			if len(n85) > 0 {
				status = "same"
			} else {
				status = "unnamed"
			}
		case len(n85) > 0 && subset(n85, n87):
			status = "same" // B87 only adds names (32-bit views of the same bytes)
		case len(n87) == 0:
			status = "B85 only"
		case len(n85) == 0:
			status = "B87 only"
		default:
			status = "differs"
		}
		note, reviewed := rev[o]
		if status != "same" && status != "unnamed" && reviewed {
			status += ", reviewed"
		}
		e := byOff[o]
		rw := make([]string, 0, 2)
		for _, c := range []byte{'r', 'w'} {
			if e.rw[c] {
				rw = append(rw, string(c))
			}
		}
		funcs := make([]string, 0, len(e.funcs))
		for f := range e.funcs {
			funcs = append(funcs, f)
		}
		sort.Strings(funcs)
		rows = append(rows, Row{o, strings.Join(rw, ""), funcs, n85, n87, status, note})
	}
	return rows
}

// Unreviewed is reg_audit.unreviewed().
func Unreviewed(rows []Row) []Row {
	var out []Row
	for _, r := range rows {
		if r.Status == "differs" || r.Status == "B85 only" {
			out = append(out, r)
		}
	}
	return out
}

func orDash(names []string) string {
	if len(names) == 0 {
		return "-"
	}
	return strings.Join(names, ",")
}

// FormatRows is reg_audit.format_rows().
func FormatRows(rows []Row) string {
	lines := make([]string, 0, len(rows))
	for _, r := range rows {
		funcs := strings.Join(r.Funcs, " ")
		if len(funcs) > 80 {
			funcs = funcs[:80]
		}
		lines = append(lines, fmt.Sprintf("0x%03x %-2s %-18s B85 %-32s B87 %-32s %s", r.Offset, r.RW, r.Status,
			orDash(r.B85), orDash(r.B87), funcs))
	}
	return strings.Join(lines, "\n")
}

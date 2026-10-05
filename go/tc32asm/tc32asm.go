// Package tc32asm translates TC32 assembly (Telink's syntax, as tc32-elf-as
// reads it) into ARMv4T Thumb assembly for a mainstream assembler. It is the
// Go port of ../../compiler/tc32asm2thumb.py, the canonical version, line
// for line: checks/run_checks.sh requires the two to write the same output
// for checks/asm/sample.S.
//
// SPDX-License-Identifier: Apache-2.0
package tc32asm

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	low  = map[string]bool{}
	spPC = map[string]bool{"sp": true, "pc": true, "r13": true, "r15": true}
	cc   = map[string]bool{}
	// simple maps a TC32 mnemonic to the Thumb one with the same encoding.
	simple = map[string]string{"tloadr": "ldr", "tstorer": "str", "tloadrb": "ldrb", "tstorerb": "strb",
		"tloadrh": "ldrh", "tstorerh": "strh", "tloadrsb": "ldrsb", "tloadrsh": "ldrsh", "tloadm": "ldm",
		"tstorem": "stm", "tj": "b", "tjl": "bl", "tjex": "bx", "tpush": "push", "tpop": "pop", "tcmp": "cmp",
		"tcmpn": "cmn", "tand": "ands", "tor": "orrs", "txor": "eors", "tbclr": "bics", "tmovn": "mvns",
		"tmul": "muls", "taddc": "adcs", "tsubc": "sbcs", "trotr": "rors", "tnand": "tst", "tshftl": "lsls",
		"tshftr": "lsrs", "tasr": "asrs", "nop": "nop"}
	sysreg = map[string]int{"tmcsr": 0xBBC0, "tmrcs": 0xBBC8, "tmssr": 0xBBD0, "tmrss": 0xBBD8}
	insn   = regexp.MustCompile(`^(\s*)(?:([.\w$]+:)(\s*))?(t[a-z]+|nop)\b(.*)$`)
)

func init() {
	for i := 0; i < 8; i++ {
		low[fmt.Sprintf("r%d", i)] = true
	}
	for _, c := range strings.Fields("eq ne cs cc hs lo mi pl vs vc hi ls ge lt gt le") {
		cc[c] = true
	}
}

// Untranslatable is an instruction the translator does not cover.
type Untranslatable struct{ Msg string }

func (u *Untranslatable) Error() string { return u.Msg }

func splitOps(text string) []string {
	var ops []string
	depth, cur := 0, ""
	for _, ch := range text {
		switch ch {
		case '[', '{':
			depth++
		case ']', '}':
			depth--
		}
		if ch == ',' && depth == 0 {
			ops = append(ops, strings.TrimSpace(cur))
			cur = ""
		} else {
			cur += string(ch)
		}
	}
	if strings.TrimSpace(cur) != "" {
		ops = append(ops, strings.TrimSpace(cur))
	}
	return ops
}

func regNum(s string) (int, error) {
	switch s {
	case "sp":
		return 13, nil
	case "lr":
		return 14, nil
	case "pc":
		return 15, nil
	}
	if len(s) < 2 {
		return 0, &Untranslatable{"bad register " + s}
	}
	return strconv.Atoi(s[1:])
}

// regList returns the register numbers of a {...} list (r0-r7 ranges, lr, pc).
func regList(text string) (map[int]bool, error) {
	out := map[int]bool{}
	for _, part := range strings.Split(strings.Trim(text, "{} "), ",") {
		part = strings.TrimSpace(part)
		if strings.Contains(part, "-") {
			ab := strings.SplitN(part, "-", 2)
			a, err := strconv.Atoi(strings.TrimSpace(ab[0])[1:])
			if err != nil {
				return nil, err
			}
			b, err := strconv.Atoi(strings.TrimSpace(ab[1])[1:])
			if err != nil {
				return nil, err
			}
			for r := a; r <= b; r++ {
				out[r] = true
			}
		} else if part != "" {
			n, err := regNum(part)
			if err != nil {
				return nil, err
			}
			out[n] = true
		}
	}
	return out, nil
}

// TranslateInsn returns the Thumb instruction for TC32 mnemonic mn and its
// operand text.
func TranslateInsn(mn, opsText string) (string, error) {
	ops := splitOps(opsText)
	need := func(n int) error {
		if len(ops) < n {
			return &Untranslatable{fmt.Sprintf("%s %s: %d operand(s)", mn, strings.TrimSpace(opsText), len(ops))}
		}
		return nil
	}
	if v, ok := sysreg[mn]; ok {
		if err := need(1); err != nil {
			return "", err
		}
		if len(ops[0]) < 2 {
			return "", &Untranslatable{mn + " " + ops[0]}
		}
		n, err := strconv.Atoi(ops[0][1:]) // as the Python version: int(ops[0][1:])
		if err != nil {
			return "", err
		}
		return fmt.Sprintf(".inst.n 0x%04x", v|n), nil
	}
	switch mn {
	case "treti":
		if err := need(1); err != nil {
			return "", err
		}
		regs, err := regList(ops[0])
		if err != nil {
			return "", err
		}
		v := 0xB800
		for r := range regs {
			switch {
			case r < 8:
				v |= 1 << uint(r)
			case r == 15:
				v |= 0x100
			default:
				return "", &Untranslatable{"treti " + ops[0]}
			}
		}
		return fmt.Sprintf(".inst.n 0x%04x", v), nil
	case "tmov":
		if len(ops) != 2 {
			return "", &Untranslatable{fmt.Sprintf("tmov %s", strings.TrimSpace(opsText))}
		}
		a, b := ops[0], ops[1]
		switch {
		case strings.HasPrefix(b, "#"):
			return fmt.Sprintf("movs %s, %s", a, b), nil
		case low[a] && low[b]:
			return fmt.Sprintf("adds %s, %s, #0", a, b), nil
		}
		return fmt.Sprintf("mov %s, %s", a, b), nil
	case "tadd", "tsub":
		s := "add"
		if mn == "tsub" {
			s = "sub"
		}
		if len(ops) == 3 {
			a, b, c := ops[0], ops[1], ops[2]
			if strings.HasPrefix(c, "#") {
				if spPC[b] {
					return fmt.Sprintf("%s %s, %s, %s", s, a, b, c), nil
				}
				return fmt.Sprintf("%ss %s, %s, %s", s, a, b, c), nil
			}
			if low[a] && low[b] && low[c] {
				return fmt.Sprintf("%ss %s, %s, %s", s, a, b, c), nil
			}
			return "", &Untranslatable{fmt.Sprintf("%s %s", mn, strings.TrimSpace(opsText))}
		}
		if len(ops) != 2 {
			return "", &Untranslatable{fmt.Sprintf("%s %s", mn, strings.TrimSpace(opsText))}
		}
		a, b := ops[0], ops[1]
		if strings.HasPrefix(b, "#") {
			if spPC[a] {
				return fmt.Sprintf("%s %s, %s", s, a, b), nil
			}
			return fmt.Sprintf("%ss %s, %s", s, a, b), nil
		}
		if mn == "tadd" && !(low[a] && low[b]) {
			return fmt.Sprintf("add %s, %s", a, b), nil
		}
		return "", &Untranslatable{fmt.Sprintf("%s %s", mn, strings.TrimSpace(opsText))}
	case "tneg":
		if err := need(2); err != nil {
			return "", err
		}
		return fmt.Sprintf("rsbs %s, %s, #0", ops[0], ops[1]), nil
	}
	if strings.HasPrefix(mn, "tj") && cc[mn[2:]] {
		return fmt.Sprintf("b%s %s", mn[2:], strings.TrimSpace(opsText)), nil
	}
	if t, ok := simple[mn]; ok {
		return strings.TrimRight(fmt.Sprintf("%s %s", t, strings.TrimSpace(opsText)), " \t\n\r"), nil
	}
	return "", &Untranslatable{mn}
}

// stripComment splits an instruction's operand text into code and comment,
// looking for the markers in the Python version's order.
func stripComment(text string) (string, string) {
	for _, marker := range []string{"/*", "@", "//"} {
		if i := strings.Index(text, marker); i >= 0 {
			return text[:i], text[i:]
		}
	}
	return text, ""
}

// Translate translates the lines of a file (each with its newline, as
// Python's readlines() gives them).
func Translate(lines []string) ([]string, error) {
	out := make([]string, 0, len(lines))
	for n, line := range lines {
		m := insn.FindStringSubmatch(strings.TrimRight(line, "\n"))
		if m == nil || strings.HasPrefix(strings.TrimLeft(line, " \t\n\r\f\v"), "#") {
			out = append(out, line)
			continue
		}
		indent, label, gap, mn, rest := m[1], m[2], m[3], m[4], m[5]
		code, comment := stripComment(rest)
		nw, err := TranslateInsn(mn, code)
		if err != nil {
			return nil, fmt.Errorf("line %d: cannot translate %s%s: %v", n+1, mn, code, err)
		}
		if comment != "" {
			comment = " " + comment
		}
		out = append(out, indent+label+gap+nw+comment+"\n")
	}
	return out, nil
}

// Header is what the Python version writes before the translated lines.
func Header(inName string) string {
	return fmt.Sprintf("/* Translated from %s by tc32asm2thumb.py: ARMv4T Thumb, for thumb2tc32.py. */\n"+
		"\t.syntax unified\n\t.thumb\n", inName)
}

// SplitLines splits text as Python's readlines() does: every line keeps its
// newline, and a last line without one is kept too.
func SplitLines(text string) []string {
	var lines []string
	for len(text) > 0 {
		i := strings.IndexByte(text, '\n')
		if i < 0 {
			lines = append(lines, text)
			break
		}
		lines = append(lines, text[:i+1])
		text = text[i+1:]
	}
	return lines
}

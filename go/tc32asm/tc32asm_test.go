// SPDX-License-Identifier: Apache-2.0
package tc32asm

import "testing"

// TestTranslateInsn: one case per rule of tc32asm2thumb.py's translate_insn().
func TestTranslateInsn(t *testing.T) {
	cases := []struct{ mn, ops, want string }{
		{"tmcsr", " r0", ".inst.n 0xbbc0"},
		{"tmrss", " r3", ".inst.n 0xbbdb"},
		{"treti", " {r15}", ".inst.n 0xb900"},
		{"treti", " {r0-r3, r15}", ".inst.n 0xb90f"},
		{"tmov", " r1, #0", "movs r1, #0"},
		{"tmov", " r2, r1", "adds r2, r1, #0"},
		{"tmov", " r8, r2", "mov r8, r2"},
		{"tmov", " sp, r0", "mov sp, r0"},
		{"tadd", " r0, r0, r3", "adds r0, r0, r3"},
		{"tadd", " r1, r1, #4", "adds r1, r1, #4"},
		{"tadd", " r0, sp, #8", "add r0, sp, #8"},
		{"tadd", " sp, #16", "add sp, #16"},
		{"tsub", " sp, #16", "sub sp, #16"},
		{"tsub", " r3, r3, #1", "subs r3, r3, #1"},
		{"tadd", " r4, #1", "adds r4, #1"},
		{"tadd", " r8, r2", "add r8, r2"},
		{"tneg", " r0, r1", "rsbs r0, r1, #0"},
		{"tjeq", " .Ldone", "beq .Ldone"},
		{"tjls", " 2f", "bls 2f"},
		{"tjl", " z_prep_c", "bl z_prep_c"},
		{"tjex", " lr", "bx lr"},
		{"tj", " 1b", "b 1b"},
		{"tloadr", " r0, [r1, #4]", "ldr r0, [r1, #4]"},
		{"tstorerb", " r1, [r0]", "strb r1, [r0]"},
		{"tloadm", " r0!, {r1-r3}", "ldm r0!, {r1-r3}"},
		{"tpush", " {r0-r7}", "push {r0-r7}"},
		{"tbclr", " r2, r0", "bics r2, r0"},
		{"tnand", " r0, r1", "tst r0, r1"},
		{"tshftl", " r0, r0, #23", "lsls r0, r0, #23"},
		{"nop", "", "nop"},
	}
	for _, c := range cases {
		got, err := TranslateInsn(c.mn, c.ops)
		if err != nil {
			t.Errorf("%s%s: %v", c.mn, c.ops, err)
		} else if got != c.want {
			t.Errorf("%s%s: got %q, want %q", c.mn, c.ops, got, c.want)
		}
	}
	for _, c := range []struct{ mn, ops string }{
		{"tmov", " r0, r1, r2"}, // three operands
		{"tadd", " r0, r1, r8"}, // three registers, one high
		{"tsub", " r8, r2"},     // sub of high registers has no Thumb form here
		{"treti", " {r0, lr}"},  // lr cannot be in the list
		{"tfoo", " r0"},         // unknown mnemonic
	} {
		if _, err := TranslateInsn(c.mn, c.ops); err == nil {
			t.Errorf("%s%s: expected a refusal", c.mn, c.ops)
		}
	}
}

// TestTranslateLines: labels, comments, directives and preprocessor lines
// pass through as tc32asm2thumb.py keeps them.
func TestTranslateLines(t *testing.T) {
	in := []string{
		"#include <x.h>\n",
		"\t.org 0x10\n",
		"__irq:\ttpush {r14}\t/* save lr */\n",
		"\ttmov r5, #TC32_MODE_IRQ  @ irq mode\n",
		"\ttjl z_tc32_handle_irqs // call\n",
		"1:\ttcmp r2, #4\n",
	}
	want := []string{
		"#include <x.h>\n",
		"\t.org 0x10\n",
		"__irq:\tpush {r14} /* save lr */\n",
		"\tmovs r5, #TC32_MODE_IRQ @ irq mode\n",
		"\tbl z_tc32_handle_irqs // call\n",
		"1:\tcmp r2, #4\n",
	}
	out, err := Translate(in)
	if err != nil {
		t.Fatal(err)
	}
	for i := range want {
		if i >= len(out) || out[i] != want[i] {
			t.Errorf("line %d: got %q, want %q", i+1, out[i], want[i])
		}
	}
}

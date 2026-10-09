// run-boot: boot a TC32 Zephyr/ZMK image in the Go emulator and report what
// it reaches. The Go build of emulator/run_boot.py (with --stacks,
// emulator/stack_use.py), with the same output line for line.
//
// Usage: run-boot --elf zmk.elf --bin zmk.ota.bin [--layout direct|installed]
//
//	[--slot-a original.bin] [--ms 300] [--hang SYMBOL] [--resets N] [--cpi 1] [--stacks]
//	[--console [REG]] [--console-out FILE] [--console-stream] [--stop-at SYMBOL] [--stop-line TEXT]...
//	[--fatal-continue] [--usb]
//
// SPDX-License-Identifier: Apache-2.0
package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/goyamamoto/tc32-devtools/go/internal/nmsyms"
	"github.com/goyamamoto/tc32-devtools/go/regaudit"
	"github.com/goyamamoto/tc32-devtools/go/tc32emu"
)

var milestones = []string{"z_cstart", "z_sys_init_run_level", "tlsr_boot_guard_boot", "usb_dc_attach", "main",
	"zmk_keymap_init", "z_tc32_handle_irqs", "idle", "tlsr_slot_revert", "tlsr_reboot", "sys_arch_reboot"}
var fatalNames = []string{"z_fatal_error", "z_irq_spurious", "arch_system_halt", "k_sys_fatal_error_handler",
	"__assert_post_action", "assert_post_action", "z_tc32_fatal_error"}

const paint = 0xAA
const margin = 0.25           // fraction of each stack that must stay unused
const stackSection = "noinit" // Zephyr's output section for thread and kernel stacks

type stack struct {
	name string
	lo   uint32
	size uint32
}

// defaultTables: emulator/ next to this repository's go/bin, or ./emulator.
func defaultTables() string {
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), "..", "..", "emulator")
		if _, err := os.Stat(filepath.Join(p, "b87_registers.csv")); err == nil {
			return p
		}
	}
	return "emulator"
}

func die(err error) {
	fmt.Fprintln(os.Stderr, "run-boot:", err)
	os.Exit(1)
}

// symbols: code symbols' addresses (bit 0 cleared) and sizes.
func symbols(elf string) (map[string]uint32, map[string]uint32) {
	addr, size, err := nmsyms.Symbols(elf)
	if err != nil {
		die(err)
	}
	return addr, size
}

// stacks: the image's stacks, data objects in the noinit section whose names
// contain "stack" (stack_use.stacks): Zephyr places the thread and kernel
// stacks there; objects of other sections keep their contents.
func stacks(elf string) []stack {
	var found []stack
	lines, err := nmsyms.SysvLines(elf)
	if err != nil {
		die(err)
	}
	for _, line := range lines {
		// name | value | class | type | size | line | section
		p := strings.Split(line, "|")
		for i := range p {
			p[i] = strings.TrimSpace(p[i])
		}
		if len(p) == 7 && len(p[2]) == 1 && strings.Contains("BbDd", p[2]) && p[6] == stackSection &&
			strings.Contains(p[0], "stack") && p[4] != "" {
			a, _ := strconv.ParseUint(p[1], 16, 32)
			s, _ := strconv.ParseUint(p[4], 16, 32)
			if s >= 64 {
				found = append(found, stack{p[0], uint32(a), uint32(s)})
			}
		}
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].lo < found[j].lo })
	return found
}

// pyBytes formats b as Python's bytes repr.
func pyBytes(b []byte) string {
	var sb strings.Builder
	sb.WriteString("b'")
	for _, c := range b {
		switch {
		case c == '\\':
			sb.WriteString(`\\`)
		case c == '\'':
			sb.WriteString(`\'`)
		case c == '\t':
			sb.WriteString(`\t`)
		case c == '\n':
			sb.WriteString(`\n`)
		case c == '\r':
			sb.WriteString(`\r`)
		case c >= 0x20 && c < 0x7F:
			sb.WriteByte(c)
		default:
			fmt.Fprintf(&sb, `\x%02x`, c)
		}
	}
	sb.WriteString("'")
	return sb.String()
}

func pyStrList(items []string) string {
	q := make([]string, len(items))
	for i, s := range items {
		q[i] = "'" + s + "'"
	}
	return "[" + strings.Join(q, ", ") + "]"
}

func main() {
	var elfPath, binPath, slotAPath, hang string
	layout, ms, resets, cpi, withStacks := "direct", 300.0, 0, int64(1), false
	regAudit, reviewed, tables := false, "", ""
	consoleReg, consoleOut, consoleStream := -1, "", false
	stopAt := ""
	var stopLines []string
	fatalContinue := false
	withUsb := false
	emuStop := false
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		next := func() string { i++; return args[i] }
		switch args[i] {
		case "--elf":
			elfPath = next()
		case "--bin":
			binPath = next()
		case "--layout":
			layout = next()
		case "--slot-a":
			slotAPath = next()
		case "--ms":
			v, err := strconv.ParseFloat(next(), 64)
			if err != nil {
				die(err)
			}
			ms = v
		case "--hang":
			hang = next()
		case "--resets":
			v, err := strconv.Atoi(next())
			if err != nil {
				die(err)
			}
			resets = v
		case "--cpi":
			v, err := strconv.ParseInt(next(), 10, 64)
			if err != nil {
				die(err)
			}
			cpi = v
		case "--stacks":
			withStacks = true
		case "--reg-audit":
			regAudit = true
		case "--reviewed":
			reviewed = next()
		case "--tables":
			tables = next()
		case "--console":
			consoleReg = 0xFFF0
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				v, err := strconv.ParseInt(next(), 0, 32)
				if err != nil {
					die(err)
				}
				consoleReg = int(v)
			}
		case "--console-out":
			consoleOut = next()
		case "--console-stream":
			consoleStream = true
		case "--stop-at":
			stopAt = next()
		case "--stop-line":
			stopLines = append(stopLines, next())
		case "--fatal-continue":
			fatalContinue = true
		case "--usb":
			withUsb = true
		default:
			die(fmt.Errorf("unknown argument %s", args[i]))
		}
	}
	if elfPath == "" || binPath == "" {
		fmt.Fprintln(os.Stderr, "usage: run-boot --elf zmk.elf --bin zmk.ota.bin [--layout direct|installed] [--slot-a image] [--ms 300] [--hang SYMBOL] [--resets N] [--cpi 1] [--stacks] [--usb]")
		os.Exit(2)
	}
	addr, size := symbols(elfPath)
	img, err := os.ReadFile(binPath)
	if err != nil {
		die(err)
	}
	fl := tc32emu.NewFlash(tc32emu.FlashSize)
	if layout == "direct" {
		copy(fl.Mem, img)
	} else {
		copy(fl.Mem[0x20000:], img)
		if slotAPath != "" {
			orig, err := os.ReadFile(slotAPath)
			if err != nil {
				die(err)
			}
			if len(orig) > 0x20000 {
				orig = orig[:0x20000]
			}
			copy(fl.Mem, orig)
			copy(fl.Mem[8:12], []byte{0, 0, 0, 0})
		}
	}
	events := 0
	var m *tc32emu.Machine
	eventHook := func(text string) {
		events++
		if events <= 200 {
			fmt.Printf("[%9.3f ms] %s\n", m.Ms(), text)
		}
	}
	// NewMachine resets before the hook can be set: replay that first event as the Python __init__ prints it.
	m, err = tc32emu.NewMachine(fl, -1, cpi)
	if err != nil {
		die(err)
	}
	m.SetSymbols(addr)
	m.Event = eventHook
	if withUsb {
		// The USB device controller model with its host idle (run_boot.py --usb).
		tc32emu.NewUsbModel(m, true)
	}
	var console []byte
	streamed := 0 // with --console-stream: how much of the console is on stdout
	streamConsole := func(end int) {
		// console[streamed:end] to stdout, after the events printed so far
		if end > streamed {
			os.Stdout.Write(console[streamed:end])
			streamed = end
		}
	}
	type lineStop struct {
		ms   float64
		text string
	}
	var stoppedLine []lineStop
	if consoleReg >= 0 {
		// The image's console: one byte per write to the register.
		prev := m.RegWriteHook
		m.RegWriteHook = func(o, size int, val uint32) (bool, error) {
			if o == consoleReg && size == 1 {
				console = append(console, byte(val))
				if byte(val) == '\n' && consoleStream {
					streamConsole(len(console))
				}
				if byte(val) == '\n' && len(stopLines) > 0 {
					start := bytes.LastIndexByte(console[:len(console)-1], '\n') + 1
					line := string(console[start : len(console)-1])
					for _, t := range stopLines {
						if strings.Contains(line, t) {
							stoppedLine = append(stoppedLine, lineStop{m.Ms(), t})
							m.Event(fmt.Sprintf("--stop-line: console line contains %s", pyRepr(t)))
							if prev != nil {
								if handled, err := prev(o, size, val); handled || err != nil {
									return handled, err
								}
							}
							m.RegWriteHook = nil
							err := m.RegWrite(o, size, val)
							m.RegWriteHook = prev
							if err != nil {
								return true, err
							}
							return true, &tc32emu.StopError{Reason: "stop-line"}
						}
					}
				}
			}
			if prev != nil {
				return prev(o, size, val)
			}
			return false, nil
		}
	}
	regLog := map[regaudit.Access]bool{}
	if regAudit {
		// The Python reg_log: every register access with its size, direction and pc.
		m.RegReadHook = func(o, size int) (uint32, bool, error) {
			regLog[regaudit.Access{Offset: o, Size: size, RW: 'r', PC: m.R[15]}] = true
			return 0, false, nil
		}
		m.RegWriteHook = func(o, size int, val uint32) (bool, error) {
			regLog[regaudit.Access{Offset: o, Size: size, RW: 'w', PC: m.R[15]}] = true
			return false, nil
		}
	}
	eventHook(fmt.Sprintf("reset #%d: boot slot 0x%05x", m.ResetCount, m.BootSlot))

	type seenAt struct {
		name string
		ms   float64
	}
	var seen []seenAt
	var fatal []string
	m.Hooks = map[uint32]func(*tc32emu.Machine) error{}
	milestone := func(name string) func(*tc32emu.Machine) error {
		return func(mm *tc32emu.Machine) error {
			for _, s := range seen {
				if s.name == name {
					return nil
				}
			}
			seen = append(seen, seenAt{name, mm.Ms()})
			mm.Event("reached " + name)
			return nil
		}
	}
	fatalHook := func(name string) func(*tc32emu.Machine) error {
		return func(mm *tc32emu.Machine) error {
			caller := mm.Symbolize(mm.R[14])
			fatal = append(fatal, fmt.Sprintf("%s (r0=%#x, lr=%s)", name, mm.R[0], caller))
			mm.Event(fmt.Sprintf("FATAL %s: r0=%#x r1=%#x lr=%s", name, mm.R[0], mm.R[1], caller))
			if fatalContinue {
				return nil
			}
			return &tc32emu.StopError{}
		}
	}
	hangHook := func(mm *tc32emu.Machine) error {
		mm.Event(fmt.Sprintf("--hang: %s spins", hang))
		mm.R[15] = addr[hang] // stays here; the hook runs again each step
		mm.Cycles++
		return &tc32emu.StopError{Reason: "hang"}
	}
	for _, n := range milestones {
		if a, ok := addr[n]; ok {
			m.Hooks[a] = milestone(n)
		}
	}
	var stoppedAt []float64
	if a, ok := addr[stopAt]; stopAt != "" && ok {
		m.Hooks[a] = func(mm *tc32emu.Machine) error {
			stoppedAt = append(stoppedAt, mm.Ms())
			mm.Event("--stop-at: reached " + stopAt)
			return &tc32emu.StopError{Reason: "stop-at"}
		}
	}
	var found []stack
	if withStacks {
		if a, ok := addr["z_cstart"]; ok {
			found = stacks(elfPath)
			cstart := milestone("z_cstart")
			m.Hooks[a] = func(mm *tc32emu.Machine) error {
				// Paint the stacks (stack_use.paint): 0xaa below any stack pointer that points into one.
				sps := []uint32{mm.R[13]}
				for _, b := range mm.BankedSP() {
					sps = append(sps, b)
				}
				for _, st := range found {
					hi := st.lo + st.size
					for _, sp := range sps {
						if st.lo < sp && sp <= st.lo+st.size && sp < hi {
							hi = sp
						}
					}
					o := st.lo - tc32emu.SRAMBase
					for i := uint32(0); i < hi-st.lo; i++ {
						mm.SRAM[o+i] = paint
					}
				}
				return cstart(mm)
			}
		}
	}
	for _, n := range fatalNames {
		if a, ok := addr[n]; ok {
			m.Hooks[a] = fatalHook(n)
		}
	}
	idle := nmsyms.IdleRanges(addr, size)

	nResets := 0
	hanging := false
	for {
		if _, ok := addr[hang]; hang != "" && ok && !hanging {
			m.Hooks[addr[hang]] = hangHook
		}
		var err error
		if hanging {
			// A hang: burn time until the watchdog fires or the limit is reached.
			for m.Ms() < ms {
				m.Cycles += 10000
				if err = m.UpdateTime(); err != nil {
					break
				}
			}
		} else {
			err = m.RunMs(ms, idle)
		}
		if err == nil {
			break
		}
		var stop *tc32emu.StopError
		var ee *tc32emu.EmuError
		switch {
		case errors.As(err, &stop):
			if stop.Reason == "hang" {
				hanging = true
				continue
			}
		case errors.Is(err, tc32emu.ErrWatchdogReset) || errors.Is(err, tc32emu.ErrSoftwareReset):
			wd := errors.Is(err, tc32emu.ErrWatchdogReset)
			if wd {
				m.Event("watchdog reset")
			} else {
				m.Event("software reset")
			}
			if nResets >= resets {
				break
			}
			nResets++
			hanging = false
			if err := m.Reset(-1, wd); err != nil {
				die(err)
			}
			seen = seen[:0]
			continue
		case errors.As(err, &ee):
			m.Event("EMULATOR STOP: " + ee.Error())
			fatal = append(fatal, ee.Error())
			emuStop = true
		default:
			die(err)
		}
		break
	}

	if consoleReg >= 0 && consoleStream && len(console) > streamed {
		// A last line the image did not end: out as it is, ended here.
		streamConsole(len(console))
		fmt.Println()
	}
	r := m.Regs
	fmt.Println("\n== summary")
	fmt.Printf("simulated %.1f ms, boot slot 0x%05x, pc %s\n", m.Ms(), m.BootSlot, m.Symbolize(m.R[15]))
	if m.ICacheMiss != 0 {
		fmt.Printf("flash cache model: %d misses at %d cycles each, %d of %d cycles\n",
			m.ICacheMisses, m.ICacheMiss, m.ICacheMisses*m.ICacheMiss, m.Cycles)
	}
	ms_ := make([]string, 0, len(seen))
	for _, s := range seen {
		ms_ = append(ms_, fmt.Sprintf("%s @%.2fms", s.name, s.ms))
	}
	if len(ms_) == 0 {
		fmt.Println("milestones: none")
	} else {
		fmt.Println("milestones:", strings.Join(ms_, ", "))
	}
	if stopAt != "" {
		if len(stoppedAt) > 0 {
			fmt.Printf("stop-at %s: reached at %.2f ms\n", stopAt, stoppedAt[0])
		} else {
			fmt.Printf("stop-at %s: not reached\n", stopAt)
		}
	}
	if len(stopLines) > 0 {
		if len(stoppedLine) > 0 {
			fmt.Printf("stop-line: %s at %.2f ms\n", pyRepr(stoppedLine[0].text), stoppedLine[0].ms)
		} else {
			fmt.Println("stop-line: not reached")
		}
	}
	fmt.Printf("USB DP pull-up (analog 0x0b bit 7): %d\n", m.Analog[0x0B]>>7)
	wd := uint32(r[0x620]) | uint32(r[0x621])<<8 | uint32(r[0x622])<<16
	fmt.Printf("watchdog: enabled=%d timer2=%d capture=%d\n", wd>>23&1, wd>>6&1, wd>>9&0x3fff)
	var ops []string
	for i, op := range fl.Log {
		if i >= 12 {
			break
		}
		if op.Op == "status" {
			ops = append(ops, fmt.Sprintf("('%s', '0x%x', '%s')", op.Op, op.Addr, op.Data))
		} else {
			ops = append(ops, fmt.Sprintf("('%s', '0x%x', %d)", op.Op, op.Addr, op.Len))
		}
	}
	more := ""
	if len(fl.Log) > 12 {
		more = "..."
	}
	fmt.Printf("flash writes: [%s] %s\n", strings.Join(ops, ", "), more)
	fmt.Printf("slot flags: A=%s B=%s\n", pyBytes(fl.Mem[8:12]), pyBytes(fl.Mem[0x20008:0x2000c]))
	if withStacks && len(found) > 0 {
		var parts, over []string
		for _, st := range found {
			o := st.lo - tc32emu.SRAMBase
			first := st.size
			for i := uint32(0); i < st.size; i++ {
				if m.SRAM[o+i] != paint {
					first = i
					break
				}
			}
			used := st.size - first
			parts = append(parts, fmt.Sprintf("%s %d of %d B", st.name, used, st.size))
			if float64(used) > float64(st.size)*(1-margin) {
				over = append(over, fmt.Sprintf("%s %d/%d", st.name, used, st.size))
			}
		}
		text := fmt.Sprintf("stack use within %d%% of each stack: %s", 100-int(margin*100), strings.Join(parts, ", "))
		if len(over) > 0 {
			text += "; over: " + pyStrList(over)
		}
		fmt.Println("stacks:", text)
	}
	if consoleReg >= 0 {
		text := string(console)
		if consoleOut != "" {
			if err := os.WriteFile(consoleOut, []byte(text), 0o644); err != nil {
				die(err)
			}
			fmt.Printf("console: %d bytes to %s\n", len(console), consoleOut)
		} else if consoleStream {
			fmt.Printf("console: %d bytes, streamed above\n", len(console))
		} else {
			fmt.Printf("console: %d bytes\n", len(console))
			fmt.Print(text)
		}
	}
	if regAudit {
		if tables == "" {
			tables = defaultTables()
		}
		b85, err := regaudit.Load(filepath.Join(tables, "b85_registers.csv"))
		if err != nil {
			die(err)
		}
		b87, err := regaudit.Load(filepath.Join(tables, "b87_registers.csv"))
		if err != nil {
			die(err)
		}
		rev, err := regaudit.LoadReviewed(reviewed)
		if err != nil {
			die(err)
		}
		log := make([]regaudit.Access, 0, len(regLog))
		for a := range regLog {
			log = append(log, a)
		}
		rows := regaudit.Audit(log, m.Symbolize, b85, b87, rev)
		fmt.Printf("registers: %d bytes touched, %d not reviewed\n", len(rows), len(regaudit.Unreviewed(rows)))
		fmt.Println(regaudit.FormatRows(rows))
	}
	if len(fatal) == 0 {
		fmt.Println("fatal: none")
	} else {
		fmt.Println("fatal:", pyStrList(fatal))
		// With --fatal-continue the image's own handling decides; only an emulator stop fails the run.
		if !fatalContinue || emuStop {
			os.Exit(1)
		}
	}
}

// pyRepr is Python's repr() of a short string, so the two run-boots print
// the same line.
func pyRepr(s string) string {
	q := "'"
	if strings.Contains(s, "'") && !strings.Contains(s, "\"") {
		q = "\""
	}
	var b strings.Builder
	b.WriteString(q)
	for _, r := range s {
		switch {
		case r == '\\':
			b.WriteString("\\\\")
		case string(r) == q:
			b.WriteString("\\" + q)
		case r == '\n':
			b.WriteString("\\n")
		case r == '\t':
			b.WriteString("\\t")
		case r == '\r':
			b.WriteString("\\r")
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, "\\x%02x", r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteString(q)
	return b.String()
}

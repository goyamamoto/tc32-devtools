// tc32emu-trace: run an image in the Go emulator and print the lockstep
// trace of emulator/trace.py, line for line (checks/lockstep.sh compares the
// two with cmp). See trace.py for the format and the --usb scenario.
//
// Usage: tc32emu-trace <image.bin> [--slot 0x0] [--cycles N] [--cpi 1] [--usb] [--check-every 32]
//
//	[--sparse N] [--idle-elf zmk.elf] [--usb-suspend MS] [--usb-resume MS] [--usb-resume-k MS]
//
// SPDX-License-Identifier: Apache-2.0
package main

import (
	"bufio"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/goyamamoto/tc32-devtools/go/internal/nmsyms"
	"github.com/goyamamoto/tc32-devtools/go/tc32emu"
)

var out *bufio.Writer

func main() {
	image := ""
	slot, cycles, cpi := -1, int64(1_000_000), int64(1)
	usb := false
	checkEvery := 32
	sparse := int64(0)
	idleElf := ""
	usbSuspend := 0.0
	usbResume := 0.0
	usbResumeK := 20.0
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--slot":
			i++
			v, err := strconv.ParseInt(args[i], 0, 32)
			if err != nil {
				die(err)
			}
			slot = int(v)
		case "--cycles":
			i++
			v, err := strconv.ParseInt(args[i], 0, 64)
			if err != nil {
				die(err)
			}
			cycles = v
		case "--cpi":
			i++
			v, err := strconv.ParseInt(args[i], 0, 64)
			if err != nil {
				die(err)
			}
			cpi = v
		case "--usb":
			usb = true
		case "--check-every":
			i++
			v, err := strconv.Atoi(args[i])
			if err != nil {
				die(err)
			}
			checkEvery = v
		case "--sparse":
			i++
			v, err := strconv.ParseInt(args[i], 0, 64)
			if err != nil {
				die(err)
			}
			sparse = v
		case "--idle-elf":
			i++
			idleElf = args[i]
		case "--usb-suspend":
			i++
			v, err := strconv.ParseFloat(args[i], 64)
			if err != nil {
				die(err)
			}
			usbSuspend = v
		case "--usb-resume":
			i++
			v, err := strconv.ParseFloat(args[i], 64)
			if err != nil {
				die(err)
			}
			usbResume = v
		case "--usb-resume-k":
			i++
			v, err := strconv.ParseFloat(args[i], 64)
			if err != nil {
				die(err)
			}
			usbResumeK = v
		default:
			if image != "" {
				usage()
			}
			image = args[i]
		}
	}
	if image == "" {
		usage()
	}
	data, err := os.ReadFile(image)
	if err != nil {
		die(err)
	}
	fl := tc32emu.NewFlash(tc32emu.FlashSize)
	copy(fl.Mem, data)
	m, err := tc32emu.NewMachine(fl, slot, cpi)
	if err != nil {
		die(err)
	}
	out = bufio.NewWriterSize(os.Stdout, 1<<20)
	defer out.Flush()
	var idle []tc32emu.Range
	if idleElf != "" {
		addr, size, err := nmsyms.Symbols(idleElf)
		if err != nil {
			die(err)
		}
		idle = nmsyms.IdleRanges(addr, size)
	}
	var executed int64
	m.TraceStep = func(pc uint32, hw uint16) {
		r := m.R
		executed++
		if sparse > 0 {
			if executed%sparse == 0 {
				fmt.Fprintf(out, "at %d %d %06x %04x %08x %08x %08x %08x %08x %08x %08x %08x %08x %08x %08x %08x %08x %08x %08x %08x %08x\n",
					executed, m.Cycles, pc, hw, r[0], r[1], r[2], r[3], r[4], r[5], r[6], r[7], r[8], r[9], r[10], r[11], r[12], r[13], r[14], r[15], m.CPSR())
			}
			return
		}
		fmt.Fprintf(out, "%06x %04x %08x %08x %08x %08x %08x %08x %08x %08x %08x %08x %08x %08x %08x %08x %08x %08x %08x\n",
			pc, hw, r[0], r[1], r[2], r[3], r[4], r[5], r[6], r[7], r[8], r[9], r[10], r[11], r[12], r[13], r[14], r[15], m.CPSR())
	}
	m.OnIrq = func() { fmt.Fprintf(out, "irq %d\n", m.Cycles) }
	m.OnWake = func(st byte) { fmt.Fprintf(out, "wake %02x %d\n", m.Analog[0x44], m.Cycles) }

	if usb {
		guarded(m, func() error { return usbScenario(m, usbSuspend, idle, usbResume, usbResumeK) })
	} else {
		for m.Cycles < cycles {
			if !guarded(m, func() error { return m.Run(cycles, idle, checkEvery) }) {
				break
			}
		}
	}
	fmt.Fprintf(out, "end cycles=%d sram=%x regs=%x analog=%x\n", m.Cycles, sha256.Sum256(m.SRAM), sha256.Sum256(m.Regs),
		sha256.Sum256(m.Analog[:]))
}

// guarded runs fn; on a reset it prints it and resets the machine (true: go
// on); on an emulator stop it prints it (false).
func guarded(m *tc32emu.Machine, fn func() error) bool {
	err := fn()
	var ee *tc32emu.EmuError
	switch {
	case err == nil:
		return true
	case errors.Is(err, tc32emu.ErrSoftwareReset):
		fmt.Fprintf(out, "reset sw %d\n", m.Cycles)
		if err := m.Reset(-1, false); err != nil {
			die(err)
		}
		return true
	case errors.Is(err, tc32emu.ErrWatchdogReset):
		fmt.Fprintf(out, "reset wd %d\n", m.Cycles)
		if err := m.Reset(-1, true); err != nil {
			die(err)
		}
		return true
	case errors.As(err, &ee):
		fmt.Fprintln(out, "stop emuerror")
		fmt.Fprintln(os.Stderr, "trace:", err)
		return false
	}
	die(err)
	return false
}

func usbScenario(m *tc32emu.Machine, suspendMs float64, idle []tc32emu.Range, resumeMs, resumeKMs float64) error {
	u := tc32emu.NewUsbModel(m, false)
	ok, err := u.RunUntil(u.DPPullup, 100)
	if err != nil {
		return err
	}
	okInt := 0
	if ok {
		okInt = 1
	}
	fmt.Fprintf(out, "usb attach %d %d\n", okInt, m.Cycles)
	if !ok {
		return nil
	}
	if err := u.BusReset(5); err != nil {
		return err
	}
	fmt.Fprintf(out, "usb reset %d\n", m.Cycles)
	err = func() error {
		dev, err := u.GetDescriptor(1, 0, 0, 18, 0x80)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "usb device %x\n", dev)
		if _, err := u.Control([]byte{0, 5, 1, 0, 0, 0, 0, 0}, nil, 0); err != nil {
			return err
		}
		fmt.Fprintf(out, "usb address %d %d\n", u.Address, m.Cycles)
		cfg, err := u.GetDescriptor(2, 0, 0, 9, 0x80)
		if err != nil {
			return err
		}
		total := int(cfg[2]) | int(cfg[3])<<8
		cfg, err = u.GetDescriptor(2, 0, 0, total, 0x80)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "usb config %x\n", cfg)
		if _, err := u.Control([]byte{0, 9, 1, 0, 0, 0, 0, 0}, nil, 0); err != nil {
			return err
		}
		fmt.Fprintf(out, "usb configured %d\n", m.Cycles)
		for i := 0; i < 200; i++ {
			if err := m.RunMs(m.Ms()+1, nil); err != nil {
				return err
			}
			for ep := byte(1); ep <= 4; ep++ {
				if pkt := u.InEp(ep, true); pkt != nil {
					fmt.Fprintf(out, "usb in%d %x %d\n", ep, pkt, m.Cycles)
				}
			}
		}
		if suspendMs > 0 {
			u.Suspend()
			fmt.Fprintf(out, "usb suspend %d\n", m.Cycles)
			if err := m.RunMs(m.Ms()+suspendMs, idle); err != nil {
				return err
			}
			fmt.Fprintf(out, "usb suspend-end %d wakes=%d remote=%d\n", m.Cycles, len(m.Wakes), len(u.RemoteWakeups))
			if resumeMs > 0 {
				// The host resumes: K for resumeKMs, then the bus runs for resumeMs.
				if err := u.Resume(resumeKMs); err != nil {
					return err
				}
				fmt.Fprintf(out, "usb resume-k-end %d wakes=%d asleep=%d\n", m.Cycles, len(m.Wakes), b2i(m.Asleep != nil))
				if err := m.RunMs(m.Ms()+resumeMs, idle); err != nil {
					return err
				}
				fmt.Fprintf(out, "usb resume-end %d wakes=%d asleep=%d\n", m.Cycles, len(m.Wakes), b2i(m.Asleep != nil))
			}
		}
		return nil
	}()
	if err != nil && tc32emu.IsUsbError(err) {
		fmt.Fprintln(out, "usb error")
		fmt.Fprintln(os.Stderr, "trace: usb:", err)
		return nil
	}
	return err
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: tc32emu-trace <image.bin> [--slot 0x0] [--cycles N] [--cpi 1] [--usb]")
	os.Exit(2)
}

func die(err error) {
	if out != nil {
		out.Flush()
	}
	fmt.Fprintln(os.Stderr, "tc32emu-trace:", err)
	os.Exit(1)
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

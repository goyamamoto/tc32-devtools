// SPDX-License-Identifier: Apache-2.0
package tc32emu

import (
	"errors"
	"fmt"
	"os"
)

// UsbModel is the Go port of ../../emulator/usb_model.py: a register model
// of the TLSR8278 USB device controller, and a USB host that drives it while
// the emulated firmware runs. See the Python docstring for what the model
// assumes; this file follows it method by method.
type UsbModel struct {
	m              *Machine
	IrqLines       bool
	StageTimeoutMs float64 // how long the host waits for a stage (the Python stage_timeout_ms)
	SlowStages     []SlowStage
	setup          []byte // the running transfer's
	fifo           [8]byte
	ep0Ptr         int
	overflow       bool
	ep0Ctrl        int // -1: none since the stage began
	usbRAM         [256]byte
	epPtr          [8]int
	toggleNext     [8]int
	ToggleChecked  int
	ToggleErrors   int
	hwConfig       byte
	Address        byte
	modeReset      byte
	Suspended      bool
	kState         bool
	// CoreWake is "level" (the default) or "edge" (TC32EMU_USB_CORE_WAKE, or
	// set here): whether the host's resume K wakes the chip for as long as it
	// lasts or once at its start. Our documents do not say which the chip
	// does; see the Python usb_model.py.
	CoreWake      string
	kEdge         bool      // edge: the start of K, not yet taken by a wake
	RemoteWakeups []float64 // ms of each resume signalled by the device (0x6e bit 6 while suspended)
	prevReadHook  func(o, size int) (uint32, bool, error)
	prevWriteHook func(o, size int, val uint32) (bool, error)
	prevPadsHook  func(port int, lvl, flt byte) (byte, byte)
}

// SpecStageMs is the Python UsbModel.SPEC_STAGE_MS: USB 2.0 9.2.6.4's 50 ms
// for a standard request without data and for the status stage. A longer
// stage is an event and a SlowStage, not an error: hosts wait longer
// (StageTimeoutMs, 5000 ms as Linux's control transfer timeout).
const SpecStageMs = 50

// SlowStage is one entry of UsbModel.SlowStages (the Python slow_stages):
// a stage of a standard request that took longer than SpecStageMs.
type SlowStage struct {
	StartMs     float64
	RequestType byte
	Request     byte
	Stage       byte
	TookMs      float64
}

// UsbError is a protocol failure seen by the host (the Python UsbError).
type UsbError struct{ Msg string }

func (e *UsbError) Error() string { return e.Msg }

// UsbModelError is an access the model does not cover.
type UsbModelError struct{ Msg string }

func (e *UsbModelError) Error() string { return e.Msg }

const (
	staSetup, staData, staSta          = 0x10, 0x20, 0x40
	datAck, datStall, stAck, stStall   = 0x01, 0x02, 0x04, 0x08
	epBusy, epDat0, epDat1             = 0x01, 0x04, 0x08
	irqEp0Setup, irqEp0Data, irqEp0Sta = 8, 9, 10
	irqEpData, irqUsbRst               = 12, 17
)

// NewUsbModel attaches the model to m, as UsbModel.__init__ does.
func NewUsbModel(m *Machine, irqLines bool) *UsbModel {
	u := &UsbModel{m: m, IrqLines: irqLines, StageTimeoutMs: 5000, ep0Ctrl: -1, modeReset: 0xFF}
	m.Regs[0x104] = u.modeReset
	m.LevelSources = append(m.LevelSources, u.level)
	u.CoreWake = os.Getenv("TC32EMU_USB_CORE_WAKE")
	if u.CoreWake == "" {
		u.CoreWake = "level"
	}
	if u.CoreWake != "level" && u.CoreWake != "edge" {
		panic("TC32EMU_USB_CORE_WAKE: " + u.CoreWake)
	}
	m.CoreWakeSources = append(m.CoreWakeSources, u.coreWakeNow)
	u.prevPadsHook = m.PadLevelsHook
	m.PadLevelsHook = u.padLevels
	m.ResetHooks = append(m.ResetHooks, u.onReset)
	u.prevReadHook, u.prevWriteHook = m.RegReadHook, m.RegWriteHook
	m.RegReadHook, m.RegWriteHook = u.regRead, u.regWrite
	return u
}

func (u *UsbModel) onReset() {
	u.m.Regs[0x104] = u.modeReset
	u.ep0Ptr, u.epPtr, u.ep0Ctrl = 0, [8]int{}, -1
}

// ------------------------------------------------------------ device side

func (u *UsbModel) regRead(o, size int) (uint32, bool, error) {
	if !(0x100 <= o && o < 0x140) {
		if u.prevReadHook != nil {
			return u.prevReadHook(o, size)
		}
		return 0, false, nil
	}
	if size != 1 {
		return 0, true, &UsbModelError{fmt.Sprintf("%d-bit read of USB register 0x%03x", size*8, o)}
	}
	r := u.m.Regs
	switch {
	case o == 0x100:
		return uint32(u.ep0Ptr & 0xFF), true, nil
	case o == 0x101:
		v := u.fifo[u.ep0Ptr&7]
		u.ep0Ptr++
		return uint32(v), true, nil
	case 0x110 <= o && o < 0x118:
		return uint32(u.epPtr[o-0x110] & 0xFF), true, nil
	case 0x118 <= o && o < 0x120:
		s := o - 0x118
		v := u.usbRAM[(int(r[0x128+s])+u.epPtr[s])&0xFF]
		u.epPtr[s]++
		return uint32(v), true, nil
	}
	return uint32(r[o]), true, nil
}

func (u *UsbModel) regWrite(o, size int, val uint32) (bool, error) {
	if o == 0x6E && size == 1 && val&0x40 != 0 && u.Suspended {
		u.RemoteWakeups = append(u.RemoteWakeups, u.m.Ms())
		u.m.event("USB: resume signalled by the device (0x6e bit 6 while suspended)")
	}
	if !(0x100 <= o && o < 0x140) {
		if u.prevWriteHook != nil {
			return u.prevWriteHook(o, size, val)
		}
		return false, nil
	}
	if size != 1 {
		return true, &UsbModelError{fmt.Sprintf("%d-bit write of USB register 0x%03x", size*8, o)}
	}
	r := u.m.Regs
	switch {
	case o == 0x100:
		u.ep0Ptr = int(val)
	case o == 0x101:
		if u.ep0Ptr >= 8 {
			u.overflow = true
		}
		u.fifo[u.ep0Ptr&7] = byte(val)
		u.ep0Ptr++
	case o == 0x102:
		u.ep0Ctrl = int(val)
		r[o] = byte(val)
	case o == 0x103 || o == 0x139:
		r[o] &^= byte(val)
	case 0x110 <= o && o < 0x118:
		u.epPtr[o-0x110] = int(val)
	case 0x118 <= o && o < 0x120:
		s := o - 0x118
		u.usbRAM[(int(r[0x128+s])+u.epPtr[s])&0xFF] = byte(val)
		u.epPtr[s]++
	default:
		r[o] = byte(val)
	}
	return true, nil
}

// level: source 3 while suspended; 8-12 as the controller's status holds
// them (with IrqLines).
func (u *UsbModel) level() uint32 {
	if !u.IrqLines {
		if u.Suspended {
			return 1 << 3
		}
		return 0
	}
	r := u.m.Regs
	sta := uint32(r[0x103])
	bits := ((sta>>4)&1)<<8 | ((sta>>5)&1)<<9 | ((sta>>6)&1)<<10
	if r[0x139]&r[0x13A] != 0 {
		bits |= 1 << 12
	}
	if u.Suspended {
		bits |= 1 << 3
	}
	return bits
}

// padLevels: DP (PA6) and DM (PA5): J with the device's pull-up on DP, K
// during resume (DP low, DM high); the host's pull-downs hold them otherwise.
func (u *UsbModel) padLevels(port int, lvl, flt byte) (byte, byte) {
	if u.prevPadsHook != nil {
		lvl, flt = u.prevPadsHook(port, lvl, flt)
	}
	if port == 0 {
		lvl, flt = lvl&^0x60, flt&^0x60
		if u.kState {
			lvl |= 0x20
		} else if u.DPPullup() {
			lvl |= 0x40
		}
	}
	return lvl, flt
}

// ------------------------------------------------------------ host side

// DPPullup says whether the device's DP pull-up is on (analog 0x0b bit 7).
func (u *UsbModel) DPPullup() bool { return u.m.Analog[0x0B]&0x80 != 0 }

// RunUntil runs the machine until cond holds or timeoutMs pass; false on the
// timeout. An emulator stop is returned as the error.
func (u *UsbModel) RunUntil(cond func() bool, timeoutMs float64) (bool, error) {
	end := u.m.Ms() + timeoutMs
	for !cond() {
		if u.m.Ms() >= end {
			return false, nil
		}
		if err := u.m.Run(u.m.Cycles+256, nil, 32); err != nil {
			return false, err
		}
	}
	return true, nil
}

// Suspend: the host stops the bus.
func (u *UsbModel) Suspend() {
	u.Suspended = true
	u.m.event("USB: bus suspended by the host")
}

// coreWakeNow is the Python core_wake_now(): the USB core wake source, asked
// when the chip sleeps.
func (u *UsbModel) coreWakeNow() bool {
	if u.m.Regs[0x6E]&0x04 == 0 {
		return false
	}
	if u.CoreWake == "edge" {
		taken := u.kEdge
		u.kEdge = false
		return taken
	}
	return u.kState
}

// Resume: the host resumes the bus: K for kMs, then SOFs again.
func (u *UsbModel) Resume(kMs float64) error {
	u.kState, u.kEdge = true, true
	u.m.event("USB: resume (K) from the host")
	err := u.m.RunMs(u.m.Ms()+kMs, nil)
	u.kState, u.kEdge = false, false
	u.Suspended = false
	return err
}

// ResetToggles restarts the data toggles at DATA0 (the Python
// usb.toggle_next = [0] * 8 a host does after SET_CONFIGURATION).
func (u *UsbModel) ResetToggles() { u.toggleNext = [8]int{} }

// BusReset resets the bus and lets the device settle for settleMs.
func (u *UsbModel) BusReset(settleMs float64) error {
	u.toggleNext = [8]int{}
	u.Suspended = false
	u.m.Regs[0x10A] &^= 0x04
	u.m.Regs[0x10B] = 0
	u.m.irqLatch |= 1 << irqUsbRst
	return u.m.RunMs(u.m.Ms()+settleMs, nil)
}

func (u *UsbModel) stage(sta byte, ack, stall int) error {
	u.ep0Ctrl = -1
	u.m.Regs[0x103] |= sta
	start := u.m.Ms()
	ok, err := u.RunUntil(func() bool { return u.ep0Ctrl >= 0 && u.m.Regs[0x103]&sta == 0 }, u.StageTimeoutMs)
	took := u.m.Ms() - start
	if err != nil {
		return err
	}
	if u.overflow {
		return &UsbError{"device wrote more than 8 bytes into the EP0 FIFO"}
	}
	if !ok || u.ep0Ctrl < 0 {
		return &UsbError{fmt.Sprintf("no answer to stage 0x%02x within %g ms", sta, u.StageTimeoutMs)}
	}
	if took > SpecStageMs && u.setup[0]&0x60 == 0 {
		u.SlowStages = append(u.SlowStages, SlowStage{start, u.setup[0], u.setup[1], sta, took})
		u.m.event(fmt.Sprintf("USB: stage 0x%02x of request 0x%02x/0x%02x took %.1f ms, more than USB 2.0 9.2.6.4's %d ms for a standard request (hosts wait longer)",
			sta, u.setup[0], u.setup[1], took, SpecStageMs))
	}
	if u.ep0Ctrl&stall != 0 {
		return &UsbError{"STALL"}
	}
	if u.ep0Ctrl&ack == 0 {
		return &UsbError{fmt.Sprintf("stage 0x%02x armed without ACK (ctrl 0x%02x)", sta, u.ep0Ctrl)}
	}
	return nil
}

var autoBit = map[byte]byte{5: 0x01, 9: 0x02, 8: 0x02, 11: 0x04, 10: 0x04, 0: 0x08, 12: 0x10, 6: 0x20, 3: 0x40, 1: 0x40}

// HardwareAnswers says whether the controller handles this request itself (0x104).
func (u *UsbModel) HardwareAnswers(setup []byte) bool {
	if setup[0]&0x60 != 0 { // class or vendor: always the firmware
		return false
	}
	bit, ok := autoBit[setup[1]]
	if !ok {
		bit = 0x80
	}
	return u.m.Regs[0x104]&bit != 0
}

// Control performs one control transfer; it returns the IN data, or nil for OUT.
func (u *UsbModel) Control(setup []byte, out []byte, inMax int) ([]byte, error) {
	wlength := int(setup[6]) | int(setup[7])<<8
	if setup[0] == 0 && setup[1] == 9 { // SET_CONFIGURATION
		if setup[2] != 0 {
			u.m.Regs[0x10B] = 1
		} else {
			u.m.Regs[0x10B] = 0
		}
	}
	if u.HardwareAnswers(setup) {
		if setup[0] == 0 && (setup[1] == 1 || setup[1] == 3) && setup[2] == 1 { // CLEAR/SET_FEATURE(DEVICE_REMOTE_WAKEUP)
			if setup[1] == 3 {
				u.m.Regs[0x10A] |= 0x04
			} else {
				u.m.Regs[0x10A] &^= 0x04
			}
		}
		if setup[1] == 5 {
			u.Address = setup[2]
		} else if setup[1] == 9 {
			u.hwConfig = setup[2]
		}
		if setup[0]&0x80 != 0 {
			var data []byte
			if setup[1] == 8 {
				data = []byte{u.hwConfig}
			} else {
				data = make([]byte, wlength)
			}
			n := inMax
			if n == 0 {
				n = wlength
			}
			if n < len(data) {
				data = data[:n]
			}
			return data, nil
		}
		return nil, nil
	}
	u.overflow = false
	u.setup = append(u.setup[:0], setup...)
	copy(u.fifo[:], setup)
	u.ep0Ptr = 8
	if err := u.stage(staSetup, datAck, datStall); err != nil {
		return nil, err
	}
	if setup[0]&0x80 != 0 {
		var data []byte
		for {
			n := u.ep0Ptr
			if n > 8 {
				return nil, &UsbError{"IN packet longer than 8 bytes"}
			}
			data = append(data, u.fifo[:n]...)
			if err := u.stage(staData, datAck, datStall); err != nil {
				return nil, err
			}
			if n < 8 || len(data) >= wlength {
				break
			}
		}
		u.ep0Ptr = 0
		if err := u.stage(staSta, stAck, stStall); err != nil {
			return nil, err
		}
		n := inMax
		if n == 0 {
			n = wlength
		}
		if n < len(data) {
			data = data[:n]
		}
		return data, nil
	}
	for i := 0; i < len(out); i += 8 {
		chunk := out[i:]
		if len(chunk) > 8 {
			chunk = chunk[:8]
		}
		copy(u.fifo[:], chunk)
		u.ep0Ptr = len(chunk)
		if err := u.stage(staData, datAck, datStall); err != nil {
			return nil, err
		}
	}
	if err := u.stage(staSta, stAck, stStall); err != nil {
		return nil, err
	}
	return nil, nil
}

// GetDescriptor is the GET_DESCRIPTOR control transfer.
func (u *UsbModel) GetDescriptor(dtype, index byte, windex uint16, length int, recipient byte) ([]byte, error) {
	return u.Control([]byte{recipient, 6, index, dtype, byte(windex), byte(windex >> 8), byte(length), byte(length >> 8)},
		nil, 0)
}

// OutEp sends an OUT packet to data endpoint ep; false if the endpoint NAKs.
func (u *UsbModel) OutEp(ep byte, data []byte) bool {
	s := int(ep & 7)
	r := u.m.Regs
	if r[0x120+s]&epBusy == 0 {
		return false
	}
	for i, b := range data {
		u.usbRAM[(int(r[0x128+s])+i)&0xFF] = b
	}
	u.epPtr[s] = len(data)
	r[0x120+s] &^= epBusy
	r[0x139] |= 1 << uint(s)
	return true
}

// InEp takes the packet armed on IN endpoint ep; nil if NAK. raiseIrq false
// leaves the endpoint's status bit alone.
func (u *UsbModel) InEp(ep byte, raiseIrq bool) []byte {
	s := int(ep & 7)
	r := u.m.Regs
	ctrl := r[0x120+s]
	if ctrl&epBusy == 0 {
		return nil
	}
	u.ToggleChecked++
	want := byte(epDat0)
	if u.toggleNext[s] != 0 {
		want = epDat1
	}
	if ctrl&(epDat0|epDat1) != want {
		u.ToggleErrors++
	}
	u.toggleNext[s] ^= 1
	n := u.epPtr[s] & 0xFF
	data := make([]byte, n)
	for i := 0; i < n; i++ {
		data[i] = u.usbRAM[(int(r[0x128+s])+i)&0xFF]
	}
	r[0x120+s] = ctrl &^ epBusy
	if raiseIrq {
		r[0x139] |= 1 << uint(s) // the endpoint's status bit; the line follows it
	}
	return data
}

// IsUsbError says whether err is a protocol failure (as opposed to an
// emulator stop).
func IsUsbError(err error) bool {
	var e *UsbError
	return errors.As(err, &e)
}

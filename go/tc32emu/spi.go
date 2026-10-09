// The SPI master (DS-TLSR8278 7.4) and one device on its bus: SPI_DEVICE in
// emulator/tc32emu.py, the same model.
//
// SPDX-License-Identifier: Apache-2.0
package tc32emu

import "fmt"

// SpiFifoMax is how many received octets a device keeps (SPI_FIFO_MAX).
const SpiFifoMax = 4096

// SpiDevice is the device the emulator-only register 0xffd8 attaches.
type SpiDevice struct {
	Mode         int  // 0 echo, 1 constant K, 2 a count up from K
	K            byte // the constant or the count's start
	Cs           int  // chip select pin, 8 * port + pin; 0xff the master's own (PD2, 0x09 bit 0)
	High         bool // the chip select is active high
	Fifo         []byte
	Count        int // octets received while selected
	Transactions int // chip selects going active
	answered     int
	prev         byte
	selected     bool
}

// spiRunning: a device attached, master mode, the SPI function, the module
// clock on, out of reset.
func (m *Machine) spiRunning() bool {
	r := m.Regs
	return m.SpiDev != nil && r[0x09]&0x02 != 0 && r[0x0A]&0x80 != 0 && r[0x63]&0x01 != 0 && r[0x60]&0x01 == 0
}

// spiPin: the pad has function fn (Table 7-1) and its GPIO function off.
func (m *Machine) spiPin(port, pin, fn int) bool {
	r := m.Regs
	mux := int(r[0x5A8+2*port+pin>>2]>>(2*(pin&3))) & 3
	return mux == fn && (r[0x586+8*port]>>pin)&1 == 0
}

// spiRoutes: a CK, a DO and a DI pad routed to the SPI.
func (m *Machine) spiRoutes() (ck, do, di bool) {
	r := m.Regs
	ck = (m.spiPin(0, 4, 0) && r[0x5B6]&0x20 != 0) || (m.spiPin(3, 7, 0) && r[0x5B6]&0x80 != 0)
	do = m.spiPin(0, 2, 0) || m.spiPin(1, 7, 1)
	di = (m.spiPin(0, 3, 0) && r[0x5B7]&0x01 != 0 && r[0x581]&0x08 != 0) ||
		(m.spiPin(1, 6, 1) && r[0x5B7]&0x04 != 0 && r[0x589]&0x40 != 0)
	return
}

// spiSelected: the device's chip select is active.
func (m *Machine) spiSelected() bool {
	d := m.SpiDev
	if d.Cs == 0xFF {
		return m.spiPin(3, 2, 0) && m.Regs[0x09]&0x01 == 0
	}
	port, bit := d.Cs>>3, uint(d.Cs&7)
	lvl, flt := m.padLevels(port)
	return (flt>>bit)&1 == 0 && ((lvl>>bit)&1 == 1) == d.High
}

// spiCsUpdate: a chip select going active starts a transaction.
func (m *Machine) spiCsUpdate() {
	d := m.SpiDev
	if d == nil {
		return
	}
	sel := m.spiSelected()
	if sel && !d.selected {
		d.Transactions++
		d.prev = 0xFF
	}
	d.selected = sel
}

// spiOctet: one octet, out to the device (-1: DO not driven), its answer
// into 0x08; busy for 8 SPI clocks of 2 * (divider + 1) cycles each.
func (m *Machine) spiOctet(out int) {
	ck, do, di := m.spiRoutes()
	m.spiBusyUntil = m.Cycles + 16*(int64(m.Regs[0x0A]&0x7F)+1)
	var ans byte
	d := m.SpiDev
	if d != nil && ck {
		m.spiCsUpdate()
		if d.selected {
			switch d.Mode {
			case 0:
				ans = d.prev
			case 1:
				ans = d.K
			default:
				ans = byte(int(d.K) + d.answered)
			}
			d.answered++
			if do && out >= 0 {
				d.Count++
				if len(d.Fifo) < SpiFifoMax {
					d.Fifo = append(d.Fifo, byte(out))
				}
				d.prev = byte(out)
			}
		}
	}
	if ck && di {
		m.spiRx = ans
	} else {
		m.spiRx = 0
	}
}

// spiAttach handles the emulator-only register 0xffd8: a device on the bus,
// afresh.
func (m *Machine) spiAttach(val uint32) error {
	if val&0xFF > 2 {
		return emuErr("0xffd8: no SPI device answer %d", val&0xFF)
	}
	m.SpiDev = &SpiDevice{Mode: int(val & 0xFF), K: byte(val >> 8), Cs: int(val>>16) & 0xFF,
		High: val>>24&1 != 0, prev: 0xFF}
	m.SpiDev.selected = m.spiSelected()
	high := ""
	if m.SpiDev.High {
		high = " active high"
	}
	m.event(fmt.Sprintf("SPI device: answer %d, chip select 0x%02x%s", val&0xFF, (val>>16)&0xFF, high))
	return nil
}

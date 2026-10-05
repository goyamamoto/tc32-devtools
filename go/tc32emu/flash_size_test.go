// SPDX-License-Identifier: Apache-2.0
package tc32emu

// The flash as a part smaller than 1 MB: the Go port of
// checks/flash_size_check.py, the same 18 cases with a 512 KB part and with
// 1 MB. The Python runs each half in its own interpreter, since it reads the
// size when tc32emu is imported; here FlashSize (the address-map bound) is set
// for the half and put back.

import (
	"strings"
	"testing"
)

func TestFlashSize(t *testing.T) {
	saved := FlashSize
	defer func() { FlashSize = saved }()
	for _, size := range []int{0x80000, FlashMem} {
		FlashSize = size
		flashSizeHalf(t, size)
	}
}

func flashSizeHalf(t *testing.T, size int) {
	small := size < FlashMem
	check := func(cond bool, text string) {
		t.Helper()
		if !cond {
			t.Errorf("flash size 0x%x: FAIL %s", size, text)
		}
	}
	errText := func(err error) string {
		if err == nil {
			return ""
		}
		return err.Error()
	}
	refused := func(err error) bool {
		return err != nil && strings.Contains(err.Error(), "the part has 0x"+strings.TrimPrefix(hex(size), "0x")+" bytes")
	}
	flash := func(slot int) *Flash {
		fl := NewFlash(size)
		fl.Mem[slot+8] = 0x4B
		return fl
	}
	// spi clocks the bytes in, then `read` more; the bytes read and the commit's error.
	spi := func(fl *Flash, data []byte, read int) ([]byte, error) {
		var got []byte
		if err := fl.Select(true); err != nil {
			return got, err
		}
		for _, b := range data {
			if _, err := fl.Clock(b); err != nil {
				return got, err
			}
		}
		for i := 0; i < read; i++ {
			v, err := fl.Clock(0)
			if err != nil {
				return got, err
			}
			got = append(got, v)
		}
		return got, fl.Select(false)
	}
	// read is flash_mspi_read_ram(0x03, addr, ...) for n bytes: the bytes taken and the error.
	read := func(m *Machine, addr, n int) ([]byte, error) {
		var got []byte
		_ = m.Write(RegBase+0x0D, 1, 0x00) // chip select low
		for _, b := range []byte{0x03, byte(addr >> 16), byte(addr >> 8), byte(addr), 0x00} {
			_ = m.Write(RegBase+0x0C, 1, uint32(b)) // the last write clocks the first data byte
		}
		_ = m.Write(RegBase+0x0D, 1, 0x0A) // auto mode
		var err error
		for i := 0; i < n; i++ {
			v, e := m.Read(RegBase+0x0C, 1)
			if e != nil {
				err = e
				break
			}
			got = append(got, byte(v))
		}
		_ = m.Write(RegBase+0x0D, 1, 0x01) // chip select high
		return got, err
	}
	machine := func(fl *Flash) *Machine {
		m, err := NewMachine(fl, -1, 1)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}

	fl := flash(0)
	check(fl.Size == size && len(fl.Mem) == FlashMem, "the part's size and the array's")
	got, _ := spi(fl, []byte{0x9F}, 3)
	wantCap := byte(0x14)
	if small {
		wantCap = 0x13
	}
	check(len(got) == 3 && got[0] == 0x85 && got[1] == 0x60 && got[2] == wantCap, "JEDEC ID "+hexBytes(got))
	copy(fl.Mem[0x7FFFE:], []byte{0x11, 0x22, 0x33, 0x44})
	top := 0x100000
	if small {
		top = 0x80000
	}
	// Reads, through the MSPI registers.
	m := machine(fl)
	got, err := read(m, 0x7FFFE, 2)
	check(string(got) == "\x11\x22" && err == nil, "read 0x7fffe-0x7ffff: 11 22 "+errText(err))
	got, err = read(m, 0x80000, 1)
	if small {
		check(refused(err), "read 0x80000: refused "+errText(err))
	} else {
		check(string(got) == "\x33" && err == nil, "read 0x80000: 33 "+errText(err))
	}
	got, err = read(m, 0x7FFFE, 3)
	if small {
		check(refused(err) && string(got) == "\x11\x22", "read of 3 bytes from 0x7fffe: the third refused "+errText(err))
	} else {
		check(string(got) == "\x11\x22\x33", "read of 3 bytes from 0x7fffe: 11 22 33")
	}
	// TC32EMU_FLASH_BEYOND: a taken read beyond the part gives 0xff or wraps, and is logged.
	if small {
		n0 := len(fl.Log)
		fl.BeyondMode = "ff"
		got, err = read(m, 0x7FFFE, 4)
		check(string(got) == "\x11\x22\xff\xff" && err == nil, "beyond = ff")
		fl.Mem[0], fl.Mem[1] = 0xA1, 0xA2
		fl.BeyondMode = "wrap"
		got, err = read(m, 0x7FFFE, 4)
		check(string(got) == "\x11\x22\xa1\xa2" && err == nil, "beyond = wrap")
		got, err = read(m, 0xFFFFFB, 4)
		check(string(got) == string(fl.Mem[0x7FFFB:0x7FFFF]) && err == nil, "beyond = wrap: 0xfffffb reads 0x7fffb")
		var addrs []int
		for _, op := range fl.Log[n0:] {
			if op.Op == "read beyond" {
				addrs = append(addrs, op.Addr)
			}
		}
		want := []int{0x80000, 0x80001, 0x80000, 0x80001, 0xFFFFFB, 0xFFFFFC, 0xFFFFFD, 0xFFFFFE}
		ok := len(addrs) == len(want)
		for i := range want {
			ok = ok && addrs[i] == want[i]
		}
		check(ok, "each taken byte beyond the part is logged")
		fl.Log = fl.Log[:n0]
		fl.Mem[0], fl.Mem[1] = 0xFF, 0xFF
		fl.BeyondMode = "stop"
	}
	// Programs and erases.
	spi(fl, []byte{0x06}, 0)
	_, err = spi(fl, []byte{0x02, 0x07, 0xF0, 0x00, 0xA5}, 0)
	check(err == nil && fl.Mem[0x7F000] == 0xA5, "program 0x7f000: written "+errText(err))
	before := append([]byte(nil), fl.Mem[0x80000:0x80100]...)
	spi(fl, []byte{0x06}, 0)
	_, err = spi(fl, []byte{0x02, 0x08, 0x00, 0x10, 0x00}, 0)
	if small {
		check(refused(err) && string(fl.Mem[0x80000:0x80100]) == string(before), "program 0x80010: refused, nothing written "+errText(err))
	} else {
		check(err == nil && fl.Mem[0x80010] == 0x00, "program 0x80010: written "+errText(err))
	}
	fl.wel = false
	spi(fl, []byte{0x06}, 0)
	_, err = spi(fl, []byte{0x20, 0x07, 0xF0, 0x00}, 0)
	check(err == nil && fl.Mem[0x7F000] == 0xFF, "erase 0x7f000: erased "+errText(err))
	fl.Mem[0x80000] = 0x5A
	spi(fl, []byte{0x06}, 0)
	_, err = spi(fl, []byte{0x20, 0x08, 0x00, 0x00}, 0)
	if small {
		check(refused(err) && fl.Mem[0x80000] == 0x5A, "erase 0x80000: refused, nothing erased "+errText(err))
	} else {
		check(err == nil && fl.Mem[0x80000] == 0xFF, "erase 0x80000: erased "+errText(err))
	}
	for _, op := range fl.Log {
		check(op.Addr < top, "the flash's log holds no write at or above the top: "+op.Op+" "+hex(op.Addr))
	}
	// XIP, from slot A and from slot B.
	for _, slot := range []int{0x0, 0x20000} {
		fl = flash(slot)
		copy(fl.Mem[0x7FFFC:], []byte{0, 1, 2, 3, 4, 5, 6, 7})
		m = machine(fl)
		last := uint32(0x80000 - 4) // at or above twice the slot's offset an XIP address is the flash's own
		v, err := m.Read(last, 4)
		check(err == nil && v == 0x03020100, "boot slot "+hex(slot)+": XIP read of flash 0x7fffc "+errText(err))
		v, err = m.Read(last+4, 4)
		if small {
			check(err != nil, "boot slot "+hex(slot)+": XIP read of flash 0x80000: refused")
		} else {
			check(err == nil && v == 0x07060504, "boot slot "+hex(slot)+": XIP read of flash 0x80000 "+errText(err))
		}
		// The startup loader's register (0x608): bit 24 starts a load of 16 bytes of flash into SRAM at the
		// offset in bits 23:0. SRAM is smaller than any flash, so the SRAM's end is the bound it can reach.
		for i := 0; i < 16; i++ {
			fl.Mem[slot+0x1000+i] = byte(0x40 + i)
		}
		err = m.Write(RegBase+0x608, 4, (1<<24)|0x1000)
		ok := err == nil
		for i := 0; i < 16 && ok; i++ {
			ok = m.SRAM[0x1000+i] == byte(0x40+i)
		}
		check(ok, "boot slot "+hex(slot)+": loader load of offset 0x1000 "+errText(err))
		err = m.Write(RegBase+0x608, 4, (1<<24)|uint32(SRAMSize))
		check(err != nil && len(m.SRAM) == SRAMSize, "boot slot "+hex(slot)+": loader load of offset SRAM_SIZE: refused")
	}
}

func hex(v int) string {
	const digits = "0123456789abcdef"
	if v == 0 {
		return "0x0"
	}
	var b []byte
	for ; v > 0; v >>= 4 {
		b = append([]byte{digits[v&0xF]}, b...)
	}
	return "0x" + string(b)
}

func hexBytes(b []byte) string {
	s := ""
	for _, x := range b {
		s += hex(int(x)) + " "
	}
	return s
}

// SPDX-License-Identifier: Apache-2.0
package tc32emu

import "github.com/goyamamoto/tc32-devtools/go/tc32isa"

// ------------------------------------------------------------ flags

func (m *Machine) nz(x uint32) {
	m.N = x >> 31
	if x == 0 {
		m.Z = 1
	} else {
		m.Z = 0
	}
}

func (m *Machine) add(a, b, carry uint32) uint32 {
	s := uint64(a) + uint64(b) + uint64(carry)
	r := uint32(s)
	m.nz(r)
	if s > m32 {
		m.C = 1
	} else {
		m.C = 0
	}
	if (^(a ^ b))&(a^r)&0x80000000 != 0 {
		m.V = 1
	} else {
		m.V = 0
	}
	return r
}

// sub computes a - b as a + ~b + borrowIn, like ARM.
func (m *Machine) sub(a, b, borrowIn uint32) uint32 { return m.add(a, ^b, borrowIn) }

func (m *Machine) cond(cc uint32) bool {
	n, z, c, v := m.N != 0, m.Z != 0, m.C != 0, m.V != 0
	switch cc {
	case 0:
		return z
	case 1:
		return !z
	case 2:
		return c
	case 3:
		return !c
	case 4:
		return n
	case 5:
		return !n
	case 6:
		return v
	case 7:
		return !v
	case 8:
		return c && !z
	case 9:
		return !c || z
	case 10:
		return n == v
	case 11:
		return n != v
	case 12:
		return !z && n == v
	case 13:
		return z || n != v
	case 14:
		return true
	}
	return false
}

func (m *Machine) chk(a uint32, size int) (uint32, error) {
	if a%uint32(size) != 0 {
		if m.ChkHook != nil {
			if addr, ok := m.ChkHook(a, size); ok {
				return addr, nil
			}
		}
		return 0, emuErr("unaligned %d-bit access 0x%08x at %s", size*8, a, m.Symbolize(m.R[15]-2))
	}
	return a, nil
}

func (m *Machine) setreg(rd int, val uint32) {
	if rd == 15 {
		m.R[15] = val &^ 1
	} else {
		m.R[rd] = val
	}
}

func sext(v uint32, bits uint) uint32 {
	if v&(1<<(bits-1)) != 0 {
		return v | ^uint32(0)<<bits
	}
	return v
}

// Step executes one instruction. A sleeping machine does nothing (ran is
// false). pc and hw are the instruction's address and halfword. TraceStep,
// when set, is called after every instruction that completed.
func (m *Machine) Step() (pc uint32, hw uint16, ran bool, err error) {
	pc, hw, ran, err = m.step()
	if ran && err == nil && m.TraceStep != nil {
		m.TraceStep(pc, hw)
	}
	return
}

func (m *Machine) step() (pc uint32, hw uint16, ran bool, err error) {
	if m.Asleep != nil {
		return 0, 0, false, nil
	}
	pc = m.R[15]
	if hook := m.Hooks[pc]; hook != nil {
		if err := hook(m); err != nil {
			return pc, 0, false, err
		}
		if m.R[15] != pc {
			return pc, 0, false, nil
		}
	}
	v, err := m.fetch(pc)
	if err != nil {
		return pc, 0, false, err
	}
	hw = uint16(v)
	m.R[15] = pc + 2
	m.Cycles += m.CPI
	r := &m.R

	// TC32-only instructions.
	if hw&0xFFE0 == 0x6BC0 {
		op, rd := (hw>>3)&3, hw&7
		switch op {
		case 0:
			err = m.setCPSR(r[rd])
		case 1:
			r[rd] = m.CPSR()
		case 2:
			m.SPSR[m.Mode] = r[rd]
		default:
			r[rd] = m.SPSR[m.Mode]
		}
		return pc, hw, true, err
	}
	if hw&0xFE00 == 0x6800 { // treti {list[, pc]}
		if hw&0x100 == 0 {
			return pc, hw, true, emuErr("treti without pc (0x%04x) at %s: not modelled", hw, m.Symbolize(pc))
		}
		sp := r[13]
		for i := 0; i < 8; i++ {
			if hw&(1<<uint(i)) != 0 {
				if r[i], err = m.Read(sp, 4); err != nil {
					return pc, hw, true, err
				}
				sp += 4
			}
		}
		newpc, err := m.Read(sp, 4)
		if err != nil {
			return pc, hw, true, err
		}
		sp += 4
		r[13] = sp
		if err := m.setCPSR(m.SPSR[m.Mode]); err != nil {
			return pc, hw, true, err
		}
		r[15] = newpc &^ 1
		if m.OnTreti != nil {
			m.OnTreti(m)
		}
		return pc, hw, true, nil
	}
	if hw&0xFF00 == 0xCF00 {
		return pc, hw, true, emuErr("tserv %d at %s", hw&0xff, m.Symbolize(pc))
	}

	t := uint32(tc32isa.ToThumb(hw))
	top := t >> 11
	switch {
	case top <= 2: // shift by immediate
		imm, rm, rd := (t>>6)&31, (t>>3)&7, t&7
		x := r[rm]
		switch top {
		case 0:
			if imm != 0 {
				m.C = (x >> (32 - imm)) & 1
				x <<= imm
			}
		case 1:
			if imm == 0 {
				imm = 32
			}
			m.C = (x >> (imm - 1)) & 1
			if imm < 32 {
				x >>= imm
			} else {
				x = 0
			}
		default:
			if imm == 0 {
				imm = 32
			}
			if imm < 32 {
				m.C = (x >> (imm - 1)) & 1
			} else {
				m.C = x >> 31
			}
			sh := imm
			if sh > 31 {
				sh = 31
			}
			x = uint32(int32(x) >> sh)
		}
		r[rd] = x
		m.nz(x)
	case top == 3: // add/sub register or imm3
		rd, rn, f := t&7, (t>>3)&7, (t>>6)&7
		b := r[f]
		if t&0x400 != 0 {
			b = f
		}
		if t&0x200 != 0 {
			r[rd] = m.sub(r[rn], b, 1)
		} else {
			r[rd] = m.add(r[rn], b, 0)
		}
	case top <= 7: // mov/cmp/add/sub imm8
		rd, imm := (t>>8)&7, t&0xFF
		switch top - 4 {
		case 0:
			r[rd] = imm
			m.nz(imm)
		case 1:
			m.sub(r[rd], imm, 1)
		case 2:
			r[rd] = m.add(r[rd], imm, 0)
		default:
			r[rd] = m.sub(r[rd], imm, 1)
		}
	case t>>10 == 0x10: // ALU
		m.alu((t>>6)&15, int(t&7), int((t>>3)&7))
	case t>>10 == 0x11: // hi register ops, bx
		op := (t >> 8) & 3
		rd := int((t & 7) | ((t >> 4) & 8))
		rm := int((t >> 3) & 15)
		// ARMv4T leaves these UNPREDICTABLE, and no Telink code uses them.
		if op == 3 && t&0x87 != 0 {
			return pc, hw, true, emuErr("bx with bit 7 or bits 2:0 set (0x%04x) at %s: UNPREDICTABLE in ARMv4T", hw, m.Symbolize(pc))
		}
		if op < 3 && rd < 8 && rm < 8 {
			return pc, hw, true, emuErr("high-register add/cmp/mov with two low registers (0x%04x) at %s: UNPREDICTABLE in ARMv4T", hw, m.Symbolize(pc))
		}
		// PC as an operand: ARM Thumb reads the instruction's address + 4;
		// nothing shows which the TC32 does at a halfword address: stop there.
		readsPC := rm == 15 || (rd == 15 && op <= 1)
		if readsPC && (pc+4)&2 != 0 {
			return pc, hw, true, emuErr("PC read by a high-register add/cmp/mov at 0x%x, a halfword address: unknown on TC32", pc)
		}
		vm := r[rm]
		if rm == 15 {
			vm = pc + 4
		}
		switch op {
		case 0:
			vd := r[rd]
			if rd == 15 {
				vd = pc + 4
			}
			m.setreg(rd, vd+vm)
		case 1:
			vd := r[rd]
			if rd == 15 {
				vd = pc + 4
			}
			m.sub(vd, vm, 1)
		case 2:
			m.setreg(rd, vm)
		default:
			r[15] = vm &^ 1
		}
	case top == 9: // ldr pc-relative
		v, err := m.Read(((pc+4)&^3)+(t&0xFF)*4, 4)
		if err != nil {
			return pc, hw, true, err
		}
		r[(t>>8)&7] = v
	case top == 0x0A || top == 0x0B: // load/store register offset
		op, ro, rb, rd := (t>>9)&7, (t>>6)&7, (t>>3)&7, t&7
		a := r[rb] + r[ro]
		switch op {
		case 0:
			err = m.writeChk(a, 4, r[rd])
		case 1:
			err = m.writeChk(a, 2, r[rd])
		case 2:
			err = m.Write(a, 1, r[rd])
		case 3:
			var x uint32
			if x, err = m.Read(a, 1); err == nil {
				r[rd] = sext(x, 8)
			}
		case 4:
			r[rd], err = m.readChk(a, 4)
		case 5:
			r[rd], err = m.readChk(a, 2)
		case 6:
			r[rd], err = m.Read(a, 1)
		default:
			var x uint32
			if x, err = m.readChk(a, 2); err == nil {
				r[rd] = sext(x, 16)
			}
		}
	case top == 0x0C || top == 0x0D: // ldr/str word imm5
		imm, rb, rd := (t>>6)&31, (t>>3)&7, t&7
		a := r[rb] + imm*4
		if top == 0x0D {
			r[rd], err = m.readChk(a, 4)
		} else {
			err = m.writeChk(a, 4, r[rd])
		}
	case top == 0x0E || top == 0x0F: // ldrb/strb imm5
		imm, rb, rd := (t>>6)&31, (t>>3)&7, t&7
		a := r[rb] + imm
		if top == 0x0F {
			r[rd], err = m.Read(a, 1)
		} else {
			err = m.Write(a, 1, r[rd])
		}
	case top == 0x10 || top == 0x11: // ldrh/strh imm5
		imm, rb, rd := (t>>6)&31, (t>>3)&7, t&7
		a := r[rb] + imm*2
		if top == 0x11 {
			r[rd], err = m.readChk(a, 2)
		} else {
			err = m.writeChk(a, 2, r[rd])
		}
	case top == 0x12 || top == 0x13: // sp-relative
		rd, a := (t>>8)&7, r[13]+(t&0xFF)*4
		if top == 0x13 {
			r[rd], err = m.readChk(a, 4)
		} else {
			err = m.writeChk(a, 4, r[rd])
		}
	case top == 0x14 || top == 0x15: // add rd, pc/sp, imm8*4
		rd := (t >> 8) & 7
		base := (pc + 4) &^ 3
		if top == 0x15 {
			base = r[13]
		}
		r[rd] = base + (t&0xFF)*4
	case top == 0x16 || top == 0x17: // misc
		switch {
		case t&0xFF00 == 0xB000:
			imm := (t & 0x7F) * 4
			if t&0x80 != 0 {
				r[13] -= imm
			} else {
				r[13] += imm
			}
		case t&0xF600 == 0xB400: // push/pop
			if t&0x1FF == 0 {
				return pc, hw, true, emuErr("push/pop of no registers (0x%04x) at %s: UNPREDICTABLE in ARMv4T", hw, m.Symbolize(pc))
			}
			if t&0x0800 != 0 { // pop
				sp := r[13]
				for i := 0; i < 8; i++ {
					if t&(1<<uint(i)) != 0 {
						if r[i], err = m.readChk(sp, 4); err != nil {
							return pc, hw, true, err
						}
						sp += 4
					}
				}
				if t&0x100 != 0 {
					v, err := m.readChk(sp, 4)
					if err != nil {
						return pc, hw, true, err
					}
					r[15] = v &^ 1
					sp += 4
				}
				r[13] = sp
			} else {
				var regs []int
				for i := 0; i < 8; i++ {
					if t&(1<<uint(i)) != 0 {
						regs = append(regs, i)
					}
				}
				if t&0x100 != 0 {
					regs = append(regs, 14)
				}
				sp := r[13] - 4*uint32(len(regs))
				r[13] = sp
				for _, i := range regs {
					if err = m.writeChk(sp, 4, r[i]); err != nil {
						return pc, hw, true, err
					}
					sp += 4
				}
			}
		default:
			return pc, hw, true, emuErr("undefined 0x%04x at %s", hw, m.Symbolize(pc))
		}
	case top == 0x18 || top == 0x19: // stmia/ldmia
		rb := (t >> 8) & 7
		if t&0xFF == 0 {
			return pc, hw, true, emuErr("ldm/stm of no registers (0x%04x) at %s: UNPREDICTABLE in ARMv4T", hw, m.Symbolize(pc))
		}
		if top == 0x18 && t&(1<<rb) != 0 && t&((1<<rb)-1) != 0 {
			return pc, hw, true, emuErr("stm storing its base register after a lower one (0x%04x) at %s: the stored value is UNPREDICTABLE in ARMv4T", hw, m.Symbolize(pc))
		}
		a := r[rb]
		for i := 0; i < 8; i++ {
			if t&(1<<uint(i)) != 0 {
				if top == 0x19 {
					if r[i], err = m.readChk(a, 4); err != nil {
						return pc, hw, true, err
					}
				} else if err = m.writeChk(a, 4, r[i]); err != nil {
					return pc, hw, true, err
				}
				a += 4
			}
		}
		if !(top == 0x19 && t&(1<<rb) != 0) {
			r[rb] = a
		}
	case top == 0x1A || top == 0x1B: // conditional branch
		cc := (t >> 8) & 15
		if cc >= 14 {
			return pc, hw, true, emuErr("undefined 0x%04x at %s", hw, m.Symbolize(pc))
		}
		if m.cond(cc) {
			r[15] = pc + 4 + sext(t&0xFF, 8)*2
		}
	case top == 0x1C: // b
		r[15] = pc + 4 + sext(t&0x7FF, 11)*2
	case top == 0x1E: // bl, first half
		r[14] = pc + 4 + sext(t&0x7FF, 11)<<12
	case top == 0x1F: // bl, second half
		target := r[14] + (t&0x7FF)*2
		r[14] = pc + 2
		r[15] = target
	default:
		return pc, hw, true, emuErr("undefined 0x%04x at %s", hw, m.Symbolize(pc))
	}
	return pc, hw, true, err
}

// fetch is Read(pc, 2) for the instruction at pc. The usual case, an even
// address in the flash's address range with the flash idle, is written out
// (the XIP address, the cache model, the two bytes); anything else is Read's.
func (m *Machine) fetch(pc uint32) (uint32, error) {
	f := m.Flash
	if m.MemReadHook != nil || pc&1 != 0 || int(pc) >= FlashSize || f.DPD || f.BusyUntil != 0 || f.csLow {
		return m.Read(pc, 2)
	}
	p, s := int(pc), m.BootSlot
	if p < s {
		p += s
	} else if p < 2*s {
		return m.Read(pc, 2)
	}
	if p+2 > f.Size {
		return m.Read(pc, 2)
	}
	if m.ICacheMiss != 0 {
		// A line the cache holds costs nothing wherever the SRAM code ends, so
		// that end is looked up only for a line it does not hold.
		line := int(pc) >> icacheLineShift
		if slot := line % icacheLines; m.icacheTags[slot] != line && int(pc) >= m.sramCodeEnd() {
			m.icacheTags[slot] = line
			m.ICacheMisses++
			m.Cycles += m.ICacheMiss
		}
	}
	return uint32(f.Mem[p]) | uint32(f.Mem[p+1])<<8, nil
}

func (m *Machine) readChk(a uint32, size int) (uint32, error) {
	a, err := m.chk(a, size)
	if err != nil {
		return 0, err
	}
	return m.Read(a, size)
}

func (m *Machine) writeChk(a uint32, size int, val uint32) error {
	a, err := m.chk(a, size)
	if err != nil {
		return err
	}
	return m.Write(a, size, val)
}

func (m *Machine) alu(op uint32, rd, rs int) {
	r := &m.R
	a, b := r[rd], r[rs]
	switch op {
	case 0:
		r[rd] = a & b
		m.nz(r[rd])
	case 1:
		r[rd] = a ^ b
		m.nz(r[rd])
	case 2, 3, 4, 7: // lsl, lsr, asr, ror by register
		s := b & 0xFF
		x := a
		switch op {
		case 2:
			if s != 0 {
				if s <= 32 {
					m.C = (x >> (32 - s)) & 1
				} else {
					m.C = 0
				}
				if s < 32 {
					x <<= s
				} else {
					x = 0
				}
			}
		case 3:
			if s != 0 {
				if s <= 32 {
					m.C = (x >> (s - 1)) & 1
				} else {
					m.C = 0
				}
				if s < 32 {
					x >>= s
				} else {
					x = 0
				}
			}
		case 4:
			if s != 0 {
				sx := int32(x)
				cs := s
				if cs > 32 {
					cs = 32
				}
				m.C = uint32(sx>>(cs-1)) & 1
				xs := s
				if xs > 31 {
					xs = 31
				}
				x = uint32(sx >> xs)
			}
		default:
			if s != 0 {
				k := s & 31
				if k != 0 {
					x = x>>k | x<<(32-k)
				}
				m.C = x >> 31
			}
		}
		r[rd] = x
		m.nz(x)
	case 5:
		r[rd] = m.add(a, b, m.C)
	case 6:
		r[rd] = m.sub(a, b, m.C)
	case 8:
		m.nz(a & b)
	case 9:
		r[rd] = m.sub(0, b, 1)
	case 10:
		m.sub(a, b, 1)
	case 11:
		m.add(a, b, 0)
	case 12:
		r[rd] = a | b
		m.nz(r[rd])
	case 13:
		r[rd] = a * b
		m.nz(r[rd])
	case 14:
		r[rd] = a &^ b
		m.nz(r[rd])
	default:
		r[rd] = ^b
		m.nz(r[rd])
	}
}

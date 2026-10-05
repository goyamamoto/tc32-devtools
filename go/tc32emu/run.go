// SPDX-License-Identifier: Apache-2.0
package tc32emu

// Range is a half-open [Lo, Hi) address range.
type Range struct{ Lo, Hi uint32 }

// Run is the Python Machine.run(): blocks of checkEvery instructions, then
// UpdateTime() and one interrupt if pending; an idle loop (pc in idleRanges)
// skips ahead to the next compare while an interrupt could be taken (0x643
// bit 0 set, the CPSR's I bit clear). It returns ErrWatchdogReset,
// ErrSoftwareReset or an *EmuError, or nil at maxCycles.
func (m *Machine) Run(maxCycles int64, idleRanges []Range, checkEvery int) error {
	if checkEvery <= 0 {
		checkEvery = 32
	}
	for m.Cycles < maxCycles {
		if m.Asleep != nil {
			m.SleepUntil(maxCycles)
			continue
		}
		for i := 0; i < checkEvery; i++ {
			if _, _, _, err := m.Step(); err != nil {
				return err
			}
		}
		if m.Asleep != nil {
			continue
		}
		if err := m.UpdateTime(); err != nil {
			return err
		}
		if len(m.padRCSince) > 0 {
			m.padRCService()
		}
		if m.IrqPending() {
			m.TakeIrq()
		} else if len(idleRanges) > 0 && m.Regs[0x643]&1 != 0 && m.IBit == 0 {
			// Only while an interrupt could be taken: one that became pending
			// while they are off is taken when the code turns them on, not at
			// the next compare (idle_skip_test.go).
			idle := false
			for _, rg := range idleRanges {
				if rg.Lo <= m.R[15] && m.R[15] < rg.Hi {
					idle = true
					break
				}
			}
			if idle {
				// Idle loop: jump to the next compare or watchdog deadline.
				var skip int64
				if m.Regs[0x748]&stimerIRQ != 0 {
					skip = m.CyclesToStimer(m.StimerCmp)
				}
				if m.IdleSkipHook != nil {
					skip = m.IdleSkipHook(skip)
				}
				if left := maxCycles - m.Cycles; left < skip {
					skip = left // the caller's time limit stands
				}
				if due, ok := m.padRCDue(); ok {
					if due < 0 {
						due = 0
					}
					if due+32 < skip {
						skip = due + 32 // a pad reaching its level is an event
					}
				}
				if skip > 64 {
					m.Cycles += skip - 32
					m.IdleSkipped += skip - 32
				}
			}
		}
	}
	return nil
}

// RunMs runs until the simulated time reaches targetMs (the clock may change
// on the way).
func (m *Machine) RunMs(targetMs float64, idleRanges []Range) error {
	for m.Ms() < targetMs {
		left := int64((targetMs-m.Ms())*float64(m.CPUHz)/1000) + 1
		if c := m.CPUHz / 100; c < left {
			left = c
		}
		if err := m.Run(m.Cycles+left, idleRanges, 32); err != nil {
			return err
		}
	}
	return nil
}

"""Register model of the TLSR8278 USB device controller for tc32emu, and a
USB host that drives it while the emulated firmware runs.

The model sits next to the emulator so that whole TC32 images can be
enumerated: firmware built on Telink's SDK and a ZMK image.

As far as the Telink SDK (drivers/B87, application/usbstd/usb.c) shows it:
- EP0: an 8-byte FIFO behind a data port (0x101) that post-increments the
  pointer (0x100). The host's packet sits in the FIFO before the SETUP/DATA
  status bit; the device's IN packet is what it wrote since it last reset the
  pointer. The control register (0x102) arms a stage with DAT_ACK/DAT_STALL or
  STA_ACK/STA_STALL; the status register (0x103) is write-one-to-clear.
- Data endpoints: slot i (= ep & 7) has a pointer (0x110+i), a data port
  (0x118+i) into the 256-byte USB RAM at its buffer address (0x128+i), and a
  control register (0x120+i) whose busy bit the device sets to hand over a
  packet and the host clears when it takes it. USB_IRQ (0x139) is
  write-one-to-clear, USB_MASK is 0x13a.
- Interrupt sources: EP0 SETUP 8, DATA 9, STATUS 10, endpoint data 12
  (irq_udc[0..4]; level-triggered per DS-TLSR8278 Table 6-1), bus reset 17
  (edge-triggered, 0x64a bit 1).
- 0x104 (reg_ctrl_ep_irq_mode) selects standard requests the hardware answers
  itself (bit set) instead of the firmware: the SDK names bit 0 ADDR, 1 CFG,
  2 INTF, 3 STA, 4 SYN, 5 DESC, 6 FEAT, 7 STD, calls 0xff "hardware mode",
  and clears STD, DESC and CFG to handle those in software.
Assumed, not shown by the SDK or the datasheet:
- 0x104 is 0xff after reset. Firmware that clears only 0xa0 still
  enumerates on the chip, so at least ADDR and CFG must be set at reset
  (with 0x00 such firmware stalls SET_ADDRESS here).
- Which requests the bits cover: ADDR SET_ADDRESS, CFG SET/GET_CONFIGURATION,
  INTF SET/GET_INTERFACE, STA GET_STATUS, SYN SYNCH_FRAME, DESC
  GET_DESCRIPTOR, FEAT SET/CLEAR_FEATURE, STD the rest. STA could also mean
  the status stage.
- What the hardware answers to the GET requests it handles (zeros, or the
  configuration value it stored).
- The level sources 8-10 follow 0x103 bits 4-6 and source 12 follows
  0x139 & 0x13a (the SDK never uses these lines).
- 0x139 gets the endpoint's bit when the host takes an IN packet.
- The EP0 pointer holds the byte count of a received OUT packet.
- 0x10b (reg_usb_host_conn) turns nonzero when the host sets a configuration
  and back to 0 on a bus reset, whoever answers the request. Firmware can
  hold back IN reports while it is 0.
- OUT data endpoints (the SDK's usb.c: the firmware arms one by setting the
  busy bit, usbhw_data_ep_ack(); on its interrupt bit it resets the pointer
  and reads the data): a packet is taken only while the busy bit is set; the
  hardware then writes it at the endpoint's buffer, leaves the pointer at its
  length, clears the busy bit and sets the endpoint's bit in 0x139.
With irq_lines False, events only set status bits (the Telink SDK polls
them); with True they also set the interrupt source bits.

Suspend and resume (the SDK's usb.c):
- While the host keeps the bus idle, interrupt source 3 (FLD_IRQ_USB_PWDN_EN)
  is set; the SDK reads it in 0x648 as "suspended" (a level, whatever the
  mask; firmware need not enable an interrupt for it).
- Host resume: K state (DP low, DM high) for 20 ms, then the bus runs; the
  DP and DM pads show it (firmware can wake from suspend on DP going low), and
  it is a core wake source while 0x6e bit 2 (FLD_WAKEUP_SRC_USB) is set.
  Whether that wake is a level (the chip wakes for as long as the host drives
  K) or an edge (one wake at the start of K) is in none of our documents.
  The model's default is the level. TC32EMU_USB_CORE_WAKE=edge in the
  environment, or UsbModel.core_wake = "edge", makes it an edge: one wake per
  K period, taken by the first sleep it ends; a firmware that goes back to
  sleep while the host still drives K then sleeps on until another source
  wakes it, which the level hides.
- Remote wakeup: firmware (older Telink SDKs' usb_resume_host) writes
  0x6e = 0x40 (FLD_WAKEUP_SRC_USB_RESM), then 4, while suspended and
  0x10a bit 2 is set; the model records the 0x40 write as resume signalling
  from the device.
Assumed: the controller sets 0x10a bit 2 when it answers SET_FEATURE
(DEVICE_REMOTE_WAKEUP) itself (0x104 bit 6) and clears it on CLEAR_FEATURE
and bus reset. Firmware reads the bit and never writes it.

SPDX-License-Identifier: Apache-2.0
"""

STA_SETUP, STA_DATA, STA_STA = 0x10, 0x20, 0x40
DAT_ACK, DAT_STALL, ST_ACK, ST_STALL = 0x01, 0x02, 0x04, 0x08
EP_BUSY, EP_DAT0, EP_DAT1 = 0x01, 0x04, 0x08
IRQ_EP0_SETUP, IRQ_EP0_DATA, IRQ_EP0_STA, IRQ_EP_DATA, IRQ_USB_RST = 8, 9, 10, 12, 17
CORE_WAKE = __import__("os").environ.get("TC32EMU_USB_CORE_WAKE", "level")
if CORE_WAKE not in ("level", "edge"):
    raise ValueError("TC32EMU_USB_CORE_WAKE: " + CORE_WAKE)


class UsbError(Exception):
    pass


class UsbModelError(Exception):
    """An access the model does not cover (the firmware used the controller in
    a way the model cannot answer for)."""


class UsbModel:
    # USB 2.0 9.2.6.4: a standard request without a data stage completes within
    # 50 ms, and so does the status stage after the last data. A device that
    # takes longer breaks the spec; hosts wait longer (below), so it works.
    SPEC_STAGE_MS = 50

    def __init__(self, machine, irq_lines=False, stage_timeout_ms=5000):
        """How long the host waits for each stage of a control transfer before
        it gives up: stage_timeout_ms, 5000 ms as Linux's USB_CTRL_GET_TIMEOUT
        and USB_CTRL_SET_TIMEOUT, for every request (a real host does not hold
        a device to the spec's 50 ms). A stage of a standard request that
        takes longer than SPEC_STAGE_MS is not an error but an event, and is
        kept in slow_stages as (ms, bmRequestType, bRequest, stage, duration
        ms): a firmware that erases a flash sector with interrupts off while
        the host enumerates it (the boot guard's healthy mark) or answers a
        report (the OTA receiver) shows there, at the erase time."""
        self.m = machine
        self.irq_lines = irq_lines
        self.stage_timeout_ms = stage_timeout_ms
        self.slow_stages = []
        self.setup = b""                      # the running transfer's (control())
        self.fifo = bytearray(8)
        self.ep0_ptr = 0
        self.overflow = False
        self.ep0_ctrl = None
        self.usb_ram = bytearray(256)
        self.ep_ptr = [0] * 8
        self.toggle_next = [0] * 8
        self.toggle_checked = self.toggle_errors = 0
        self.hw_config = 0
        self.address = 0
        self.mode_reset = 0xFF
        self.suspended = False
        self.k_state = False
        self.core_wake = CORE_WAKE  # "level" or "edge", see the module's text
        self.k_edge = False         # edge: the start of K, not yet taken by a wake
        self.remote_wakeups = []   # ms of each resume signalled by the device (0x6e bit 6 while suspended)
        machine.regs_mem[0x104] = self.mode_reset
        machine.level_sources.append(self.level)
        machine.core_wake_sources.append(self.core_wake_now)
        self._pads = machine.pad_levels
        machine.pad_levels = self.pad_levels
        machine.reset_hooks = getattr(machine, "reset_hooks", []) + [self.on_reset]
        self._rd, self._wr = machine.reg_read, machine.reg_write
        machine.reg_read, machine.reg_write = self.reg_read, self.reg_write

    def on_reset(self):
        self.m.regs_mem[0x104] = self.mode_reset
        self.ep0_ptr, self.ep_ptr, self.ep0_ctrl = 0, [0] * 8, None

    # ------------------------------------------------------------ device side
    def reg_read(self, o, size):
        if not (0x100 <= o < 0x140):
            return self._rd(o, size)
        if size != 1:
            raise UsbModelError(f"{size * 8}-bit read of USB register 0x{o:03x}")
        r = self.m.regs_mem
        if o == 0x100:
            return self.ep0_ptr & 0xFF
        if o == 0x101:
            v = self.fifo[self.ep0_ptr & 7]
            self.ep0_ptr += 1
            return v
        if 0x110 <= o < 0x118:
            return self.ep_ptr[o - 0x110] & 0xFF
        if 0x118 <= o < 0x120:
            s = o - 0x118
            v = self.usb_ram[(r[0x128 + s] + self.ep_ptr[s]) & 0xFF]
            self.ep_ptr[s] += 1
            return v
        return r[o]

    def reg_write(self, o, size, val):
        if o == 0x6E and size == 1 and val & 0x40 and self.suspended:
            self.remote_wakeups.append(self.m.ms())
            self.m.event("USB: resume signalled by the device (0x6e bit 6 while suspended)")
        if not (0x100 <= o < 0x140):
            return self._wr(o, size, val)
        if size != 1:
            raise UsbModelError(f"{size * 8}-bit write of USB register 0x{o:03x}")
        r = self.m.regs_mem
        if o == 0x100:
            self.ep0_ptr = val
        elif o == 0x101:
            if self.ep0_ptr >= 8:
                self.overflow = True
            self.fifo[self.ep0_ptr & 7] = val
            self.ep0_ptr += 1
        elif o == 0x102:
            self.ep0_ctrl = val
            r[o] = val
        elif o in (0x103, 0x139):
            r[o] &= ~val & 0xFF
        elif 0x110 <= o < 0x118:
            self.ep_ptr[o - 0x110] = val
        elif 0x118 <= o < 0x120:
            s = o - 0x118
            self.usb_ram[(r[0x128 + s] + self.ep_ptr[s]) & 0xFF] = val
            self.ep_ptr[s] += 1
        else:
            r[o] = val

    def level(self):
        """Level interrupt sources: 3 while suspended; 8-12 as the controller's
        status holds them (with irq_lines)."""
        if not self.irq_lines:
            return (1 << 3) if self.suspended else 0
        r = self.m.regs_mem
        sta = r[0x103]
        bits = ((sta >> 4) & 1) << 8 | ((sta >> 5) & 1) << 9 | ((sta >> 6) & 1) << 10
        if r[0x139] & r[0x13A]:
            bits |= 1 << 12
        if self.suspended:
            bits |= 1 << 3
        return bits

    def pad_levels(self, port):
        """DP (PA6) and DM (PA5): J with the device's pull-up on DP (DP high),
        K during resume (DP low, DM high); the host's pull-downs hold them
        otherwise."""
        lvl, flt = self._pads(port)
        if port == 0:
            lvl, flt = lvl & ~0x60, flt & ~0x60
            if self.k_state:
                lvl |= 0x20
            elif self.dp_pullup():
                lvl |= 0x40
        return lvl, flt

    # ------------------------------------------------------------ host side
    def dp_pullup(self):
        return bool(self.m.analog[0x0B] & 0x80)

    def run_until(self, cond, timeout_ms):
        end = self.m.ms() + timeout_ms
        while not cond():
            if self.m.ms() >= end:
                return False
            self.m.run(self.m.cycles + 256)
        return True

    def core_wake_now(self):
        """The USB core wake source, asked when the chip sleeps (Machine.wake_status)."""
        if not self.m.regs_mem[0x6E] & 0x04:
            return False
        if self.core_wake == "edge":
            taken, self.k_edge = self.k_edge, False
            return taken
        return self.k_state

    def suspend(self):
        """The host stops the bus."""
        self.suspended = True
        self.m.event("USB: bus suspended by the host")

    def resume(self, k_ms=20):
        """The host resumes the bus: K for k_ms, then SOFs again."""
        self.k_state = self.k_edge = True
        self.m.event("USB: resume (K) from the host")
        self.m.run_ms(self.m.ms() + k_ms)
        self.k_state = self.k_edge = False
        self.suspended = False

    def bus_reset(self, settle_ms=5):
        self.toggle_next = [0] * 8
        self.suspended = False
        self.m.regs_mem[0x10A] &= ~0x04
        self.m.regs_mem[0x10B] = 0
        self.m.irq_latch |= 1 << IRQ_USB_RST
        self.m.run_ms(self.m.ms() + settle_ms)

    def _stage(self, sta, irq, ack, stall):
        self.ep0_ctrl = None
        self.m.regs_mem[0x103] |= sta
        start = self.m.ms()
        ok = self.run_until(lambda: self.ep0_ctrl is not None and not (self.m.regs_mem[0x103] & sta),
                            self.stage_timeout_ms)
        took = self.m.ms() - start
        if self.overflow:
            raise UsbError("device wrote more than 8 bytes into the EP0 FIFO")
        if not ok or self.ep0_ctrl is None:
            raise UsbError(f"no answer to stage 0x{sta:02x} within {self.stage_timeout_ms} ms")
        if took > self.SPEC_STAGE_MS and not self.setup[0] & 0x60:
            self.slow_stages.append((round(start, 3), self.setup[0], self.setup[1], sta, round(took, 3)))
            self.m.event(f"USB: stage 0x{sta:02x} of request 0x{self.setup[0]:02x}/0x{self.setup[1]:02x} took "
                         f"{took:.1f} ms, more than USB 2.0 9.2.6.4's {self.SPEC_STAGE_MS} ms for a standard "
                         f"request (hosts wait longer)")
        if self.ep0_ctrl & stall:
            raise UsbError("STALL")
        if not self.ep0_ctrl & ack:
            raise UsbError(f"stage 0x{sta:02x} armed without ACK (ctrl 0x{self.ep0_ctrl:02x})")

    AUTO_BIT = {5: 0x01, 9: 0x02, 8: 0x02, 11: 0x04, 10: 0x04, 0: 0x08, 12: 0x10, 6: 0x20, 3: 0x40, 1: 0x40}

    def hardware_answers(self, setup):
        """Whether the controller handles this request itself (0x104)."""
        if setup[0] & 0x60:          # class or vendor: always the firmware
            return False
        bit = self.AUTO_BIT.get(setup[1], 0x80)
        return bool(self.m.regs_mem[0x104] & bit)

    def control(self, setup, out=b"", in_max=0):
        """One control transfer; returns the IN data (bytes) or b"" for OUT."""
        wlength = setup[6] | setup[7] << 8
        if setup[0] == 0 and setup[1] == 9:        # SET_CONFIGURATION
            self.m.regs_mem[0x10B] = 1 if setup[2] else 0
        if self.hardware_answers(setup):
            if setup[0] == 0 and setup[1] in (1, 3) and setup[2] == 1:    # CLEAR/SET_FEATURE(DEVICE_REMOTE_WAKEUP)
                r = self.m.regs_mem
                r[0x10A] = (r[0x10A] | 0x04) if setup[1] == 3 else (r[0x10A] & ~0x04)
            if setup[1] == 5:
                self.address = setup[2]
            elif setup[1] == 9:
                self.hw_config = setup[2]
            if setup[0] & 0x80:
                data = bytes([self.hw_config]) if setup[1] == 8 else bytes(wlength)
                return data[:in_max or wlength]
            return b""
        self.overflow = False
        self.setup = bytes(setup)
        self.fifo[:] = bytes(setup)
        self.ep0_ptr = 8
        self._stage(STA_SETUP, IRQ_EP0_SETUP, DAT_ACK, DAT_STALL)
        if setup[0] & 0x80:
            data = bytearray()
            while True:
                n = self.ep0_ptr
                if n > 8:
                    raise UsbError("IN packet longer than 8 bytes")
                data += self.fifo[:n]
                self._stage(STA_DATA, IRQ_EP0_DATA, DAT_ACK, DAT_STALL)
                if n < 8 or len(data) >= wlength:
                    break
            self.ep0_ptr = 0
            self._stage(STA_STA, IRQ_EP0_STA, ST_ACK, ST_STALL)
            return bytes(data[:in_max or wlength])
        for i in range(0, len(out), 8):
            chunk = out[i:i + 8]
            self.fifo[:len(chunk)] = chunk
            self.ep0_ptr = len(chunk)
            self._stage(STA_DATA, IRQ_EP0_DATA, DAT_ACK, DAT_STALL)
        self._stage(STA_STA, IRQ_EP0_STA, ST_ACK, ST_STALL)
        return b""

    def get_descriptor(self, dtype, index=0, windex=0, length=255, recipient=0x80):
        return self.control(bytes([recipient, 6, index, dtype, windex & 0xFF, windex >> 8, length & 0xFF, length >> 8]))

    def out_ep(self, ep, data):
        """Send an OUT packet to data endpoint ep, as an OUT token would;
        False if the endpoint NAKs (not armed)."""
        s = ep & 7
        r = self.m.regs_mem
        if not r[0x120 + s] & EP_BUSY:
            return False
        for i, byte in enumerate(data):
            self.usb_ram[(r[0x128 + s] + i) & 0xFF] = byte
        self.ep_ptr[s] = len(data)
        r[0x120 + s] &= ~EP_BUSY & 0xFF
        r[0x139] |= 1 << s
        return True

    def in_ep(self, ep, raise_irq=None):
        """Take the packet armed on IN endpoint ep, as an IN token would; None if NAK."""
        s = ep & 7
        r = self.m.regs_mem
        ctrl = r[0x120 + s]
        if not ctrl & EP_BUSY:
            return None
        self.toggle_checked += 1
        want = EP_DAT1 if self.toggle_next[s] else EP_DAT0
        if ctrl & (EP_DAT0 | EP_DAT1) != want:
            self.toggle_errors += 1
        self.toggle_next[s] ^= 1
        n = self.ep_ptr[s] & 0xFF
        data = bytes(self.usb_ram[(r[0x128 + s] + i) & 0xFF] for i in range(n))
        r[0x120 + s] = ctrl & ~EP_BUSY
        if raise_irq is None or raise_irq:
            r[0x139] |= 1 << s        # the endpoint's status bit; the line follows it
        return data

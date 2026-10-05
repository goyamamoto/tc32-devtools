"""The air between the modelled radio (ble_radio.py) and its centrals
(ble_central.py): packets lost on the way, a central whose clock runs at
another rate than the chip's, and connection events that start a little
early or late. Radio.air holds one; with None, the default, every packet
arrives and both sides keep one clock.

- Lost packets. A packet is lost with the probability of its way: loss_keyboard
  for a central's packets (CONNECT_IND, SCAN_REQ, the packets of a
  connection event), loss_host for the chip's (advertising, its replies in a
  connection event). On the channels in bad (channel indexes 0-39, as 0x40d
  holds them) the probability is bad_loss when that is higher. A lost packet
  is not heard at all: the chip's receiver stays open as when no packet came,
  a central takes no reply as received. Corrupted packets (a CRC error with
  the packet's time still taken) are not modelled.
- Until blackout_until (ms of the machine's time) every packet is lost, both
  ways.
- The central's clock: clock_ppm parts per million slower (positive) or faster
  (negative) than the chip's. The central measures its transmit window and
  its connection interval with it, so its events come that much later or
  earlier than the chip's clock gives them.
- Jitter: each connection event starts a whole number of microseconds off its
  time, drawn evenly from -jitter_us..jitter_us. The time of the next event
  does not move with it.

Probabilities are whole millionths and every draw is an integer from
random.Random, one generator for each way and one for the jitter (seeds
4 * seed, + 1, + 2), so a run repeats exactly and a port can draw the same
numbers. A way with probability 0 draws nothing.

SPDX-License-Identifier: Apache-2.0
"""
import random

MILLION = 1_000_000
WAYS = ("keyboard", "host")


def millionths(p):
    """A probability 0..1 as whole millionths."""
    n = int(round(float(p) * MILLION))
    if not 0 <= n <= MILLION:
        raise ValueError(f"probability {p}: not in 0..1")
    return n


class Air:
    """What packets meet between the chip and its centrals."""

    def __init__(self, seed=1, loss_keyboard=0, loss_host=0, bad=(), bad_loss=0, clock_ppm=0, jitter_us=0):
        self.seed = seed
        self.loss = {"keyboard": loss_keyboard, "host": loss_host}   # millionths
        self.bad = frozenset(bad)
        self.bad_loss = bad_loss                                      # millionths
        self.clock_ppm = clock_ppm
        self.jitter_us = jitter_us
        self.blackout_until = 0.0
        for name, n in (("loss_keyboard", loss_keyboard), ("loss_host", loss_host), ("bad_loss", bad_loss)):
            if not 0 <= n <= MILLION:
                raise ValueError(f"{name}: {n} is not 0..{MILLION} millionths")
        if any(not 0 <= ch <= 39 for ch in self.bad):
            raise ValueError(f"bad: {sorted(self.bad)} holds a channel index outside 0..39")
        if jitter_us < 0:
            raise ValueError(f"jitter_us: {jitter_us}")
        self.rng = {"keyboard": random.Random(4 * seed), "host": random.Random(4 * seed + 1)}
        self.jit = random.Random(4 * seed + 2)
        self.sent = {"keyboard": 0, "host": 0}      # packets asked about, each way
        self.lost_n = {"keyboard": 0, "host": 0}    # of those, lost
        self.losses = []                            # (ms, way, channel) of each lost packet

    def clock(self):
        """The central's time for one unit of the chip's."""
        return 1.0 + self.clock_ppm / MILLION

    def lost(self, way, ch, now_ms):
        """Whether the packet sent now on channel index ch is lost on its way to the keyboard or the host."""
        self.sent[way] += 1
        p = self.loss[way]
        if ch in self.bad and self.bad_loss > p:
            p = self.bad_loss
        if now_ms < self.blackout_until:
            gone = True
        elif p <= 0:
            gone = False
        else:
            gone = self.rng[way].randrange(MILLION) < p
        if gone:
            self.lost_n[way] += 1
            self.losses.append((round(now_ms, 3), way, ch))
        return gone

    def jitter_ms(self):
        """How far the next connection event starts off its time, in ms."""
        if self.jitter_us <= 0:
            return 0.0
        return (self.jit.randrange(2 * self.jitter_us + 1) - self.jitter_us) / 1000

    def summary(self):
        k, h = self.sent, self.lost_n
        return (f"to the keyboard {h['keyboard']} of {k['keyboard']} packets lost, "
                f"to the host {h['host']} of {k['host']}")

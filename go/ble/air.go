// SPDX-License-Identifier: Apache-2.0

package ble

import (
	"fmt"
	"math"
)

// Million: probabilities are whole millionths.
const Million = 1_000_000

// The ways a packet goes (the Python "keyboard" and "host").
const (
	ToKeyboard = 0
	ToHost     = 1
)

// WayNames are the ways' names in logs.
var WayNames = [2]string{"keyboard", "host"}

// AirLoss is one lost packet (Air.Losses).
type AirLoss struct {
	Ms  float64
	Way int
	Ch  byte
}

// Air is the air between the modelled radio and its centrals (the Python
// ble_air.Air): packets lost on the way, a central whose clock runs at
// another rate than the chip's, and connection events that start a little
// early or late. Radio.Air holds one; with nil, the default, every packet
// arrives and both sides keep one clock.
//
// Every draw is an integer from pyRandom, one generator for each way and one
// for the jitter (seeds 4*seed, +1, +2), so a run draws the numbers the
// Python model draws. A way with probability 0 draws nothing.
type Air struct {
	Seed          uint32
	Loss          [2]int // millionths: ToKeyboard, ToHost
	Bad           map[byte]bool
	BadLoss       int // millionths
	ClockPPM      float64
	JitterUs      int
	BlackoutUntil float64
	rng           [2]*pyRandom
	jit           *pyRandom
	Sent          [2]int // packets asked about, each way
	LostN         [2]int // of those, lost
	Losses        []AirLoss
}

// Millionths is a probability 0..1 as whole millionths.
func Millionths(p float64) (int, error) {
	n := int(math.RoundToEven(p * Million))
	if n < 0 || n > Million {
		return 0, fmt.Errorf("probability %v: not in 0..1", p)
	}
	return n, nil
}

// NewAir makes an air; the probabilities are millionths, bad the channel
// indexes (0-39, as 0x40d holds them) that take badLoss when it is higher.
func NewAir(seed uint32, lossKeyboard, lossHost int, bad []byte, badLoss int, clockPPM float64, jitterUs int) (*Air, error) {
	for _, x := range []struct {
		name string
		n    int
	}{{"loss_keyboard", lossKeyboard}, {"loss_host", lossHost}, {"bad_loss", badLoss}} {
		if x.n < 0 || x.n > Million {
			return nil, fmt.Errorf("%s: %d is not 0..%d millionths", x.name, x.n, Million)
		}
	}
	a := &Air{Seed: seed, Loss: [2]int{lossKeyboard, lossHost}, Bad: map[byte]bool{}, BadLoss: badLoss,
		ClockPPM: clockPPM, JitterUs: jitterUs}
	for _, ch := range bad {
		if ch > 39 {
			return nil, fmt.Errorf("bad: a channel index outside 0..39 (%d)", ch)
		}
		a.Bad[ch] = true
	}
	if jitterUs < 0 {
		return nil, fmt.Errorf("jitter_us: %d", jitterUs)
	}
	a.rng = [2]*pyRandom{newPyRandom(4 * seed), newPyRandom(4*seed + 1)}
	a.jit = newPyRandom(4*seed + 2)
	return a, nil
}

// Clock is the central's time for one unit of the chip's.
func (a *Air) Clock() float64 { return 1.0 + a.ClockPPM/Million }

// Lost: whether the packet sent now on channel index ch is lost on its way.
func (a *Air) Lost(way int, ch byte, nowMs float64) bool {
	a.Sent[way]++
	p := a.Loss[way]
	if a.Bad[ch] && a.BadLoss > p {
		p = a.BadLoss
	}
	var gone bool
	switch {
	case nowMs < a.BlackoutUntil:
		gone = true
	case p <= 0:
		gone = false
	default:
		gone = int(a.rng[way].randrange(Million)) < p
	}
	if gone {
		a.LostN[way]++
		a.Losses = append(a.Losses, AirLoss{round3(nowMs), way, ch})
	}
	return gone
}

// JitterMs: how far the next connection event starts off its time, in ms.
func (a *Air) JitterMs() float64 {
	if a.JitterUs <= 0 {
		return 0.0
	}
	return float64(int(a.jit.randrange(uint32(2*a.JitterUs+1)))-a.JitterUs) / 1000
}

// Summary is the Python Air.summary.
func (a *Air) Summary() string {
	return fmt.Sprintf("to the keyboard %d of %d packets lost, to the host %d of %d",
		a.LostN[ToKeyboard], a.Sent[ToKeyboard], a.LostN[ToHost], a.Sent[ToHost])
}

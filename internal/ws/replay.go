package ws

import (
	"errors"
	"math"
)

// This file is the anti-replay half of message sealing: a per-direction
// message counter on the sending side, and a sliding window on the
// receiving side that refuses any counter it has already accepted.
//
// It exists because synq-server relays opaque bytes and is not trusted:
// a relay (or anyone who can inject into one) can record a sealed frame
// and deliver it again later, and without a counter the receiver has no
// way to tell the second delivery from a fresh message. The counter
// travels inside the authenticated plaintext (see SealCounted), so it
// can't be altered or stripped without failing authentication.
//
// Both types are plain data with no dependencies, deliberately: the
// logic is easy to get subtly wrong, so it is kept free of crypto and
// I/O to be tested exhaustively on its own.

// replayWindowSize is how far behind the highest accepted counter a
// message may still arrive and be accepted (once). A window rather than
// a strict "must increase" rule, because legitimate delivery is not
// guaranteed to be in order: a burst of queued messages is sent by
// concurrent commands, and strict ordering would silently drop any that
// overtook an earlier one. Anything older than the window is refused as
// too old to tell apart from a replay.
const replayWindowSize = 1024

const replayWords = replayWindowSize / 64

// ReplayWindow tracks which counters have been accepted under one
// session key. The zero value is ready to use and accepts any first
// counter. Not safe for concurrent use; a chat session is only touched
// from Bubble Tea's single Update goroutine.
type ReplayWindow struct {
	started bool
	highest uint64
	// seen is a bitmap of the window: bit i (word i/64, bit i%64) is set
	// when counter (highest - i) has been accepted. Bit 0 is always set
	// once started.
	seen [replayWords]uint64
}

// Accept reports whether counter is fresh, and records it if so. It
// returns false for a counter already accepted, for one older than the
// window, and for math.MaxUint64 (which could never be advanced past).
// Call it only AFTER the message has authenticated, so that forged
// frames can't move the window.
func (w *ReplayWindow) Accept(counter uint64) bool {
	if counter == math.MaxUint64 {
		return false
	}

	if !w.started {
		w.started = true
		w.highest = counter
		w.seen = [replayWords]uint64{}
		w.seen[0] = 1
		return true
	}

	if counter > w.highest {
		w.shift(counter - w.highest)
		w.highest = counter
		w.seen[0] |= 1
		return true
	}

	age := w.highest - counter
	if age >= replayWindowSize {
		return false
	}
	word, bit := age/64, age%64
	if w.seen[word]&(1<<bit) != 0 {
		return false
	}
	w.seen[word] |= 1 << bit
	return true
}

// shift ages every recorded counter by n positions (bit i becomes bit
// i+n), discarding any that fall off the far end of the window.
func (w *ReplayWindow) shift(n uint64) {
	if n >= replayWindowSize {
		w.seen = [replayWords]uint64{}
		return
	}
	wordShift := int(n / 64)
	bitShift := uint(n % 64)

	var out [replayWords]uint64
	for i := replayWords - 1; i >= wordShift; i-- {
		v := w.seen[i-wordShift] << bitShift
		if bitShift > 0 && i-wordShift-1 >= 0 {
			v |= w.seen[i-wordShift-1] >> (64 - bitShift)
		}
		out[i] = v
	}
	w.seen = out
}

// ErrCounterExhausted is returned by SendCounter.Next after 2^64-1
// messages under one key. Unreachable in practice; it exists so the
// counter can never wrap and repeat.
var ErrCounterExhausted = errors.New("ws: message counter exhausted for this session key")

// SendCounter hands out strictly increasing counters for one sending
// direction under one session key. The zero value is ready to use. The
// first counter issued is 1.
type SendCounter struct {
	last uint64
}

// Next returns the next counter to seal a message with.
func (c *SendCounter) Next() (uint64, error) {
	if c.last == math.MaxUint64-1 {
		return 0, ErrCounterExhausted
	}
	c.last++
	return c.last, nil
}

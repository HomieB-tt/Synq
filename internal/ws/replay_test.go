package ws

import (
	"math"
	"math/rand"
	"testing"
)

func TestReplayWindowAcceptsEachCounterOnce(t *testing.T) {
	var w ReplayWindow
	for _, c := range []uint64{1, 2, 3, 10} {
		if !w.Accept(c) {
			t.Fatalf("Accept(%d) = false on first delivery", c)
		}
	}
	for _, c := range []uint64{1, 2, 3, 10} {
		if w.Accept(c) {
			t.Errorf("Accept(%d) = true on replay", c)
		}
	}
}

func TestReplayWindowToleratesReordering(t *testing.T) {
	var w ReplayWindow
	// 5 overtakes 3 and 4, as concurrent sends can.
	for _, c := range []uint64{1, 2, 5, 3, 4} {
		if !w.Accept(c) {
			t.Fatalf("Accept(%d) = false, want an out-of-order but fresh counter accepted", c)
		}
	}
	for _, c := range []uint64{1, 2, 3, 4, 5} {
		if w.Accept(c) {
			t.Errorf("Accept(%d) = true after it was already accepted", c)
		}
	}
}

func TestReplayWindowRejectsCountersOlderThanTheWindow(t *testing.T) {
	var w ReplayWindow
	if !w.Accept(1) {
		t.Fatal("Accept(1) = false")
	}
	far := uint64(1 + replayWindowSize)
	if !w.Accept(far) {
		t.Fatalf("Accept(%d) = false", far)
	}
	// 1 is now exactly replayWindowSize behind: outside the window.
	if w.Accept(1) {
		t.Error("Accept(1) = true although it is older than the window")
	}
	// The oldest counter still inside the window, never seen, is fine.
	if !w.Accept(far - (replayWindowSize - 1)) {
		t.Error("Accept(oldest in-window counter) = false, want true")
	}
}

func TestReplayWindowJumpFarAheadForgetsEverythingOlder(t *testing.T) {
	var w ReplayWindow
	for c := uint64(1); c <= 50; c++ {
		w.Accept(c)
	}
	w.Accept(1_000_000)
	for c := uint64(1); c <= 50; c++ {
		if w.Accept(c) {
			t.Fatalf("Accept(%d) = true after a jump far past the window", c)
		}
	}
}

func TestReplayWindowAcceptsAnyFirstCounter(t *testing.T) {
	var w ReplayWindow
	if !w.Accept(987654321) {
		t.Error("a fresh window refused its first counter")
	}
	if w.Accept(987654321) {
		t.Error("the first counter was accepted twice")
	}
}

func TestReplayWindowRefusesMaxUint64(t *testing.T) {
	var w ReplayWindow
	if w.Accept(math.MaxUint64) {
		t.Error("Accept(MaxUint64) = true, but the window could never advance past it")
	}
}

// The window must behave exactly like an unbounded "set of accepted
// counters plus a cut-off" for any delivery order. This drives it with
// random orderings and compares every verdict against that obvious
// reference model - which is what catches mistakes in the bit-shifting.
func TestReplayWindowMatchesReferenceModel(t *testing.T) {
	rng := rand.New(rand.NewSource(1))

	for trial := 0; trial < 300; trial++ {
		var w ReplayWindow
		seen := map[uint64]bool{}
		var highest uint64
		started := false

		for step := 0; step < 400; step++ {
			var c uint64
			switch rng.Intn(4) {
			case 0: // brand-new, slightly ahead
				c = highest + uint64(rng.Intn(5)) + 1
			case 1: // jump, sometimes far past the window
				c = highest + uint64(rng.Intn(3000)) + 1
			case 2: // something near the top, probably a repeat
				back := uint64(rng.Intn(replayWindowSize + 200))
				if back > highest {
					back = highest
				}
				c = highest - back
			default: // a previously accepted counter
				for k := range seen {
					c = k
					break
				}
			}
			if c == 0 || c == math.MaxUint64 {
				continue
			}

			want := false
			switch {
			case !started:
				want = true
			case c > highest:
				want = true
			case highest-c >= replayWindowSize:
				want = false
			default:
				want = !seen[c]
			}

			got := w.Accept(c)
			if got != want {
				t.Fatalf("trial %d step %d: Accept(%d) = %v, want %v (highest=%d)", trial, step, c, got, want, highest)
			}
			if got {
				seen[c] = true
				if !started || c > highest {
					highest = c
				}
				started = true
			}
		}
	}
}

func TestSendCounterIsStrictlyIncreasingFromOne(t *testing.T) {
	var c SendCounter
	var prev uint64
	for i := 0; i < 100; i++ {
		n, err := c.Next()
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if n != prev+1 {
			t.Fatalf("Next = %d, want %d", n, prev+1)
		}
		prev = n
	}
}

func TestSendCounterNeverWraps(t *testing.T) {
	c := SendCounter{last: math.MaxUint64 - 1}
	if _, err := c.Next(); err != ErrCounterExhausted {
		t.Fatalf("Next at the limit: err = %v, want ErrCounterExhausted", err)
	}
	if c.last != math.MaxUint64-1 {
		t.Errorf("an exhausted counter advanced to %d", c.last)
	}
}

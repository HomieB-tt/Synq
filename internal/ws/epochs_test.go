package ws

import "testing"

func key(b byte) *[32]byte {
	var k [32]byte
	k[0] = b
	return &k
}

func TestEpochsForReturnsTheSameStateForTheSamePair(t *testing.T) {
	var e Epochs
	a := e.For(key(1), key(2))
	n, err := a.Send.Next()
	if err != nil || n != 1 {
		t.Fatalf("Next = %d, %v", n, err)
	}
	if !a.Recv.Accept(7) {
		t.Fatal("first Accept(7) refused")
	}

	b := e.For(key(1), key(2))
	if b != a {
		t.Fatal("For returned a different Epoch for the same pair")
	}
	if n, _ := b.Send.Next(); n != 2 {
		t.Errorf("send counter restarted: got %d, want 2", n)
	}
	if b.Recv.Accept(7) {
		t.Error("a counter accepted earlier was accepted again")
	}
}

func TestEpochsKeepStateAcrossAnotherPairComingAndGoing(t *testing.T) {
	var e Epochs
	first := e.For(key(1), key(2))
	first.Recv.Accept(5)

	// The contact relaunches (new ephemeral), then an old handshake
	// brings the first pair back: its window must still remember 5.
	_ = e.For(key(1), key(3))
	if e.For(key(1), key(2)).Recv.Accept(5) {
		t.Error("returning to an earlier pair forgot an accepted counter")
	}
}

func TestEpochsAreIndependentPerPair(t *testing.T) {
	var e Epochs
	e.For(key(1), key(2)).Recv.Accept(5)
	if !e.For(key(1), key(3)).Recv.Accept(5) {
		t.Error("counter 5 under a different key pair was refused")
	}
	if !e.For(key(9), key(2)).Recv.Accept(5) {
		t.Error("counter 5 under a different local key was refused")
	}
}

func TestEpochsNilKeysShareAnEpoch(t *testing.T) {
	var e Epochs
	if e.For(key(1), nil) != e.For(key(1), nil) {
		t.Error("nil peer key did not map to a stable epoch")
	}
	if e.For(nil, nil) != e.For(nil, nil) {
		t.Error("nil keys did not map to a stable epoch")
	}
}

func TestEpochsRetire(t *testing.T) {
	var e Epochs
	if e.Retired(key(2)) {
		t.Fatal("unknown key reported retired")
	}
	e.Retire(key(2))
	if !e.Retired(key(2)) {
		t.Error("retired key not reported retired")
	}
	if e.Retired(key(3)) {
		t.Error("a different key reported retired")
	}
	e.Retire(nil)
	if e.Retired(nil) {
		t.Error("nil reported retired")
	}
}

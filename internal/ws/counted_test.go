package ws

import (
	"bytes"
	"testing"
)

// Both parties derive the same session key and, from it, mirrored
// directional keys: what one seals with, the other opens with.
func TestDirectionalKeysMirrorBetweenParties(t *testing.T) {
	alice, bob := generateIdentityOrFatal(t), generateIdentityOrFatal(t)
	aEph, bEph := generateEphemeralOrFatal(t), generateEphemeralOrFatal(t)

	aRoot, err := DeriveSessionKey(alice, aEph, bob.BoxPublic, bEph.Public)
	if err != nil {
		t.Fatalf("alice DeriveSessionKey: %v", err)
	}
	bRoot, err := DeriveSessionKey(bob, bEph, alice.BoxPublic, aEph.Public)
	if err != nil {
		t.Fatalf("bob DeriveSessionKey: %v", err)
	}
	aSend, aRecv, err := DirectionalKeys(aRoot, alice.BoxPublic, bob.BoxPublic)
	if err != nil {
		t.Fatalf("alice DirectionalKeys: %v", err)
	}
	bSend, bRecv, err := DirectionalKeys(bRoot, bob.BoxPublic, alice.BoxPublic)
	if err != nil {
		t.Fatalf("bob DirectionalKeys: %v", err)
	}

	if *aSend != *bRecv {
		t.Error("alice's send key is not bob's receive key")
	}
	if *bSend != *aRecv {
		t.Error("bob's send key is not alice's receive key")
	}
	if *aSend == *aRecv {
		t.Error("the two directions share a key, so reflection would verify")
	}
	if *aSend == *aRoot || *aRecv == *aRoot {
		t.Error("a directional key equals the root key")
	}
}

func TestDirectionalKeysRejectNil(t *testing.T) {
	k := &[32]byte{1}
	if _, _, err := DirectionalKeys(nil, k, k); err == nil {
		t.Error("accepted a nil root key")
	}
	if _, _, err := DirectionalKeys(k, nil, k); err == nil {
		t.Error("accepted a nil local key")
	}
	if _, _, err := DirectionalKeys(k, k, nil); err == nil {
		t.Error("accepted a nil remote key")
	}
}

func TestDirectionalKeysDifferPerPair(t *testing.T) {
	root := &[32]byte{7}
	a, b, c := &[32]byte{1}, &[32]byte{2}, &[32]byte{3}
	ab, _, _ := DirectionalKeys(root, a, b)
	ac, _, _ := DirectionalKeys(root, a, c)
	if *ab == *ac {
		t.Error("the same send key was derived for two different contact pairs")
	}
}

func TestSealCountedRoundTripCarriesTheCounter(t *testing.T) {
	key := randomSessionKey(t)
	sealed, err := SealCounted(key, 42, []byte("hello"))
	if err != nil {
		t.Fatalf("SealCounted: %v", err)
	}
	counter, body, err := OpenCounted(key, sealed)
	if err != nil {
		t.Fatalf("OpenCounted: %v", err)
	}
	if counter != 42 || !bytes.Equal(body, []byte("hello")) {
		t.Errorf("got counter %d body %q, want 42 %q", counter, body, "hello")
	}
}

func TestSealCountedAllowsAnEmptyBody(t *testing.T) {
	key := randomSessionKey(t)
	sealed, err := SealCounted(key, 1, nil)
	if err != nil {
		t.Fatalf("SealCounted: %v", err)
	}
	if _, body, err := OpenCounted(key, sealed); err != nil || len(body) != 0 {
		t.Errorf("OpenCounted = %q, %v", body, err)
	}
}

func TestOpenCountedRejectsAWrongKey(t *testing.T) {
	sealed, err := SealCounted(randomSessionKey(t), 1, []byte("x"))
	if err != nil {
		t.Fatalf("SealCounted: %v", err)
	}
	if _, _, err := OpenCounted(randomSessionKey(t), sealed); err != ErrMessageAuthFailed {
		t.Errorf("err = %v, want ErrMessageAuthFailed", err)
	}
}

// A message sealed without a counter (too short to hold one) must not
// be mistaken for counter zero.
func TestOpenCountedRejectsABodyTooShortForACounter(t *testing.T) {
	key := randomSessionKey(t)
	sealed, err := SealMessage(key, []byte{1, 2, 3})
	if err != nil {
		t.Fatalf("SealMessage: %v", err)
	}
	if _, _, err := OpenCounted(key, sealed); err != ErrMessageMalformed {
		t.Errorf("err = %v, want ErrMessageMalformed", err)
	}
}

// The counter is inside the authenticated bytes: flipping any bit,
// including in the counter, fails authentication.
func TestOpenCountedDetectsTamperingWithTheCounter(t *testing.T) {
	key := randomSessionKey(t)
	sealed, err := SealCounted(key, 5, []byte("x"))
	if err != nil {
		t.Fatalf("SealCounted: %v", err)
	}
	for i := range sealed {
		tampered := append([]byte(nil), sealed...)
		tampered[i] ^= 0x01
		if _, _, err := OpenCounted(key, tampered); err == nil {
			t.Fatalf("flipping byte %d went undetected", i)
		}
	}
}

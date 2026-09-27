package ws

import (
	"bytes"
	"testing"

	"github.com/HomieB-tt/synq/internal/crypto"
)

// TestDeriveSessionKeyIsSymmetric is the load-bearing test for this
// package: it fails if the canonical-ordering fix documented on
// DeriveSessionKey ever regresses back to the perspective-dependent
// reading of DESIGN.md's formula, in which case two honest parties
// would silently derive different keys and never manage to decrypt
// each other's messages.
//
// Run twice, with the two identities generated in each order, so the
// "whichever static key sorts first" comparison gets exercised in
// both directions rather than happening to pass because of which
// identity this particular test generated first.
func TestDeriveSessionKeyIsSymmetric(t *testing.T) {
	for i := 0; i < 20; i++ {
		alice := generateIdentityOrFatal(t)
		bob := generateIdentityOrFatal(t)
		aliceEph := generateEphemeralOrFatal(t)
		bobEph := generateEphemeralOrFatal(t)

		aliceKey, err := DeriveSessionKey(alice, aliceEph, bob.BoxPublic, bobEph.Public)
		if err != nil {
			t.Fatalf("alice DeriveSessionKey: %v", err)
		}
		bobKey, err := DeriveSessionKey(bob, bobEph, alice.BoxPublic, aliceEph.Public)
		if err != nil {
			t.Fatalf("bob DeriveSessionKey: %v", err)
		}

		if !bytes.Equal(aliceKey[:], bobKey[:]) {
			t.Fatalf("iteration %d: alice and bob derived different session keys:\nalice: %x\nbob:   %x", i, aliceKey[:], bobKey[:])
		}
	}
}

// TestDeriveSessionKeyDiffersPerContact guards against a derivation
// that accidentally ignores one of its four key inputs (e.g. if the
// canonical-ordering swap were applied to the wrong pair of values,
// collapsing two different handshakes onto the same key).
func TestDeriveSessionKeyDiffersPerContact(t *testing.T) {
	alice := generateIdentityOrFatal(t)
	aliceEph := generateEphemeralOrFatal(t)

	bob := generateIdentityOrFatal(t)
	bobEph := generateEphemeralOrFatal(t)
	carol := generateIdentityOrFatal(t)
	carolEph := generateEphemeralOrFatal(t)

	keyWithBob, err := DeriveSessionKey(alice, aliceEph, bob.BoxPublic, bobEph.Public)
	if err != nil {
		t.Fatalf("DeriveSessionKey(bob): %v", err)
	}
	keyWithCarol, err := DeriveSessionKey(alice, aliceEph, carol.BoxPublic, carolEph.Public)
	if err != nil {
		t.Fatalf("DeriveSessionKey(carol): %v", err)
	}

	if bytes.Equal(keyWithBob[:], keyWithCarol[:]) {
		t.Fatal("sessions with two different contacts derived the same key")
	}
}

func TestDeriveSessionKeyRejectsNilInputs(t *testing.T) {
	alice := generateIdentityOrFatal(t)
	aliceEph := generateEphemeralOrFatal(t)
	bob := generateIdentityOrFatal(t)
	bobEph := generateEphemeralOrFatal(t)

	cases := []struct {
		name string
		fn   func() (*[32]byte, error)
	}{
		{"nil local", func() (*[32]byte, error) { return DeriveSessionKey(nil, aliceEph, bob.BoxPublic, bobEph.Public) }},
		{"nil localEphemeral", func() (*[32]byte, error) { return DeriveSessionKey(alice, nil, bob.BoxPublic, bobEph.Public) }},
		{"nil remoteStaticPub", func() (*[32]byte, error) { return DeriveSessionKey(alice, aliceEph, nil, bobEph.Public) }},
		{"nil remoteEphemeralPub", func() (*[32]byte, error) { return DeriveSessionKey(alice, aliceEph, bob.BoxPublic, nil) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := c.fn(); err == nil {
				t.Fatal("expected an error, got nil")
			}
		})
	}
}

func TestNewEphemeralKeypairIsFreshEachTime(t *testing.T) {
	a := generateEphemeralOrFatal(t)
	b := generateEphemeralOrFatal(t)

	if bytes.Equal(a.Public[:], b.Public[:]) {
		t.Fatal("two calls to NewEphemeralKeypair produced the same public key")
	}
	if bytes.Equal(a.Private[:], b.Private[:]) {
		t.Fatal("two calls to NewEphemeralKeypair produced the same private key")
	}
}

func generateIdentityOrFatal(t *testing.T) *crypto.Identity {
	t.Helper()
	id, err := crypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	return id
}

func generateEphemeralOrFatal(t *testing.T) *EphemeralKeypair {
	t.Helper()
	eph, err := NewEphemeralKeypair()
	if err != nil {
		t.Fatalf("NewEphemeralKeypair: %v", err)
	}
	return eph
}

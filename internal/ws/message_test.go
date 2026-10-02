package ws

import (
	"bytes"
	"crypto/rand"
	"io"
	"testing"

	"golang.org/x/crypto/nacl/secretbox"
)

func randomSessionKey(t *testing.T) *[32]byte {
	t.Helper()
	var key [32]byte
	if _, err := io.ReadFull(rand.Reader, key[:]); err != nil {
		t.Fatalf("generate random key: %v", err)
	}
	return &key
}

func TestSealThenOpenMessageRoundTrip(t *testing.T) {
	key := randomSessionKey(t)
	plaintext := []byte("hey, are you free to talk?")

	sealed, err := SealMessage(key, plaintext)
	if err != nil {
		t.Fatalf("SealMessage: %v", err)
	}

	got, err := OpenMessage(key, sealed)
	if err != nil {
		t.Fatalf("OpenMessage: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("got %q, want %q", got, plaintext)
	}
}

func TestSealMessageProducesFreshNonceEachTime(t *testing.T) {
	key := randomSessionKey(t)
	plaintext := []byte("same message, twice")

	a, err := SealMessage(key, plaintext)
	if err != nil {
		t.Fatalf("SealMessage (a): %v", err)
	}
	b, err := SealMessage(key, plaintext)
	if err != nil {
		t.Fatalf("SealMessage (b): %v", err)
	}

	if bytes.Equal(a, b) {
		t.Fatal("two seals of the identical plaintext under the identical key produced identical ciphertext - nonce reuse")
	}
	if bytes.Equal(a[:nonceSize], b[:nonceSize]) {
		t.Fatal("two seals produced the same nonce")
	}
}

func TestOpenMessageRejectsWrongKey(t *testing.T) {
	sealed, err := SealMessage(randomSessionKey(t), []byte("secret"))
	if err != nil {
		t.Fatalf("SealMessage: %v", err)
	}

	if _, err := OpenMessage(randomSessionKey(t), sealed); err != ErrMessageAuthFailed {
		t.Fatalf("got err = %v, want ErrMessageAuthFailed", err)
	}
}

func TestOpenMessageRejectsTamperedCiphertext(t *testing.T) {
	key := randomSessionKey(t)
	sealed, err := SealMessage(key, []byte("do not modify me"))
	if err != nil {
		t.Fatalf("SealMessage: %v", err)
	}

	tampered := append([]byte(nil), sealed...)
	tampered[len(tampered)-1] ^= 0xFF // flip a bit in the ciphertext/tag

	if _, err := OpenMessage(key, tampered); err != ErrMessageAuthFailed {
		t.Fatalf("got err = %v, want ErrMessageAuthFailed", err)
	}
}

func TestOpenMessageRejectsTruncatedInput(t *testing.T) {
	key := randomSessionKey(t)

	cases := [][]byte{
		nil,
		{},
		make([]byte, nonceSize), // nonce only, no ciphertext/tag at all
		make([]byte, nonceSize+secretbox.Overhead-1),
	}
	for i, c := range cases {
		if _, err := OpenMessage(key, c); err != ErrMessageAuthFailed {
			t.Errorf("case %d (len=%d): got err = %v, want ErrMessageAuthFailed", i, len(c), err)
		}
	}
}

func TestSealMessageEndToEndWithRealHandshake(t *testing.T) {
	// Exercises the full, realistic path: two identities complete the
	// X3DH-lite handshake from handshake.go, both derive the session
	// key independently, and one seals a message the other opens -
	// the actual sequence a real chat message would follow, not just
	// the two pieces tested in isolation.
	alice := generateIdentityOrFatal(t)
	aliceEph := generateEphemeralOrFatal(t)
	bob := generateIdentityOrFatal(t)
	bobEph := generateEphemeralOrFatal(t)

	aliceKey, err := DeriveSessionKey(alice, aliceEph, bob.BoxPublic, bobEph.Public)
	if err != nil {
		t.Fatalf("alice DeriveSessionKey: %v", err)
	}
	bobKey, err := DeriveSessionKey(bob, bobEph, alice.BoxPublic, aliceEph.Public)
	if err != nil {
		t.Fatalf("bob DeriveSessionKey: %v", err)
	}

	plaintext := []byte("hello from alice")
	sealed, err := SealMessage(aliceKey, plaintext)
	if err != nil {
		t.Fatalf("SealMessage: %v", err)
	}

	got, err := OpenMessage(bobKey, sealed)
	if err != nil {
		t.Fatalf("bob OpenMessage: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("got %q, want %q", got, plaintext)
	}
}

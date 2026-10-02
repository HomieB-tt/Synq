package crypto

import (
	"crypto/ed25519"
	"encoding/hex"
	"testing"
)

func TestSignChallengeProducesAVerifiableSignature(t *testing.T) {
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("generate identity: %v", err)
	}

	challenge := "deadbeefcafef00d" // stands in for a real hex challenge string
	sigHex := id.SignChallenge(challenge)

	sig, err := hex.DecodeString(sigHex)
	if err != nil {
		t.Fatalf("signature is not valid hex: %v", err)
	}

	// The critical property synq-server actually relies on: the
	// signature verifies over the challenge's UTF-8 bytes, not its
	// hex-decoded raw bytes - see the doc comment on SignChallenge.
	if !ed25519.Verify(id.SigningPublic, []byte(challenge), sig) {
		t.Fatal("signature does not verify over the challenge string's UTF-8 bytes")
	}

	decodedChallenge, err := hex.DecodeString(challenge)
	if err != nil {
		t.Fatalf("test setup: challenge fixture is not valid hex: %v", err)
	}
	if ed25519.Verify(id.SigningPublic, decodedChallenge, sig) {
		t.Fatal("signature should NOT verify over the hex-decoded raw bytes - " +
			"if it does, SignChallenge is signing the wrong thing and will fail against the real server")
	}
}

func TestSigningPublicHexRoundTrips(t *testing.T) {
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("generate identity: %v", err)
	}

	got := id.SigningPublicHex()
	raw, err := hex.DecodeString(got)
	if err != nil {
		t.Fatalf("not valid hex: %v", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		t.Fatalf("expected %d bytes, got %d", ed25519.PublicKeySize, len(raw))
	}
	if !ed25519.PublicKey(raw).Equal(id.SigningPublic) {
		t.Fatal("decoded key does not match the original SigningPublic")
	}
}

func TestBoxPublicHexRoundTrips(t *testing.T) {
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("generate identity: %v", err)
	}

	got := id.BoxPublicHex()
	raw, err := hex.DecodeString(got)
	if err != nil {
		t.Fatalf("not valid hex: %v", err)
	}
	if len(raw) != 32 {
		t.Fatalf("expected 32 bytes, got %d", len(raw))
	}
	if [32]byte(raw) != *id.BoxPublic {
		t.Fatal("decoded key does not match the original BoxPublic")
	}
}

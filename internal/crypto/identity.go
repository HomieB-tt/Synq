// Package crypto handles local identity generation, passphrase-derived
// vault encryption, and (later) E2EE message encrypt/decrypt logic for
// the Synq client.
//
// See DESIGN.md sections 1-3 for the key storage, authenticity, and
// forward-secrecy design this package implements.
package crypto

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"

	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/nacl/box"
)

// Identity holds a user's long-lived Ed25519 signing keypair (used for
// server authentication and key-verification fingerprints) and X25519
// key-exchange keypair (used, together with a per-launch ephemeral key,
// to derive per-session message-encryption keys).
type Identity struct {
	SigningPublic  ed25519.PublicKey
	SigningPrivate ed25519.PrivateKey
	BoxPublic      *[32]byte
	BoxPrivate     *[32]byte
}

// GenerateIdentity creates a fresh signing keypair and a fresh
// key-exchange keypair. This is called exactly once per device, on
// first boot (Synq is single-device by design — see DESIGN.md
// section 5).
func GenerateIdentity() (*Identity, error) {
	signPub, signPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate signing key: %w", err)
	}

	boxPub, boxPriv, err := box.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate key-exchange key: %w", err)
	}

	return &Identity{
		SigningPublic:  signPub,
		SigningPrivate: signPriv,
		BoxPublic:      boxPub,
		BoxPrivate:     boxPriv,
	}, nil
}

// seed returns the 32-byte Ed25519 seed this identity's signing keypair
// was derived from. The vault only needs to persist this seed (32
// bytes) rather than the full 64-byte private key, since the private
// key, public key, and everything else are all deterministically
// derivable from it.
func (id *Identity) seed() []byte {
	return id.SigningPrivate.Seed()
}

// identityFromSeeds reconstructs a full Identity from its two pieces of
// persisted secret material: the 32-byte Ed25519 seed and the 32-byte
// X25519 private key. Used when opening a vault.
func identityFromSeeds(edSeed []byte, boxPriv *[32]byte) (*Identity, error) {
	if len(edSeed) != ed25519.SeedSize {
		return nil, fmt.Errorf("identity: invalid ed25519 seed size %d", len(edSeed))
	}

	signPriv := ed25519.NewKeyFromSeed(edSeed)
	signPub, ok := signPriv.Public().(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("identity: unexpected public key type")
	}

	pub, err := curve25519.X25519(boxPriv[:], curve25519.Basepoint)
	if err != nil {
		return nil, fmt.Errorf("identity: derive x25519 public key: %w", err)
	}
	var boxPub [32]byte
	copy(boxPub[:], pub)

	return &Identity{
		SigningPublic:  signPub,
		SigningPrivate: signPriv,
		BoxPublic:      &boxPub,
		BoxPrivate:     boxPriv,
	}, nil
}

// NOTE: the `:verify` safety-number fingerprint (DESIGN.md section 2)
// intentionally isn't implemented here — it needs both parties' public
// keys, so it belongs in the verification/handshake module, not
// identity generation.

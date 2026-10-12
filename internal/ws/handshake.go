package ws

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
	"golang.org/x/crypto/nacl/box"

	"github.com/HomieB-tt/synq/internal/crypto"
)

// EphemeralKeypair is the per-launch X25519 keypair described in
// DESIGN.md section 3. Exactly one should exist per process lifetime
// (Client.Connect generates it once, in NewClient, not per connection
// attempt) and it's reused for every handshake with every contact
// during that run. It is never persisted and never regenerated mid-
// session - regenerating it on reconnect, or per contact, would defeat
// the point: DESIGN.md calls for per-*launch* granularity specifically
// so that losing a static key later can't retroactively decrypt a
// session whose ephemeral key is already gone, which only holds if the
// ephemeral key's lifetime is tied to the process, not to any one
// connection or conversation.
type EphemeralKeypair struct {
	Public  *[32]byte
	Private *[32]byte
}

// NewEphemeralKeypair generates a fresh X25519 keypair.
func NewEphemeralKeypair() (*EphemeralKeypair, error) {
	pub, priv, err := box.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("ws: generate ephemeral keypair: %w", err)
	}
	return &EphemeralKeypair{Public: pub, Private: priv}, nil
}

// sessionKeyInfo domain-separates this HKDF use from any other use of
// HKDF Synq's crypto surface might ever add. There's only this one
// today (TECH_STACK.md), but tying the derivation to a fixed context
// string costs nothing and doesn't depend on that staying true.
//
// "v2" matches handshakeVersion 2 (see payload.go): the derivation itself
// is unchanged from v1, but the label is bumped so a root key can never
// coincide with one from the old protocol.
var sessionKeyInfo = []byte("synq-session-v2")

// DeriveSessionKey computes the shared per-session message-encryption
// key for a contact, per the X3DH-lite construction in DESIGN.md
// section 3: two Diffie-Hellman outputs - one combining each side's
// ephemeral key with the other's static key - fed through HKDF.
//
// DESIGN.md phrases the combination as "DH(ephemeral_local,
// static_remote) + DH(static_local, ephemeral_remote)", but taken
// literally and independently by both sides that phrasing doesn't
// actually converge: each of the two DH outputs is identical no matter
// which side computes it (that's ordinary Diffie-Hellman symmetry -
// DH(a_priv, b_pub) always equals DH(b_priv, a_pub) for a matching
// keypair), but "local" and "remote" swap meaning depending which
// party is asking, so each side lands on the *same two values in
// opposite concatenation order* - and HKDF, like any KDF, produces an
// unrelated key for swapped input order. Left as DESIGN.md states it,
// the two honest parties in a handshake would derive two different
// keys and never manage to decrypt each other's messages.
//
// This function fixes that by picking a canonical order instead of a
// perspective-dependent one: whichever party's static public key
// sorts first (byte-wise) always contributes its DH output first, and
// the other party's second. Both sides already know both static keys
// - that's what TOFU pinning (section 2) is for - so both sides always
// agree on that ordering regardless of who's "local". See
// TestDeriveSessionKeyIsSymmetric, which fails if this ever regresses.
func DeriveSessionKey(local *crypto.Identity, localEphemeral *EphemeralKeypair, remoteStaticPub, remoteEphemeralPub *[32]byte) (*[32]byte, error) {
	if local == nil || localEphemeral == nil || remoteStaticPub == nil || remoteEphemeralPub == nil {
		return nil, errors.New("ws: DeriveSessionKey: nil key material")
	}

	ephLocalStaticRemote, err := x25519(localEphemeral.Private, remoteStaticPub)
	if err != nil {
		return nil, fmt.Errorf("ws: DeriveSessionKey: DH(ephemeral_local, static_remote): %w", err)
	}
	staticLocalEphRemote, err := x25519(local.BoxPrivate, remoteEphemeralPub)
	if err != nil {
		return nil, fmt.Errorf("ws: DeriveSessionKey: DH(static_local, ephemeral_remote): %w", err)
	}

	first, second := ephLocalStaticRemote, staticLocalEphRemote
	if bytes.Compare(local.BoxPublic[:], remoteStaticPub[:]) > 0 {
		first, second = second, first
	}

	ikm := make([]byte, 0, 64)
	ikm = append(ikm, first[:]...)
	ikm = append(ikm, second[:]...)

	kdf := hkdf.New(sha256.New, ikm, nil, sessionKeyInfo)
	var sessionKey [32]byte
	if _, err := io.ReadFull(kdf, sessionKey[:]); err != nil {
		return nil, fmt.Errorf("ws: DeriveSessionKey: %w", err)
	}
	return &sessionKey, nil
}

// x25519 performs the raw X25519 scalar multiplication and rejects a
// low-order point rather than silently returning the resulting
// all-zero, session-key-compromising shared secret - see
// curve25519.X25519's doc comment, which is precisely why this uses
// that function instead of the deprecated curve25519.ScalarMult.
func x25519(priv, pub *[32]byte) (*[32]byte, error) {
	out, err := curve25519.X25519(priv[:], pub[:])
	if err != nil {
		return nil, err
	}
	var result [32]byte
	copy(result[:], out)
	return &result, nil
}

// directionInfoPrefix domain-separates the per-direction subkeys from the
// root key's own derivation above.
const directionInfoPrefix = "synq-session-v2/direction/"

// DirectionalKeys splits a root session key (DeriveSessionKey's output)
// into one key per direction of travel: send, to seal what this client
// transmits, and recv, to open what the contact transmits.
//
// Using one shared key for both directions - as the root key alone would
// - lets a relay reflect a message back at its own sender and have it
// authenticate, because each party can open anything sealed under the
// key the other party seals with. With separate keys, a reflected
// message was sealed under our send key and we only ever try our recv
// key, so it fails authentication.
//
// Both parties must agree which key is which without negotiating, so the
// directions are named by a canonical order both already know: whichever
// static public key sorts first (byte-wise) is "low", the other "high".
// The key for the low-to-high direction is therefore the same value no
// matter which party computes it - the sender's send key equals the
// receiver's recv key. Each subkey is also bound to both static keys, so
// it can't be carried over to a different pair of contacts.
func DirectionalKeys(root, localStatic, remoteStatic *[32]byte) (send, recv *[32]byte, err error) {
	if root == nil || localStatic == nil || remoteStatic == nil {
		return nil, nil, errors.New("ws: DirectionalKeys: nil key material")
	}

	low, high := localStatic, remoteStatic
	localIsLow := bytes.Compare(localStatic[:], remoteStatic[:]) <= 0
	if !localIsLow {
		low, high = remoteStatic, localStatic
	}

	derive := func(label string) (*[32]byte, error) {
		info := make([]byte, 0, len(directionInfoPrefix)+len(label)+64)
		info = append(info, directionInfoPrefix...)
		info = append(info, label...)
		info = append(info, low[:]...)
		info = append(info, high[:]...)

		var key [32]byte
		if _, err := io.ReadFull(hkdf.Expand(sha256.New, root[:], info), key[:]); err != nil {
			return nil, fmt.Errorf("ws: DirectionalKeys: %w", err)
		}
		return &key, nil
	}

	lowToHigh, err := derive("low-to-high")
	if err != nil {
		return nil, nil, err
	}
	highToLow, err := derive("high-to-low")
	if err != nil {
		return nil, nil, err
	}

	if localIsLow {
		return lowToHigh, highToLow, nil
	}
	return highToLow, lowToHigh, nil
}

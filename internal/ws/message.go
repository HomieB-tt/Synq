package ws

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/nacl/secretbox"
)

// nonceSize is secretbox's fixed nonce length (24 bytes, XSalsa20's
// nonce size) - large enough that a fresh random nonce per message
// carries negligible collision risk, which is the approach this file
// uses rather than a counter (see sealNonce).
const nonceSize = 24

// ErrMessageAuthFailed means OpenMessage's authentication check
// failed: the ciphertext was tampered with, truncated, or (most
// likely in practice) encrypted under a different session key than
// the one passed in - e.g. a stale key left over from a previous
// handshake. It deliberately carries no more detail than that:
// secretbox's own design goal is that a failed authentication check
// shouldn't leak *why* it failed.
var ErrMessageAuthFailed = errors.New("ws: message authentication failed")

// SealMessage encrypts and authenticates plaintext under sessionKey
// (as produced by DeriveSessionKey), per DESIGN.md section 3 ("That
// session key, not the static key, encrypts message content for the
// session via nacl/secretbox") and TECH_STACK.md's matching entry.
//
// The returned blob is a fresh random nonce followed by the
// ciphertext (nonce || secretbox-sealed-message) - the nonce isn't
// secret, so it travels alongside the ciphertext rather than needing
// separate transport, and OpenMessage expects exactly this layout.
// plaintext is not retained or mutated.
func SealMessage(sessionKey *[32]byte, plaintext []byte) ([]byte, error) {
	if sessionKey == nil {
		return nil, errors.New("ws: SealMessage: nil session key")
	}

	var nonce [nonceSize]byte
	if _, err := io.ReadFull(rand.Reader, nonce[:]); err != nil {
		return nil, fmt.Errorf("ws: SealMessage: generate nonce: %w", err)
	}

	// Seal appends the ciphertext to its first argument and returns
	// the result, so starting from nonce[:] produces nonce||ciphertext
	// in one call - the standard nacl/secretbox idiom for a
	// self-contained sealed message.
	sealed := secretbox.Seal(nonce[:], plaintext, &nonce, sessionKey)
	return sealed, nil
}

// OpenMessage reverses SealMessage: it splits sealed's leading nonce
// from its ciphertext and authenticates/decrypts the rest under
// sessionKey. Returns ErrMessageAuthFailed if sealed is too short to
// contain a nonce plus secretbox's overhead, or if authentication
// fails for any reason.
func OpenMessage(sessionKey *[32]byte, sealed []byte) ([]byte, error) {
	if sessionKey == nil {
		return nil, errors.New("ws: OpenMessage: nil session key")
	}
	if len(sealed) < nonceSize+secretbox.Overhead {
		return nil, ErrMessageAuthFailed
	}

	var nonce [nonceSize]byte
	copy(nonce[:], sealed[:nonceSize])
	ciphertext := sealed[nonceSize:]

	plaintext, ok := secretbox.Open(nil, ciphertext, &nonce, sessionKey)
	if !ok {
		return nil, ErrMessageAuthFailed
	}
	return plaintext, nil
}

// counterSize is the length of the message counter SealCounted places in
// front of the plaintext: a big-endian uint64.
const counterSize = 8

// ErrMessageMalformed means a message authenticated correctly but is too
// short to hold the counter every sealed message carries - a peer
// speaking a different version of the protocol, not tampering (tampering
// fails authentication first).
var ErrMessageMalformed = errors.New("ws: sealed message has no counter")

// SealCounted seals plaintext under sendKey - one of the two directional
// keys from DirectionalKeys - with counter bound inside the
// authenticated plaintext (counter || plaintext). The receiver recovers
// it with OpenCounted and refuses any counter it has already accepted
// (see ReplayWindow), which is what stops a relay from re-delivering a
// recorded message. Because the counter is inside the sealed bytes, it
// can't be changed or removed without failing authentication.
//
// The nonce is still a fresh random one per message (see SealMessage):
// the counter provides ordering and replay detection, not nonce
// uniqueness, so a counter that ever repeated could not cause nonce
// reuse.
func SealCounted(sendKey *[32]byte, counter uint64, plaintext []byte) ([]byte, error) {
	buf := make([]byte, counterSize+len(plaintext))
	binary.BigEndian.PutUint64(buf, counter)
	copy(buf[counterSize:], plaintext)
	return SealMessage(sendKey, buf)
}

// OpenCounted reverses SealCounted: it authenticates and decrypts sealed
// under recvKey and returns the message counter and the plaintext. It
// does not itself check the counter for freshness - the caller must pass
// it to a ReplayWindow, and only after this call has succeeded, so that
// forged frames can't move the window.
//
// recvKey must be the OTHER direction's key from the one this client
// seals with. That is what defeats reflection: a relay that sends our own
// ciphertext back to us as if the contact had written it fails
// authentication here, because it was sealed under our send key, not our
// receive key.
func OpenCounted(recvKey *[32]byte, sealed []byte) (uint64, []byte, error) {
	buf, err := OpenMessage(recvKey, sealed)
	if err != nil {
		return 0, nil, err
	}
	if len(buf) < counterSize {
		return 0, nil, ErrMessageMalformed
	}
	return binary.BigEndian.Uint64(buf[:counterSize]), buf[counterSize:], nil
}

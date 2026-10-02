package ws

import (
	"crypto/rand"
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

package crypto

import (
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
)

// A user's X25519 box key is what messages to them are encrypted to, but
// it reaches other users through synq-server, which is not trusted. The
// safety-number fingerprint (Fingerprint) covers only the Ed25519
// identity keys, so on its own it would not notice a server swapping in a
// box key of its own. This binding closes that gap: the identity key
// signs the box key (together with the username), and clients verify the
// signature before pinning a contact or encrypting to them. The server
// can't produce one for a key it substituted.
//
// The signed bytes are laid out identically on the server
// (src/auth/boxkey.rs); both sides pin the layout in a test.

const boxKeyBindingDomain = "synq-box-key-v1\x00"

var (
	// ErrBoxKeyUnsigned means no binding signature was supplied. Servers
	// leave it out for accounts that predate the feature and haven't
	// uploaded one yet.
	ErrBoxKeyUnsigned = errors.New("crypto: box key has no signature binding it to the identity key")

	// ErrBoxKeyBindingInvalid means the signature is present but doesn't
	// verify: the box key was not signed by this identity key for this
	// username.
	ErrBoxKeyBindingInvalid = errors.New("crypto: box key signature does not verify against the identity key")
)

// BoxKeyBindingMessage returns the exact bytes the identity key signs:
// a domain tag (so a signature made for another purpose, such as an auth
// challenge, can never be passed off as a binding), the username (so the
// binding can't be lifted onto another account), a NUL separator, and the
// raw 32-byte box key.
func BoxKeyBindingMessage(username string, boxPub *[32]byte) []byte {
	msg := make([]byte, 0, len(boxKeyBindingDomain)+len(username)+1+32)
	msg = append(msg, boxKeyBindingDomain...)
	msg = append(msg, username...)
	msg = append(msg, 0)
	return append(msg, boxPub[:]...)
}

// SignBoxKey signs the binding of this identity's box key to username and
// returns the signature hex-encoded, in the form synq-server expects as
// box_key_sig.
func (id *Identity) SignBoxKey(username string) string {
	sig := ed25519.Sign(id.SigningPrivate, BoxKeyBindingMessage(username, id.BoxPublic))
	return hex.EncodeToString(sig)
}

// VerifyBoxKeyBinding checks that sigHex is signingPubHex's signature
// over the binding of boxPubHex to username. All three keys/signatures
// are hex, as the server serves them. It returns ErrBoxKeyUnsigned for an
// empty signature and ErrBoxKeyBindingInvalid for anything that doesn't
// verify, including malformed input - a caller has no use for telling
// "garbled" from "forged".
func VerifyBoxKeyBinding(signingPubHex, username, boxPubHex, sigHex string) error {
	if sigHex == "" {
		return ErrBoxKeyUnsigned
	}
	signingPub, err := hex.DecodeString(signingPubHex)
	if err != nil || len(signingPub) != ed25519.PublicKeySize {
		return fmt.Errorf("%w: bad identity key", ErrBoxKeyBindingInvalid)
	}
	boxRaw, err := hex.DecodeString(boxPubHex)
	if err != nil || len(boxRaw) != 32 {
		return fmt.Errorf("%w: bad box key", ErrBoxKeyBindingInvalid)
	}
	sig, err := hex.DecodeString(sigHex)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("%w: bad signature encoding", ErrBoxKeyBindingInvalid)
	}
	var box [32]byte
	copy(box[:], boxRaw)
	if !ed25519.Verify(ed25519.PublicKey(signingPub), BoxKeyBindingMessage(username, &box), sig) {
		return ErrBoxKeyBindingInvalid
	}
	return nil
}

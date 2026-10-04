package crypto

import (
	"crypto/ed25519"
	"encoding/hex"
)

// SignChallenge signs an auth challenge exactly the way synq-server
// expects it (see synq-server's API.md, "Authentication"): over the
// UTF-8 bytes of the challenge string itself, as received in the
// server's JSON response - NOT the hex-decoded raw bytes the
// challenge string represents. Returns the signature, hex-encoded,
// ready to drop into a register/verify or login/verify request body.
func (id *Identity) SignChallenge(challenge string) string {
	sig := ed25519.Sign(id.SigningPrivate, []byte(challenge))
	return hex.EncodeToString(sig)
}

// SigningPublicHex hex-encodes the Ed25519 identity key, in the form
// synq-server's registration endpoint expects as pub_key.
func (id *Identity) SigningPublicHex() string {
	return hex.EncodeToString(id.SigningPublic)
}

// BoxPublicHex hex-encodes the X25519 key, in the form synq-server's
// registration endpoint expects as box_pub_key (see
// synq-server-DESIGN.md section 1 and migrations/0005_box_pub_key.sql
// on why the server needs this at all, despite never using it itself:
// it only ever stores and re-serves it, for other clients' E2EE).
func (id *Identity) BoxPublicHex() string {
	return hex.EncodeToString(id.BoxPublic[:])
}

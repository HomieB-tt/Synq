package ws

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
)

// A frame's payload is the only bytes on the wire this client fully
// controls - synq-server relays them untouched (see envelope.go) - so
// it carries a one-byte kind tag followed by that kind's body:
//
//	0x01 handshake | JSON {"v":1,"eph":"<base64 32-byte X25519 pub>"}
//	0x02 sealed    | nacl/secretbox blob, exactly as SealMessage
//	                       produced it (nonce || ciphertext)
//
// The tag exists because the server can't tell the two apart either
// (it must not try) and the receiver otherwise couldn't: a sealed blob
// is 56+ random bytes whose first byte could be anything, so
// distinguishing "key exchange" from "message" by inspecting the body
// would be guesswork. Tagging *both* kinds removes the ambiguity - a
// sealed body is only ever read after its 0x02 tag, and a handshake
// body only ever after its 0x01.
//
// Nothing here authenticates anything: the handshake frame says "this
// is my ephemeral public key" and the server could swap it. That's
// deliberate (DESIGN.md §2 answers key substitution with TOFU pinning
// at lookup time, not with per-frame signatures), and swapping a key
// buys an attacker nothing: DeriveSessionKey mixes the peer's static
// key in as well, so a substituted ephemeral yields a key only the
// substituter and the intended recipient share a *different* view of -
// messages simply fail to decrypt rather than leaking. The cost is
// availability, which a malicious server already controls outright by
// dropping frames.
type PayloadKind byte

const (
	// PayloadKindHandshake carries one side's per-launch ephemeral
	// public key (DESIGN.md §3).
	PayloadKindHandshake PayloadKind = 0x01
	// PayloadKindSealed carries an already-encrypted message body.
	PayloadKindSealed PayloadKind = 0x02
)

// handshakeVersion is the only version this client writes or accepts.
// Bumping it means the frame shape changed, so an older peer that
// can't read the new one must be able to tell that apart from a
// corrupt frame.
const handshakeVersion = 1

// handshakeFrame is the JSON body behind PayloadKindHandshake.
type handshakeFrame struct {
	Version   int    `json:"v"`
	Ephemeral string `json:"eph"`
}

// EncodeHandshake builds a handshake payload carrying ephemeral, this
// client's per-launch X25519 public key. The result is meant for
// EncodeSend like any other payload.
func EncodeHandshake(ephemeral *[32]byte) ([]byte, error) {
	if ephemeral == nil {
		return nil, errors.New("ws: EncodeHandshake: nil ephemeral key")
	}
	body, err := json.Marshal(handshakeFrame{
		Version:   handshakeVersion,
		Ephemeral: base64.StdEncoding.EncodeToString(ephemeral[:]),
	})
	if err != nil {
		return nil, fmt.Errorf("ws: EncodeHandshake: %w", err)
	}
	return append([]byte{byte(PayloadKindHandshake)}, body...), nil
}

// EncodeSealed tags SealMessage's output as a message payload. The
// prefixing is the only thing this adds - sealing itself is
// SealMessage's job - but doing it here keeps every frame's kind tag
// written in one place, next to the code that reads it back.
func EncodeSealed(sealed []byte) []byte {
	out := make([]byte, 0, len(sealed)+1)
	out = append(out, byte(PayloadKindSealed))
	return append(out, sealed...)
}

// DecodePayload splits a relayed frame's payload into its kind and
// that kind's body: the JSON for PayloadKindHandshake (hand that to
// ParseHandshake) or the secretbox blob for PayloadKindSealed (hand
// that to OpenMessage). A payload that is empty, tagged with a kind
// this client has never heard of, or too short to contain a body is an
// error - it means the sender is speaking a protocol we don't share,
// which is worth reporting rather than guessing at.
func DecodePayload(payload []byte) (PayloadKind, []byte, error) {
	if len(payload) == 0 {
		return 0, nil, errors.New("ws: empty payload")
	}
	kind := PayloadKind(payload[0])
	switch kind {
	case PayloadKindHandshake, PayloadKindSealed:
		if len(payload) == 1 {
			return 0, nil, fmt.Errorf("ws: payload kind %#x has no body", byte(kind))
		}
		return kind, payload[1:], nil
	default:
		return 0, nil, fmt.Errorf("ws: unknown payload kind %#x", byte(kind))
	}
}

// ParseHandshake reads the ephemeral public key out of a
// PayloadKindHandshake body produced by EncodeHandshake. The version
// is checked rather than ignored: a peer speaking a future version
// sent something we can't interpret, and treating that as a valid key
// (or as garbage) would both be wrong.
func ParseHandshake(body []byte) (*[32]byte, error) {
	var f handshakeFrame
	if err := json.Unmarshal(body, &f); err != nil {
		return nil, fmt.Errorf("ws: parse handshake: %w", err)
	}
	if f.Version != handshakeVersion {
		return nil, fmt.Errorf("ws: unsupported handshake version %d (want %d)", f.Version, handshakeVersion)
	}
	raw, err := base64.StdEncoding.DecodeString(f.Ephemeral)
	if err != nil {
		return nil, fmt.Errorf("ws: handshake ephemeral key is not valid base64: %w", err)
	}
	if len(raw) != 32 {
		return nil, fmt.Errorf("ws: handshake ephemeral key is %d bytes, want 32", len(raw))
	}
	var key [32]byte
	copy(key[:], raw)
	return &key, nil
}

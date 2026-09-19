package crypto

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"fmt"
	"strings"
)

// Fingerprint computes a short, human-comparable fingerprint from two
// parties' long-term Ed25519 identity public keys, for the `:verify`
// safety-number command described in DESIGN.md section 2.
//
// Both parties get the SAME fingerprint regardless of which one is
// "self" vs "other" - the two keys are sorted into a canonical order
// before hashing, so the result doesn't depend on call order.
//
// The output is GPG-style hex, not Signal-style decimal digits: this
// is a developer tool, and hex key fingerprints are the format this
// audience already recognizes from `gpg --fingerprint` and SSH host
// keys.
func Fingerprint(a, b ed25519.PublicKey) string {
	first, second := a, b
	if bytes.Compare(first, second) > 0 {
		first, second = second, first
	}

	h := sha256.New()
	h.Write(first)
	h.Write(second)
	sum := h.Sum(nil)

	// 20 bytes (40 hex chars / 10 groups of 4) - long enough that
	// hunting for a colliding keypair is impractical, short enough
	// for two people to read aloud and compare over a call.
	hexStr := fmt.Sprintf("%x", sum[:20])

	var groups []string
	for i := 0; i < len(hexStr); i += 4 {
		groups = append(groups, hexStr[i:i+4])
	}
	return strings.ToUpper(strings.Join(groups, " "))
}

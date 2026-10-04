package crypto

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/nacl/secretbox"
)

const (
	saltSize      = 16
	nonceSize     = 24
	derivedKeyLen = 32
	plainSize     = ed25519.SeedSize + 32 // ed25519 seed + x25519 private key
)

// Argon2Params controls the cost parameters used to derive the vault's
// encryption key from a user passphrase. Persisted alongside each vault
// (not secret) so parameters can be strengthened for newly created
// vaults later without invalidating ones created under older settings.
type Argon2Params struct {
	MemoryKiB   uint32
	Iterations  uint32
	Parallelism uint8
}

// DefaultArgon2Params returns interactive-use parameters in line with
// OWASP's current baseline recommendation for Argon2id (as of 2026):
// 64 MiB memory, 3 iterations, 4-way parallelism. These are deliberately
// heavier than the historical "minimum" figures, since Synq only pays
// this cost once per launch (see DESIGN.md section 1), not per request.
func DefaultArgon2Params() Argon2Params {
	return Argon2Params{
		MemoryKiB:   64 * 1024,
		Iterations:  3,
		Parallelism: 4,
	}
}

// EncryptedVault is the on-disk representation of a passphrase-protected
// identity: everything needed to decrypt it, except the passphrase
// itself, which is never stored anywhere. See DESIGN.md section 1.
type EncryptedVault struct {
	Salt       []byte
	Params     Argon2Params
	Nonce      []byte
	Ciphertext []byte
}

// SealIdentity encrypts an identity's secret key material - and,
// optionally, a synq-server refresh token - under a key derived from
// passphrase, using a freshly generated random salt and nonce.
//
// refreshToken may be empty, for an identity that has never registered
// a username with synq-server yet (see internal/app's registration
// flow). It's sealed inside the same ciphertext as the identity keys,
// not stored separately in plaintext (e.g. in db.KeyStore's
// preferences table): unlike a theme choice, a refresh token is a real
// bearer credential - anyone holding it can act as this identity
// against the server for as long as it remains valid - so it gets the
// same protection as the identity keys themselves, under the same
// passphrase.
//
// There is no recovery path if the passphrase is lost — that is a
// deliberate consequence of Synq's zero-knowledge design (DESIGN.md
// section 1), not an oversight, and callers presenting this to a user
// for the first time should say so explicitly.
func SealIdentity(id *Identity, refreshToken string, passphrase []byte, params Argon2Params) (*EncryptedVault, error) {
	if len(passphrase) == 0 {
		return nil, errors.New("crypto: passphrase must not be empty")
	}

	salt := make([]byte, saltSize)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("generate salt: %w", err)
	}

	key := deriveKey(passphrase, salt, params)
	defer zero(key)
	var keyArr [derivedKeyLen]byte
	copy(keyArr[:], key)
	defer zero(keyArr[:])

	var nonce [nonceSize]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}

	// No length prefix needed for the trailing refresh token: secretbox
	// decrypts back to the exact original plaintext in one piece, so
	// OpenIdentity already knows the total length and can just slice
	// off everything after the fixed-size key material - see there.
	plaintext := make([]byte, 0, plainSize+len(refreshToken))
	plaintext = append(plaintext, id.seed()...)
	plaintext = append(plaintext, id.BoxPrivate[:]...)
	plaintext = append(plaintext, refreshToken...)
	defer zero(plaintext)

	ciphertext := secretbox.Seal(nil, plaintext, &nonce, &keyArr)

	return &EncryptedVault{
		Salt:       salt,
		Params:     params,
		Nonce:      nonce[:],
		Ciphertext: ciphertext,
	}, nil
}

// OpenIdentity decrypts a vault with the given passphrase, reconstructs
// the identity, and returns whatever refresh token was sealed
// alongside it (see SealIdentity) - empty if none was, including for
// every vault sealed before this field existed: those are exactly
// plainSize bytes of plaintext with nothing trailing, which this
// parses as "no refresh token" rather than a format error, so
// existing identities keep opening normally after this change.
//
// Any failure — wrong passphrase, corrupted data, tampered ciphertext —
// returns the same generic error. Callers must not try to distinguish
// these cases for the user; doing so (e.g. "salt looked fine but
// decryption failed" vs "malformed vault") can leak information useful
// to an attacker probing the vault format.
func OpenIdentity(vault *EncryptedVault, passphrase []byte) (*Identity, string, error) {
	const errMsg = "crypto: incorrect passphrase or corrupted vault"

	if len(vault.Nonce) != nonceSize {
		return nil, "", errors.New(errMsg)
	}

	key := deriveKey(passphrase, vault.Salt, vault.Params)
	defer zero(key)
	var keyArr [derivedKeyLen]byte
	copy(keyArr[:], key)
	defer zero(keyArr[:])

	var nonce [nonceSize]byte
	copy(nonce[:], vault.Nonce)

	plaintext, ok := secretbox.Open(nil, vault.Ciphertext, &nonce, &keyArr)
	if !ok {
		return nil, "", errors.New(errMsg)
	}
	defer zero(plaintext)

	if len(plaintext) < plainSize {
		return nil, "", errors.New(errMsg)
	}

	edSeed := make([]byte, ed25519.SeedSize)
	copy(edSeed, plaintext[:ed25519.SeedSize])

	var boxPriv [32]byte
	copy(boxPriv[:], plaintext[ed25519.SeedSize:plainSize])

	refreshToken := string(plaintext[plainSize:])

	id, err := identityFromSeeds(edSeed, &boxPriv)
	if err != nil {
		return nil, "", err
	}
	return id, refreshToken, nil
}

func deriveKey(passphrase, salt []byte, p Argon2Params) []byte {
	return argon2.IDKey(passphrase, salt, p.Iterations, p.MemoryKiB, p.Parallelism, derivedKeyLen)
}

// zero overwrites a byte slice with zeros. This is a best-effort
// defense against secret material lingering in memory longer than
// necessary — Go's garbage collector and compiler optimizations mean
// it is not a hard guarantee (a copy could exist elsewhere), but it
// costs nothing and narrows the window regardless.
func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// constantTimeEqual is exposed for callers (e.g. the future `:verify`
// fingerprint comparison) that need to compare secret-derived byte
// strings without leaking timing information.
func constantTimeEqual(a, b []byte) bool {
	return subtle.ConstantTimeCompare(a, b) == 1
}

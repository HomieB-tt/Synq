package crypto

import (
	"bytes"
	"testing"
)

// fastParams keeps tests quick — production uses DefaultArgon2Params(),
// which is deliberately much more expensive.
func fastParams() Argon2Params {
	return Argon2Params{MemoryKiB: 8 * 1024, Iterations: 1, Parallelism: 1}
}

func TestGenerateIdentityProducesUsableKeys(t *testing.T) {
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	if len(id.SigningPublic) == 0 || len(id.SigningPrivate) == 0 {
		t.Fatal("signing keys are empty")
	}
	if id.BoxPublic == nil || id.BoxPrivate == nil {
		t.Fatal("box keys are nil")
	}
}

func TestSealOpenRoundTrip(t *testing.T) {
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}

	passphrase := []byte("correct horse battery staple")
	vault, err := SealIdentity(id, "", passphrase, fastParams())
	if err != nil {
		t.Fatalf("SealIdentity: %v", err)
	}

	opened, refreshToken, err := OpenIdentity(vault, passphrase)
	if err != nil {
		t.Fatalf("OpenIdentity: %v", err)
	}

	if !bytes.Equal(id.SigningPrivate, opened.SigningPrivate) {
		t.Error("signing private key did not round-trip")
	}
	if !bytes.Equal(id.SigningPublic, opened.SigningPublic) {
		t.Error("signing public key did not round-trip")
	}
	if *id.BoxPrivate != *opened.BoxPrivate {
		t.Error("box private key did not round-trip")
	}
	if *id.BoxPublic != *opened.BoxPublic {
		t.Error("box public key did not round-trip")
	}
	if refreshToken != "" {
		t.Errorf("refreshToken = %q, want empty (none was sealed)", refreshToken)
	}
}

// TestSealOpenRoundTripWithRefreshToken is the load-bearing test for
// why SealIdentity/OpenIdentity gained a refreshToken parameter at
// all: a synq-server refresh token must survive being sealed inside
// the vault and read back out correctly, byte for byte, alongside the
// identity keys it shares ciphertext with.
func TestSealOpenRoundTripWithRefreshToken(t *testing.T) {
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	passphrase := []byte("correct horse battery staple")
	want := "rt_abcdef0123456789_a-real-looking-refresh-token"

	vault, err := SealIdentity(id, want, passphrase, fastParams())
	if err != nil {
		t.Fatalf("SealIdentity: %v", err)
	}

	_, got, err := OpenIdentity(vault, passphrase)
	if err != nil {
		t.Fatalf("OpenIdentity: %v", err)
	}
	if got != want {
		t.Errorf("refreshToken = %q, want %q", got, want)
	}
}

// TestOpenIdentityOnPreRefreshTokenVaultReturnsEmptyToken simulates a
// vault sealed before refreshToken existed as a concept: sealing with
// "" produces byte-for-byte what the old SealIdentity(id, passphrase,
// params) signature always produced (no trailing data appended to the
// plaintext at all), so this is the real backward-compatibility
// guarantee, not just a convention - an identity created before this
// field was added must keep opening normally, with no refresh token,
// rather than erroring as if the vault were corrupted.
func TestOpenIdentityOnPreRefreshTokenVaultReturnsEmptyToken(t *testing.T) {
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	passphrase := []byte("correct horse battery staple")

	vault, err := SealIdentity(id, "", passphrase, fastParams())
	if err != nil {
		t.Fatalf("SealIdentity: %v", err)
	}

	opened, refreshToken, err := OpenIdentity(vault, passphrase)
	if err != nil {
		t.Fatalf("OpenIdentity: %v", err)
	}
	if opened == nil {
		t.Fatal("identity is nil")
	}
	if refreshToken != "" {
		t.Errorf("refreshToken = %q, want empty", refreshToken)
	}
}

func TestOpenWithWrongPassphraseFails(t *testing.T) {
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}

	vault, err := SealIdentity(id, "", []byte("correct horse battery staple"), fastParams())
	if err != nil {
		t.Fatalf("SealIdentity: %v", err)
	}

	if _, _, err := OpenIdentity(vault, []byte("wrong passphrase")); err == nil {
		t.Fatal("expected error opening vault with wrong passphrase, got nil")
	}
}

func TestSealProducesDifferentCiphertextEachTime(t *testing.T) {
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	passphrase := []byte("correct horse battery staple")

	v1, err := SealIdentity(id, "", passphrase, fastParams())
	if err != nil {
		t.Fatalf("SealIdentity (1): %v", err)
	}
	v2, err := SealIdentity(id, "", passphrase, fastParams())
	if err != nil {
		t.Fatalf("SealIdentity (2): %v", err)
	}

	if bytes.Equal(v1.Salt, v2.Salt) {
		t.Error("salt reused across separate SealIdentity calls")
	}
	if bytes.Equal(v1.Nonce, v2.Nonce) {
		t.Error("nonce reused across separate SealIdentity calls")
	}
	if bytes.Equal(v1.Ciphertext, v2.Ciphertext) {
		t.Error("identical ciphertext produced from two separate seals (salt/nonce reuse?)")
	}
}

func TestSealRejectsEmptyPassphrase(t *testing.T) {
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	if _, err := SealIdentity(id, "", []byte(""), fastParams()); err == nil {
		t.Fatal("expected error sealing with empty passphrase, got nil")
	}
}

func TestOpenRejectsCorruptedCiphertext(t *testing.T) {
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	passphrase := []byte("correct horse battery staple")
	vault, err := SealIdentity(id, "", passphrase, fastParams())
	if err != nil {
		t.Fatalf("SealIdentity: %v", err)
	}

	// Flip a byte in the middle of the ciphertext.
	vault.Ciphertext[len(vault.Ciphertext)/2] ^= 0xFF

	if _, _, err := OpenIdentity(vault, passphrase); err == nil {
		t.Fatal("expected error opening tampered vault, got nil")
	}
}

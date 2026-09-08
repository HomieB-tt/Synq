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
	vault, err := SealIdentity(id, passphrase, fastParams())
	if err != nil {
		t.Fatalf("SealIdentity: %v", err)
	}

	opened, err := OpenIdentity(vault, passphrase)
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
}

func TestOpenWithWrongPassphraseFails(t *testing.T) {
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}

	vault, err := SealIdentity(id, []byte("correct horse battery staple"), fastParams())
	if err != nil {
		t.Fatalf("SealIdentity: %v", err)
	}

	if _, err := OpenIdentity(vault, []byte("wrong passphrase")); err == nil {
		t.Fatal("expected error opening vault with wrong passphrase, got nil")
	}
}

func TestSealProducesDifferentCiphertextEachTime(t *testing.T) {
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	passphrase := []byte("correct horse battery staple")

	v1, err := SealIdentity(id, passphrase, fastParams())
	if err != nil {
		t.Fatalf("SealIdentity (1): %v", err)
	}
	v2, err := SealIdentity(id, passphrase, fastParams())
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
	if _, err := SealIdentity(id, []byte(""), fastParams()); err == nil {
		t.Fatal("expected error sealing with empty passphrase, got nil")
	}
}

func TestOpenRejectsCorruptedCiphertext(t *testing.T) {
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	passphrase := []byte("correct horse battery staple")
	vault, err := SealIdentity(id, passphrase, fastParams())
	if err != nil {
		t.Fatalf("SealIdentity: %v", err)
	}

	// Flip a byte in the middle of the ciphertext.
	vault.Ciphertext[len(vault.Ciphertext)/2] ^= 0xFF

	if _, err := OpenIdentity(vault, passphrase); err == nil {
		t.Fatal("expected error opening tampered vault, got nil")
	}
}

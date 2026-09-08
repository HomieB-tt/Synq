package db

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"

	"github.com/HomieB-tt/synq/internal/crypto"
)

func openTestStore(t *testing.T) *KeyStore {
	t.Helper()
	dir := t.TempDir()
	ks, err := OpenKeyStore(filepath.Join(dir, "synq-test.db"))
	if err != nil {
		t.Fatalf("OpenKeyStore: %v", err)
	}
	t.Cleanup(func() { ks.Close() })
	return ks
}

func TestHasIdentityFalseOnFreshStore(t *testing.T) {
	ks := openTestStore(t)

	has, err := ks.HasIdentity()
	if err != nil {
		t.Fatalf("HasIdentity: %v", err)
	}
	if has {
		t.Error("expected no identity on a freshly created store")
	}
}

func TestLoadReturnsErrNoIdentityOnFreshStore(t *testing.T) {
	ks := openTestStore(t)

	_, err := ks.Load()
	if !errors.Is(err, ErrNoIdentity) {
		t.Fatalf("expected ErrNoIdentity, got %v", err)
	}
}

func TestSaveThenLoadRoundTrip(t *testing.T) {
	ks := openTestStore(t)

	id, err := crypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	params := crypto.Argon2Params{MemoryKiB: 8 * 1024, Iterations: 1, Parallelism: 1}
	vault, err := crypto.SealIdentity(id, []byte("test passphrase"), params)
	if err != nil {
		t.Fatalf("SealIdentity: %v", err)
	}

	if err := ks.Save(vault); err != nil {
		t.Fatalf("Save: %v", err)
	}

	has, err := ks.HasIdentity()
	if err != nil {
		t.Fatalf("HasIdentity: %v", err)
	}
	if !has {
		t.Fatal("expected HasIdentity to be true after Save")
	}

	loaded, err := ks.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if !bytes.Equal(vault.Salt, loaded.Salt) {
		t.Error("salt did not round-trip")
	}
	if !bytes.Equal(vault.Nonce, loaded.Nonce) {
		t.Error("nonce did not round-trip")
	}
	if !bytes.Equal(vault.Ciphertext, loaded.Ciphertext) {
		t.Error("ciphertext did not round-trip")
	}
	if vault.Params != loaded.Params {
		t.Errorf("argon2 params did not round-trip: got %+v, want %+v", loaded.Params, vault.Params)
	}

	// And the round-tripped vault should still decrypt correctly end to end.
	opened, err := crypto.OpenIdentity(loaded, []byte("test passphrase"))
	if err != nil {
		t.Fatalf("OpenIdentity on loaded vault: %v", err)
	}
	if !bytes.Equal(id.SigningPrivate, opened.SigningPrivate) {
		t.Error("identity recovered from stored vault does not match original")
	}
}

func TestSaveOverwritesExistingVault(t *testing.T) {
	ks := openTestStore(t)
	params := crypto.Argon2Params{MemoryKiB: 8 * 1024, Iterations: 1, Parallelism: 1}

	id1, _ := crypto.GenerateIdentity()
	vault1, err := crypto.SealIdentity(id1, []byte("first passphrase"), params)
	if err != nil {
		t.Fatalf("SealIdentity (1): %v", err)
	}
	if err := ks.Save(vault1); err != nil {
		t.Fatalf("Save (1): %v", err)
	}

	id2, _ := crypto.GenerateIdentity()
	vault2, err := crypto.SealIdentity(id2, []byte("second passphrase"), params)
	if err != nil {
		t.Fatalf("SealIdentity (2): %v", err)
	}
	if err := ks.Save(vault2); err != nil {
		t.Fatalf("Save (2): %v", err)
	}

	loaded, err := ks.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	opened, err := crypto.OpenIdentity(loaded, []byte("second passphrase"))
	if err != nil {
		t.Fatalf("expected second vault to be the one stored, got error: %v", err)
	}
	if !bytes.Equal(id2.SigningPrivate, opened.SigningPrivate) {
		t.Error("loaded identity does not match the second (overwriting) identity")
	}

	if _, err := crypto.OpenIdentity(loaded, []byte("first passphrase")); err == nil {
		t.Error("expected first passphrase to no longer work after overwrite, but it did")
	}
}

func TestOpenKeyStoreIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "synq-test.db")

	ks1, err := OpenKeyStore(path)
	if err != nil {
		t.Fatalf("OpenKeyStore (1): %v", err)
	}
	id, _ := crypto.GenerateIdentity()
	params := crypto.Argon2Params{MemoryKiB: 8 * 1024, Iterations: 1, Parallelism: 1}
	vault, _ := crypto.SealIdentity(id, []byte("pw"), params)
	if err := ks1.Save(vault); err != nil {
		t.Fatalf("Save: %v", err)
	}
	ks1.Close()

	// Re-opening the same path (e.g. a real second launch of the app)
	// must not error and must see the previously saved vault.
	ks2, err := OpenKeyStore(path)
	if err != nil {
		t.Fatalf("OpenKeyStore (2): %v", err)
	}
	defer ks2.Close()

	has, err := ks2.HasIdentity()
	if err != nil {
		t.Fatalf("HasIdentity: %v", err)
	}
	if !has {
		t.Error("expected identity to persist across a reopen of the same database file")
	}
}

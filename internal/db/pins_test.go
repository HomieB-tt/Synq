package db

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestLoadPinnedKeyNotFound(t *testing.T) {
	ks := openTestStore(t)

	_, err := ks.LoadPinnedKey("alice")
	if !errors.Is(err, ErrPinNotFound) {
		t.Fatalf("expected ErrPinNotFound, got %v", err)
	}
}

func TestPinThenLoadRoundTrip(t *testing.T) {
	ks := openTestStore(t)

	want := PinnedKey{Username: "alice", BoxPubKey: "box-aaa", SigningPubKey: "sign-bbb"}
	if err := ks.PinKey(want); err != nil {
		t.Fatalf("PinKey: %v", err)
	}

	got, err := ks.LoadPinnedKey("alice")
	if err != nil {
		t.Fatalf("LoadPinnedKey: %v", err)
	}
	if got != want {
		t.Errorf("LoadPinnedKey = %+v, want %+v", got, want)
	}
}

func TestPinKeyOverwritesExistingPin(t *testing.T) {
	ks := openTestStore(t)

	if err := ks.PinKey(PinnedKey{Username: "alice", BoxPubKey: "old-box", SigningPubKey: "old-sign"}); err != nil {
		t.Fatalf("PinKey (1): %v", err)
	}
	if err := ks.PinKey(PinnedKey{Username: "alice", BoxPubKey: "new-box", SigningPubKey: "new-sign"}); err != nil {
		t.Fatalf("PinKey (2): %v", err)
	}

	got, err := ks.LoadPinnedKey("alice")
	if err != nil {
		t.Fatalf("LoadPinnedKey: %v", err)
	}
	if got.BoxPubKey != "new-box" || got.SigningPubKey != "new-sign" {
		t.Errorf("LoadPinnedKey = %+v, want the replaced keys", got)
	}
}

func TestPinKeyRequiresUsername(t *testing.T) {
	ks := openTestStore(t)

	if err := ks.PinKey(PinnedKey{BoxPubKey: "box"}); err == nil {
		t.Fatal("PinKey with no username succeeded, want an error")
	}
}

func TestPinsSurviveReopeningTheStore(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "synq-test.db")

	ks, err := OpenKeyStore(path)
	if err != nil {
		t.Fatalf("OpenKeyStore: %v", err)
	}
	if err := ks.PinKey(PinnedKey{Username: "bob", BoxPubKey: "box", SigningPubKey: "sign"}); err != nil {
		t.Fatalf("PinKey: %v", err)
	}
	if err := ks.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := OpenKeyStore(path)
	if err != nil {
		t.Fatalf("OpenKeyStore (reopen): %v", err)
	}
	defer reopened.Close()

	got, err := reopened.LoadPinnedKey("bob")
	if err != nil {
		t.Fatalf("LoadPinnedKey: %v", err)
	}
	if got.BoxPubKey != "box" {
		t.Errorf("LoadPinnedKey = %+v, want the pin written before the close", got)
	}
}

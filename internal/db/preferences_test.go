package db

import (
	"errors"
	"testing"
)

func TestLoadPreferenceNotFound(t *testing.T) {
	ks := openTestStore(t)

	_, err := ks.LoadPreference("theme")
	if !errors.Is(err, ErrPreferenceNotFound) {
		t.Fatalf("expected ErrPreferenceNotFound, got %v", err)
	}
}

func TestSaveThenLoadPreferenceRoundTrip(t *testing.T) {
	ks := openTestStore(t)

	if err := ks.SavePreference(PrefTheme, "nord"); err != nil {
		t.Fatalf("SavePreference: %v", err)
	}

	got, err := ks.LoadPreference(PrefTheme)
	if err != nil {
		t.Fatalf("LoadPreference: %v", err)
	}
	if got != "nord" {
		t.Errorf("got %q, want %q", got, "nord")
	}
}

func TestSavePreferenceOverwrites(t *testing.T) {
	ks := openTestStore(t)

	if err := ks.SavePreference(PrefTheme, "dracula"); err != nil {
		t.Fatalf("SavePreference (1): %v", err)
	}
	if err := ks.SavePreference(PrefTheme, "monokai"); err != nil {
		t.Fatalf("SavePreference (2): %v", err)
	}

	got, err := ks.LoadPreference(PrefTheme)
	if err != nil {
		t.Fatalf("LoadPreference: %v", err)
	}
	if got != "monokai" {
		t.Errorf("got %q, want %q (expected overwrite, not a second row)", got, "monokai")
	}
}

func TestPreferencesAreIndependentOfIdentityVault(t *testing.T) {
	ks := openTestStore(t)

	// Saving a preference before any identity exists must not error,
	// and must not interfere with HasIdentity/Load for the vault.
	if err := ks.SavePreference(PrefTheme, "nord"); err != nil {
		t.Fatalf("SavePreference: %v", err)
	}

	has, err := ks.HasIdentity()
	if err != nil {
		t.Fatalf("HasIdentity: %v", err)
	}
	if has {
		t.Error("saving a preference should not create an identity vault row")
	}
}

func TestSaveThenLoadDisplayNameRoundTrip(t *testing.T) {
	ks := openTestStore(t)

	if err := ks.SavePreference(PrefDisplayName, "Ada Lovelace"); err != nil {
		t.Fatalf("SavePreference: %v", err)
	}

	got, err := ks.LoadPreference(PrefDisplayName)
	if err != nil {
		t.Fatalf("LoadPreference: %v", err)
	}
	if got != "Ada Lovelace" {
		t.Errorf("got %q, want %q", got, "Ada Lovelace")
	}
}

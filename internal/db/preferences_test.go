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

func TestSaveThenLoadUsernameRoundTrip(t *testing.T) {
	ks := openTestStore(t)

	if err := ks.SavePreference(PrefUsername, "ada"); err != nil {
		t.Fatalf("SavePreference: %v", err)
	}

	got, err := ks.LoadPreference(PrefUsername)
	if err != nil {
		t.Fatalf("LoadPreference: %v", err)
	}
	if got != "ada" {
		t.Errorf("got %q, want %q", got, "ada")
	}
}

// TestDisplayNameAndUsernameAreIndependent guards against the two
// preferences ever accidentally sharing storage - see PrefDisplayName
// and PrefUsername's doc comments for why conflating them would be a
// real bug, not just a style preference.
func TestDisplayNameAndUsernameAreIndependent(t *testing.T) {
	ks := openTestStore(t)

	if err := ks.SavePreference(PrefDisplayName, "Ada"); err != nil {
		t.Fatalf("SavePreference(display name): %v", err)
	}
	if err := ks.SavePreference(PrefUsername, "ada_lovelace_1815"); err != nil {
		t.Fatalf("SavePreference(username): %v", err)
	}

	gotName, err := ks.LoadPreference(PrefDisplayName)
	if err != nil {
		t.Fatalf("LoadPreference(display name): %v", err)
	}
	gotUsername, err := ks.LoadPreference(PrefUsername)
	if err != nil {
		t.Fatalf("LoadPreference(username): %v", err)
	}

	if gotName != "Ada" {
		t.Errorf("display name = %q, want %q", gotName, "Ada")
	}
	if gotUsername != "ada_lovelace_1815" {
		t.Errorf("username = %q, want %q", gotUsername, "ada_lovelace_1815")
	}
}

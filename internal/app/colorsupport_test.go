package app

import (
	"testing"

	"github.com/charmbracelet/colorprofile"

	"github.com/HomieB-tt/synq/internal/db"
	"github.com/HomieB-tt/synq/internal/ui/styles"
)

func TestRichColorOnlyForTrueColorAnd256(t *testing.T) {
	for p, want := range map[colorprofile.Profile]bool{
		colorprofile.TrueColor: true,
		colorprofile.ANSI256:   true,
		colorprofile.ANSI:      false,
		colorprofile.ASCII:     false,
		colorprofile.NoTTY:     false,
	} {
		if got := richColor(p); got != want {
			t.Errorf("richColor(%v) = %v, want %v", p, got, want)
		}
	}
}

func TestStartingThemeFallsBackToSystemOnlyForThePoorDefault(t *testing.T) {
	dracula := styles.Default()
	if got := startingTheme(dracula, nil, colorprofile.TrueColor); got.Palette.Name != "dracula" {
		t.Errorf("true colour changed the theme to %s", got.Palette.Name)
	}
	if got := startingTheme(dracula, nil, colorprofile.ANSI); got.Palette.Name != "system" {
		t.Errorf("16-colour default = %s, want system", got.Palette.Name)
	}
	// A saved choice is the user's, whatever the terminal.
	nord := styles.New(styles.Nord)
	ks := newTestKeyStore(t)
	if err := ks.SavePreference(db.PrefTheme, "nord"); err != nil {
		t.Fatal(err)
	}
	if got := startingTheme(nord, ks, colorprofile.ANSI); got.Palette.Name != "nord" {
		t.Errorf("overrode a saved theme with %s", got.Palette.Name)
	}
}

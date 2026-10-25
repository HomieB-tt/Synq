package app

import (
	"os"

	"github.com/charmbracelet/colorprofile"

	"github.com/HomieB-tt/synq/internal/db"
	"github.com/HomieB-tt/synq/internal/ui/styles"
)

// The built-in themes are hex colours. A terminal that can only show the
// 16 base colours (older Windows consoles, some SSH and multiplexer
// setups, NO_COLOR) squeezes them into whichever of those 16 is nearest,
// and the result looks nothing like the theme or like the same theme
// elsewhere. The system theme uses those 16 slots directly, so it looks
// right by construction there.

// detectColorProfile asks what the terminal behind stdout can show. It
// is a variable so tests need no real terminal.
var detectColorProfile = func() colorprofile.Profile {
	return colorprofile.Detect(os.Stdout, os.Environ())
}

// richColor reports whether the terminal can show the built-in themes
// faithfully: true colour, or the 256-colour palette (a close match).
func richColor(p colorprofile.Profile) bool {
	return p == colorprofile.TrueColor || p == colorprofile.ANSI256
}

// startingTheme is the theme to open with. A theme the user chose is
// never overridden; only the default is replaced, and only when the
// terminal could not do the default justice. :theme still changes it.
func startingTheme(current styles.Theme, store *db.KeyStore, p colorprofile.Profile) styles.Theme {
	if richColor(p) {
		return current
	}
	if store != nil {
		if name, err := store.LoadPreference(db.PrefTheme); err == nil {
			if _, ok := styles.All[name]; ok {
				return current // the user picked one
			}
		}
	}
	return styles.New(styles.System)
}

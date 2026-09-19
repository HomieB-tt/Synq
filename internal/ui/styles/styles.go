// Package styles defines Lip Gloss v2 theme palettes and the derived
// styles the TUI shell uses for chrome: tabs, borders, status bar,
// command palette. Built-in themes: Dracula, Nord, Monokai, Catppuccin,
// Gruvbox, Tokyo Night, Solarized, One Dark, Rosé Pine, Ayu, and
// Everforest.
//
// Lip Gloss v2 removed automatic background/adaptive-color detection
// (see DESIGN.md section 7) - theme selection here is explicit, with
// charm.land/bubbletea/v2's tea.RequestBackgroundColor used only as a
// hint for which default to start with, not as automatic switching.
package styles

import (
	"image/color"
	"sort"

	"charm.land/lipgloss/v2"
)

// Palette holds the raw colors for a single theme. Fields are typed as
// the standard image/color.Color interface, not lipgloss.Color -
// in Lip Gloss v2, Color is a function (lipgloss.Color("#hex")) that
// returns color.Color, not a type itself. Using lipgloss.Color as a
// struct field type is a compile error ("is not a type").
type Palette struct {
	Name       string
	Background color.Color
	Foreground color.Color
	Muted      color.Color
	Accent     color.Color
	Border     color.Color
	Warning    color.Color
	Error      color.Color
}

// Dracula, Nord, and Monokai are the three palettes named in the
// original feature spec. All three are dark-background themes, which
// is why background detection only informs the *hint* shown to the
// user, not an automatic light/dark switch between unrelated palettes.
var (
	Dracula = Palette{
		Name:       "dracula",
		Background: lipgloss.Color("#282a36"),
		Foreground: lipgloss.Color("#f8f8f2"),
		Muted:      lipgloss.Color("#6272a4"),
		Accent:     lipgloss.Color("#bd93f9"),
		Border:     lipgloss.Color("#44475a"),
		Warning:    lipgloss.Color("#f1fa8c"),
		Error:      lipgloss.Color("#ff5555"),
	}

	Nord = Palette{
		Name:       "nord",
		Background: lipgloss.Color("#2e3440"),
		Foreground: lipgloss.Color("#eceff4"),
		Muted:      lipgloss.Color("#4c566a"),
		Accent:     lipgloss.Color("#88c0d0"),
		Border:     lipgloss.Color("#3b4252"),
		Warning:    lipgloss.Color("#ebcb8b"),
		Error:      lipgloss.Color("#bf616a"),
	}

	Monokai = Palette{
		Name:       "monokai",
		Background: lipgloss.Color("#272822"),
		Foreground: lipgloss.Color("#f8f8f2"),
		Muted:      lipgloss.Color("#75715e"),
		Accent:     lipgloss.Color("#a6e22e"),
		Border:     lipgloss.Color("#49483e"),
		Warning:    lipgloss.Color("#e6db74"),
		Error:      lipgloss.Color("#f92672"),
	}

	// Catppuccin is the Mocha variant - the theme your terminal is
	// probably already using, per the bug report that led to fixing
	// the full-screen background painting above.
	Catppuccin = Palette{
		Name:       "catppuccin",
		Background: lipgloss.Color("#1e1e2e"),
		Foreground: lipgloss.Color("#cdd6f4"),
		Muted:      lipgloss.Color("#6c7086"),
		Accent:     lipgloss.Color("#cba6f7"),
		Border:     lipgloss.Color("#45475a"),
		Warning:    lipgloss.Color("#f9e2af"),
		Error:      lipgloss.Color("#f38ba8"),
	}

	Gruvbox = Palette{
		Name:       "gruvbox",
		Background: lipgloss.Color("#282828"),
		Foreground: lipgloss.Color("#ebdbb2"),
		Muted:      lipgloss.Color("#928374"),
		Accent:     lipgloss.Color("#fe8019"),
		Border:     lipgloss.Color("#3c3836"),
		Warning:    lipgloss.Color("#fabd2f"),
		Error:      lipgloss.Color("#fb4934"),
	}

	TokyoNight = Palette{
		Name:       "tokyonight",
		Background: lipgloss.Color("#1a1b26"),
		Foreground: lipgloss.Color("#c0caf5"),
		Muted:      lipgloss.Color("#565f89"),
		Accent:     lipgloss.Color("#7aa2f7"),
		Border:     lipgloss.Color("#292e42"),
		Warning:    lipgloss.Color("#e0af68"),
		Error:      lipgloss.Color("#f7768e"),
	}

	Solarized = Palette{
		Name:       "solarized",
		Background: lipgloss.Color("#002b36"),
		Foreground: lipgloss.Color("#839496"),
		Muted:      lipgloss.Color("#586e75"),
		Accent:     lipgloss.Color("#268bd2"),
		Border:     lipgloss.Color("#073642"),
		Warning:    lipgloss.Color("#b58900"),
		Error:      lipgloss.Color("#dc322f"),
	}

	// OneDark is Atom's flagship theme.
	OneDark = Palette{
		Name:       "onedark",
		Background: lipgloss.Color("#282c34"),
		Foreground: lipgloss.Color("#abb2bf"),
		Muted:      lipgloss.Color("#5c6370"),
		Accent:     lipgloss.Color("#61afef"),
		Border:     lipgloss.Color("#3e4451"),
		Warning:    lipgloss.Color("#e5c07b"),
		Error:      lipgloss.Color("#e06c75"),
	}

	RosePine = Palette{
		Name:       "rosepine",
		Background: lipgloss.Color("#191724"),
		Foreground: lipgloss.Color("#e0def4"),
		Muted:      lipgloss.Color("#6e6a86"),
		Accent:     lipgloss.Color("#c4a7e7"),
		Border:     lipgloss.Color("#403d52"),
		Warning:    lipgloss.Color("#f6c177"),
		Error:      lipgloss.Color("#eb6f92"),
	}

	Ayu = Palette{
		Name:       "ayu",
		Background: lipgloss.Color("#0a0e14"),
		Foreground: lipgloss.Color("#b3b1ad"),
		Muted:      lipgloss.Color("#4d5566"),
		Accent:     lipgloss.Color("#ff8f40"),
		Border:     lipgloss.Color("#131721"),
		Warning:    lipgloss.Color("#ffb454"),
		Error:      lipgloss.Color("#ff3333"),
	}

	Everforest = Palette{
		Name:       "everforest",
		Background: lipgloss.Color("#2d353b"),
		Foreground: lipgloss.Color("#d3c6aa"),
		Muted:      lipgloss.Color("#7a8478"),
		Accent:     lipgloss.Color("#a7c080"),
		Border:     lipgloss.Color("#414b50"),
		Warning:    lipgloss.Color("#dbbc7f"),
		Error:      lipgloss.Color("#e67e80"),
	}
)

// All is every built-in palette, keyed by the name used to select it
// from the command palette (e.g. `:theme nord`).
var All = map[string]Palette{
	Dracula.Name:    Dracula,
	Nord.Name:       Nord,
	Monokai.Name:    Monokai,
	Catppuccin.Name: Catppuccin,
	Gruvbox.Name:    Gruvbox,
	TokyoNight.Name: TokyoNight,
	Solarized.Name:  Solarized,
	OneDark.Name:    OneDark,
	RosePine.Name:   RosePine,
	Ayu.Name:        Ayu,
	Everforest.Name: Everforest,
}

// Names returns every built-in theme name, sorted, for use in help and
// error text (e.g. "Usage: :theme <dracula|nord|...>") - listed
// programmatically rather than hardcoded so this never drifts out of
// sync with All as themes are added.
func Names() []string {
	names := make([]string, 0, len(All))
	for name := range All {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Theme is the set of ready-to-use styles derived from a Palette.
type Theme struct {
	Palette Palette

	// Screen fills the entire terminal viewport with the palette's
	// background/foreground via ordinary SGR styling (not terminal OSC
	// color-setting - see model.go's View for why that distinction
	// matters). Confined to the alt-screen buffer, so it can never
	// leak into the user's terminal after Synq exits.
	Screen lipgloss.Style

	TabActive   lipgloss.Style
	TabInactive lipgloss.Style
	Border      lipgloss.Style
	StatusBar   lipgloss.Style
	CommandBar  lipgloss.Style
	Muted       lipgloss.Style
	Warning     lipgloss.Style
	Error       lipgloss.Style
}

// New derives a full Theme from a Palette.
func New(p Palette) Theme {
	return Theme{
		Palette: p,

		Screen: lipgloss.NewStyle().
			Background(p.Background).
			Foreground(p.Foreground),

		TabActive: lipgloss.NewStyle().
			Bold(true).
			Foreground(p.Background).
			Background(p.Accent).
			Padding(0, 2),

		TabInactive: lipgloss.NewStyle().
			Foreground(p.Muted).
			Background(p.Background).
			Padding(0, 2),

		Border: lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(p.Border).
			Background(p.Background).
			Foreground(p.Foreground),

		StatusBar: lipgloss.NewStyle().
			Foreground(p.Muted).
			Background(p.Background),

		CommandBar: lipgloss.NewStyle().
			Foreground(p.Foreground).
			Background(p.Background).
			Padding(0, 1),

		Muted: lipgloss.NewStyle().
			Foreground(p.Muted).
			Background(p.Background),

		Warning: lipgloss.NewStyle().
			Foreground(p.Warning).
			Background(p.Background).
			Bold(true),

		Error: lipgloss.NewStyle().
			Foreground(p.Error).
			Background(p.Background).
			Bold(true),
	}
}

// Default returns the starting theme (Dracula) used before any
// explicit selection or background-color hint is available.
func Default() Theme {
	return New(Dracula)
}

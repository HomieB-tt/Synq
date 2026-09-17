// Package styles defines Lip Gloss v2 theme palettes (Dracula, Nord,
// Monokai) and the derived styles the TUI shell uses for chrome: tabs,
// borders, status bar, command palette.
//
// Lip Gloss v2 removed automatic background/adaptive-color detection
// (see DESIGN.md section 7) - theme selection here is explicit, with
// charm.land/bubbletea/v2's tea.RequestBackgroundColor used only as a
// hint for which default to start with, not as automatic switching.
package styles

import (
	"image/color"

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
)

// All is every built-in palette, keyed by the name used to select it
// from the command palette (e.g. `:theme nord`).
var All = map[string]Palette{
	Dracula.Name: Dracula,
	Nord.Name:    Nord,
	Monokai.Name: Monokai,
}

// Theme is the set of ready-to-use styles derived from a Palette.
type Theme struct {
	Palette Palette

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

		TabActive: lipgloss.NewStyle().
			Bold(true).
			Foreground(p.Background).
			Background(p.Accent).
			Padding(0, 2),

		TabInactive: lipgloss.NewStyle().
			Foreground(p.Muted).
			Padding(0, 2),

		Border: lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(p.Border),

		StatusBar: lipgloss.NewStyle().
			Foreground(p.Muted),

		CommandBar: lipgloss.NewStyle().
			Foreground(p.Foreground).
			Padding(0, 1),

		Muted: lipgloss.NewStyle().Foreground(p.Muted),

		Warning: lipgloss.NewStyle().Foreground(p.Warning).Bold(true),

		Error: lipgloss.NewStyle().Foreground(p.Error).Bold(true),
	}
}

// Default returns the starting theme (Dracula) used before any
// explicit selection or background-color hint is available.
func Default() Theme {
	return New(Dracula)
}

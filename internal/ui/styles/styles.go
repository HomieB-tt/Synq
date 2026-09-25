// Package styles defines Lip Gloss v2 theme palettes and the derived
// styles the TUI shell uses for chrome: tabs, borders, status bar,
// command palette. See All for the full, current list of built-in
// themes - deliberately not enumerated here in prose, since that list
// has already grown past the point where a hardcoded comment would
// stay in sync (the same reasoning behind Names() being computed from
// All rather than hardcoded).
//
// Lip Gloss v2 removed automatic background/adaptive-color detection
// (see DESIGN.md section 7) - theme selection here is explicit, with
// charm.land/bubbletea/v2's tea.RequestBackgroundColor used only as a
// hint for which default to start with, not as automatic switching.
//
// A few palettes below (see their individual doc comments) are not
// reproductions of a specific published color scheme - either because
// the requested name doesn't correspond to one well-known, canonical
// source, or because it's a deliberately original design. Those are
// called out explicitly rather than presented as if they were exact
// ports, the same way DESIGN.md prefers stating a decision plainly
// over leaving it ambiguous.
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

	// Aura is Dalton Menezes' "Aura Dark" VS Code theme - dark violet
	// background, mint/lavender accents.
	Aura = Palette{
		Name:       "aura",
		Background: lipgloss.Color("#15141b"),
		Foreground: lipgloss.Color("#edecee"),
		Muted:      lipgloss.Color("#6d6d6d"),
		Accent:     lipgloss.Color("#a277ff"),
		Border:     lipgloss.Color("#29263c"),
		Warning:    lipgloss.Color("#ffca85"),
		Error:      lipgloss.Color("#ff6767"),
	}

	// CarbonFox is the Nightfox family's variant built on IBM's Carbon
	// Design System palette - true near-black background, cool blue
	// accent.
	CarbonFox = Palette{
		Name:       "carbonfox",
		Background: lipgloss.Color("#161616"),
		Foreground: lipgloss.Color("#f2f4f8"),
		Muted:      lipgloss.Color("#525252"),
		Accent:     lipgloss.Color("#78a9ff"),
		Border:     lipgloss.Color("#393939"),
		Warning:    lipgloss.Color("#ffe97b"),
		Error:      lipgloss.Color("#fa4d56"),
	}

	// CatppuccinFrappe is the Catppuccin project's "Frappé" variant -
	// softer/lower-contrast than the Mocha palette already named
	// Catppuccin above.
	CatppuccinFrappe = Palette{
		Name:       "catppuccin-frappe",
		Background: lipgloss.Color("#303446"),
		Foreground: lipgloss.Color("#c6d0f5"),
		Muted:      lipgloss.Color("#737994"),
		Accent:     lipgloss.Color("#ca9ee6"),
		Border:     lipgloss.Color("#51576d"),
		Warning:    lipgloss.Color("#e5c890"),
		Error:      lipgloss.Color("#e78284"),
	}

	// CatppuccinMacchiato is the Catppuccin project's "Macchiato"
	// variant - between Frappé and Mocha in contrast.
	CatppuccinMacchiato = Palette{
		Name:       "catppuccin-macchiato",
		Background: lipgloss.Color("#24273a"),
		Foreground: lipgloss.Color("#cad3f5"),
		Muted:      lipgloss.Color("#6e738d"),
		Accent:     lipgloss.Color("#c6a0f6"),
		Border:     lipgloss.Color("#494d64"),
		Warning:    lipgloss.Color("#eed49f"),
		Error:      lipgloss.Color("#ed8796"),
	}

	// Cobalt2 is Wes Bos' long-running VS Code theme - deep blue
	// background, signature yellow accent.
	Cobalt2 = Palette{
		Name:       "cobalt2",
		Background: lipgloss.Color("#193549"),
		Foreground: lipgloss.Color("#ffffff"),
		Muted:      lipgloss.Color("#5a7a94"),
		Accent:     lipgloss.Color("#ffc600"),
		Border:     lipgloss.Color("#0d3a58"),
		Warning:    lipgloss.Color("#ff9d00"),
		Error:      lipgloss.Color("#ff2222"),
	}

	// Cursor is an original palette for this list, not a reproduction
	// of Cursor's actual editor theme (which isn't published as a
	// fixed, citable color spec) - a clean neutral dark with the
	// blue-indigo tone associated with the Cursor brand as its accent.
	Cursor = Palette{
		Name:       "cursor",
		Background: lipgloss.Color("#181818"),
		Foreground: lipgloss.Color("#e5e5e5"),
		Muted:      lipgloss.Color("#6b6b6b"),
		Accent:     lipgloss.Color("#6b7fff"),
		Border:     lipgloss.Color("#2a2a2a"),
		Warning:    lipgloss.Color("#e5c07b"),
		Error:      lipgloss.Color("#ff6b6b"),
	}

	// Flexoki is Steph Ango's ink-on-paper-inspired scheme (dark
	// variant here) - warm near-black background, muted blue accent.
	Flexoki = Palette{
		Name:       "flexoki",
		Background: lipgloss.Color("#100f0f"),
		Foreground: lipgloss.Color("#cecdc3"),
		Muted:      lipgloss.Color("#878580"),
		Accent:     lipgloss.Color("#4385be"),
		Border:     lipgloss.Color("#343331"),
		Warning:    lipgloss.Color("#ad8301"),
		Error:      lipgloss.Color("#d14d41"),
	}

	// GitHub is GitHub's own "Dark" default editor/UI theme.
	GitHub = Palette{
		Name:       "github",
		Background: lipgloss.Color("#0d1117"),
		Foreground: lipgloss.Color("#c9d1d9"),
		Muted:      lipgloss.Color("#8b949e"),
		Accent:     lipgloss.Color("#58a6ff"),
		Border:     lipgloss.Color("#30363d"),
		Warning:    lipgloss.Color("#d29922"),
		Error:      lipgloss.Color("#f85149"),
	}

	// Kanagawa is the Neovim colorscheme inspired by Katsushika
	// Hokusai's "The Great Wave off Kanagawa" - ink-blue background,
	// wave-crest blue accent.
	Kanagawa = Palette{
		Name:       "kanagawa",
		Background: lipgloss.Color("#1f1f28"),
		Foreground: lipgloss.Color("#dcd7ba"),
		Muted:      lipgloss.Color("#727169"),
		Accent:     lipgloss.Color("#7e9cd8"),
		Border:     lipgloss.Color("#54546d"),
		Warning:    lipgloss.Color("#c0a36e"),
		Error:      lipgloss.Color("#c34043"),
	}

	// LucentOrng is an original palette (no established "lucent-orng"
	// color scheme exists to port) - a near-black background with a
	// soft glow ("lucent") of warm orange running through the accent
	// and warning colors, deliberately gentler than Orng below.
	LucentOrng = Palette{
		Name:       "lucent-orng",
		Background: lipgloss.Color("#14100d"),
		Foreground: lipgloss.Color("#f5ece1"),
		Muted:      lipgloss.Color("#8a7a68"),
		Accent:     lipgloss.Color("#ff9d4d"),
		Border:     lipgloss.Color("#2e2620"),
		Warning:    lipgloss.Color("#ffb672"),
		Error:      lipgloss.Color("#ff5c4d"),
	}

	// Material is the Material Theme's darker variant - neutral
	// charcoal background, cool blue accent.
	Material = Palette{
		Name:       "material",
		Background: lipgloss.Color("#212121"),
		Foreground: lipgloss.Color("#eeffff"),
		Muted:      lipgloss.Color("#616161"),
		Accent:     lipgloss.Color("#82aaff"),
		Border:     lipgloss.Color("#333333"),
		Warning:    lipgloss.Color("#ffcb6b"),
		Error:      lipgloss.Color("#f07178"),
	}

	// Matrix is an original, deliberately literal palette - black
	// background, phosphor-green everything, in the spirit of the
	// film's falling code rather than any specific published theme.
	Matrix = Palette{
		Name:       "matrix",
		Background: lipgloss.Color("#000000"),
		Foreground: lipgloss.Color("#00ff41"),
		Muted:      lipgloss.Color("#008f11"),
		Accent:     lipgloss.Color("#00ff41"),
		Border:     lipgloss.Color("#003b00"),
		Warning:    lipgloss.Color("#00cc33"),
		Error:      lipgloss.Color("#ff1a1a"),
	}

	// Mercury is an original palette (no established "mercury" color
	// scheme exists to port) - cool graphite background with a
	// liquid-metal silver-blue accent.
	Mercury = Palette{
		Name:       "mercury",
		Background: lipgloss.Color("#14171c"),
		Foreground: lipgloss.Color("#dfe6ea"),
		Muted:      lipgloss.Color("#6b7684"),
		Accent:     lipgloss.Color("#9fb4c7"),
		Border:     lipgloss.Color("#2b323b"),
		Warning:    lipgloss.Color("#d8b36a"),
		Error:      lipgloss.Color("#d97469"),
	}

	// NightOwl is Sarah Drasner's theme, built for coding late/in low
	// light - deep navy background, high-contrast blue accent.
	NightOwl = Palette{
		Name:       "nightowl",
		Background: lipgloss.Color("#011627"),
		Foreground: lipgloss.Color("#d6deeb"),
		Muted:      lipgloss.Color("#637777"),
		Accent:     lipgloss.Color("#82aaff"),
		Border:     lipgloss.Color("#1d3b53"),
		Warning:    lipgloss.Color("#ecc48d"),
		Error:      lipgloss.Color("#ef5350"),
	}

	// Orng is an original palette (no established "orng" color scheme
	// exists to port) - a flatter, punchier companion to LucentOrng
	// above: plain warm-charcoal background, pure saturated orange
	// accent rather than a soft glow.
	Orng = Palette{
		Name:       "orng",
		Background: lipgloss.Color("#191714"),
		Foreground: lipgloss.Color("#f2ede6"),
		Muted:      lipgloss.Color("#7a7367"),
		Accent:     lipgloss.Color("#ff7a1a"),
		Border:     lipgloss.Color("#322c24"),
		Warning:    lipgloss.Color("#ffab5e"),
		Error:      lipgloss.Color("#ff4d4d"),
	}

	// OsakaJade is an original palette (no established "osaka-jade"
	// color scheme exists to port, and it isn't the same project as
	// the unrelated "solarized-osaka" Neovim theme) - deep jade-black
	// background, bright jade-green accent.
	OsakaJade = Palette{
		Name:       "osaka-jade",
		Background: lipgloss.Color("#10201a"),
		Foreground: lipgloss.Color("#d7e8de"),
		Muted:      lipgloss.Color("#5f8375"),
		Accent:     lipgloss.Color("#3ddc97"),
		Border:     lipgloss.Color("#1c3229"),
		Warning:    lipgloss.Color("#e0c068"),
		Error:      lipgloss.Color("#e06868"),
	}

	// Palenight is the Material Theme's purple-leaning "Palenight"
	// variant - muted indigo background, lavender accent.
	Palenight = Palette{
		Name:       "palenight",
		Background: lipgloss.Color("#292d3e"),
		Foreground: lipgloss.Color("#a6accd"),
		Muted:      lipgloss.Color("#676e95"),
		Accent:     lipgloss.Color("#c792ea"),
		Border:     lipgloss.Color("#3a3f58"),
		Warning:    lipgloss.Color("#ffcb6b"),
		Error:      lipgloss.Color("#f07178"),
	}

	// Synthwave84 is Robb Owen's "SynthWave '84" theme - deep purple
	// background, neon magenta accent, in the outrun/retrowave style.
	Synthwave84 = Palette{
		Name:       "synthwave84",
		Background: lipgloss.Color("#2a2139"),
		Foreground: lipgloss.Color("#f8f8f2"),
		Muted:      lipgloss.Color("#848bbd"),
		Accent:     lipgloss.Color("#ff7edb"),
		Border:     lipgloss.Color("#34294f"),
		Warning:    lipgloss.Color("#fede5d"),
		Error:      lipgloss.Color("#fe4450"),
	}

	// System doesn't hardcode any colors at all - every field is set
	// to a base ANSI (0-15) color index rather than a hex value. Those
	// sixteen slots are defined by the terminal emulator itself, so
	// this palette renders using whatever colors the user's terminal
	// is already configured with (the same idea as a tool that just
	// says "use my terminal's colors" instead of shipping its own
	// look). It won't look identical across terminals by design - that
	// consistency trade-off is the whole point of the name.
	System = Palette{
		Name:       "system",
		Background: lipgloss.Color("0"),  // terminal's ANSI black/background slot
		Foreground: lipgloss.Color("7"),  // terminal's ANSI white/foreground slot
		Muted:      lipgloss.Color("8"),  // bright black
		Accent:     lipgloss.Color("12"), // bright blue
		Border:     lipgloss.Color("8"),  // bright black
		Warning:    lipgloss.Color("11"), // bright yellow
		Error:      lipgloss.Color("9"),  // bright red
	}

	// Vesper is Rauno Freiberg's minimalist theme - near-black
	// background, warm amber accent, very little else.
	Vesper = Palette{
		Name:       "vesper",
		Background: lipgloss.Color("#101010"),
		Foreground: lipgloss.Color("#ffffff"),
		Muted:      lipgloss.Color("#8f8f8f"),
		Accent:     lipgloss.Color("#ffc799"),
		Border:     lipgloss.Color("#262626"),
		Warning:    lipgloss.Color("#e6b673"),
		Error:      lipgloss.Color("#ff8080"),
	}
)

// All is every built-in palette, keyed by the name used to select it
// from the command palette (e.g. `:theme nord`).
var All = map[string]Palette{
	Dracula.Name:             Dracula,
	Nord.Name:                Nord,
	Monokai.Name:             Monokai,
	Catppuccin.Name:          Catppuccin,
	Gruvbox.Name:             Gruvbox,
	TokyoNight.Name:          TokyoNight,
	Solarized.Name:           Solarized,
	OneDark.Name:             OneDark,
	RosePine.Name:            RosePine,
	Ayu.Name:                 Ayu,
	Everforest.Name:          Everforest,
	Aura.Name:                Aura,
	CarbonFox.Name:           CarbonFox,
	CatppuccinFrappe.Name:    CatppuccinFrappe,
	CatppuccinMacchiato.Name: CatppuccinMacchiato,
	Cobalt2.Name:             Cobalt2,
	Cursor.Name:              Cursor,
	Flexoki.Name:             Flexoki,
	GitHub.Name:              GitHub,
	Kanagawa.Name:            Kanagawa,
	LucentOrng.Name:          LucentOrng,
	Material.Name:            Material,
	Matrix.Name:              Matrix,
	Mercury.Name:             Mercury,
	NightOwl.Name:            NightOwl,
	Orng.Name:                Orng,
	OsakaJade.Name:           OsakaJade,
	Palenight.Name:           Palenight,
	Synthwave84.Name:         Synthwave84,
	System.Name:              System,
	Vesper.Name:              Vesper,
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

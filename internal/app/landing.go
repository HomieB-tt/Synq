package app

import (
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/HomieB-tt/synq/internal/db"
	"github.com/HomieB-tt/synq/internal/ui/banner"
	"github.com/HomieB-tt/synq/internal/ui/styles"
)

// This file is the first-run screen: what a device with no identity sees
// before anything else. It is its own small Bubble Tea program (it runs
// before, and separately from, the app), centred in the terminal, with a
// little motion - the wordmark draws in, a highlight sweeps across it, the
// tagline types itself out - none of which gets in the way of the keys.
// SYNQ_NO_ANIMATION=1 shows it still.

// LandingChoice is what the person picked on the first-run screen.
type LandingChoice int

const (
	LandingGuest  LandingChoice = iota // browse the public feed
	LandingCreate                      // create an identity
	LandingQuit
)

const (
	landingTick     = 60 * time.Millisecond
	landingRevealBy = 14 // frames until the wordmark is fully drawn
	landingTagEvery = 52 // frames each tagline stays (~3 s)
)

// landingTaglines are what the one-line pitch rotates through. Each is a
// claim the app keeps: end-to-end encryption, no phone number, no
// directory of users, rooms that vanish.
var landingTaglines = []string{
	"Say it. Then let it go.",
	"Private by design. Ephemeral by default.",
	"No phone number. No user search. Just your keys.",
	"Small rooms that vanish when you're done.",
}

const landingBlurb = "End-to-end encrypted messaging for your terminal: talk to people you trust, share files, and gather in synapses - small group chats that disappear when they end."

type landingTickMsg struct{}

type landingModel struct {
	theme   styles.Theme
	width   int
	height  int
	sel     int
	frame   int
	static  bool // no animation: everything shown, nothing moving
	choice  LandingChoice
	decided bool
}

var landingItems = []struct{ title, desc string }{
	{"Browse the public feed as a guest", "look around first - no account, nothing to remember"},
	{"Create your identity", "a passphrase and a username - there is no recovery"},
	{"Quit", ""},
}

func newLandingModel(th styles.Theme, static bool) landingModel {
	m := landingModel{theme: th, width: 80, height: 24, static: static}
	if static {
		m.frame = 1 << 20
	}
	return m
}

// RunLanding shows the first-run screen and returns what was chosen.
func RunLanding(store *db.KeyStore) (LandingChoice, error) {
	m := newLandingModel(StartingTheme(store), os.Getenv("SYNQ_NO_ANIMATION") == "1")
	final, err := tea.NewProgram(m).Run()
	if err != nil {
		return LandingQuit, err
	}
	if lm, ok := final.(landingModel); ok && lm.decided {
		return lm.choice, nil
	}
	return LandingQuit, nil
}

func (m landingModel) Init() tea.Cmd {
	if m.static {
		return nil
	}
	return tea.Tick(landingTick, func(time.Time) tea.Msg { return landingTickMsg{} })
}

func (m landingModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case landingTickMsg:
		m.frame++
		return m, tea.Tick(landingTick, func(time.Time) tea.Msg { return landingTickMsg{} })

	case tea.KeyPressMsg:
		switch msg.String() {
		case "1":
			return m.decide(LandingGuest)
		case "2":
			return m.decide(LandingCreate)
		case "3", "q", "esc", "ctrl+c":
			return m.decide(LandingQuit)
		case "up", "k":
			m.sel = (m.sel + len(landingItems) - 1) % len(landingItems)
		case "down", "j", "tab":
			m.sel = (m.sel + 1) % len(landingItems)
		case "enter", "space":
			return m.decide(LandingChoice(m.sel))
		}
	}
	return m, nil
}

func (m landingModel) decide(c LandingChoice) (tea.Model, tea.Cmd) {
	m.choice, m.decided = c, true
	return m, tea.Quit
}

func (m landingModel) View() tea.View {
	w, h := max(m.width, 1), max(m.height, 1)
	if m.decided {
		return tea.NewView("")
	}
	v := tea.NewView(m.theme.Screen.Width(w).Height(h).Render(m.render(w, h)))
	v.AltScreen = true
	return v
}

// render lays the screen out for a w x h terminal and returns exactly h
// lines of exactly w columns. It tries the full layout first and drops
// pieces (menu descriptions, the blurb, the big wordmark) until it fits.
func (m landingModel) render(w, h int) string {
	var lines []string
	for level := 0; level <= 3; level++ {
		lines = m.build(w, level)
		if len(lines) <= h || level == 3 {
			break
		}
	}
	fill := lipgloss.NewStyle().Background(m.theme.Palette.Background)
	blank := fill.Render(strings.Repeat(" ", w))
	if len(lines) > h {
		lines = lines[:h]
	}
	top := (h - len(lines)) / 2
	out := make([]string, 0, h)
	for i := 0; i < top; i++ {
		out = append(out, blank)
	}
	out = append(out, lines...)
	for len(out) < h {
		out = append(out, blank)
	}
	return strings.Join(out, "\n")
}

// centre pads line to w columns with background-coloured space on both
// sides, so the margin matches the screen instead of showing through.
func (m landingModel) centre(line string, w int) string {
	fill := lipgloss.NewStyle().Background(m.theme.Palette.Background)
	if lipgloss.Width(line) > w {
		line = ansi.Truncate(line, w, "")
	}
	gap := max(w-lipgloss.Width(line), 0)
	left := gap / 2
	return fill.Render(strings.Repeat(" ", left)) + line + fill.Render(strings.Repeat(" ", gap-left))
}

// build returns the centred lines at one detail level: 0 everything,
// 1 no menu descriptions, 2 also no blurb, 3 also the small wordmark.
func (m landingModel) build(w, level int) []string {
	t := m.theme
	accent := t.AccentText.Bold(true)
	var lines []string
	add := func(s string) { lines = append(lines, m.centre(s, w)) }
	gap := func() { add("") }

	// Wordmark.
	if level < 3 && w >= banner.Width+2 {
		for _, r := range m.wordmarkRows() {
			add(r)
		}
	} else {
		add(accent.Render("S Y N Q"))
	}
	gap()

	// Tagline, typed out.
	add(m.taglineLine())
	if level < 2 {
		gap()
		for _, l := range wrapToWidth(landingBlurb, min(max(w-6, 20), 62)) {
			add(t.Muted.Render(l))
		}
	}
	gap()

	// Menu.
	rich := level == 0 && w >= 64
	revealed := m.menuRevealed()
	menu := m.menuLines(rich, revealed)
	if w >= 52 {
		menuW := 0
		for _, l := range menu {
			menuW = max(menuW, lipgloss.Width(l))
		}
		box := lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(t.Palette.Border).
			BorderBackground(t.Palette.Background).
			Background(t.Palette.Background).
			Padding(0, 2)
		if level >= 1 {
			box = box.Padding(0, 1)
		}
		var padded []string
		for _, l := range menu {
			padded = append(padded, l+lipgloss.NewStyle().Background(t.Palette.Background).Render(strings.Repeat(" ", menuW-lipgloss.Width(l))))
		}
		for _, l := range strings.Split(box.Render(strings.Join(padded, "\n")), "\n") {
			add(l)
		}
	} else {
		for _, l := range menu {
			add(l)
		}
	}
	gap()
	add(t.Muted.Render("↑/↓ choose · enter select · 1-3 jump · q quit"))
	return lines
}

// wordmarkRows draws the SYNQ banner for the current frame: drawn in
// column by column, then a bright band sweeping across it.
func (m landingModel) wordmarkRows() []string {
	t := m.theme
	base := t.AccentText.Bold(true)
	bright := lipgloss.NewStyle().Foreground(t.Palette.Foreground).Background(t.Palette.Background).Bold(true)
	blank := lipgloss.NewStyle().Background(t.Palette.Background)

	visible := banner.Width
	sweep := -100
	if !m.static {
		visible = min(m.frame*3, banner.Width)
		if m.frame >= landingRevealBy {
			// A band 5 columns wide crosses the wordmark, then rests
			// off to the side for a while before the next pass.
			cycle := banner.Width + 40
			sweep = (m.frame-landingRevealBy)*2%cycle - 8
		}
	}

	rows := make([]string, 0, len(banner.Rows))
	for _, row := range banner.Rows {
		runes := []rune(row)
		var b strings.Builder
		for i := 0; i < len(runes); {
			// Group consecutive cells that share a style into one span.
			style, j := cellStyle(i, visible, sweep, base, bright, blank), i
			for j < len(runes) && sameCell(i, j, visible, sweep) {
				j++
			}
			seg := string(runes[i:j])
			if i >= visible {
				seg = strings.Repeat(" ", j-i)
			}
			b.WriteString(style.Render(seg))
			i = j
		}
		rows = append(rows, b.String())
	}
	return rows
}

func cellKind(i, visible, sweep int) int {
	switch {
	case i >= visible:
		return 0 // not drawn yet
	case i >= sweep-2 && i <= sweep+2:
		return 2 // under the highlight
	}
	return 1
}

func sameCell(a, b, visible, sweep int) bool {
	return cellKind(a, visible, sweep) == cellKind(b, visible, sweep)
}

func cellStyle(i, visible, sweep int, base, bright, blank lipgloss.Style) lipgloss.Style {
	switch cellKind(i, visible, sweep) {
	case 0:
		return blank
	case 2:
		return bright
	}
	return base
}

// taglineLine is the current tagline, typed out a couple of characters per
// frame. The line is padded to the full tagline's width, so centring it
// does not make the text drift sideways as it types.
func (m landingModel) taglineLine() string {
	idx, since := 0, 0
	if !m.static {
		idx = (m.frame / landingTagEvery) % len(landingTaglines)
		since = m.frame % landingTagEvery
		if m.frame < landingTagEvery {
			since = m.frame - landingRevealBy // the first one waits for the wordmark
		}
	}
	text := []rune(landingTaglines[idx])
	shown := len(text)
	if !m.static {
		shown = min(max(since*2, 0), len(text))
	}
	style := m.theme.Text.Bold(true)
	return style.Render(string(text[:shown])) + style.Render(strings.Repeat(" ", len(text)-shown))
}

// menuRevealed is how many menu items are on screen yet: they appear one
// after another just after the wordmark finishes.
func (m landingModel) menuRevealed() int {
	if m.static {
		return len(landingItems)
	}
	if m.frame < landingRevealBy {
		return 0
	}
	return min((m.frame-landingRevealBy)/4+1, len(landingItems))
}

// menuLines draws the numbered items; the selected one gets a marker and
// the surface highlight. Items not yet revealed are blank so nothing
// shifts as they appear.
func (m landingModel) menuLines(rich bool, revealed int) []string {
	t := m.theme
	bg := lipgloss.NewStyle().Background(t.Palette.Background)
	// Fixed width so the highlight is the same width for every row.
	itemW := 0
	for _, it := range landingItems {
		itemW = max(itemW, 5+lipgloss.Width(it.title))
		if rich && it.desc != "" {
			itemW = max(itemW, 5+lipgloss.Width(it.desc))
		}
	}
	var out []string
	for i, it := range landingItems {
		show := i < revealed
		sel := i == m.sel
		titleStyle, numStyle, markStyle := t.Text.Bold(true), t.AccentText.Bold(true), t.AccentText
		descStyle := t.Muted
		if sel {
			titleStyle = t.Selected.Bold(true)
			numStyle = t.Selected.Foreground(t.Palette.Accent).Bold(true)
			markStyle = t.Selected.Foreground(t.Palette.Accent)
			descStyle = t.Selected.Foreground(t.Palette.Muted)
		}
		pad := func(used int, st lipgloss.Style) string {
			return st.Render(strings.Repeat(" ", max(itemW-used, 0)))
		}
		if !show {
			out = append(out, bg.Render(strings.Repeat(" ", itemW)))
			if rich {
				out = append(out, bg.Render(strings.Repeat(" ", itemW)))
			}
			continue
		}
		mark := "  "
		if sel {
			mark = "▌ "
		}
		numStr := string(rune('1'+i)) + "  "
		used := 2 + 3 + lipgloss.Width(it.title)
		out = append(out, markStyle.Render(mark)+numStyle.Render(numStr)+titleStyle.Render(it.title)+pad(used, titleStyle))
		if rich {
			desc := it.desc
			du := 5 + lipgloss.Width(desc)
			out = append(out, descStyle.Render("     "+desc)+pad(du, descStyle))
		}
	}
	return out
}

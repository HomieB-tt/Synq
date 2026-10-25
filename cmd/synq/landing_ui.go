package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"golang.org/x/term"

	"github.com/HomieB-tt/synq/internal/app"
	"github.com/HomieB-tt/synq/internal/db"
	"github.com/HomieB-tt/synq/internal/ui/banner"
	"github.com/HomieB-tt/synq/internal/ui/styles"
)

// The first-run screens run before Bubble Tea starts, so they are plain
// line-by-line output - but styled with the same theme the app will open
// with (foreground colours only: no background fill, so they sit on the
// terminal's own background instead of painting a rectangle on it).

// landingOutput is where the first-run screens are written: stdout,
// downsampled to what the terminal can show.
func landingOutput() io.Writer {
	return colorprofile.NewWriter(os.Stdout, os.Environ())
}

// terminalWidth is the width of stdout, or 80 when it can't be found.
func terminalWidth() int {
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		return w
	}
	return 80
}

// renderLanding draws the first-run menu for a terminal width columns
// wide. It never emits a line wider than width.
func renderLanding(th styles.Theme, width int) string {
	p := th.Palette
	accent := lipgloss.NewStyle().Foreground(p.Accent).Bold(true)
	muted := lipgloss.NewStyle().Foreground(p.Muted)
	text := lipgloss.NewStyle().Foreground(p.Foreground)
	strong := text.Bold(true)

	var b strings.Builder
	b.WriteString("\n")
	if width >= banner.Width+2 {
		for _, r := range banner.Rows {
			b.WriteString(" " + accent.Render(r) + "\n")
		}
	} else {
		b.WriteString(" " + accent.Render("SYNQ") + "\n")
	}
	b.WriteString(" " + muted.Render("private, ephemeral messaging for the terminal") + "\n\n")

	item := func(n, title string) string {
		return accent.Render(n) + "  " + strong.Render(title)
	}
	rich := width >= 64
	lines := []string{
		strong.Render("Welcome."),
		muted.Render("No identity exists on this device yet."),
		"",
		item("1", "Browse the public feed as a guest"),
	}
	if rich {
		lines = append(lines, "   "+muted.Render("look around first - no account, nothing to remember"))
	}
	lines = append(lines, item("2", "Create your identity"))
	if rich {
		lines = append(lines, "   "+muted.Render("a passphrase and a username - there is no recovery"))
	}
	lines = append(lines, item("3", "Quit"))
	body := strings.Join(lines, "\n")

	if rich {
		box := lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(p.Border).
			Padding(0, 2)
		body = box.Render(body)
	}
	for _, l := range strings.Split(body, "\n") {
		b.WriteString(" " + l + "\n")
	}
	b.WriteString("\n")
	return b.String()
}

// promptLine is the styled "Choose an option" prompt.
func promptLine(th styles.Theme, label string) string {
	return lipgloss.NewStyle().Foreground(th.Palette.Accent).Bold(true).Render(label+" ›") + " "
}

// noteLine prints a short styled message (an error or a hint).
func noteLine(w io.Writer, th styles.Theme, kind, msg string) {
	c := th.Palette.Muted
	if kind == "error" {
		c = th.Palette.Error
	}
	fmt.Fprintln(w, " "+lipgloss.NewStyle().Foreground(c).Render(msg))
}

// landingChoice asks for the first-run option and returns "1", "2" or "3"
// (anything else is whatever was typed, for the caller to reject). On a
// real terminal it is the animated, centred screen; with piped input or
// output - scripts, tests, a dumb terminal - it is the plain menu.
func landingChoice(out io.Writer, th styles.Theme, ks *db.KeyStore) (string, error) {
	if term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd())) && os.Getenv("TERM") != "dumb" {
		c, err := app.RunLanding(ks)
		if err != nil {
			return "", err
		}
		return string(rune('1' + int(c))), nil
	}
	fmt.Fprint(out, renderLanding(th, terminalWidth()))
	fmt.Fprint(out, promptLine(th, "Choose 1-3"))
	return readLine()
}

// --- the screens after the menu: create identity, unlock, register ---
//
// These stay line-by-line (a passphrase prompt must not be a full-screen
// program), but share the menu's look: the same theme, accent prompts, a
// boxed notice for the one thing that must not be skimmed, and green/red
// result lines. All of it goes through a writer that drops colour when the
// output is not a terminal, so piped runs stay clean.

// uiTheme is the theme these screens use; run() sets it from the store.
var uiTheme = styles.Default()

// initUI loads the theme the app will open with.
func initUI(ks *db.KeyStore) { uiTheme = app.StartingTheme(ks) }

func uiStdout() io.Writer { return colorprofile.NewWriter(os.Stdout, os.Environ()) }
func uiStderr() io.Writer { return colorprofile.NewWriter(os.Stderr, os.Environ()) }

// uiHeading is a section title with a rule under it.
func uiHeading(w io.Writer, title string) {
	p := uiTheme.Palette
	fmt.Fprintln(w)
	fmt.Fprintln(w, " "+lipgloss.NewStyle().Foreground(p.Accent).Bold(true).Render(title))
	fmt.Fprintln(w, " "+lipgloss.NewStyle().Foreground(p.Border).Render(strings.Repeat("─", min(max(lipgloss.Width(title)+8, 24), 48))))
}

// uiNotice prints lines in a rounded box with a warning-coloured border -
// for what has to be read, like "there is no recovery".
func uiNotice(w io.Writer, title string, lines []string) {
	p := uiTheme.Palette
	head := lipgloss.NewStyle().Foreground(p.Warning).Bold(true).Render(title)
	body := lipgloss.NewStyle().Foreground(p.Foreground).Render(strings.Join(lines, "\n"))
	box := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(p.Warning).
		Padding(0, 2)
	width := max(min(terminalWidth()-4, 66), 30)
	fmt.Fprintln(w)
	for _, l := range strings.Split(box.Width(width).Render(head+"\n"+body), "\n") {
		fmt.Fprintln(w, " "+l)
	}
	fmt.Fprintln(w)
}

// uiOK, uiFail and uiHint print one styled result line.
func uiOK(w io.Writer, msg string) {
	fmt.Fprintln(w, " "+lipgloss.NewStyle().Foreground(uiTheme.Palette.Success).Bold(true).Render("✓ "+msg))
}

func uiFail(w io.Writer, msg string) {
	fmt.Fprintln(w, " "+lipgloss.NewStyle().Foreground(uiTheme.Palette.Error).Render("✗ "+msg))
}

func uiHint(w io.Writer, msg string) {
	fmt.Fprintln(w, " "+lipgloss.NewStyle().Foreground(uiTheme.Palette.Muted).Render(msg))
}

// uiAsk is an input prompt: accent label, arrow, then a space.
func uiAsk(label string) string { return promptLine(uiTheme, label) }

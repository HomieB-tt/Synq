package app

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// appProcessStart marks when this process reached package-level
// initialization - the closest instant to "the user typed synq" that
// this package can see. Everything main.go does before the TUI exists
// (Argon2id unlock, session resolution, the landing menu) happens
// after it and before app.New, so timing the identity step against it
// reports the real cost of getting to this point rather than only the
// time inside the event loop.
var appProcessStart = time.Now()

const (
	bootTickInterval = 80 * time.Millisecond

	// bootMinTicks is how long the fully-settled checklist stays on
	// screen before the TUI takes over (~640ms). Short enough to not
	// feel like a delay, long enough that a boot resolving instantly
	// (no server configured) doesn't flash past unread.
	bootMinTicks = 8

	// bootMaxTicks caps the whole splash (~3s). The connection step
	// finishes when the socket says something, which on a bad network
	// may be never - the splash must not hold the UI hostage to a
	// dial. Anything still pending at the cap is abandoned to finish
	// in the background: the wsClientReadyMsg/wsStatusMsg handlers run
	// whether or not the splash is still showing.
	bootMaxTicks = 38
)

// Boot step ids, so the Update handlers that settle them name them
// without duplicating the labels shown on screen.
const (
	bootIdentity = "identity"
	bootConnect  = "connect"
)

type bootStepState int

const (
	// stepPending: still waiting on whatever the step is named after;
	// renders with the spinner.
	stepPending bootStepState = iota
	// stepDone: finished cleanly; renders ✓ with elapsed time.
	stepDone
	// stepFailed: ended in an error the user needs to see; renders ✗
	// with the reason.
	stepFailed
	// stepSkipped: not applicable to this launch at all (no server
	// configured, not logged in); renders muted, not as a failure.
	stepSkipped
)

// bootStep is one line of the startup checklist. Unlike the splash's
// original fixed-duration message sequence, every step here settles
// from a real event (see Model.finishBootStep): the checklist is a
// status readout of the launch, not a timer wearing a status
// readout's clothes.
type bootStep struct {
	id      string
	label   string
	state   bootStepState
	detail  string        // failure text (stepFailed) or skip reason (stepSkipped)
	elapsed time.Duration // zero while pending
}

// spinnerFrames is a small hand-rolled animation (deliberately not
// using bubbles/spinner - see TECH_STACK.md on why this project avoids
// pulling in bubbles components whose exact v2 API hasn't been
// confirmed against live documentation).
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// bootTickMsg drives the spinner and the settle/finish checks - one
// message per bootTickInterval while the splash is up.
type bootTickMsg time.Time

func bootTick() tea.Cmd {
	return tea.Tick(bootTickInterval, func(t time.Time) tea.Msg {
		return bootTickMsg(t)
	})
}

// bootStepsFor builds the checklist for this launch. The identity
// step is already done: main.go finished loading it before the TUI
// started. The connection step is decided here too - a launch with no
// server to connect to, or no session to authenticate with, skips it
// outright rather than spinning against something that will never
// happen (see the same reasoning in Init's doc comment).
func bootStepsFor(session Session) []bootStep {
	identity := bootStep{
		id:      bootIdentity,
		label:   "Loading identity",
		state:   stepDone,
		elapsed: time.Since(appProcessStart),
	}

	connect := bootStep{id: bootConnect, label: "Connecting to synq-server"}
	switch {
	case session.API == nil:
		connect.state = stepSkipped
		connect.detail = "SYNQ_SERVER_URL not set"
	case session.AccessToken == "":
		connect.state = stepSkipped
		connect.detail = "not logged in"
	}

	return []bootStep{identity, connect}
}

// finishBootStep records how the step id ended. The first result wins:
// a step that already settled (a failure reported before the success
// event it raced with, say) keeps what it has, so the checklist can't
// flip from ✗ to ✓ after the user has read it.
func (m *Model) finishBootStep(id string, state bootStepState, detail string) {
	if m.bootStart.IsZero() {
		return
	}
	for i := range m.bootSteps {
		if m.bootSteps[i].id != id {
			continue
		}
		step := &m.bootSteps[i]
		if step.state != stepPending {
			return
		}
		step.state = state
		step.detail = detail
		step.elapsed = time.Since(m.bootStart)
		return
	}
}

// bootStepsSettled reports whether every step has reached a final
// state - the moment the closing beat (bootMinTicks) starts counting.
func (m Model) bootStepsSettled() bool {
	for _, step := range m.bootSteps {
		if step.state == stepPending {
			return false
		}
	}
	return true
}

// handleBootTick advances the spinner and decides whether the splash
// has earned the right to end: everything settled plus a short hold,
// or the hard cap when the connection step never answers.
func (m Model) handleBootTick() (tea.Model, tea.Cmd) {
	if !m.booting {
		// A stray tick arriving after boot ended (e.g. the user skipped
		// it by pressing a key) - do nothing, and critically, don't
		// requeue another tick, or it would tick forever in the
		// background.
		return m, nil
	}

	m.spinnerFrame = (m.spinnerFrame + 1) % len(spinnerFrames)
	m.bootTickCount++

	if m.bootStepsSettled() {
		if m.bootSettledAt < 0 {
			m.bootSettledAt = m.bootTickCount
		}
		if m.bootTickCount-m.bootSettledAt >= bootMinTicks {
			m.booting = false
			return m, nil
		}
	}
	if m.bootTickCount >= bootMaxTicks {
		m.booting = false
		return m, nil
	}
	return m, bootTick()
}

// renderBoot draws the startup splash: the Synq header, the subtitle,
// and the live checklist - one line per launch step with its outcome
// and elapsed time - centered in the terminal, with a skip hint.
func (m Model) renderBoot() string {
	header := lipgloss.NewStyle().
		Bold(true).
		Foreground(m.theme.Palette.Accent).
		Background(m.theme.Palette.Background).
		Render("SYNQ")

	subtitle := m.theme.Muted.Render("terminal-native developer network")

	lines := []string{header, "", subtitle, ""}
	for _, step := range m.bootSteps {
		lines = append(lines, m.renderBootStep(step))
	}
	lines = append(lines, "", m.theme.Muted.Render("any key to skip"))

	return placeWithBackground(strings.Join(lines, "\n"), m.viewWidth(), m.viewHeight(), m.theme.Palette.Background)
}

// renderBootStep renders one checklist line: mark, padded label, then
// the step's outcome - elapsed time, failure, or skip reason.
func (m Model) renderBootStep(step bootStep) string {
	const labelPad = 30

	// Every run of the line carries the theme background explicitly,
	// the same rule renderHeader's doc comment gives for the chrome:
	// an unstyled gap between two styled spans shows the terminal's
	// raw background instead of the theme's.
	plain := lipgloss.NewStyle().
		Foreground(m.theme.Palette.Foreground).
		Background(m.theme.Palette.Background)
	lead := plain.Render("  ")
	label := plain.Render(fmt.Sprintf("%-*s", labelPad, step.label))

	switch step.state {
	case stepDone:
		mark := lipgloss.NewStyle().
			Foreground(m.theme.Palette.Accent).
			Background(m.theme.Palette.Background).
			Render("✓")
		return lead + mark + plain.Render(" ") + label + plain.Render(" ") + m.theme.Muted.Render(formatElapsed(step.elapsed))
	case stepFailed:
		mark := lipgloss.NewStyle().
			Foreground(m.theme.Palette.Error).
			Background(m.theme.Palette.Background).
			Render("✗")
		return lead + mark + plain.Render(" ") + label + plain.Render(" ") + m.theme.Error.Render(step.detail)
	case stepSkipped:
		return lead + m.theme.Muted.Render("-") + plain.Render(" ") + label + plain.Render(" ") +
			m.theme.Muted.Render("skipped - "+step.detail)
	default:
		return lead + m.theme.StatusBar.Render(spinnerFrames[m.spinnerFrame]) + plain.Render(" ") + label +
			plain.Render(" ") + m.theme.Muted.Render("working…")
	}
}

// formatElapsed renders a boot step's duration the way a checklist
// wants it: whole milliseconds under a second, tenths above.
func formatElapsed(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return "<1ms"
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	default:
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
}

// viewWidth/viewHeight are the render dimensions View already
// defaults centrally - kept as helpers so renderBoot and future
// full-screen overlays agree on the same fallbacks.
func (m Model) viewWidth() int {
	if m.width <= 0 {
		return 80
	}
	return m.width
}

func (m Model) viewHeight() int {
	if m.height <= 0 {
		return 24
	}
	return m.height
}

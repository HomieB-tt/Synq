package app

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/HomieB-tt/synq/internal/api"
	"github.com/HomieB-tt/synq/internal/crypto"
	"github.com/HomieB-tt/synq/internal/db"
	"github.com/HomieB-tt/synq/internal/ws"
)

// keyPress is any dismiss key - the splash skip and the help panel
// both close on whatever the user pressed first.
func keyPress() tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: tea.KeyEsc}
}

// keyPressWith builds a printable key like "?" - Key.String returns
// Text for those, which is what Update switches on.
func keyPressWith(text string) tea.KeyPressMsg {
	return tea.KeyPressMsg{Text: text}
}

// newBootModel builds a Model through New (the real constructor), so
// the checklist is populated exactly as it is on a real launch.
func newBootModel(t *testing.T, session Session) Model {
	t.Helper()
	id, err := crypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	return New(id, newTestKeyStore(t), session)
}

// tickBoot delivers one splash tick without executing the returned
// tea.Tick command - the test drives time itself.
func tickBoot(t *testing.T, m Model) Model {
	t.Helper()
	next, _ := m.Update(bootTickMsg(time.Now()))
	got, ok := next.(Model)
	if !ok {
		t.Fatalf("Update(bootTickMsg) returned %T, want Model", next)
	}
	return got
}

// runBootTicks ticks until the splash ends (or the hard cap, which
// should never be what decides a settled launch).
func runBootTicks(t *testing.T, m Model) Model {
	t.Helper()
	for i := 0; i < bootMaxTicks+2 && m.booting; i++ {
		m = tickBoot(t, m)
	}
	return m
}

// The connection step is the whole point of the checklist being
// event-driven: it ends when the socket says connected, and the splash
// follows it down after the closing beat.
func TestBootWaitsForTheConnectionThenEnds(t *testing.T) {
	session := Session{API: api.NewClient("http://example.invalid"), AccessToken: "tok"}
	m := newBootModel(t, session)
	if !m.booting {
		t.Fatal("booting = false, want the splash up on a connecting launch")
	}
	if got := m.bootSteps[1].state; got != stepPending {
		t.Fatalf("connect step state = %v, want pending", got)
	}

	// Still pending: ticks spin, splash stays.
	m = tickBoot(t, m)
	m = tickBoot(t, m)
	if !m.booting {
		t.Fatal("splash ended while the connection step was still pending")
	}

	// The socket reports success - that's what settles the step. The
	// status arrives on the client Update is already listening to
	// (wsClientReadyMsg installed it, as on a real launch), since the
	// wsStatusMsg handler returns early without one.
	client, err := ws.NewClient("ws://example.invalid/ws")
	if err != nil {
		t.Fatalf("ws.NewClient: %v", err)
	}
	ready, _ := m.Update(wsClientReadyMsg{client: client})
	m = ready.(Model)

	next, _ := m.Update(wsStatusMsg(ws.StatusConnected))
	m = next.(Model)
	if m.bootSteps[1].state != stepDone {
		t.Fatalf("connect step state = %v, want done", m.bootSteps[1].state)
	}

	before := m.bootTickCount
	m = runBootTicks(t, m)
	if m.booting {
		t.Fatal("splash never ended after everything settled")
	}
	if held := m.bootTickCount - before; held < bootMinTicks {
		t.Errorf("settled checklist shown for %d ticks, want at least %d", held, bootMinTicks)
	}

	out := m.renderBoot()
	if !strings.Contains(out, "✓") {
		t.Errorf("renderBoot = %q, want a ✓ for the settled steps", out)
	}
	if !strings.Contains(out, "ms") && !strings.Contains(out, "s") {
		t.Errorf("renderBoot = %q, want elapsed times", out)
	}
}

// A launch with no server to reach skips the connection step instead
// of spinning against it - and skips to the TUI fast.
func TestBootSkipsConnectionWithoutAServer(t *testing.T) {
	m := newBootModel(t, Session{})
	if !m.booting {
		t.Fatal("booting = false, want the splash up by default")
	}
	if got := m.bootSteps[1].state; got != stepSkipped {
		t.Fatalf("connect step state = %v, want skipped", got)
	}
	if !strings.Contains(m.renderBoot(), "skipped") {
		t.Errorf("renderBoot = %q, want the skip visible", m.renderBoot())
	}

	m = runBootTicks(t, m)
	if m.booting {
		t.Fatal("splash never ended on a launch with nothing to connect to")
	}
}

// A connection error is the failure case the old fixed sequence could
// only pretend to handle: it shows the reason, and the splash still
// ends rather than trapping the user in an error frame.
func TestBootFailureIsShownAndStillEnds(t *testing.T) {
	session := Session{API: api.NewClient("http://example.invalid"), AccessToken: "tok"}
	m := newBootModel(t, session)

	next, _ := m.Update(wsConnectErrorMsg("SYNQ_SERVER_URL is invalid: no scheme"))
	m = next.(Model)
	if m.bootSteps[1].state != stepFailed {
		t.Fatalf("connect step state = %v, want failed", m.bootSteps[1].state)
	}
	out := m.renderBoot()
	if !strings.Contains(out, "✗") || !strings.Contains(out, "SYNQ_SERVER_URL is invalid") {
		t.Errorf("renderBoot = %q, want the ✗ and the reason", out)
	}

	m = runBootTicks(t, m)
	if m.booting {
		t.Fatal("splash never ended after a failed connection")
	}
}

// A dial that never answers must not hold the UI hostage: the cap ends
// the splash, and the connection keeps going in the background.
func TestBootCapEndsWhenTheConnectionNeverAnswers(t *testing.T) {
	session := Session{API: api.NewClient("http://example.invalid"), AccessToken: "tok"}
	m := newBootModel(t, session)

	for i := 0; i < bootMaxTicks+2 && m.booting; i++ {
		m = tickBoot(t, m)
	}
	if m.booting {
		t.Fatal("splash still up after the hard cap - it would block the UI forever")
	}
	if m.bootSteps[1].state != stepPending {
		t.Errorf("connect step state = %v, want still pending (it settles later, in the background)", m.bootSteps[1].state)
	}
}

// The first result wins: a success event arriving after a failure has
// already been shown must not repaint the checklist.
func TestBootStepKeepsTheFirstResult(t *testing.T) {
	session := Session{API: api.NewClient("http://example.invalid"), AccessToken: "tok"}
	m := newBootModel(t, session)

	m.finishBootStep(bootConnect, stepFailed, "first failure")
	m.finishBootStep(bootConnect, stepDone, "")
	if m.bootSteps[1].state != stepFailed || m.bootSteps[1].detail != "first failure" {
		t.Errorf("connect step = %+v, want the first result kept", m.bootSteps[1])
	}
}

// :boot off is a preference, checked when New builds the model - it
// can't be switched mid-session (the splash is already behind us by
// then), which the command's wording says out loud.
func TestBootPreferenceDisablesTheSplashOnNextLaunch(t *testing.T) {
	ks := newTestKeyStore(t)
	id, err := crypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}

	m := New(id, ks, Session{})
	if result, _, _ := m.runCommand("boot off"); !strings.Contains(result, "next launch") {
		t.Fatalf("boot off result = %q, want it to defer to the next launch", result)
	}
	if v, err := ks.LoadPreference(db.PrefBoot); err != nil || v != "off" {
		t.Fatalf("PrefBoot = (%q, %v), want off", v, err)
	}

	relaunched := New(id, ks, Session{})
	if relaunched.booting || relaunched.bootEnabled {
		t.Errorf("booting=%v bootEnabled=%v, want the splash off after :boot off", relaunched.booting, relaunched.bootEnabled)
	}
	if _, cmd := relaunched.Update(bootTickMsg(time.Now())); cmd != nil {
		t.Error("a disabled splash still scheduled a tick - it would run forever in the background")
	}

	if result, _, _ := relaunched.runCommand("boot"); !strings.Contains(result, "off") {
		t.Errorf(":boot status = %q, want it to report off", result)
	}
}

func TestBootCommandRejectsBadArguments(t *testing.T) {
	m := newBootModel(t, Session{})
	if result, _, _ := m.runCommand("boot maybe"); !strings.Contains(result, "Usage") {
		t.Errorf("result = %q, want usage", result)
	}
	if result, _, _ := m.runCommand("boot"); !strings.Contains(result, "Usage") {
		t.Errorf("status result = %q, want usage alongside the state", result)
	}
}

// The help panel is the onboarding safety net: reachable by key and
// by command, and it lists the commands that exist.
func TestHelpOpensByCommandAndClosesOnAnyKey(t *testing.T) {
	m := newBootModel(t, Session{})
	m.booting = false

	if result, quit, cmd := m.runCommand("help"); result != "" || quit || cmd != nil {
		t.Fatalf(":help returned (%q, %v, %v), want it to just open the panel", result, quit, cmd)
	}
	if !m.helpOpen {
		t.Fatal(":help didn't open the panel")
	}

	out := m.renderHelp()
	for _, want := range []string{":chat", ":accept", ":verify", ":boot", "?", "q / Ctrl+C"} {
		if !strings.Contains(out, want) {
			t.Errorf("renderHelp is missing %q", want)
		}
	}

	next, _ := m.Update(keyPress())
	if got := next.(Model); got.helpOpen {
		t.Error("any key didn't close the help panel")
	}
}

func TestHelpOpensByQuestionMarkKey(t *testing.T) {
	m := newBootModel(t, Session{})
	m.booting = false

	next, _ := m.Update(keyPressWith("?"))
	if got := next.(Model); !got.helpOpen {
		t.Fatal("'?' didn't open the help panel")
	}
}

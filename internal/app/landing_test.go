package app

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/HomieB-tt/synq/internal/ui/banner"
	"github.com/HomieB-tt/synq/internal/ui/styles"
)

func landingKeys(m landingModel, keys ...tea.KeyPressMsg) (landingModel, tea.Cmd) {
	var cmd tea.Cmd
	for _, k := range keys {
		next, c := m.Update(k)
		m, cmd = next.(landingModel), c
	}
	return m, cmd
}

func TestLandingKeysChooseAndQuit(t *testing.T) {
	m := newLandingModel(styles.Default(), true)
	for key, want := range map[string]LandingChoice{"1": LandingGuest, "2": LandingCreate, "3": LandingQuit, "q": LandingQuit} {
		got, cmd := landingKeys(m, tea.KeyPressMsg{Text: key, Code: rune(key[0])})
		if !got.decided || got.choice != want || cmd == nil {
			t.Errorf("key %q: decided=%v choice=%v cmd=%v", key, got.decided, got.choice, cmd != nil)
		}
	}
}

func TestLandingArrowsMoveAndEnterSelects(t *testing.T) {
	m := newLandingModel(styles.Default(), true)
	got, _ := landingKeys(m, tea.KeyPressMsg{Code: tea.KeyDown}, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !got.decided || got.choice != LandingCreate {
		t.Errorf("down+enter: decided=%v choice=%v", got.decided, got.choice)
	}
	got, _ = landingKeys(m, tea.KeyPressMsg{Code: tea.KeyUp}, tea.KeyPressMsg{Code: tea.KeyEnter})
	if got.choice != LandingQuit {
		t.Errorf("up wraps to the last item, got %v", got.choice)
	}
	if got, _ = landingKeys(m, tea.KeyPressMsg{Code: tea.KeyEnter}); got.choice != LandingGuest {
		t.Errorf("enter on the first item = %v", got.choice)
	}
}

func TestLandingFillsTheTerminalExactly(t *testing.T) {
	for _, size := range [][2]int{{120, 40}, {80, 24}, {64, 24}, {60, 20}, {52, 14}, {40, 12}, {30, 8}, {100, 12}} {
		w, h := size[0], size[1]
		for _, static := range []bool{true, false} {
			m := newLandingModel(styles.Default(), static)
			m.frame = map[bool]int{true: 1 << 20, false: 40}[static]
			lines := strings.Split(m.render(w, h), "\n")
			if len(lines) != h {
				t.Errorf("%dx%d static=%v: %d lines", w, h, static, len(lines))
			}
			for i, l := range lines {
				if got := ansi.StringWidth(l); got != w {
					t.Errorf("%dx%d static=%v line %d is %d wide", w, h, static, i, got)
				}
			}
		}
	}
}

func TestLandingStaticShowsEverythingCentred(t *testing.T) {
	m := newLandingModel(styles.Default(), true)
	out := ansi.Strip(m.render(100, 30))
	for _, want := range []string{banner.Rows[0], "Say it. Then let it go.", "Browse the public feed as a guest", "Create your identity", "Quit"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, banner.Rows[0]) {
			if lead := len(l) - len(strings.TrimLeft(l, " ")); lead < 20 {
				t.Errorf("banner not centred: %d leading spaces", lead)
			}
		}
	}
}

func TestLandingAnimatesIn(t *testing.T) {
	m := newLandingModel(styles.Default(), false)
	early := ansi.Strip(m.render(100, 30))
	if strings.Contains(early, banner.Rows[0]) || strings.Contains(early, "Create your identity") {
		t.Error("frame 0 already shows the finished screen")
	}
	m.frame = landingRevealBy + 30
	late := ansi.Strip(m.render(100, 30))
	if !strings.Contains(late, "Create your identity") || !strings.Contains(late, "Quit") {
		t.Error("the menu never appeared")
	}
	next, cmd := m.Update(landingTickMsg{})
	if next.(landingModel).frame != m.frame+1 || cmd == nil {
		t.Error("a tick should advance the frame and schedule the next")
	}
	if cmd := newLandingModel(styles.Default(), true).Init(); cmd != nil {
		t.Error("the static screen should not tick")
	}
}

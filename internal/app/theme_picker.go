package app

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/HomieB-tt/synq/internal/db"
	"github.com/HomieB-tt/synq/internal/ui/styles"
)

// themePickerMaxVisible caps how many theme names are drawn in the
// popup at once. With more themes than this, the list scrolls, always
// keeping the highlighted entry in view - see themePickerWindow.
const themePickerMaxVisible = 10

// openThemePicker enters the live-preview theme picker (triggered by
// running `:theme` with no argument - see runCommand). It starts on
// whichever theme is currently active, so opening the picker never
// itself changes anything; only moving the selection or confirming
// with Enter does.
func (m *Model) openThemePicker() {
	m.themePickerNames = styles.Names()
	m.themePickerPrev = m.theme
	m.themePickerIndex = 0
	for i, name := range m.themePickerNames {
		if name == m.theme.Palette.Name {
			m.themePickerIndex = i
			break
		}
	}
	m.themePickerOpen = true
}

// updateThemePicker handles keys while the theme picker popup is open.
// It owns all key input the same way updateCommandMode does while the
// `:` prompt is open - normal-mode keys like "1" or "tab" are inert
// here rather than switching tabs out from under the popup.
func (m Model) updateThemePicker(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		// Cancel: restore whatever theme was active before the picker
		// opened, discarding the live preview entirely.
		m.theme = m.themePickerPrev
		m.themePickerOpen = false
		m.commandMsg = "Theme unchanged."
		return m, nil

	case "enter":
		name := m.themePickerNames[m.themePickerIndex]
		m.themePickerOpen = false
		if err := m.store.SavePreference(db.PrefTheme, name); err != nil {
			// The preview theme still applies for this session even if
			// saving it failed - just say so, rather than reverting a
			// choice the user just confirmed.
			m.commandMsg = fmt.Sprintf("Theme set to %s, but couldn't save it: %v", name, err)
			return m, nil
		}
		m.commandMsg = fmt.Sprintf("Theme set to %s.", name)
		return m, nil

	case "up", "k":
		m.moveThemePicker(-1)
	case "down", "j":
		m.moveThemePicker(1)
	case "pgup":
		m.moveThemePicker(-themePickerMaxVisible)
	case "pgdown":
		m.moveThemePicker(themePickerMaxVisible)
	case "home":
		m.setThemePickerIndex(0)
	case "end":
		m.setThemePickerIndex(len(m.themePickerNames) - 1)
	}

	return m, nil
}

// moveThemePicker shifts the highlighted entry by delta, wrapping
// around at either end. Every path that changes the selection goes
// through here (or setThemePickerIndex directly) so the live preview
// - re-deriving m.theme from the newly highlighted palette - can never
// be forgotten on some branch.
func (m *Model) moveThemePicker(delta int) {
	n := len(m.themePickerNames)
	if n == 0 {
		return
	}
	idx := (m.themePickerIndex + delta) % n
	if idx < 0 {
		idx += n
	}
	m.setThemePickerIndex(idx)
}

// setThemePickerIndex jumps the highlighted entry straight to idx
// (clamped in range) and applies that entry's palette as the live
// preview.
func (m *Model) setThemePickerIndex(idx int) {
	n := len(m.themePickerNames)
	if n == 0 {
		return
	}
	if idx < 0 {
		idx = 0
	} else if idx >= n {
		idx = n - 1
	}
	m.themePickerIndex = idx
	m.theme = styles.New(styles.All[m.themePickerNames[idx]])
}

// themePickerWindow returns the [start, end) slice bounds of
// themePickerNames that should currently be drawn, keeping the
// highlighted entry within view and scrolling only once the full list
// no longer fits in themePickerMaxVisible rows.
func (m Model) themePickerWindow() (start, end int) {
	n := len(m.themePickerNames)
	if n <= themePickerMaxVisible {
		return 0, n
	}
	start = m.themePickerIndex - themePickerMaxVisible/2
	if start < 0 {
		start = 0
	}
	end = start + themePickerMaxVisible
	if end > n {
		end = n
		start = end - themePickerMaxVisible
	}
	return start, end
}

// renderThemePicker draws the popup itself: a bordered box listing
// every theme name, with the currently-highlighted one marked. The
// caller (renderContent) places this centered within the content pane
// the same way renderBoot centers the startup splash. Everything
// around it (tab bar, borders, status bar) is already drawn using
// m.theme, which setThemePickerIndex keeps in sync with the highlight
// - so scrolling through this list is what re-skins the rest of the
// screen live.
//
// Every line's plain text is padded to a shared width *before* its
// style is applied and Rendered, rather than rendering the title, each
// row, and the footer independently (at their own different lengths)
// and stacking them with lipgloss.JoinVertical. JoinVertical pads
// narrower lines with plain, unstyled space - the same class of bug
// renderHeader already guards against for horizontal gaps - which is
// exactly what left a ragged, un-backgrounded edge down the right side
// of this popup.
func (m Model) renderThemePicker() string {
	start, end := m.themePickerWindow()

	const title = "Choose a theme"
	const footerText = "↑/↓ or j/k preview · pgup/pgdn · enter apply · esc cancel"

	type row struct {
		text      string
		highlight bool
	}

	var rows []row
	if start > 0 {
		rows = append(rows, row{text: fmt.Sprintf("  ↑ %d more", start)})
	}
	for i := start; i < end; i++ {
		name := m.themePickerNames[i]
		label := name
		if name == m.themePickerPrev.Palette.Name {
			label += " (current)"
		}
		if i == m.themePickerIndex {
			rows = append(rows, row{text: "› " + label, highlight: true})
		} else {
			rows = append(rows, row{text: "  " + label})
		}
	}
	if n := len(m.themePickerNames); end < n {
		rows = append(rows, row{text: fmt.Sprintf("  ↓ %d more", n-end)})
	}

	contentWidth := lipgloss.Width(title)
	if w := lipgloss.Width(footerText); w > contentWidth {
		contentWidth = w
	}
	for _, r := range rows {
		if w := lipgloss.Width(r.text); w > contentWidth {
			contentWidth = w
		}
	}

	rightPad := func(s string) string {
		if gap := contentWidth - lipgloss.Width(s); gap > 0 {
			s += strings.Repeat(" ", gap)
		}
		return s
	}

	lines := make([]string, 0, len(rows)+4)
	lines = append(lines, m.theme.Warning.Render(rightPad(title)), "")
	for _, r := range rows {
		if r.highlight {
			lines = append(lines, m.theme.TabActive.Render(rightPad(r.text)))
		} else {
			lines = append(lines, m.theme.Muted.Render(rightPad(r.text)))
		}
	}
	lines = append(lines, "", m.theme.Muted.Render(rightPad(footerText)))

	body := strings.Join(lines, "\n")

	return m.theme.Border.
		BorderForeground(m.theme.Palette.Accent).
		Padding(1, 3).
		Render(body)
}

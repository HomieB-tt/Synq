package app

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/HomieB-tt/synq/internal/transfer"
)

// Showing file-transfer progress. The transfer itself runs inside a
// tea.Cmd on its own goroutine and cannot send the UI a message per chunk,
// so it updates a transfer.Progress (a few atomic counters) and the UI
// polls it: a tick redraws the screen a few times a second for as long as
// any transfer is running, and the tick stops itself when none is.

const transferTickEvery = 250 * time.Millisecond

type transferTickMsg struct{}

func transferTickCmd() tea.Cmd {
	return tea.Tick(transferTickEvery, func(time.Time) tea.Msg { return transferTickMsg{} })
}

// activeTransfer is one upload or download in flight.
type activeTransfer struct {
	id    string // the attachment id of a download; "" for an upload
	label string // the file's name
	up    bool
	prog  *transfer.Progress
}

// beginTransfer registers a transfer and returns it along with the command
// that starts the redraw tick, if one isn't already running.
func (m *Model) beginTransfer(id, label string, up bool) (*activeTransfer, tea.Cmd) {
	t := &activeTransfer{id: id, label: sanitizeSingleLine(label), up: up, prog: transfer.NewProgress()}
	var tick tea.Cmd
	if len(m.transfers) == 0 {
		tick = transferTickCmd()
	}
	m.transfers = append(m.transfers, t)
	return t, tick
}

// endTransfer forgets a finished transfer (successful or not).
func (m *Model) endTransfer(t *activeTransfer) {
	for i, x := range m.transfers {
		if x == t {
			m.transfers = append(m.transfers[:i:i], m.transfers[i+1:]...)
			return
		}
	}
}

// transferFor finds the running download of an attachment.
func (m Model) transferFor(id string) *activeTransfer {
	for _, t := range m.transfers {
		if !t.up && t.id == id {
			return t
		}
	}
	return nil
}

// progressBar draws frac (0..1) as a bar width cells wide.
func progressBar(frac float64, width int) string {
	if width < 1 {
		return ""
	}
	filled := int(min(max(frac, 0), 1)*float64(width) + 0.5)
	return strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
}

// percentLabel is "43%" while the size is known, "" otherwise.
func percentLabel(p *transfer.Progress) string {
	f := p.Fraction()
	if f < 0 {
		return ""
	}
	return fmt.Sprintf("%d%%", int(f*100))
}

// transferStatus is the one-line status shown at the bottom while a
// transfer runs: direction, name, phase, a bar and the byte counts. With
// several running it shows the oldest and says how many more.
func (m Model) transferStatus(width int) string {
	if len(m.transfers) == 0 {
		return ""
	}
	t := m.transfers[0]
	arrow := "↓"
	if t.up {
		arrow = "↑"
	}
	phase, done, total := t.prog.Snapshot()

	line := fmt.Sprintf("%s %s  %s", arrow, t.label, phase)
	if total > 0 {
		line += fmt.Sprintf("  %s %s  %s of %s",
			progressBar(t.prog.Fraction(), 10), percentLabel(t.prog), humanSize(done), humanSize(total))
	} else {
		line += "…"
	}
	if extra := len(m.transfers) - 1; extra > 0 {
		line += fmt.Sprintf("  (+%d more)", extra)
	}
	return truncateToWidth(line, width)
}

// fileStateLabel is a received file's state for the file list, with the
// percentage while it downloads.
func (m Model) fileStateLabel(e *fileEntry) string {
	if e.state == fileDownloading {
		if t := m.transferFor(e.offer.AttachmentID); t != nil {
			phase, _, _ := t.prog.Snapshot()
			if p := percentLabel(t.prog); p != "" {
				return fmt.Sprintf("%s %s", phase, p)
			}
			return phase.String() + "…"
		}
	}
	return e.state.label()
}

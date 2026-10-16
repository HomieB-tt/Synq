package app

import (
	"errors"
	"strings"
	"testing"

	"github.com/HomieB-tt/synq/internal/transfer"
)

func TestOnlyTheFirstTransferStartsTheTick(t *testing.T) {
	m := chatViewModel(t, 100, 30)
	t1, tick1 := m.beginTransfer("", "a.txt", true)
	_, tick2 := m.beginTransfer("att-2", "b.txt", false)
	if tick1 == nil {
		t.Error("the first transfer should start the redraw tick")
	}
	if tick2 != nil {
		t.Error("a second transfer must not start a second tick chain")
	}
	m.endTransfer(t1)
	if len(m.transfers) != 1 || m.transfers[0].label != "b.txt" {
		t.Fatalf("transfers after ending the first: %+v", m.transfers)
	}
	m.endTransfer(t1) // already gone: harmless
	if len(m.transfers) != 1 {
		t.Fatal("ending a transfer twice removed another one")
	}
}

func TestTickKeepsRunningOnlyWhileTransfersDo(t *testing.T) {
	m := chatViewModel(t, 100, 30)
	if _, cmd := m.Update(transferTickMsg{}); cmd != nil {
		t.Error("the tick should stop when nothing is transferring")
	}
	m.beginTransfer("", "a.txt", true)
	if _, cmd := m.Update(transferTickMsg{}); cmd == nil {
		t.Error("the tick should re-arm while a transfer runs")
	}
}

func TestTransferStatusShowsDirectionPhaseBarAndBytes(t *testing.T) {
	m := chatViewModel(t, 100, 30)
	up, _ := m.beginTransfer("", "report.pdf", true)
	up.prog.Start(transfer.PhaseUploading, 2<<20)
	up.prog.Add(1 << 20)

	got := m.transferStatus(100)
	for _, want := range []string{"↑", "report.pdf", "uploading", "50%", "1.0 MB of 2.0 MB", "█████░░░░░"} {
		if !strings.Contains(got, want) {
			t.Errorf("status %q lacks %q", got, want)
		}
	}

	down, _ := m.beginTransfer("att", "photo.png", false)
	down.prog.Start(transfer.PhaseVerifying, 0) // no size known
	_ = down
	if got := m.transferStatus(100); !strings.Contains(got, "(+1 more)") {
		t.Errorf("status %q should mention the other transfer", got)
	}
	m.endTransfer(up)
	if got := m.transferStatus(100); !strings.Contains(got, "↓") || !strings.Contains(got, "verifying…") {
		t.Errorf("status %q", got)
	}
	m.endTransfer(m.transfers[0])
	if got := m.transferStatus(100); got != "" {
		t.Errorf("status with nothing running = %q", got)
	}
}

func TestTransferStatusFitsItsWidth(t *testing.T) {
	m := chatViewModel(t, 40, 20)
	tr, _ := m.beginTransfer("", strings.Repeat("long-name-", 10), true)
	tr.prog.Start(transfer.PhaseUploading, 1000)
	if got := plain(m.transferStatus(40)); len([]rune(got)) > 40 {
		t.Errorf("status is %d cells wide in a 40-wide pane: %q", len([]rune(got)), got)
	}
}

func TestProgressBarClampsAndFills(t *testing.T) {
	for frac, want := range map[float64]string{0: "░░░░", 0.5: "██░░", 1: "████", 7: "████", -3: "░░░░"} {
		if got := progressBar(frac, 4); got != want {
			t.Errorf("progressBar(%v, 4) = %q, want %q", frac, got, want)
		}
	}
	if progressBar(0.5, 0) != "" {
		t.Error("zero width should be empty")
	}
}

func TestBottomBarShowsTransferOverTheLastMessage(t *testing.T) {
	m := chatViewModel(t, 100, 30)
	m.commandMsg = "something older"
	tr, _ := m.beginTransfer("", "x.bin", true)
	tr.prog.Start(transfer.PhaseUploading, 100)
	for _, got := range []string{plain(m.renderBottomBar()), strings.Join(plainAll(m.bottomLines()), "\n")} {
		if !strings.Contains(got, "uploading") || strings.Contains(got, "something older") {
			t.Errorf("bottom bar = %q", got)
		}
	}
}

func plainAll(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = plain(l)
	}
	return out
}

func TestFileListShowsDownloadPercentage(t *testing.T) {
	m := chatViewModel(t, 100, 30)
	offer := &transfer.Offer{AttachmentID: "att-1", Name: "photo.png", Size: 100}
	e := &fileEntry{offer: offer, state: fileDownloading}
	tr, _ := m.beginTransfer("att-1", "photo.png", false)
	tr.prog.Start(transfer.PhaseDownloading, 200)
	tr.prog.Add(50)
	if got := m.fileStateLabel(e); got != "downloading 25%" {
		t.Errorf("label = %q", got)
	}
	e.state = fileReady
	if got := m.fileStateLabel(e); got != "ready" {
		t.Errorf("label = %q", got)
	}
}

// Finishing - success or failure - must always take the transfer off the
// list, or the status bar would show it forever.
func TestResultsEndTheirTransfer(t *testing.T) {
	m := chatViewModel(t, 100, 30)
	up, _ := m.beginTransfer("", "a.bin", true)
	next, _ := m.handleFileSendResult(fileSendResultMsg{err: errors.New("boom"), tr: up})
	m = next.(Model)
	if len(m.transfers) != 0 {
		t.Fatalf("a failed send left %d transfers", len(m.transfers))
	}

	down, _ := m.beginTransfer("att-9", "b.bin", false)
	next, _ = m.handleFileReceiveResult(fileReceiveResultMsg{id: "att-9", err: errors.New("boom"), tr: down})
	m = next.(Model)
	if len(m.transfers) != 0 {
		t.Fatalf("a failed download left %d transfers", len(m.transfers))
	}
}

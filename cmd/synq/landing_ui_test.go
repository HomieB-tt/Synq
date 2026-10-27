package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/HomieB-tt/synq/internal/ui/styles"
)

func TestLandingShowsTheMenuAndFitsTheTerminal(t *testing.T) {
	for _, width := range []int{50, 60, 64, 80, 120} {
		out := renderLanding(styles.Default(), width)
		plain := ansi.Strip(out)
		for _, want := range []string{"Browse the public feed as a guest", "Create your identity", "Quit"} {
			if !strings.Contains(plain, want) {
				t.Errorf("width %d: missing %q", width, want)
			}
		}
		for _, l := range strings.Split(plain, "\n") {
			if w := ansi.StringWidth(l); w > width {
				t.Errorf("width %d: line is %d wide: %q", width, w, l)
			}
		}
	}
}

func TestFirstRunHelpersPrintReadableBoxedText(t *testing.T) {
	var buf bytes.Buffer
	uiHeading(&buf, "Create your identity")
	uiNotice(&buf, "There is no recovery", []string{"Your passphrase encrypts your keys. " + strings.Repeat("word ", 30), "", "Back it up."})
	uiOK(&buf, "Identity created.")
	uiFail(&buf, "Incorrect passphrase.")
	uiHint(&buf, "a hint")
	plain := ansi.Strip(buf.String())
	for _, want := range []string{"Create your identity", "There is no recovery", "Back it up.", "✓ Identity created.", "✗ Incorrect passphrase.", "a hint"} {
		if !strings.Contains(plain, want) {
			t.Errorf("missing %q in:\n%s", want, plain)
		}
	}
	for _, l := range strings.Split(plain, "\n") {
		if w := ansi.StringWidth(l); w > 80 {
			t.Errorf("line is %d wide: %q", w, l)
		}
	}
}

func TestAskPromptKeepsTheLabelText(t *testing.T) {
	if got := ansi.Strip(uiAsk("Choose a username")); got != "Choose a username › " {
		t.Errorf("prompt = %q", got)
	}
}

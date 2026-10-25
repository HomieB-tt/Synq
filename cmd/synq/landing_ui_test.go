package main

import (
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

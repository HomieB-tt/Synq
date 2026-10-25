package banner

import (
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestBannerRowsAreTheSameWidth(t *testing.T) {
	for i, r := range Rows {
		if got := ansi.StringWidth(r); got != Width {
			t.Errorf("row %d is %d wide, want %d: %q", i, got, Width, r)
		}
	}
	if len(Rows) != 6 {
		t.Errorf("banner has %d rows", len(Rows))
	}
}

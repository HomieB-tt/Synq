package app

import (
	"strings"
	"testing"

	"github.com/HomieB-tt/synq/internal/api"
)

func TestFingerprintGroupsSplitsIntoFourDigitBlocks(t *testing.T) {
	key := []byte{0xa3, 0xf9, 0xc2, 0x1b, 0x77, 0xd0, 0x01}
	rows := fingerprintGroups(key, 2)
	want := []string{"a3f9 c21b", "77d0 01"}
	if strings.Join(rows, "|") != strings.Join(want, "|") {
		t.Errorf("rows = %q, want %q", rows, want)
	}
	if got := fingerprintGroups(key, 0); len(got) != 4 {
		t.Errorf("perRow 0 should behave as 1, got %d rows", len(got))
	}
	if got := fingerprintGroups(nil, 4); len(got) != 0 {
		t.Errorf("no key, no rows, got %q", got)
	}
}

func TestProfileShowsTheKeyInReadableBlocks(t *testing.T) {
	m := nodesViewModel(t, 80, 30)
	m.activeTab = tabProfile
	out := plain(m.renderProfile())
	rows := fingerprintGroups(m.identity.SigningPublic, 8)
	if !strings.Contains(out, rows[0]) {
		t.Errorf("profile should show the key's first row %q:\n%s", rows[0], out)
	}
}

func TestCategoryChipNeverEchoesUnknownText(t *testing.T) {
	m := nodesViewModel(t, 80, 24)
	if got := plain(m.categoryChip(api.CategoryProject)); !strings.Contains(got, "PROJECT") {
		t.Errorf("project chip = %q", got)
	}
	if got := plain(m.categoryChip("\x1b[2Jevil")); strings.Contains(got, "evil") || strings.Contains(got, "\x1b") {
		t.Errorf("unknown category leaked into the chip: %q", got)
	}
	if m.categoryColor(api.CategoryProject) == m.categoryColor("nope") {
		t.Error("an unknown category should not look like a known one")
	}
}

func TestFeedListUsesCategoryChips(t *testing.T) {
	m := filterModel()
	m.theme = nodesViewModel(t, 1, 1).theme
	m.width, m.height = 100, 30
	out := plain(m.renderFeed())
	for _, want := range []string{" PROJECT ", " HIRING ", " GENERAL "} {
		if !strings.Contains(out, want) {
			t.Errorf("feed list missing chip %q:\n%s", want, out)
		}
	}
	m.feedOpen = true
	post := plain(m.renderPost())
	if !strings.Contains(post, " GENERAL ") {
		t.Errorf("open post should lead with its chip:\n%s", post)
	}
}

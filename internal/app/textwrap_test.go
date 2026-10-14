package app

import (
	"reflect"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestWrapTextBasics(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		width int
		want  []string
	}{
		{"fits on one line", "hello world", 20, []string{"hello world"}},
		{"breaks at a space", "hello world foo", 11, []string{"hello world", "foo"}},
		{"collapses runs of spaces", "a    b", 10, []string{"a b"}},
		{"keeps blank lines", "a\n\nb", 10, []string{"a", "", "b"}},
		{"empty input is one empty line", "", 10, []string{""}},
		{"hard-breaks a word wider than the pane", "abcdefghij", 4, []string{"abcd", "efgh", "ij"}},
		{"flushes the line before a hard-broken word", "hi abcdefgh", 4, []string{"hi", "abcd", "efgh"}},
		{"width below 1 is treated as 1", "ab", 0, []string{"a", "b"}},
		{"wide characters count as two cells", "世界世界", 4, []string{"世界", "世界"}},
		{"a wide character in a one-cell pane still advances", "世界", 1, []string{"世", "界"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := wrapToWidth(c.in, c.width)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("wrapToWidth(%q, %d) = %q, want %q", c.in, c.width, got, c.want)
			}
		})
	}
}

// The property the Chat view actually depends on: no line is ever wider
// than the pane, whatever the input looks like.
func TestWrapTextNeverExceedsWidth(t *testing.T) {
	inputs := []string{
		"the quick brown fox jumps over the lazy dog",
		"averyveryverylongwordwithnospacesatalljustkeepsgoingandgoing",
		"mixed 世界 content with a-very-long-hyphenated-compound-word inside",
		"   leading and trailing   ",
		strings.Repeat("ab ", 50),
	}
	for _, in := range inputs {
		for width := 1; width <= 30; width++ {
			for _, line := range wrapToWidth(in, width) {
				// A single double-width rune in a one-cell pane is the
				// one case that can't fit; everything else must.
				if width == 1 && lipgloss.Width(line) == 2 {
					continue
				}
				if w := lipgloss.Width(line); w > width {
					t.Fatalf("wrapToWidth(%q, %d) produced %q (width %d)", in, width, line, w)
				}
			}
		}
	}
}

// Wrapping may only ever move words between lines, never lose, reorder
// or split them - as long as the pane is at least as wide as the longest
// word (below that, hard-breaking a word is the intended behaviour).
func TestWrapTextKeepsEveryWord(t *testing.T) {
	in := "one two three four five six seven eight nine ten"
	for width := 5; width <= 20; width++ {
		got := strings.Fields(strings.Join(wrapToWidth(in, width), " "))
		if !reflect.DeepEqual(got, strings.Fields(in)) {
			t.Errorf("width %d lost or reordered words: %q", width, got)
		}
	}
}

func TestTailByWidth(t *testing.T) {
	cases := []struct {
		in    string
		width int
		want  string
	}{
		{"hello", 10, "hello"},
		{"hello", 3, "llo"},
		{"hello", 0, ""},
		{"ab世界", 4, "世界"},
		{"ab世界", 3, "界"}, // the wide rune that doesn't fit is dropped whole
	}
	for _, c := range cases {
		if got := tailByWidth(c.in, c.width); got != c.want {
			t.Errorf("tailByWidth(%q, %d) = %q, want %q", c.in, c.width, got, c.want)
		}
	}
}

func TestTruncateToWidth(t *testing.T) {
	cases := []struct {
		in    string
		width int
		want  string
	}{
		{"hello", 10, "hello"},
		{"hello", 5, "hello"},
		{"hello world", 5, "hell…"},
		{"hello", 1, "…"},
		{"hello", 0, ""},
		{"世界世界", 5, "世界…"},
	}
	for _, c := range cases {
		got := truncateToWidth(c.in, c.width)
		if got != c.want {
			t.Errorf("truncateToWidth(%q, %d) = %q, want %q", c.in, c.width, got, c.want)
		}
		if c.width > 0 && lipgloss.Width(got) > c.width {
			t.Errorf("truncateToWidth(%q, %d) = %q is wider than %d", c.in, c.width, got, c.width)
		}
	}
}

func TestClipLines(t *testing.T) {
	if got := clipLines("a\nb\nc", 2); got != "a\nb" {
		t.Errorf("clipLines(3 lines, 2) = %q", got)
	}
	if got := clipLines("a\nb", 5); got != "a\nb" {
		t.Errorf("clipLines leaves a short body alone, got %q", got)
	}
	if got := clipLines("a\nb", 0); got != "" {
		t.Errorf("clipLines(_, 0) = %q, want empty", got)
	}
}

func TestPadTop(t *testing.T) {
	got := padTop([]string{"x"}, 3)
	if !reflect.DeepEqual(got, []string{"", "", "x"}) {
		t.Errorf("padTop = %q", got)
	}
	if got := padTop([]string{"a", "b", "c"}, 2); len(got) != 3 {
		t.Errorf("padTop must not trim, got %q", got)
	}
}

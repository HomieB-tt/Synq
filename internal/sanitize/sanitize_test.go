package sanitize

import (
	"strings"
	"testing"
)

func TestStripEscapesRemovesWholeSequences(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"colour", "\x1b[31mred\x1b[0m text", "red text"},
		{"bold and 256 colour", "\x1b[1;38;5;196mhot\x1b[m", "hot"},
		{"truecolour", "\x1b[38;2;10;20;30mx\x1b[0m", "x"},
		{"cursor and erase", "a\x1b[2J\x1b[Hb\x1b[3Ac", "abc"},
		{"private mode", "\x1b[?25lhidden cursor\x1b[?25h", "hidden cursor"},
		{"osc title with bel", "\x1b]0;pwned\x07after", "after"},
		{"osc title with st", "\x1b]2;pwned\x1b\\after", "after"},
		{"osc hyperlink", "\x1b]8;;http://evil\x1b\\link\x1b]8;;\x1b\\", "link"},
		{"dcs", "\x1bPq#0;2;0;0;0\x1b\\done", "done"},
		{"apc", "\x1b_Gf=100;AAAA\x1b\\done", "done"},
		{"charset select", "\x1b(Bplain", "plain"},
		{"single final", "a\x1bcb\x1b7c\x1b8d", "abcd"},
		{"c1 csi", "a\u009b31mb", "ab"},
		{"c1 osc", "a\u009d0;t\u009cb", "ab"},
		{"plain unicode survives", "héllo 日本語 ✓", "héllo 日本語 ✓"},
		{"newlines survive", "a\n\x1b[1mb\x1b[0m\nc", "a\nb\nc"},
		{"lone esc at the end", "text\x1b", "text"},
		{"esc before a control", "a\x1b\x07b", "a\x07b"},
	}
	for _, tc := range tests {
		if got := StripEscapes(tc.in); got != tc.want {
			t.Errorf("%s: StripEscapes(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

func TestUnterminatedStringSequenceCostsOnlyItsLine(t *testing.T) {
	got := StripEscapes("keep\n\x1b]0;never ends\nnext line\x1b_also open\nlast")
	if got != "keep\n\nnext line\nlast" {
		t.Errorf("got %q", got)
	}
}

func TestNoEscapeByteOrIntroducerSurvivesClean(t *testing.T) {
	nasty := "\x1b[2J\x1b]0;x\x07\x1b[31m\u009b31m\u009d0;y\u009c\x1bP1$r\x1b\\\x00\x07\x08\x0b\x0c\x7f"
	out := Clean("start " + nasty + " end")
	for _, r := range out {
		if r < 0x20 && r != '\n' || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			t.Fatalf("control character %U survived: %q", r, out)
		}
	}
	if !strings.HasPrefix(out, "start") || !strings.HasSuffix(out, "end") {
		t.Errorf("text around the escapes was lost: %q", out)
	}
	if strings.Contains(out, "[31m") || strings.Contains(out, "0;x") {
		t.Errorf("escape payload left as visible text: %q", out)
	}
}

func TestCleanHandlesLineEndingsAndProgressBars(t *testing.T) {
	if got := Clean("a\r\nb\r\n"); got != "a\nb" {
		t.Errorf("CRLF: %q", got)
	}
	if got := Clean("10%\r50%\r100%\ndone"); got != "100%\ndone" {
		t.Errorf("progress bar: %q", got)
	}
	if got := Clean("line\r"); got != "line" {
		t.Errorf("trailing CR: %q", got)
	}
}

func TestCleanTabsBlankEdgesAndInvalidUTF8(t *testing.T) {
	if got := Clean("\n\n\tcode\n\t\tmore\n\n"); got != "code\n        more" {
		t.Errorf("tabs/edges: %q", got)
	}
	if got := Clean("ok\xff\xfebad"); got != "ok\uFFFDbad" {
		t.Errorf("invalid UTF-8: %q", got)
	}
}

func TestCleanRemovesBidiOverrides(t *testing.T) {
	if got := Clean("a‮b⁦c⁩d"); got != "abcd" {
		t.Errorf("bidi controls survived: %q", got)
	}
}

func TestOneLineCollapsesWhitespace(t *testing.T) {
	if got := OneLine("  one \n\ttwo\x1b[1m   three\n"); got != "one two three" {
		t.Errorf("OneLine = %q", got)
	}
}

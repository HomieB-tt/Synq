package lineedit

import "testing"

// run feeds keys to a text with the cursor at the end, then returns the
// resulting text with a "|" marking the cursor, which makes expected
// values readable.
func run(t *testing.T, text string, multiline bool, keys ...string) string {
	t.Helper()
	back := 0
	for _, k := range keys {
		switch {
		case len(k) > 0 && k[0] == '+': // "+x" types x
			text, back = Insert(text, back, k[1:])
		default:
			var ok bool
			text, back, ok = Apply(text, back, k, multiline)
			if !ok {
				t.Fatalf("key %q not handled", k)
			}
		}
	}
	b, a := Split(text, back)
	return b + "|" + a
}

func TestMoveAndInsertInMiddle(t *testing.T) {
	// The reported bug: fix a typo without deleting everything after it.
	got := run(t, "helo world", false, "home", "right", "right", "right", "+l")
	if got != "hell|o world" {
		t.Fatalf("got %q", got)
	}
}

func TestBackspaceAndDeleteAtCursor(t *testing.T) {
	if got := run(t, "abcd", false, "left", "left", "backspace"); got != "a|cd" {
		t.Fatalf("backspace: %q", got)
	}
	if got := run(t, "abcd", false, "left", "left", "delete"); got != "ab|d" {
		t.Fatalf("delete: %q", got)
	}
}

func TestEdgesAreSafe(t *testing.T) {
	if got := run(t, "", false, "left", "backspace", "delete", "right", "ctrl+w", "ctrl+u", "ctrl+k"); got != "|" {
		t.Fatalf("empty: %q", got)
	}
	if got := run(t, "ab", false, "home", "left", "backspace"); got != "|ab" {
		t.Fatalf("at start: %q", got)
	}
	if got := run(t, "ab", false, "right", "delete"); got != "ab|" {
		t.Fatalf("at end: %q", got)
	}
}

func TestMultiByteIsNeverSplit(t *testing.T) {
	// "é" is 2 bytes, "😀" is 4: one cursor step each.
	if got := run(t, "aé😀b", false, "left", "left", "backspace"); got != "a|😀b" {
		t.Fatalf("got %q", got)
	}
	if got := run(t, "aé😀b", false, "home", "right", "delete"); got != "a|😀b" {
		t.Fatalf("got %q", got)
	}
	if got := run(t, "日本語", false, "left", "+x"); got != "日本x|語" {
		t.Fatalf("got %q", got)
	}
}

func TestWordJumps(t *testing.T) {
	if got := run(t, "one two  three", false, "ctrl+left"); got != "one two  |three" {
		t.Fatalf("back1: %q", got)
	}
	if got := run(t, "one two  three", false, "alt+b", "alt+b"); got != "one |two  three" {
		t.Fatalf("back2: %q", got)
	}
	if got := run(t, "one two  three", false, "home", "ctrl+right"); got != "one| two  three" {
		t.Fatalf("fwd: %q", got)
	}
	if got := run(t, "foo, bar", false, "alt+b"); got != "foo, |bar" {
		t.Fatalf("punct: %q", got)
	}
}

func TestKillKeys(t *testing.T) {
	if got := run(t, "one two three", false, "ctrl+w"); got != "one two |" {
		t.Fatalf("ctrl+w: %q", got)
	}
	if got := run(t, "one two three", false, "left", "left", "ctrl+w"); got != "one two |ee" {
		// cursor sits mid-"three": ctrl+w eats the part of the word before it
		t.Fatalf("ctrl+w mid: %q", got)
	}
	if got := run(t, "one two three", false, "alt+b", "ctrl+u"); got != "|three" {
		t.Fatalf("ctrl+u: %q", got)
	}
	if got := run(t, "one two three", false, "alt+b", "ctrl+k"); got != "one two |" {
		t.Fatalf("ctrl+k: %q", got)
	}
}

func TestHomeEndPerLine(t *testing.T) {
	text := "first\nsecond"
	if got := run(t, text, true, "home"); got != "first\n|second" {
		t.Fatalf("home: %q", got)
	}
	if got := run(t, text, true, "up", "end"); got != "first|\nsecond" {
		t.Fatalf("up,end: %q", got)
	}
	if got := run(t, text, true, "home", "ctrl+u"); got != "first\n|second" {
		t.Fatalf("ctrl+u at line start must not eat the line above: %q", got)
	}
}

func TestUpDownKeepColumn(t *testing.T) {
	text := "abcdef\nxy\nhijklm"
	// From the end of "hijklm" (col 6), Up lands at the end of the short
	// "xy" (col 2). There is no sticky goal column, so a second Up goes
	// to col 2 of "abcdef", not back out to col 6.
	if got := run(t, text, true, "up"); got != "abcdef\nxy|\nhijklm" {
		t.Fatalf("up: %q", got)
	}
	if got := run(t, text, true, "up", "up"); got != "ab|cdef\nxy\nhijklm" {
		t.Fatalf("up up: %q", got)
	}
	if got := run(t, text, true, "home", "right", "right", "up"); got != "abcdef\nxy|\nhijklm" {
		t.Fatalf("col: %q", got)
	}
	if got := run(t, text, true, "home", "right", "right", "up", "up", "down", "down"); got != "abcdef\nxy\nhi|jklm" {
		t.Fatalf("down: %q", got)
	}
	// Edges: Up on the first line goes to the start; Down on the last, to the end.
	if got := run(t, "ab\ncd", true, "up", "up"); got != "|ab\ncd" {
		t.Fatalf("first line up: %q", got)
	}
	if got := run(t, "ab\ncd", true, "up", "down", "down"); got != "ab\ncd|" {
		t.Fatalf("last line down: %q", got)
	}
}

func TestUpDownNotHandledInSingleLine(t *testing.T) {
	if _, _, ok := Apply("abc", 0, "up", false); ok {
		t.Fatal("up should fall through in a single-line box")
	}
	if _, _, ok := Apply("abc", 0, "a", true); ok {
		t.Fatal("plain letters are the caller's business")
	}
	if _, _, ok := Apply("abc", 0, "enter", true); ok {
		t.Fatal("enter is the caller's business")
	}
}

func TestStaleCursorIsClamped(t *testing.T) {
	// A draft that was cleared or replaced elsewhere leaves `back` stale.
	if got := Clamp("abc", 99); got != 3 {
		t.Fatalf("clamp high: %d", got)
	}
	if got := Clamp("abc", -4); got != 0 {
		t.Fatalf("clamp low: %d", got)
	}
	text, back := Insert("", 17, "hi")
	if text != "hi" || back != 0 {
		t.Fatalf("insert with stale back: %q %d", text, back)
	}
	b, a := Split("abc", 99)
	if b != "" || a != "abc" {
		t.Fatalf("split stale: %q %q", b, a)
	}
}

func cw(r rune) int {
	if r >= 0x3000 { // stand-in for wide CJK
		return 2
	}
	return 1
}

func TestWindow(t *testing.T) {
	// Fits: untouched.
	if b, a := Window("abc", "de", 10, cw); b != "abc" || a != "de" {
		t.Fatalf("fits: %q %q", b, a)
	}
	// Cursor at end: the tail, exactly like the old append-only box.
	if b, a := Window("0123456789", "", 4, cw); b != "6789" || a != "" {
		t.Fatalf("tail: %q %q", b, a)
	}
	// Mid-text: the cursor's neighbourhood, within the width.
	b, a := Window("0123456789", "abcdefghij", 9, cw)
	if cells(b, cw)+cells(a, cw) > 9 {
		t.Fatalf("too wide: %q %q", b, a)
	}
	if b == "" || a == "" {
		t.Fatalf("should show both sides: %q %q", b, a)
	}
	if b[len(b)-1] != '9' || a[0] != 'a' {
		t.Fatalf("not anchored on the cursor: %q %q", b, a)
	}
	// Wide runes are never cut in half or allowed to overflow.
	b, a = Window("日本語日本語", "日本語日本語", 7, cw)
	if cells(b, cw)+cells(a, cw) > 7 {
		t.Fatalf("wide overflow: %q %q", b, a)
	}
	if b, a := Window("abc", "def", 0, cw); b != "" || a != "" {
		t.Fatalf("zero width: %q %q", b, a)
	}
}

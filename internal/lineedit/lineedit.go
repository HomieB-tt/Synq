// Package lineedit is the editing core behind every text box in the TUI:
// the chat composer, the command palette and the Feed post/comment
// composer.
//
// It is deliberately free of Bubble Tea and Lip Gloss. The functions take
// the text and a cursor, and return the new text and cursor, so the rules
// (what Ctrl+W deletes, where Up lands) can be unit-tested without a
// terminal, and every box edits identically.
//
// The cursor is stored as `back`: how many runes lie between the cursor
// and the END of the text. That looks odd but has one useful property -
// 0 means "at the end", which is what a freshly assigned or cleared
// string wants, so code that sets a draft directly (sending clears it,
// the external editor replaces it) never has to remember to reposition a
// cursor. Out-of-range values are clamped on every use.
//
// Positions are runes, not bytes, so accented letters and emoji are never
// split. (A single on-screen emoji built from several runes - a family
// emoji, say - is several cursor steps; that is the same as most
// terminal editors.)
package lineedit

import (
	"unicode"
	"unicode/utf8"
)

// Clamp returns back limited to [0, runes in text].
func Clamp(text string, back int) int {
	n := utf8.RuneCountInString(text)
	switch {
	case back < 0:
		return 0
	case back > n:
		return n
	}
	return back
}

// Split returns the text before and after the cursor.
func Split(text string, back int) (before, after string) {
	rs := []rune(text)
	i := len(rs) - Clamp(text, back)
	return string(rs[:i]), string(rs[i:])
}

// Insert puts s at the cursor and leaves the cursor just after it.
func Insert(text string, back int, s string) (string, int) {
	before, after := Split(text, back)
	if s == "" {
		return text, utf8.RuneCountInString(after)
	}
	return before + s + after, utf8.RuneCountInString(after)
}

// Apply handles one editing or cursor-movement key, named the way Bubble
// Tea's KeyPressMsg.String() names it. handled is false for any other key
// (printable characters, Enter, Tab, ...), which the caller deals with.
// A key that is handled but has nothing to do - Left at the start - still
// reports true, so it does not fall through and get typed as text.
//
// multiline enables Up/Down between the lines of text; in a single-line
// box those keys are not handled.
func Apply(text string, back int, key string, multiline bool) (newText string, newBack int, handled bool) {
	rs := []rune(text)
	pos := len(rs) - Clamp(text, back)

	del := func(from, to int) {
		if from < to {
			rs = append(rs[:from:from], rs[to:]...)
			pos = from
		}
	}

	switch key {
	case "left", "ctrl+b":
		pos = max(pos-1, 0)
	case "right", "ctrl+f":
		pos = min(pos+1, len(rs))
	case "ctrl+left", "alt+left", "alt+b":
		pos = prevWord(rs, pos)
	case "ctrl+right", "alt+right", "alt+f":
		pos = nextWord(rs, pos)
	case "home", "ctrl+a":
		pos = lineStart(rs, pos)
	case "end", "ctrl+e":
		pos = lineEnd(rs, pos)
	case "up", "down":
		if !multiline {
			return text, back, false
		}
		dir := 1
		if key == "up" {
			dir = -1
		}
		pos = moveLine(rs, pos, dir)
	case "backspace", "ctrl+h":
		if pos > 0 {
			del(pos-1, pos)
		}
	case "delete", "ctrl+d":
		if pos < len(rs) {
			del(pos, pos+1)
		}
	case "ctrl+w", "alt+backspace":
		del(prevWord(rs, pos), pos)
	case "ctrl+u":
		del(lineStart(rs, pos), pos)
	case "ctrl+k":
		del(pos, lineEnd(rs, pos))
	default:
		return text, back, false
	}
	return string(rs), len(rs) - pos, true
}

// Window picks what to show of a single-line box that is narrower than
// its text. before and after are the text either side of the cursor;
// width is the room in terminal cells and cw measures one rune. The
// result always includes the cursor's neighbourhood: with the cursor at
// the end it is the tail of the text (the old behaviour), and mid-text it
// keeps up to a third of the box for what follows the cursor so you can
// see what you are about to type in front of.
func Window(before, after string, width int, cw func(rune) int) (b, a string) {
	if width <= 0 {
		return "", ""
	}
	bw, aw := cells(before, cw), cells(after, cw)
	if bw+aw <= width {
		return before, after
	}
	keepAfter := min(aw, max(width/3, 1))
	b = tailCells(before, width-keepAfter, cw)
	a = headCells(after, width-cells(b, cw), cw)
	return b, a
}

func cells(s string, cw func(rune) int) int {
	n := 0
	for _, r := range s {
		n += cw(r)
	}
	return n
}

func tailCells(s string, width int, cw func(rune) int) string {
	rs := []rune(s)
	w, i := 0, len(rs)
	for i > 0 {
		rw := cw(rs[i-1])
		if w+rw > width {
			break
		}
		w += rw
		i--
	}
	return string(rs[i:])
}

func headCells(s string, width int, cw func(rune) int) string {
	w, end := 0, 0
	for i, r := range s {
		rw := cw(r)
		if w+rw > width {
			return s[:i]
		}
		w += rw
		end = i + utf8.RuneLen(r)
	}
	return s[:end]
}

func isWord(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' }

// prevWord is the start of the word at or before pos: skip any
// non-word characters behind the cursor, then the word itself.
func prevWord(rs []rune, pos int) int {
	for pos > 0 && !isWord(rs[pos-1]) {
		pos--
	}
	for pos > 0 && isWord(rs[pos-1]) {
		pos--
	}
	return pos
}

// nextWord is the end of the word at or after pos.
func nextWord(rs []rune, pos int) int {
	for pos < len(rs) && !isWord(rs[pos]) {
		pos++
	}
	for pos < len(rs) && isWord(rs[pos]) {
		pos++
	}
	return pos
}

func lineStart(rs []rune, pos int) int {
	for pos > 0 && rs[pos-1] != '\n' {
		pos--
	}
	return pos
}

func lineEnd(rs []rune, pos int) int {
	for pos < len(rs) && rs[pos] != '\n' {
		pos++
	}
	return pos
}

// moveLine moves to the same column on the previous (dir<0) or next line.
// Up on the first line goes to the start of the text and Down on the last
// goes to the end, which is what most editors do.
func moveLine(rs []rune, pos, dir int) int {
	start := lineStart(rs, pos)
	col := pos - start
	if dir < 0 {
		if start == 0 {
			return 0
		}
		prevEnd := start - 1 // the newline ending the previous line
		prevStart := lineStart(rs, prevEnd)
		return prevStart + min(col, prevEnd-prevStart)
	}
	end := lineEnd(rs, pos)
	if end >= len(rs) {
		return len(rs)
	}
	nextStart := end + 1
	nextEnd := lineEnd(rs, nextStart)
	return nextStart + min(col, nextEnd-nextStart)
}

// Package sanitize cleans text that came from somewhere Synq does not
// control - a pipe, a file - before it is stored or drawn.
//
// DESIGN.md section 6: content piped into `synq post` is stripped of raw
// ANSI and control escape sequences before it touches storage or the
// render loop, so piped text can never move the cursor, retitle the
// window, or otherwise drive the terminal.
package sanitize

import (
	"strings"
)

// StripEscapes removes terminal escape sequences from s, whole - not just
// the ESC byte, which would leave "[31m" behind as visible junk:
//
//   - CSI sequences (ESC [ ... final), colours, cursor movement, erase
//   - OSC strings (ESC ] ... BEL or ST), titles and hyperlinks
//   - DCS, SOS, PM and APC strings (ESC P, X, ^, _ ... ST)
//   - two- and three-byte forms (ESC c, ESC 7, ESC ( B, ...)
//   - the single-character C1 forms of the above (U+009B, U+009D, ...)
//
// A string sequence that never terminates is cut at the end of its line,
// so one stray introducer can cost a line of text, not the whole post.
// Other control characters are left for Clean to handle.
func StripEscapes(s string) string {
	rs := []rune(strings.ToValidUTF8(s, "�"))
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(rs); {
		switch r := rs[i]; r {
		case 0x1b:
			i = skipEscape(rs, i+1)
		case 0x9b:
			i = skipCSI(rs, i+1)
		case 0x9d:
			i = skipString(rs, i+1, true)
		case 0x90, 0x98, 0x9e, 0x9f:
			i = skipString(rs, i+1, false)
		default:
			b.WriteRune(r)
			i++
		}
	}
	return b.String()
}

// skipEscape returns the index just past the sequence whose ESC was at
// j-1.
func skipEscape(rs []rune, j int) int {
	if j >= len(rs) {
		return j
	}
	switch c := rs[j]; {
	case c == '[':
		return skipCSI(rs, j+1)
	case c == ']':
		return skipString(rs, j+1, true)
	case c == 'P' || c == 'X' || c == '^' || c == '_':
		return skipString(rs, j+1, false)
	case c >= 0x20 && c <= 0x2f: // intermediates, then a final byte
		for j < len(rs) && rs[j] >= 0x20 && rs[j] <= 0x2f {
			j++
		}
		if j < len(rs) && rs[j] >= 0x30 && rs[j] <= 0x7e {
			j++
		}
		return j
	case c >= 0x30 && c <= 0x7e: // a single final byte
		return j + 1
	}
	return j // ESC before something else: drop just the ESC
}

// skipCSI skips parameter bytes, intermediate bytes and the final byte.
// A byte that cannot be part of a CSI sequence ends it, unconsumed.
func skipCSI(rs []rune, j int) int {
	for j < len(rs) && rs[j] >= 0x30 && rs[j] <= 0x3f {
		j++
	}
	for j < len(rs) && rs[j] >= 0x20 && rs[j] <= 0x2f {
		j++
	}
	if j < len(rs) && rs[j] >= 0x40 && rs[j] <= 0x7e {
		j++
	}
	return j
}

// skipString skips a string sequence up to and including its terminator:
// ST (ESC \ or U+009C), or BEL when bel is set (OSC only). It stops
// before a newline, and before an ESC that does not begin ST.
func skipString(rs []rune, j int, bel bool) int {
	for j < len(rs) {
		switch r := rs[j]; {
		case r == 0x07 && bel:
			return j + 1
		case r == 0x9c:
			return j + 1
		case r == '\n':
			return j
		case r == 0x1b:
			if j+1 < len(rs) && rs[j+1] == '\\' {
				return j + 2
			}
			return j
		}
		j++
	}
	return j
}

// Clean makes untrusted text safe to store and draw as a multi-line
// body: escape sequences are removed (StripEscapes), CRLF and lone CR
// are handled the way a terminal would show them (a progress bar that
// rewrites its line keeps its last state), tabs become four spaces, and
// every other control character is dropped, along with the bidirectional
// overrides that can make text read differently from how it is stored.
// Newlines are kept, and the result has no leading or trailing blank
// lines.
func Clean(s string) string {
	s = StripEscapes(s)
	s = strings.ReplaceAll(s, "\r\n", "\n")

	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = lastSegment(l)
	}
	s = strings.Join(lines, "\n")

	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n':
			return r
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
			return -1
		case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
			return -1
		}
		return r
	}, strings.ReplaceAll(s, "\t", "    "))
	return strings.Trim(s, "\n \t")
}

// lastSegment is what a terminal would show for a line containing
// carriage returns: each one sends the cursor back to the start, so the
// last non-empty piece is what is left.
func lastSegment(line string) string {
	if !strings.Contains(line, "\r") {
		return line
	}
	segs := strings.Split(line, "\r")
	for i := len(segs) - 1; i >= 0; i-- {
		if segs[i] != "" {
			return segs[i]
		}
	}
	return ""
}

// OneLine is Clean for a single-line field such as a title: the same
// stripping, with every run of whitespace, newlines included, collapsed
// to one space.
func OneLine(s string) string {
	return strings.Join(strings.Fields(Clean(s)), " ")
}

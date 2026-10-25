// Package banner holds the SYNQ block-letter wordmark shared by the
// first-run screens.
package banner

// Rows is the SYNQ wordmark, one string per row, built letter by letter
// so each glyph can be checked on its own. (The earlier one-piece version
// drew the wrong letters.)
var Rows = func() []string {
	letters := [][]string{
		{ // S
			"███████╗",
			"██╔════╝",
			"███████╗",
			"╚════██║",
			"███████║",
			"╚══════╝",
		},
		{ // Y
			"██╗   ██╗",
			"╚██╗ ██╔╝",
			" ╚████╔╝ ",
			"  ╚██╔╝  ",
			"   ██║   ",
			"   ╚═╝   ",
		},
		{ // N
			"███╗   ██╗",
			"████╗  ██║",
			"██╔██╗ ██║",
			"██║╚██╗██║",
			"██║ ╚████║",
			"╚═╝  ╚═══╝",
		},
		{ // Q
			" ██████╗ ",
			"██╔═══██╗",
			"██║   ██║",
			"██║▄▄ ██║",
			"╚██████╔╝",
			" ╚══▀▀═╝ ",
		},
	}
	rows := make([]string, 6)
	for r := range rows {
		for _, l := range letters {
			rows[r] += l[r]
		}
	}
	return rows
}()

// Width is the display width of one banner row (every row is the
// same width; the test checks it).
const Width = 8 + 9 + 10 + 9

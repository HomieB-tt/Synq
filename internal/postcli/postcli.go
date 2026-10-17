// Package postcli is the pure half of `synq post`: argument parsing,
// reading the pipe, and turning raw bytes into a validated post. The
// terminal, key store and network halves live in cmd/synq.
package postcli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/HomieB-tt/synq/internal/api"
	"github.com/HomieB-tt/synq/internal/sanitize"
)

// Limits match the composer in the TUI (internal/app/feed.go) and the
// server.
const (
	MaxTitleRunes   = 120
	MaxContentRunes = 10000

	// maxInputBytes bounds what is read from the pipe, so
	// `cat huge.log | synq post` fails fast instead of buffering it.
	// Generous for 10000 runes of multi-byte text plus escape sequences.
	maxInputBytes = 256 * 1024
)

// Usage is printed for `synq post -h` and for argument errors.
const Usage = `Usage: command | synq post [-c category] [-t title]

Posts piped text to the Feed without opening the TUI.

  -c, --category  project, hiring or general (default general)
  -t, --title     post title (default: the first line of the input)

Escape sequences and control characters are stripped from the input
before anything is stored. Posts are limited to 10000 characters, so for
long output keep the part that matters:  tail -n 50 error.log | synq post
`

// Options are the parsed command-line options.
type Options struct {
	Category string // an api.Category* value
	Title    string // may be empty: derived from the content
}

// ParseArgs parses the arguments after "post". flag.ErrHelp is returned
// for -h; the caller prints Usage.
func ParseArgs(args []string) (Options, error) {
	var category, title string
	fs := flag.NewFlagSet("post", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&category, "category", "general", "")
	fs.StringVar(&category, "c", "general", "")
	fs.StringVar(&title, "title", "", "")
	fs.StringVar(&title, "t", "", "")
	if err := fs.Parse(args); err != nil {
		return Options{}, err
	}
	if fs.NArg() > 0 {
		return Options{}, fmt.Errorf("unexpected argument %q (the post text comes from stdin)", fs.Arg(0))
	}

	var cat string
	switch strings.ToLower(strings.TrimSpace(category)) {
	case "project":
		cat = api.CategoryProject
	case "hiring":
		cat = api.CategoryHiring
	case "general":
		cat = api.CategoryGeneral
	default:
		return Options{}, fmt.Errorf("unknown category %q (use project, hiring or general)", category)
	}
	return Options{Category: cat, Title: title}, nil
}

// ReadInput reads all of r, failing if it is larger than maxInputBytes.
func ReadInput(r io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxInputBytes+1))
	if err != nil {
		return "", fmt.Errorf("read stdin: %w", err)
	}
	if len(data) > maxInputBytes {
		return "", fmt.Errorf("input is larger than %d KiB; post the part that matters (tail -n 50 file | synq post)", maxInputBytes/1024)
	}
	return string(data), nil
}

// Build sanitizes the raw input and the optional title and checks the
// result against the post limits. With no title, the first line of the
// content is used, cut to MaxTitleRunes.
func Build(raw, title string) (cleanTitle, content string, err error) {
	content = sanitize.Clean(raw)
	if content == "" {
		return "", "", errors.New("nothing to post: the input was empty")
	}
	if n := len([]rune(content)); n > MaxContentRunes {
		return "", "", fmt.Errorf("the input is %d characters; the limit is %d (try tail -n 50 | synq post)", n, MaxContentRunes)
	}

	cleanTitle = sanitize.OneLine(title)
	if cleanTitle == "" && strings.TrimSpace(title) != "" {
		return "", "", errors.New("the title is empty once control characters are removed")
	}
	if cleanTitle == "" {
		first, _, _ := strings.Cut(content, "\n")
		cleanTitle = sanitize.OneLine(first)
		if r := []rune(cleanTitle); len(r) > MaxTitleRunes {
			cleanTitle = strings.TrimSpace(string(r[:MaxTitleRunes-1])) + "…"
		}
	} else if n := len([]rune(cleanTitle)); n > MaxTitleRunes {
		return "", "", fmt.Errorf("the title is %d characters; the limit is %d", n, MaxTitleRunes)
	}
	return cleanTitle, content, nil
}

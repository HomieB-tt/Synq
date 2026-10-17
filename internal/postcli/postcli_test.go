package postcli

import (
	"errors"
	"flag"
	"strings"
	"testing"

	"github.com/HomieB-tt/synq/internal/api"
)

func TestParseArgs(t *testing.T) {
	o, err := ParseArgs(nil)
	if err != nil || o.Category != api.CategoryGeneral || o.Title != "" {
		t.Fatalf("defaults: %+v %v", o, err)
	}
	o, err = ParseArgs([]string{"-c", "Project", "--title", "build broke"})
	if err != nil || o.Category != api.CategoryProject || o.Title != "build broke" {
		t.Fatalf("flags: %+v %v", o, err)
	}
	o, err = ParseArgs([]string{"--category=hiring"})
	if err != nil || o.Category != api.CategoryHiring {
		t.Fatalf("hiring: %+v %v", o, err)
	}
	if _, err = ParseArgs([]string{"-c", "memes"}); err == nil {
		t.Error("unknown category accepted")
	}
	if _, err = ParseArgs([]string{"some text"}); err == nil {
		t.Error("positional text accepted")
	}
	if _, err = ParseArgs([]string{"-h"}); !errors.Is(err, flag.ErrHelp) {
		t.Errorf("-h: %v", err)
	}
}

func TestReadInputRejectsOversizedInput(t *testing.T) {
	if got, err := ReadInput(strings.NewReader("small")); err != nil || got != "small" {
		t.Fatalf("small: %q %v", got, err)
	}
	if _, err := ReadInput(strings.NewReader(strings.Repeat("x", maxInputBytes+1))); err == nil {
		t.Error("oversized input accepted")
	}
	if _, err := ReadInput(strings.NewReader(strings.Repeat("x", maxInputBytes))); err != nil {
		t.Errorf("input at the limit rejected: %v", err)
	}
}

func TestBuildStripsEscapesAndDerivesTitle(t *testing.T) {
	title, content, err := Build("\x1b[31merror:\x1b[0m disk full\n\x1b]0;x\x07at line 2\n", "")
	if err != nil {
		t.Fatal(err)
	}
	if title != "error: disk full" || content != "error: disk full\nat line 2" {
		t.Errorf("title %q content %q", title, content)
	}
}

func TestBuildUsesAndCleansExplicitTitle(t *testing.T) {
	title, _, err := Build("body", " my \x1b[1mtitle\x1b[0m\n")
	if err != nil || title != "my title" {
		t.Errorf("%q %v", title, err)
	}
	if _, _, err := Build("body", "\x1b[1m\x07"); err == nil {
		t.Error("title that is only escapes accepted")
	}
	if _, _, err := Build("body", strings.Repeat("t", MaxTitleRunes+1)); err == nil {
		t.Error("overlong title accepted")
	}
}

func TestBuildCutsLongFirstLineIntoTitle(t *testing.T) {
	title, _, err := Build(strings.Repeat("é", 300), "")
	if err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(title)); n != MaxTitleRunes {
		t.Errorf("title is %d runes, want %d", n, MaxTitleRunes)
	}
	if !strings.HasSuffix(title, "…") {
		t.Errorf("cut title not marked: %q", title)
	}
}

func TestBuildLimitsAndEmptyInput(t *testing.T) {
	if _, _, err := Build("\x1b[2J\n \n", ""); err == nil {
		t.Error("input that is empty after cleaning accepted")
	}
	if _, _, err := Build(strings.Repeat("a", MaxContentRunes+1), ""); err == nil {
		t.Error("overlong content accepted")
	}
	if _, _, err := Build(strings.Repeat("a", MaxContentRunes), ""); err != nil {
		t.Errorf("content at the limit rejected: %v", err)
	}
}

package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HomieB-tt/synq/internal/transfer"
)

func TestHumanSize(t *testing.T) {
	cases := map[int64]string{0: "0 B", 1023: "1023 B", 1024: "1.0 KB", 1536: "1.5 KB", 5 << 20: "5.0 MB", 100 << 20: "100.0 MB", 3 << 30: "3.0 GB"}
	for in, want := range cases {
		if got := humanSize(in); got != want {
			t.Errorf("humanSize(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestWrapText(t *testing.T) {
	got := wrapText("one two three four five", 9)
	for _, l := range got {
		if len(l) > 9 {
			t.Errorf("line %q is wider than 9", l)
		}
	}
	if strings.Join(got, " ") != "one two three four five" {
		t.Errorf("wrapping lost words: %q", got)
	}
	if wrapText("", 10) != nil {
		t.Error("empty text should wrap to nothing")
	}
}

func TestSanitizeForTerminal(t *testing.T) {
	cases := map[string]string{
		"hello":                 "hello",
		"a\x1b[2Jb":             "a[2Jb",
		"title\x1b]0;pwned\x07": "title]0;pwned",
		"line1\nline2":          "line1 line2",
		"tab\there":             "tab here",
		"del\x7fete":            "delete",
		"c1\u009bcontrol":       "c1control",
		"émoji 📎 ok":            "émoji 📎 ok",
	}
	for in, want := range cases {
		if got := sanitizeForTerminal(in); got != want {
			t.Errorf("sanitizeForTerminal(%q) = %q, want %q", in, got, want)
		}
	}
}

func newFileTestModel(t *testing.T) *Model {
	t.Helper()
	m := &Model{}
	cache, err := transfer.NewCache(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m.fileCache = cache
	return m
}

func testEntry(t *testing.T, m *Model, name string, content string) *fileEntry {
	t.Helper()
	const id = "0a1b2c3d-4e5f-6071-8293-a4b5c6d7e8f9"
	dir, err := m.fileCache.Dir(id)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	e := &fileEntry{
		offer: &transfer.Offer{AttachmentID: id, Name: name, Size: int64(len(content))},
		state: fileReady,
		path:  path,
	}
	m.files = append(m.files, e)
	return e
}

func TestViewOpensTheCachedFileInTheDefaultApp(t *testing.T) {
	m := newFileTestModel(t)
	e := testEntry(t, m, "photo.png", "pixels")
	var opened string
	m.fileOpen = func(p string) error { opened = p; return nil }

	cmd := m.viewCmd(e)
	if cmd == nil {
		t.Fatal("viewCmd returned nothing for a viewable file")
	}
	msg := cmd().(fileViewResultMsg)
	if msg.err != nil || opened != e.path {
		t.Errorf("opened %q, err %v; want %q", opened, msg.err, e.path)
	}
}

func TestViewRefusesFilesAnOSWouldRun(t *testing.T) {
	m := newFileTestModel(t)
	e := testEntry(t, m, "installer.exe", "MZ")
	called := false
	m.fileOpen = func(string) error { called = true; return nil }

	if cmd := m.viewCmd(e); cmd != nil {
		cmd()
	}
	if called {
		t.Fatal("an executable was handed to the opener")
	}
	if !strings.Contains(e.note, "Save it") {
		t.Errorf("note = %q; the user should be told to save instead", e.note)
	}
}

func TestViewIsRefusedBeforeTheFileIsReady(t *testing.T) {
	m := newFileTestModel(t)
	e := testEntry(t, m, "a.txt", "x")
	e.state = fileOffered
	m.fileOpen = func(string) error { t.Fatal("opened a file that isn't downloaded"); return nil }
	if cmd := m.viewCmd(e); cmd != nil {
		t.Error("viewCmd ran for an undownloaded file")
	}
}

func TestSaveCopiesToTheConfiguredDirectory(t *testing.T) {
	m := newFileTestModel(t)
	e := testEntry(t, m, "report.pdf", "contents")
	m.saveDir = t.TempDir()

	msg := m.saveCmd(e)().(fileSaveResultMsg)
	if msg.err != nil {
		t.Fatal(msg.err)
	}
	if filepath.Dir(msg.path) != m.saveDir || filepath.Base(msg.path) != "report.pdf" {
		t.Errorf("saved to %q", msg.path)
	}
	if b, _ := os.ReadFile(msg.path); string(b) != "contents" {
		t.Errorf("saved file holds %q", b)
	}
	// The cached copy stays so the user can still view it.
	if _, err := os.Stat(e.path); err != nil {
		t.Errorf("cache copy vanished after save: %v", err)
	}
}

func TestSaveWithoutADirectoryAsksForOne(t *testing.T) {
	m := newFileTestModel(t)
	e := testEntry(t, m, "a.txt", "x")
	if cmd := m.saveCmd(e); cmd != nil {
		t.Error("saveCmd ran with no save directory")
	}
	if !strings.Contains(e.note, ":savedir") {
		t.Errorf("note = %q; it should point at :savedir", e.note)
	}
}

func TestIgnoreDeletesThePlaintext(t *testing.T) {
	m := newFileTestModel(t)
	e := testEntry(t, m, "secret.txt", "x")
	path := e.path

	m.ignoreCmd(e)
	if e.state != fileIgnored {
		t.Errorf("state = %v", e.state)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("plaintext survived ignore")
	}
}

func TestSavedirCommand(t *testing.T) {
	m := newFileTestModel(t)
	if got := m.savedirCommand(""); !strings.Contains(got, "No save folder") {
		t.Errorf("unset: %q", got)
	}
}

func TestQuitDeletesTheCache(t *testing.T) {
	m := newFileTestModel(t)
	testEntry(t, m, "a.txt", "x")
	root := m.fileCache.Root()
	if cmd := m.quitCmd(); cmd == nil {
		t.Fatal("quitCmd returned nil")
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Error("cache survived quit")
	}
	if len(m.files) != 0 {
		t.Error("file list survived quit")
	}
}

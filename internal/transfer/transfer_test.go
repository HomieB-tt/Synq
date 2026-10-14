package transfer

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HomieB-tt/synq/internal/filecrypt"
)

const goodID = "0a1b2c3d-4e5f-6071-8293-a4b5c6d7e8f9"

var goodHash = "sha256:" + strings.Repeat("ab", 32)

func TestOfferRoundTrip(t *testing.T) {
	key, _ := filecrypt.NewKey()
	body, err := EncodeOffer(goodID, "report.pdf", 1234, goodHash, key)
	if err != nil {
		t.Fatal(err)
	}
	if !IsOffer(body) {
		t.Fatal("encoded offer not recognised as an offer")
	}
	o, k, err := DecodeOffer(body)
	if err != nil {
		t.Fatal(err)
	}
	if o.AttachmentID != goodID || o.Name != "report.pdf" || o.Size != 1234 || o.CiphertextSHA256 != goodHash || *k != *key {
		t.Errorf("offer changed in transit: %+v", o)
	}
}

func TestOrdinaryTextIsNotAnOffer(t *testing.T) {
	for _, s := range []string{"", "hello", "synq-file-v1", "{\"id\":1}"} {
		if IsOffer([]byte(s)) {
			t.Errorf("%q taken for an offer", s)
		}
	}
}

func TestDecodeOfferRejectsBadFields(t *testing.T) {
	key, _ := filecrypt.NewKey()
	good, _ := EncodeOffer(goodID, "a.txt", 10, goodHash, key)
	if _, _, err := DecodeOffer(good); err != nil {
		t.Fatal(err)
	}
	mutate := func(from, to string) []byte {
		return bytes.Replace(good, []byte(from), []byte(to), 1)
	}
	cases := map[string][]byte{
		"path-traversal id": mutate(goodID, "../../../../etc/passwd-----------"),
		"short id":          mutate(goodID, "abc"),
		"negative size":     mutate(`"size":10`, `"size":-1`),
		"huge size":         mutate(`"size":10`, `"size":999999999999`),
		"bad hash":          mutate(goodHash, "sha256:nothex"),
		"short key":         mutate(`"key":"`, `"key":"AAAA`+`"}`+`,"x":"`),
		"unknown field":     append(append([]byte(nil), good[:len(good)-1]...), []byte(`,"extra":1}`)...),
		"not json":          append(append([]byte(nil), offerMagic...), []byte("{{{")...),
	}
	for name, body := range cases {
		if _, _, err := DecodeOffer(body); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestDecodeOfferSanitisesTheName(t *testing.T) {
	key, _ := filecrypt.NewKey()
	body, _ := EncodeOffer(goodID, `..\..\Windows\evil.exe`, 1, goodHash, key)
	o, _, err := DecodeOffer(body)
	if err != nil {
		t.Fatal(err)
	}
	if o.Name != "evil.exe" {
		t.Errorf("name = %q, want evil.exe", o.Name)
	}
}

func TestMaxPlainBytesFitsTheServerCap(t *testing.T) {
	m := MaxPlainBytes()
	if filecrypt.CiphertextSize(m) > MaxBlobBytes {
		t.Errorf("MaxPlainBytes %d seals to %d, over the cap", m, filecrypt.CiphertextSize(m))
	}
	if filecrypt.CiphertextSize(m+filecrypt.TagSize) <= MaxBlobBytes && m+filecrypt.TagSize <= MaxBlobBytes {
		// Not necessarily tight, but must be close.
		if MaxBlobBytes-filecrypt.CiphertextSize(m) > 4*filecrypt.TagSize {
			t.Errorf("MaxPlainBytes is far below the cap: %d", m)
		}
	}
}

func TestSafeName(t *testing.T) {
	long := strings.Repeat("a", 300)
	cases := []struct{ in, want string }{
		{"report.pdf", "report.pdf"},
		{"../../etc/passwd", "passwd"},
		{`C:\Users\x\evil.txt`, "evil.txt"},
		{"a/b/c.txt", "c.txt"},
		{"", "file"},
		{"..", "file"},
		{"...", "file"},
		{"  name.txt  ", "name.txt"},
		{"na\x00me\n.txt", "name.txt"},
		{`a<b>c:d"e|f?g*h.txt`, "a_b_c_d_e_f_g_h.txt"},
		{"photo\u202egpj.exe", "photogpj.exe"},
		{"CON", "_CON"},
		{"nul.txt", "_nul.txt"},
		{"trailing.dot.", "trailing.dot"},
		{".hidden", "hidden"},
		{"日本語.txt", "日本語.txt"},
	}
	for _, c := range cases {
		if got := SafeName(c.in); got != c.want {
			t.Errorf("SafeName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	got := SafeName(long + ".txt")
	if len(got) > maxNameBytes || !strings.HasSuffix(got, ".txt") {
		t.Errorf("long name = %q (%d bytes)", got, len(got))
	}
	// Truncation must not split a multi-byte character.
	got = SafeName(strings.Repeat("日", 100) + ".txt")
	if !strings.HasSuffix(got, ".txt") || strings.ContainsRune(got, '\uFFFD') {
		t.Errorf("multibyte truncation broke: %q", got)
	}
}

func TestSafeNameNeverEscapesADirectory(t *testing.T) {
	for _, in := range []string{"..", "../x", `..\x`, "/abs", "a/../../b", "~/x", "x/.."} {
		got := SafeName(in)
		if strings.ContainsAny(got, `/\`) || got == ".." || got == "." || got == "" {
			t.Errorf("SafeName(%q) = %q", in, got)
		}
	}
}

func TestCanView(t *testing.T) {
	for _, n := range []string{"a.pdf", "photo.JPG", "notes.txt", "song.mp3", "a.tar.gz"} {
		if !CanView(n) {
			t.Errorf("CanView(%q) = false", n)
		}
	}
	for _, n := range []string{"a.exe", "A.EXE", "run.sh", "x.bat", "x.lnk", "Thing.app", "noext", "x.js", "x.py", "macro.docm", "x.desktop", "x.ps1"} {
		if CanView(n) {
			t.Errorf("CanView(%q) = true, want false", n)
		}
	}
}

func TestCacheWipesLeftoversAndClosesClean(t *testing.T) {
	base := t.TempDir()
	stale := filepath.Join(base, "synq", "files", "old")
	if err := os.MkdirAll(stale, 0o700); err != nil {
		t.Fatal(err)
	}
	c, err := NewCache(base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("stale cache content survived NewCache")
	}
	st, _ := os.Stat(c.Root())
	if st.Mode().Perm() != 0o700 {
		t.Errorf("cache mode = %v, want 0700", st.Mode().Perm())
	}
	d, err := c.Dir(goodID)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(d, "x"), []byte("plain"), 0o600)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(c.Root()); !os.IsNotExist(err) {
		t.Error("Close left the cache behind")
	}
}

func TestCacheRejectsBadIDs(t *testing.T) {
	c, _ := NewCache(t.TempDir())
	for _, id := range []string{"", "..", "../x", "a/b", "short"} {
		if _, err := c.Dir(id); err == nil {
			t.Errorf("Dir(%q) accepted", id)
		}
		if err := c.Remove(id); err == nil {
			t.Errorf("Remove(%q) accepted", id)
		}
	}
}

func TestSaveCopyNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(t.TempDir(), "src")
	_ = os.WriteFile(src, []byte("new"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("original"), 0o600)

	p1, err := SaveCopy(dir, src, "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	p2, err := SaveCopy(dir, src, "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(p1) != "a (1).txt" || filepath.Base(p2) != "a (2).txt" {
		t.Errorf("names = %q, %q", filepath.Base(p1), filepath.Base(p2))
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "a.txt")); string(b) != "original" {
		t.Errorf("existing file was overwritten: %q", b)
	}
	if b, _ := os.ReadFile(p1); string(b) != "new" {
		t.Errorf("copy has %q", b)
	}
	if st, _ := os.Stat(p1); st.Mode().Perm()&0o111 != 0 {
		t.Errorf("saved file is executable: %v", st.Mode())
	}
}

func TestSaveCopySanitisesTheName(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(t.TempDir(), "src")
	_ = os.WriteFile(src, []byte("x"), 0o600)
	p, err := SaveCopy(dir, src, "../../escape.txt")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(p) != dir {
		t.Errorf("saved outside the save directory: %s", p)
	}
}

func TestValidateSaveDir(t *testing.T) {
	base := t.TempDir()

	got, err := ValidateSaveDir(filepath.Join(base, "new", "nested"))
	if err != nil {
		t.Fatalf("creatable dir rejected: %v", err)
	}
	if st, err := os.Stat(got); err != nil || !st.IsDir() {
		t.Error("directory was not created")
	}

	file := filepath.Join(base, "afile")
	_ = os.WriteFile(file, []byte("x"), 0o600)
	if _, err := ValidateSaveDir(file); err == nil {
		t.Error("a file was accepted as a save directory")
	}
	if _, err := ValidateSaveDir("   "); err == nil {
		t.Error("blank path accepted")
	}

	t.Setenv("HOME", base)
	got, err = ValidateSaveDir("~/Downloads/synq")
	if err != nil || got != filepath.Join(base, "Downloads", "synq") {
		t.Errorf("~ expansion: %q, %v", got, err)
	}
}

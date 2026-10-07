package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}

// The whole point of --forget is that the old store still exists
// somewhere: it gets renamed (never deleted), next to the original.
func TestArchiveKeyStoreMovesItAsideBesideItself(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "synq.db")
	writeFile(t, dbPath, "identity vault bytes")
	// Sidecars SQLite can leave after an interrupted run.
	writeFile(t, dbPath+"-wal", "wal bytes")
	writeFile(t, dbPath+"-shm", "shm bytes")

	when := time.Date(2026, time.October, 7, 15, 4, 5, 0, time.UTC)
	archive, err := archiveKeyStore(dbPath, when)
	if err != nil {
		t.Fatalf("archiveKeyStore: %v", err)
	}

	want := dbPath + ".bak-20261007-150405"
	if archive != want {
		t.Fatalf("archive = %q, want %q", archive, want)
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Errorf("original still present (err=%v), want it renamed away", err)
	}
	for _, path := range []string{archive, archive + "-wal", archive + "-shm"} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s missing: %v", filepath.Base(path), err)
		}
	}
	got, err := os.ReadFile(archive)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "identity vault bytes" {
		t.Errorf("archive contents = %q, want the original bytes untouched", got)
	}
}

// --forget on a device that never had an identity isn't an error -
// it just reports there's nothing to archive and continues.
func TestArchiveKeyStoreWithNothingToArchive(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "synq.db")

	archive, err := archiveKeyStore(missing, time.Now())
	if err != nil {
		t.Fatalf("archiveKeyStore: %v", err)
	}
	if archive != "" {
		t.Errorf("archive = %q, want empty when there is no store", archive)
	}
}

// Two --forgets inside the same second must not clobber each other:
// the first archive is someone's only copy of their identity.
func TestArchiveKeyStoreNeverOverwritesAnEarlierArchive(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "synq.db")
	when := time.Date(2026, time.October, 7, 15, 4, 5, 0, time.UTC)

	writeFile(t, dbPath, "first identity")
	first, err := archiveKeyStore(dbPath, when)
	if err != nil {
		t.Fatalf("archiveKeyStore (1): %v", err)
	}

	writeFile(t, dbPath, "second identity")
	second, err := archiveKeyStore(dbPath, when)
	if err != nil {
		t.Fatalf("archiveKeyStore (2): %v", err)
	}

	if first == second {
		t.Fatalf("both archives landed on %q - the second overwrote the first", first)
	}
	if !strings.HasSuffix(second, "-1") {
		t.Errorf("second archive = %q, want a collision suffix", second)
	}
	for _, path := range []string{first, second} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s missing: %v", filepath.Base(path), err)
		}
	}
}

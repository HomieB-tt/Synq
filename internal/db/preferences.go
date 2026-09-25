package db

import (
	"database/sql"
	"errors"
	"fmt"
)

// ErrPreferenceNotFound is returned by LoadPreference when the given
// key was never saved.
var ErrPreferenceNotFound = errors.New("db: preference not found")

const preferencesSchema = `
CREATE TABLE IF NOT EXISTS preferences (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
`

// PrefTheme is the preferences key under which the selected theme name
// (e.g. "dracula", "nord", "monokai") is stored.
const PrefTheme = "theme"

// PrefGitHubHandle is the preferences key under which a linked GitHub
// username is stored, once the optional verification flow (see
// synq-DESIGN.md section 9, synq-server-DESIGN.md section 1) succeeds.
const PrefGitHubHandle = "github_handle"

// PrefDisplayName is the preferences key under which a locally-chosen
// display name is stored (see internal/app's `:name` command).
//
// This is deliberately separate from, and not a substitute for, the
// server-backed username system described in DESIGN.md ("Nothing in
// the base design confirms that a public key returned for a username
// actually belongs to that person"): that username is assigned by
// synq-server at registration and is what other users actually see as
// a post's author. This preference is a purely local, client-side
// label - useful today as a stand-in while synq-server doesn't exist
// yet, but not something the client sends anywhere or that any other
// user will ever see.
const PrefDisplayName = "display_name"

// SavePreference stores a simple key/value setting, such as the
// selected theme. Unlike identity_vault, preferences are not secret and
// are stored in plaintext - there is nothing here that needs Argon2id
// or secretbox (see DESIGN.md section 1, which is specifically about
// the identity keys, not general settings).
func (k *KeyStore) SavePreference(key, value string) error {
	_, err := k.sqldb.Exec(
		`INSERT INTO preferences (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value,
	)
	if err != nil {
		return fmt.Errorf("db: save preference %q: %w", key, err)
	}
	return nil
}

// LoadPreference reads a previously saved preference. Returns
// ErrPreferenceNotFound if it was never set - callers should treat
// that as "use the built-in default", not as a failure.
func (k *KeyStore) LoadPreference(key string) (string, error) {
	var value string
	err := k.sqldb.QueryRow(`SELECT value FROM preferences WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrPreferenceNotFound
	}
	if err != nil {
		return "", fmt.Errorf("db: load preference %q: %w", key, err)
	}
	return value, nil
}

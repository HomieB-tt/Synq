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
// This is deliberately separate from, and never a substitute for,
// PrefUsername below. A display name is a free-form, trivially
// changeable local label with no meaning to synq-server at all; a
// username is the permanent, unique identifier synq-server actually
// knows the user by - the two must never be conflated or derived from
// one another. See PrefUsername's doc comment for the reasoning.
const PrefDisplayName = "display_name"

// PrefUsername is the preferences key under which the username this
// identity has registered with synq-server is stored, once
// registration (`:register <username>`, see internal/app) succeeds.
// Empty/unset means this identity has never registered.
//
// Unlike PrefDisplayName, this is not cosmetic and not meant to be
// casually changed: it's the primary key synq-server actually
// identifies this account by - for login, for `/users/{username}/keys`
// lookups other people use to find this identity, for everything
// `API.md` keys off a username for. Nothing in that API supports
// renaming a registered username once chosen, so the registration flow
// that writes this value is expected to say so plainly before it does.
//
// This value itself is not secret - usernames are public by design -
// so it lives here in plaintext like any other preference. What *is*
// secret is the refresh token issued alongside it at registration or
// login, which is sealed inside the encrypted identity vault instead
// (see crypto.SealIdentity's doc comment for why) rather than stored
// as a preference next to this.
const PrefUsername = "username"

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

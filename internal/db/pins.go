package db

import (
	"database/sql"
	"errors"
	"fmt"
)

// ErrPinNotFound is returned by LoadPinnedKey when this device has
// never pinned the given username. Callers should treat that as
// "trust on first use: pin whatever the lookup returned", not as a
// failure - see DESIGN.md section 2.
var ErrPinNotFound = errors.New("db: no pinned key for that username")

const pinnedKeysSchema = `
CREATE TABLE IF NOT EXISTS pinned_keys (
	username        TEXT PRIMARY KEY,
	box_pub_key     TEXT NOT NULL,
	signing_pub_key TEXT NOT NULL
);
`

// PinnedKey is the pair of public keys this device trusts for one
// username - the TOFU pin of DESIGN.md section 2.
//
// BoxPubKey (X25519) is what a mismatch actually blocks on: it is the
// static half of the handshake, so a substituted one is a man-in-the-
// middle position for every message in that thread. SigningPubKey
// (Ed25519) is stored alongside it so :verify can compute the
// fingerprint from the pin alone, without a fresh server lookup
// (which is exactly the thing a substituted key would be lying
// about).
//
// Like every other non-secret preference in this store, both values
// are stored in plaintext - they are public by definition (see
// PrefUsername's doc comment for the same reasoning).
type PinnedKey struct {
	Username      string
	BoxPubKey     string
	SigningPubKey string
}

// PinKey records pk as the keys this device trusts for pk.Username,
// replacing any previous pin for that username. Replacing is what
// :accept (the acknowledgement half of DESIGN.md section 2's hard
// warning) calls after the user has seen the change; every other
// caller is the first-use pin, which by definition has nothing to
// replace.
func (k *KeyStore) PinKey(pk PinnedKey) error {
	if pk.Username == "" {
		return errors.New("db: pin requires a username")
	}
	_, err := k.sqldb.Exec(
		`INSERT INTO pinned_keys (username, box_pub_key, signing_pub_key)
		 VALUES (?, ?, ?)
		 ON CONFLICT(username) DO UPDATE SET
			box_pub_key     = excluded.box_pub_key,
			signing_pub_key = excluded.signing_pub_key`,
		pk.Username, pk.BoxPubKey, pk.SigningPubKey,
	)
	if err != nil {
		return fmt.Errorf("db: pin key for %q: %w", pk.Username, err)
	}
	return nil
}

// LoadPinnedKey returns the keys pinned for username, or
// ErrPinNotFound if this device has never seen them.
func (k *KeyStore) LoadPinnedKey(username string) (PinnedKey, error) {
	var pk PinnedKey
	err := k.sqldb.QueryRow(
		`SELECT username, box_pub_key, signing_pub_key FROM pinned_keys WHERE username = ?`,
		username,
	).Scan(&pk.Username, &pk.BoxPubKey, &pk.SigningPubKey)
	if errors.Is(err, sql.ErrNoRows) {
		return PinnedKey{}, ErrPinNotFound
	}
	if err != nil {
		return PinnedKey{}, fmt.Errorf("db: load pinned key for %q: %w", username, err)
	}
	return pk, nil
}

// Package db manages local SQLite persistence for the Synq client: the
// encrypted identity vault (this file) and, later, the ephemeral,
// session-scoped message cache described in DESIGN.md section 4.
package db

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/HomieB-tt/synq/internal/crypto"
)

// ErrNoIdentity is returned by Load when no identity vault has been
// created on this device yet. Callers should treat this as "first
// launch, generate a new identity" (see DESIGN.md section 1), not as a
// failure.
var ErrNoIdentity = errors.New("db: no identity vault stored yet")

const keyStoreSchema = `
CREATE TABLE IF NOT EXISTS identity_vault (
	id                  INTEGER PRIMARY KEY CHECK (id = 1),
	salt                BLOB    NOT NULL,
	argon2_memory_kib   INTEGER NOT NULL,
	argon2_iterations   INTEGER NOT NULL,
	argon2_parallelism  INTEGER NOT NULL,
	nonce               BLOB    NOT NULL,
	ciphertext          BLOB    NOT NULL
);
`

// KeyStore persists a single passphrase-encrypted identity vault in the
// local SQLite database. Synq is single-device by design (DESIGN.md
// section 5), so there is ever only one row: id = 1.
type KeyStore struct {
	sqldb *sql.DB
}

// OpenKeyStore opens (creating if necessary) the SQLite database at
// path and ensures the key-store schema exists.
func OpenKeyStore(path string) (*KeyStore, error) {
	sqldb, err := sql.Open(driverName, path)
	if err != nil {
		return nil, fmt.Errorf("db: open sqlite at %s: %w", path, err)
	}

	if _, err := sqldb.Exec(keyStoreSchema); err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("db: migrate key store schema: %w", err)
	}

	return &KeyStore{sqldb: sqldb}, nil
}

// HasIdentity reports whether a vault has already been created on this
// device.
func (k *KeyStore) HasIdentity() (bool, error) {
	var count int
	err := k.sqldb.QueryRow(`SELECT COUNT(*) FROM identity_vault WHERE id = 1`).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("db: check identity: %w", err)
	}
	return count > 0, nil
}

// Save persists an encrypted vault, replacing any existing one.
func (k *KeyStore) Save(v *crypto.EncryptedVault) error {
	_, err := k.sqldb.Exec(
		`INSERT INTO identity_vault
			(id, salt, argon2_memory_kib, argon2_iterations, argon2_parallelism, nonce, ciphertext)
		 VALUES (1, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
			salt = excluded.salt,
			argon2_memory_kib = excluded.argon2_memory_kib,
			argon2_iterations = excluded.argon2_iterations,
			argon2_parallelism = excluded.argon2_parallelism,
			nonce = excluded.nonce,
			ciphertext = excluded.ciphertext`,
		v.Salt, v.Params.MemoryKiB, v.Params.Iterations, v.Params.Parallelism, v.Nonce, v.Ciphertext,
	)
	if err != nil {
		return fmt.Errorf("db: save identity vault: %w", err)
	}
	return nil
}

// Load reads the persisted vault. Returns ErrNoIdentity if none exists.
func (k *KeyStore) Load() (*crypto.EncryptedVault, error) {
	var v crypto.EncryptedVault
	var p crypto.Argon2Params

	err := k.sqldb.QueryRow(
		`SELECT salt, argon2_memory_kib, argon2_iterations, argon2_parallelism, nonce, ciphertext
		 FROM identity_vault WHERE id = 1`,
	).Scan(&v.Salt, &p.MemoryKiB, &p.Iterations, &p.Parallelism, &v.Nonce, &v.Ciphertext)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoIdentity
	}
	if err != nil {
		return nil, fmt.Errorf("db: load identity vault: %w", err)
	}

	v.Params = p
	return &v, nil
}

// Close closes the underlying database connection.
func (k *KeyStore) Close() error {
	return k.sqldb.Close()
}

// Command synq is the entry point for the Synq terminal client.
package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/term"

	"github.com/HomieB-tt/synq/internal/crypto"
	"github.com/HomieB-tt/synq/internal/db"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "synq:", err)
		os.Exit(1)
	}
}

func run() error {
	dbPath, err := keyStorePath()
	if err != nil {
		return fmt.Errorf("locate key store: %w", err)
	}

	ks, err := db.OpenKeyStore(dbPath)
	if err != nil {
		return fmt.Errorf("open key store: %w", err)
	}
	defer ks.Close()

	has, err := ks.HasIdentity()
	if err != nil {
		return fmt.Errorf("check for existing identity: %w", err)
	}

	var id *crypto.Identity
	if has {
		id, err = unlockIdentity(ks)
	} else {
		id, err = createIdentity(ks)
	}
	if err != nil {
		return err
	}

	// TODO: hand id off to internal/app to start the Bubble Tea program.
	fmt.Printf("Identity ready. Public key: %x\n", id.SigningPublic)
	return nil
}

// keyStorePath returns the path to the local SQLite key store,
// creating its parent directory if necessary.
func keyStorePath() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(configDir, "synq")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create config dir %s: %w", dir, err)
	}
	return filepath.Join(dir, "synq.db"), nil
}

// createIdentity runs the first-launch onboarding flow: warns the user
// that there is no passphrase recovery (DESIGN.md section 1), collects
// and confirms a passphrase, generates a fresh identity, and persists
// it sealed under that passphrase.
func createIdentity(ks *db.KeyStore) (*crypto.Identity, error) {
	fmt.Println("No identity found on this device - let's create one.")
	fmt.Println()
	fmt.Println("Your passphrase encrypts your identity keys on disk. There is no")
	fmt.Println("password reset and no recovery: if you forget it, this identity")
	fmt.Println("- and everything tied to it - is permanently lost. Choose something")
	fmt.Println("memorable and back it up somewhere safe (e.g. a password manager).")
	fmt.Println()

	passphrase, err := promptNewPassphrase()
	if err != nil {
		return nil, err
	}
	defer zeroBytes(passphrase)

	id, err := crypto.GenerateIdentity()
	if err != nil {
		return nil, fmt.Errorf("generate identity: %w", err)
	}

	vault, err := crypto.SealIdentity(id, passphrase, crypto.DefaultArgon2Params())
	if err != nil {
		return nil, fmt.Errorf("seal identity: %w", err)
	}

	if err := ks.Save(vault); err != nil {
		return nil, fmt.Errorf("save identity: %w", err)
	}

	fmt.Println("Identity created.")
	return id, nil
}

// unlockIdentity prompts for the existing passphrase and retries on a
// wrong guess. It never limits or slows down retries deliberately -
// Argon2id (see DESIGN.md section 1) is what defends against offline
// brute-forcing of the vault file itself; someone typing at this
// prompt already has local access to the machine, so there's nothing
// meaningful to rate-limit here.
func unlockIdentity(ks *db.KeyStore) (*crypto.Identity, error) {
	vault, err := ks.Load()
	if err != nil {
		return nil, fmt.Errorf("load identity: %w", err)
	}

	for {
		passphrase, err := promptPassphrase("Passphrase: ")
		if err != nil {
			return nil, err
		}

		id, err := crypto.OpenIdentity(vault, passphrase)
		zeroBytes(passphrase)
		if err == nil {
			return id, nil
		}

		fmt.Fprintln(os.Stderr, "Incorrect passphrase. Try again (Ctrl+C to quit).")
	}
}

// promptNewPassphrase asks for a new passphrase twice and requires the
// two entries to match before returning.
func promptNewPassphrase() ([]byte, error) {
	for {
		first, err := promptPassphrase("New passphrase: ")
		if err != nil {
			return nil, err
		}

		second, err := promptPassphrase("Confirm passphrase: ")
		if err != nil {
			zeroBytes(first)
			return nil, err
		}

		if bytes.Equal(first, second) {
			zeroBytes(second)
			return first, nil
		}

		zeroBytes(first)
		zeroBytes(second)
		fmt.Fprintln(os.Stderr, "Passphrases did not match. Try again.")
	}
}

// promptPassphrase reads a line of input from the terminal without
// echoing it. Falls back to a visible bufio read when stdin isn't an
// interactive terminal (e.g. piped input during scripted testing).
func promptPassphrase(label string) ([]byte, error) {
	fmt.Fprint(os.Stderr, label)

	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		pw, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return nil, fmt.Errorf("read passphrase: %w", err)
		}
		return pw, nil
	}

	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("read passphrase: %w", err)
	}
	return []byte(bytes.TrimRight([]byte(line), "\r\n")), nil
}

func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

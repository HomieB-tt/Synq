// Command synq is the entry point for the Synq terminal client.
package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/HomieB-tt/synq/internal/api"
	"github.com/HomieB-tt/synq/internal/app"
	"github.com/HomieB-tt/synq/internal/crypto"
	"github.com/HomieB-tt/synq/internal/db"
)

// sessionTimeout bounds every network call main.go makes before the
// TUI starts (registration, and the automatic refresh-or-login dance).
// Matches api.NewClient's own http.Client timeout - this is a second,
// outer bound covering the *sequence* of up to two calls (challenge
// then verify, or refresh then a login fallback), not just one.
const sessionTimeout = 15 * time.Second

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

	apiClient := newAPIClientFromEnv()

	has, err := ks.HasIdentity()
	if err != nil {
		return fmt.Errorf("check for existing identity: %w", err)
	}

	if !has {
		return runLanding(ks, apiClient)
	}

	id, refreshToken, passphrase, err := unlockIdentity(ks)
	if err != nil {
		return err
	}
	defer zeroBytes(passphrase)

	session := establishSession(ks, apiClient, id, passphrase, refreshToken)
	return app.Run(id, ks, session)
}

// newAPIClientFromEnv builds the synq-server REST client from
// SYNQ_SERVER_URL, or returns nil if it isn't set - nil propagates
// through runLanding/establishSession/app.Session as "no server
// configured", the same "stays off, zero behavior change" default
// every other optional integration here (SYNQ_GITHUB_CLIENT_ID,
// previously this same env var when it only drove the WS connection
// directly) already follows.
func newAPIClientFromEnv() *api.Client {
	baseURL := os.Getenv("SYNQ_SERVER_URL")
	if baseURL == "" {
		return nil
	}
	return api.NewClient(baseURL)
}

// synqBanner is the block-letter wordmark drawn above the landing
// menu, so the first thing a new device sees looks like the product
// rather than a bare numbered list. It has no color: this runs before
// any Bubble Tea styling exists (and before we know the terminal's
// capabilities - DESIGN.md section 7), so plain UTF-8 is all it can
// safely rely on.
const synqBanner = `██████╗ ██╗   ██╗███╗   ██╗██████╗ 
██╔════╝╚██╗ ██╔╝████╗  ██║██╔══██╗
█████╗   ╚████╔╝ ██╔██╗ ██║██████╔╝
██╔══╝    ╚██╔╝  ██║╚██╗██║██╔══██╗
███████╗  ██╔╝   ██║ ╚████║██████╔╝
╚══════╝  ╚═╝    ╚═╝  ╚═══╝╚═════╝ `

// runLanding is shown on a device with no identity yet, offering a
// choice before committing to identity creation - see DESIGN.md
// section 10 for why: creating an identity is a one-way, no-recovery
// commitment (section 1), so forcing it as the only option on a brand
// new device isn't the right default.
func runLanding(ks *db.KeyStore, apiClient *api.Client) error {
	for {
		fmt.Println()
		fmt.Println(synqBanner)
		fmt.Println()
		fmt.Println("Welcome to Synq. No identity exists on this device yet.")
		fmt.Println()
		fmt.Println("  1) Browse the public feed as a guest")
		fmt.Println("  2) Create your identity")
		fmt.Println("  3) Quit")
		fmt.Println()
		fmt.Print("Choose an option: ")

		choice, err := readLine()
		if err != nil {
			return err
		}

		switch strings.TrimSpace(choice) {
		case "1":
			// Guest mode: the real TUI, with a nil identity. See
			// DESIGN.md section 10 - this deliberately reuses the same
			// Model rather than building a separate stripped-down UI,
			// since theme switching and Feed browsing don't need an
			// identity at all. A guest still gets apiClient, so Feed
			// can be fetched anonymously (see api.Client.ListFeed) -
			// just never a Username or AccessToken, since there's no
			// identity here to register or log in with.
			return app.Run(nil, ks, app.Session{API: apiClient})

		case "2":
			id, passphrase, err := createIdentity(ks)
			if err != nil {
				return err
			}
			// Registration is a skippable next step, not part of
			// identity creation itself - see createIdentity's doc
			// comment on why that stays fully offline, and
			// offerRegistration's on why this is the only moment this
			// prompt is ever shown (never again on a later launch for
			// someone who skips it; :register <username> covers that).
			session := offerRegistration(ks, apiClient, id, passphrase)
			return app.Run(id, ks, session)

		case "3":
			return nil

		default:
			fmt.Println("Please enter 1, 2, or 3.")
		}
	}
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
// it sealed under that passphrase. Deliberately has no network
// dependency at all - a brand new identity is never blocked on
// synq-server being reachable, a username being available, or
// anything else server-side; see offerRegistration for the (optional,
// separate) step that does touch the network.
//
// Returns the passphrase rather than zeroing it internally, unlike
// this function's previous version: offerRegistration may need it
// again, to re-seal the vault with a refresh token if registration is
// accepted right away. The caller owns zeroing it once truly done -
// see runLanding, which does so via offerRegistration.
func createIdentity(ks *db.KeyStore) (*crypto.Identity, []byte, error) {
	fmt.Println("No identity found on this device - let's create one.")
	fmt.Println()
	fmt.Println("Your passphrase encrypts your identity keys on disk. There is no")
	fmt.Println("password reset and no recovery: if you forget it, this identity")
	fmt.Println("- and everything tied to it - is permanently lost. Choose something")
	fmt.Println("memorable and back it up somewhere safe (e.g. a password manager).")
	fmt.Println()

	passphrase, err := promptNewPassphrase()
	if err != nil {
		return nil, nil, err
	}

	id, err := crypto.GenerateIdentity()
	if err != nil {
		zeroBytes(passphrase)
		return nil, nil, fmt.Errorf("generate identity: %w", err)
	}

	vault, err := crypto.SealIdentity(id, "", passphrase, crypto.DefaultArgon2Params())
	if err != nil {
		zeroBytes(passphrase)
		return nil, nil, fmt.Errorf("seal identity: %w", err)
	}

	if err := ks.Save(vault); err != nil {
		zeroBytes(passphrase)
		return nil, nil, fmt.Errorf("save identity: %w", err)
	}

	fmt.Println("Identity created.")
	return id, passphrase, nil
}

// offerRegistration is shown exactly once, immediately after a brand
// new identity is created - see createIdentity. Skipping it (including
// just pressing enter, or a "no") is always a safe, complete choice:
// nothing about local identity creation or guest Feed browsing depends
// on having registered, and `:register <username>` from inside the TUI
// covers the same ground later for anyone who skips it here.
//
// Takes ownership of zeroing passphrase - this is the last point in
// the creation flow that can possibly need it (to re-seal the vault
// with a refresh token, if registration is accepted), so it's zeroed
// here unconditionally before returning, on every path.
func offerRegistration(ks *db.KeyStore, apiClient *api.Client, id *crypto.Identity, passphrase []byte) app.Session {
	defer zeroBytes(passphrase)

	if apiClient == nil {
		return app.Session{}
	}

	fmt.Println()
	fmt.Print("Register a username with synq-server now? [y/N]: ")
	choice, err := readLine()
	if err != nil || !strings.HasPrefix(strings.ToLower(strings.TrimSpace(choice)), "y") {
		return app.Session{API: apiClient}
	}

	username, err := promptUsername()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Registration skipped:", err)
		return app.Session{API: apiClient}
	}

	ctx, cancel := context.WithTimeout(context.Background(), sessionTimeout)
	defer cancel()

	tokens, err := registerUsername(ctx, apiClient, id, username)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Registration failed:", err)
		fmt.Fprintf(os.Stderr, "You can try again later from inside Synq with :register %s\n", username)
		return app.Session{API: apiClient}
	}

	if err := persistSession(ks, id, passphrase, username, tokens.RefreshToken); err != nil {
		// The server-side registration already succeeded at this
		// point - only the *local* bookkeeping failed. Surface that
		// distinction rather than implying registration itself
		// failed, since retrying :register <username> would now just
		// fail with "username taken" against the account that just
		// succeeded.
		fmt.Fprintf(os.Stderr, "Registered as %s, but failed to save that locally: %v\n", username, err)
		fmt.Fprintln(os.Stderr, "Try :login from inside Synq to pick the session back up.")
		return app.Session{API: apiClient}
	}

	fmt.Println("Registered as", username)
	return app.Session{
		API:          apiClient,
		Username:     username,
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
	}
}

// promptUsername asks for the username to register. Only the most
// basic client-side sanity check happens here (non-empty, no leading/
// trailing whitespace) - synq-server owns the real validation rules
// (length, character set, unicode normalization, etc. per API.md) and
// is the only place those should be allowed to drift from, so this
// deliberately doesn't try to duplicate them and risk rejecting
// something the server would have accepted, or vice versa.
func promptUsername() (string, error) {
	fmt.Print("Choose a username (this is permanent - synq-server has no rename yet): ")
	line, err := readLine()
	if err != nil {
		return "", err
	}
	username := strings.TrimSpace(line)
	if username == "" {
		return "", fmt.Errorf("username must not be empty")
	}
	return username, nil
}

// registerUsername runs the register/challenge → sign → register/
// verify exchange (API.md, "Authentication") and returns the resulting
// tokens. Does not persist anything - see persistSession for that,
// called separately so the two login paths (fresh registration here,
// and the automatic login fallback in establishSession) can share it.
func registerUsername(ctx context.Context, apiClient *api.Client, id *crypto.Identity, username string) (*api.TokenPair, error) {
	challenge, err := apiClient.RegisterChallenge(ctx, username)
	if err != nil {
		return nil, fmt.Errorf("request challenge: %w", err)
	}
	sig := id.SignChallenge(challenge)
	tokens, err := apiClient.RegisterVerify(ctx, username, id.SigningPublicHex(), id.BoxPublicHex(), sig)
	if err != nil {
		return nil, fmt.Errorf("verify registration: %w", err)
	}
	return tokens, nil
}

// loginWithIdentity runs the login/challenge → sign → login/verify
// exchange, for a username already known to be registered (unlike
// registerUsername, no pub_key is sent - see LoginVerify's own doc
// comment).
func loginWithIdentity(ctx context.Context, apiClient *api.Client, id *crypto.Identity, username string) (*api.TokenPair, error) {
	challenge, err := apiClient.LoginChallenge(ctx, username)
	if err != nil {
		return nil, fmt.Errorf("request login challenge: %w", err)
	}
	sig := id.SignChallenge(challenge)
	tokens, err := apiClient.LoginVerify(ctx, username, sig)
	if err != nil {
		return nil, fmt.Errorf("verify login: %w", err)
	}
	return tokens, nil
}

// persistSession saves username as a local preference and re-seals
// the vault with refreshToken, under the same passphrase already
// protecting the identity keys - see crypto.SealIdentity's doc comment
// on why a refresh token belongs inside the encrypted vault, not
// db.KeyStore's plaintext preferences table the way PrefUsername does.
func persistSession(ks *db.KeyStore, id *crypto.Identity, passphrase []byte, username, refreshToken string) error {
	vault, err := crypto.SealIdentity(id, refreshToken, passphrase, crypto.DefaultArgon2Params())
	if err != nil {
		return fmt.Errorf("seal updated vault: %w", err)
	}
	if err := ks.Save(vault); err != nil {
		return fmt.Errorf("save updated vault: %w", err)
	}
	if err := ks.SavePreference(db.PrefUsername, username); err != nil {
		return fmt.Errorf("save username preference: %w", err)
	}
	return nil
}

// establishSession implements the automatic-login behavior for a
// returning user: get a working access token with zero prompts beyond
// the passphrase already just entered to unlock the vault, if at all
// possible. First try refreshing the stored refresh token; if that's
// missing, expired, or revoked, fall back to a full challenge-response
// login using the identity already unlocked - the user never sees a
// login prompt either way, since nothing beyond what unlockIdentity
// already asked for is required for either path.
//
// Every failure mode here - no SYNQ_SERVER_URL configured, this
// identity never registered, genuinely offline, or synq-server simply
// down - falls through to an app.Session with no access token rather
// than blocking startup or returning an error: Synq's local-first
// design means the TUI should always come up, with whatever server
// connectivity exists today just being partial. :login retries
// manually from inside the running TUI.
func establishSession(ks *db.KeyStore, apiClient *api.Client, id *crypto.Identity, passphrase []byte, refreshToken string) app.Session {
	if apiClient == nil {
		return app.Session{}
	}

	username, err := ks.LoadPreference(db.PrefUsername)
	if err != nil || username == "" {
		// Never registered on this device - nothing to log in to.
		// Ignoring the error here is deliberate, the same as every
		// other optional preference load in this codebase:
		// ErrPreferenceNotFound and "not registered" mean the same
		// thing, so there's nothing to distinguish or report.
		return app.Session{API: apiClient}
	}

	ctx, cancel := context.WithTimeout(context.Background(), sessionTimeout)
	defer cancel()

	if refreshToken != "" {
		if accessToken, err := apiClient.Refresh(ctx, refreshToken); err == nil {
			return app.Session{
				API:          apiClient,
				Username:     username,
				AccessToken:  accessToken,
				RefreshToken: refreshToken,
			}
		}
		// Falls through to a full login attempt below instead of
		// giving up - the identity's signing key can always
		// re-authenticate from scratch even when this particular
		// stored refresh token can't (expired, revoked, or this is
		// simply the first launch since an older version of Synq
		// registered this username without refreshToken support).
	}

	tokens, err := loginWithIdentity(ctx, apiClient, id, username)
	if err != nil {
		// Couldn't log in automatically (offline, server down, or the
		// account no longer exists server-side) - fall through to a
		// working TUI that's just not connected, rather than an error
		// that blocks starting it at all.
		return app.Session{API: apiClient, Username: username}
	}

	// A full login returns a brand new refresh token (unlike Refresh,
	// which doesn't rotate it - see api.Refresh's doc comment), so the
	// vault needs re-sealing to store it. This is only possible here,
	// while main.go still holds passphrase - once app.Run hands off to
	// the Bubble Tea Model, it never will (by design: see app.Session's
	// doc comment).
	if err := persistSession(ks, id, passphrase, username, tokens.RefreshToken); err != nil {
		fmt.Fprintln(os.Stderr, "synq: logged in, but failed to save the new session locally:", err)
	}
	return app.Session{
		API:          apiClient,
		Username:     username,
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
	}
}

// unlockIdentity prompts for the existing passphrase and retries on a
// wrong guess. It never limits or slows down retries deliberately -
// Argon2id (see DESIGN.md section 1) is what defends against offline
// brute-forcing of the vault file itself; someone typing at this
// prompt already has local access to the machine, so there's nothing
// meaningful to rate-limit here.
//
// Returns the passphrase (rather than zeroing it internally, unlike
// this function's previous version) alongside the identity and
// whatever refresh token was sealed in the vault: establishSession
// needs the passphrase too, to re-seal the vault if its automatic
// login fallback obtains a new refresh token. The caller owns zeroing
// it once truly done - see run(), via a defer right after this call.
func unlockIdentity(ks *db.KeyStore) (id *crypto.Identity, refreshToken string, passphrase []byte, err error) {
	vault, err := ks.Load()
	if err != nil {
		return nil, "", nil, fmt.Errorf("load identity: %w", err)
	}

	for {
		pw, err := promptPassphrase("Passphrase: ")
		if err != nil {
			return nil, "", nil, err
		}

		openedID, openedRefreshToken, err := crypto.OpenIdentity(vault, pw)
		if err == nil {
			return openedID, openedRefreshToken, pw, nil
		}
		zeroBytes(pw)

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

// readLine reads one line of plain, visible input - unlike
// promptPassphrase, this is for menu choices, not secrets, so no
// terminal echo suppression is needed.
func readLine() (string, error) {
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("read input: %w", err)
	}
	return line, nil
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

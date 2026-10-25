// Command synq is the entry point for the Synq terminal client.
package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/HomieB-tt/synq/internal/api"
	"github.com/HomieB-tt/synq/internal/app"
	"github.com/HomieB-tt/synq/internal/crypto"
	"github.com/HomieB-tt/synq/internal/db"
	"github.com/HomieB-tt/synq/internal/postcli"
)

// sessionTimeout bounds every network call main.go makes before the
// TUI starts (registration, and the automatic refresh-or-login dance).
// Matches api.NewClient's own http.Client timeout - this is a second,
// outer bound covering the *sequence* of up to two calls (challenge
// then verify, or refresh then a login fallback), not just one.
const sessionTimeout = 15 * time.Second

func main() {
	// `synq post` is a pipe-friendly subcommand: it never starts the TUI.
	if len(os.Args) > 1 && os.Args[1] == "post" {
		if err := runPost(os.Args[2:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				fmt.Print(postcli.Usage)
				return
			}
			fmt.Fprintln(os.Stderr, "synq post:", err)
			os.Exit(1)
		}
		return
	}

	// --forget runs before the store is opened: it's the escape hatch
	// for someone who has permanently lost their passphrase (DESIGN.md
	// section 1 - there is no recovery), and it must work from exactly
	// the state where every other path into Synq is blocked behind the
	// unlock prompt.
	if slices.Contains(os.Args[1:], "--forget") {
		archive, err := forgetKeyStore()
		if err != nil {
			fmt.Fprintln(os.Stderr, "synq:", err)
			os.Exit(1)
		}
		if archive != "" {
			fmt.Printf("Archived the key store to:\n  %s\n", archive)
			fmt.Println("It still holds the old identity (useless without the passphrase),")
			fmt.Println("plus theme/key-pin preferences. Synq never deletes it - do that yourself.")
		} else {
			fmt.Println("No key store on this device yet - nothing to forget.")
		}
		fmt.Println()
	}

	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "synq:", err)
		os.Exit(1)
	}
}

// forgetKeyStore locates the key store and archives it in place - see
// archiveKeyStore. Returns the archive path, or "" when there was
// nothing to archive.
func forgetKeyStore() (string, error) {
	dbPath, err := keyStorePath()
	if err != nil {
		return "", fmt.Errorf("locate key store: %w", err)
	}
	return archiveKeyStore(dbPath, time.Now())
}

// archiveKeyStore moves the SQLite key store at dbPath aside to a
// timestamped name next to it, so the next launch treats this device
// as brand new (run() finds no identity and shows the landing menu).
//
// The archive lives beside the original - same directory, same
// permissions - rather than in a trash folder or under /tmp: it is
// the user's only remaining copy of an unrecoverable identity, and
// the whole point of the rename (instead of delete) is that they can
// decide later whether to keep it. Sidecar files SQLite may have left
// behind (-wal/-shm/-journal) follow the database to matching
// archive names, best-effort: they're only meaningful alongside the
// database they belong to.
func archiveKeyStore(dbPath string, now time.Time) (string, error) {
	if _, err := os.Stat(dbPath); errors.Is(err, fs.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", fmt.Errorf("inspect key store: %w", err)
	}

	archive := fmt.Sprintf("%s.bak-%s", dbPath, now.Format("20060102-150405"))
	for suffix := 1; ; suffix++ {
		if _, err := os.Stat(archive); errors.Is(err, fs.ErrNotExist) {
			break
		} else if err != nil {
			return "", fmt.Errorf("inspect archive target: %w", err)
		}
		// Two --forgets in the same second must not overwrite each
		// other - the first archive is someone's identity copy.
		archive = fmt.Sprintf("%s.bak-%s-%d", dbPath, now.Format("20060102-150405"), suffix)
	}

	if err := os.Rename(dbPath, archive); err != nil {
		return "", fmt.Errorf("archive key store: %w", err)
	}
	for _, side := range []string{"-wal", "-shm", "-journal"} {
		src := dbPath + side
		if _, err := os.Stat(src); err == nil {
			_ = os.Rename(src, archive+side)
		}
	}
	return archive, nil
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

	initUI(ks)

	apiClient, err := newAPIClient(ks)
	if err != nil {
		return err
	}

	has, err := ks.HasIdentity()
	if err != nil {
		return fmt.Errorf("check for existing identity: %w", err)
	}

	if !has {
		if apiClient == nil {
			apiClient, err = askServerURL(ks)
			if err != nil {
				return err
			}
		}
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

// defaultServerURL is the synq-server a release build talks to when the
// user hasn't chosen one. It is empty in a plain `go build` and is set
// for release binaries at link time:
//
//	-ldflags "-X main.defaultServerURL=https://synq.example.com"
//
// (see .goreleaser.yaml). It is the lowest-priority source: see
// resolveServerURL.
var defaultServerURL = ""

// resolveServerURL picks the server to use and says where it came from.
// Priority, highest first:
//
//  1. SYNQ_SERVER_URL - a developer / power-user override
//  2. the address saved from the app (landing prompt or `:server`)
//  3. the build-time default
//
// It returns "" when none is set.
func resolveServerURL(env, saved, built string) (raw, source string) {
	switch {
	case strings.TrimSpace(env) != "":
		return strings.TrimSpace(env), "SYNQ_SERVER_URL"
	case strings.TrimSpace(saved) != "":
		return strings.TrimSpace(saved), "saved"
	case strings.TrimSpace(built) != "":
		return strings.TrimSpace(built), "built-in"
	}
	return "", ""
}

// newAPIClient builds the synq-server REST client from the sources in
// resolveServerURL, or returns nil if none is set - nil propagates
// through runLanding/establishSession/app.Session as "no server
// configured", the same "stays off" default every other optional
// integration follows.
//
// A bad SYNQ_SERVER_URL is an error (the user typed it this launch). A
// bad saved or built-in value is skipped instead, so a typo can never
// lock someone out of the app: they can still fix it with `:server`.
//
// It refuses a cleartext http:// URL for any non-loopback host (see
// api.ValidateServerURL): access tokens, refresh tokens and all traffic
// metadata would otherwise cross the network unencrypted.
func newAPIClient(ks *db.KeyStore) (*api.Client, error) {
	allowInsecure := os.Getenv("SYNQ_ALLOW_INSECURE_HTTP") == "1"

	if env := strings.TrimSpace(os.Getenv("SYNQ_SERVER_URL")); env != "" {
		if err := api.ValidateServerURL(env, allowInsecure); err != nil {
			return nil, fmt.Errorf("SYNQ_SERVER_URL: %w", err)
		}
		return api.NewClient(env), nil
	}
	saved, _ := ks.LoadPreference(db.PrefServerURL)
	for _, cand := range []string{saved, defaultServerURL} {
		cand = strings.TrimSpace(cand)
		if cand != "" && api.ValidateServerURL(cand, allowInsecure) == nil {
			return api.NewClient(cand), nil
		}
	}
	return nil, nil
}

// askServerURL is the first-run question for a build with no built-in
// server: where is it? A blank answer skips it (guest/offline use), and
// a valid answer is saved so it is never asked again.
func askServerURL(ks *db.KeyStore) (*api.Client, error) {
	allowInsecure := os.Getenv("SYNQ_ALLOW_INSECURE_HTTP") == "1"
	th := app.StartingTheme(ks)
	out := landingOutput()
	fmt.Fprintln(out)
	noteLine(out, th, "hint", "Synq needs the address of a synq-server to chat, post and share files.")
	for {
		fmt.Fprint(out, promptLine(th, "Server address (e.g. https://synq.example.com), or Enter to skip"))
		line, err := readLine()
		if err != nil {
			return nil, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			fmt.Println("Skipped. You can set it later with :server <address> inside Synq.")
			return nil, nil
		}
		if !strings.Contains(line, "://") {
			line = "https://" + line
		}
		if err := api.ValidateServerURL(line, allowInsecure); err != nil {
			fmt.Println(err)
			continue
		}
		if err := ks.SavePreference(db.PrefServerURL, line); err != nil {
			fmt.Printf("Couldn't save the address (%v); using it for this session only.\n", err)
		}
		return api.NewClient(line), nil
	}
}

// validateServerURL is kept as the package-local name for the shared
// check in internal/api.
func validateServerURL(raw string, allowInsecure bool) error {
	return api.ValidateServerURL(raw, allowInsecure)
}

// runLanding is shown on a device with no identity yet, offering a
// choice before committing to identity creation - see DESIGN.md
// section 10 for why: creating an identity is a one-way, no-recovery
// commitment (section 1), so forcing it as the only option on a brand
// new device isn't the right default.
func runLanding(ks *db.KeyStore, apiClient *api.Client) error {
	th := app.StartingTheme(ks)
	out := landingOutput()
	for {
		choice, err := landingChoice(out, th, ks)
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
			noteLine(out, th, "error", "Please enter 1, 2, or 3.")
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
	out := uiStdout()
	uiHeading(out, "Create your identity")
	uiNotice(out, "There is no recovery", []string{
		"Your passphrase encrypts your identity keys on disk. There is no password reset: if you forget it, this identity - and everything tied to it - is permanently lost.",
		"",
		"Choose something memorable and back it up somewhere safe, such as a password manager.",
	})

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

	uiOK(out, "Identity created.")
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

	out := uiStdout()
	uiHeading(out, "Claim a username")
	uiHint(out, "A username lets people send you node requests. You can skip this and run :register inside Synq later.")
	fmt.Fprintln(out)
	fmt.Fprint(out, uiAsk("Register a username with synq-server now? [y/N]"))
	choice, err := readLine()
	if err != nil || !strings.HasPrefix(strings.ToLower(strings.TrimSpace(choice)), "y") {
		return app.Session{API: apiClient}
	}

	username, err := promptUsername()
	if err != nil {
		uiFail(uiStderr(), "Registration skipped: "+err.Error())
		return app.Session{API: apiClient}
	}

	ctx, cancel := context.WithTimeout(context.Background(), sessionTimeout)
	defer cancel()

	tokens, err := registerUsername(ctx, apiClient, id, username)
	if err != nil {
		serr := uiStderr()
		uiFail(serr, "Registration failed: "+err.Error())
		uiHint(serr, fmt.Sprintf("You can try again later from inside Synq with :register %s", username))
		return app.Session{API: apiClient}
	}

	if err := persistSession(ks, id, passphrase, username, tokens.RefreshToken); err != nil {
		// The server-side registration already succeeded at this
		// point - only the *local* bookkeeping failed. Surface that
		// distinction rather than implying registration itself
		// failed, since retrying :register <username> would now just
		// fail with "username taken" against the account that just
		// succeeded.
		serr := uiStderr()
		uiFail(serr, fmt.Sprintf("Registered as %s, but failed to save that locally: %v", username, err))
		uiHint(serr, "Try :login from inside Synq to pick the session back up.")
		return app.Session{API: apiClient}
	}

	uiOK(out, "Registered as "+username)
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
	out := uiStdout()
	uiHint(out, "Your username is permanent - synq-server has no rename yet.")
	fmt.Fprint(out, uiAsk("Choose a username"))
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
	tokens, err := apiClient.RegisterVerify(ctx, username, id.SigningPublicHex(), id.BoxPublicHex(), id.SignBoxKey(username), sig)
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

	// Two failures is early enough that someone who genuinely forgot
	// the passphrase learns the exit exists, and late enough that a
	// simple typo doesn't get told about deleting their identity.
	attempts := 0
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

		attempts++
		serr := uiStderr()
		uiFail(serr, "Incorrect passphrase. Try again (Ctrl+C to quit).")
		if attempts == 2 {
			uiHint(serr, "Forgot it? Quit and re-run synq with --forget to archive this identity and start over.")
		}
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
		uiFail(uiStderr(), "Passphrases did not match. Try again.")
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

// passphraseInput, when set, is where promptPassphrase reads from
// instead of stdin. `synq post` sets it to the controlling terminal,
// because its stdin is the pipe carrying the post.
var passphraseInput *os.File

// promptPassphrase reads a line of input from the terminal without
// echoing it. Falls back to a visible bufio read when stdin isn't an
// interactive terminal (e.g. piped input during scripted testing).
func promptPassphrase(label string) ([]byte, error) {
	// Labels arrive as "Passphrase: " - drawn as an accent prompt instead.
	fmt.Fprint(uiStderr(), uiAsk(strings.TrimSuffix(label, ": ")))

	in := os.Stdin
	if passphraseInput != nil {
		in = passphraseInput
	}
	fd := int(in.Fd())
	if term.IsTerminal(fd) {
		pw, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return nil, fmt.Errorf("read passphrase: %w", err)
		}
		return pw, nil
	}

	reader := bufio.NewReader(in)
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

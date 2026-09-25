# Synq Client — Design Decisions

This document captures the architectural decisions made for the `synq` client during the design phase, beyond what's described in the original feature spec. It exists to record *why* things work the way they do, not just what the code does — anyone extending this client should read this before touching crypto, storage, or the ephemeral-messaging model.

## 1. Key storage at rest

**Problem:** Secret keys (Ed25519 identity, X25519 encryption) need to persist between launches, but SQLite alone provides no encryption at rest.

**Decision:** Passphrase-derived encryption.

- On first boot, the user sets a passphrase.
- Argon2id derives a 256-bit key from that passphrase and a random per-user salt.
- The derived key encrypts the secret keys using XSalsa20-Poly1305 (`nacl/secretbox`) — staying within the same NaCl family already used for `nacl/box`.
- SQLite stores only: the salt, the Argon2id parameters (memory/time/parallelism, so they can be tuned in future without invalidating existing vaults), and the encrypted blob. The passphrase itself is never stored.
- The passphrase is required on every launch. Decrypted keys live in memory only for the lifetime of the process and are never written to disk in plaintext.
- **There is no recovery path.** Losing the passphrase means losing the identity permanently. This is a deliberate consequence of being genuinely zero-knowledge — Synq has no mechanism, server-side or client-side, that could recover a lost key. This must be communicated explicitly during onboarding.

## 2. Key authenticity

**Problem:** Nothing in the base design confirms that a public key returned for a username actually belongs to that person. Since the server is otherwise zero-knowledge, a compromised or malicious server could substitute a key during onboarding and sit in the middle of a conversation undetected.

**Decision:** Trust-on-first-use (TOFU), with optional manual verification.

- The first time a client sees a public key for a given username, it pins that key locally against the username.
- If a pinned key ever changes for an existing contact, this is treated as a hard warning — new messages to that contact are blocked from encrypting until the user acknowledges the change. This is the case that actually matters: routine first-contact trust is low-stakes, but an existing contact's key changing is the signature of a substitution attack.
- A `:verify <username>` command (or equivalent action in Profile/Chat) computes a short numeric fingerprint from both parties' public keys, in the same spirit as Signal's safety numbers, for out-of-band comparison.
- Since GitHub identity is already surfaced in the product, the fingerprint can optionally be published on a user's GitHub profile (bio, gist, pinned repo), giving verifiers a ready-made out-of-band channel.
- The `username` referred to throughout this section is assigned by `synq-server` at registration and is what other users actually see as a post's author - it's not something the client stores or lets you edit. The client's `:name` command (see `internal/db/preferences.go`'s `PrefDisplayName`) is a separate, purely local label shown only in your own Profile tab, useful as a stand-in while `synq-server` doesn't exist yet, but never sent anywhere and never seen by anyone else.

## 3. Forward secrecy

**Problem:** Static X25519 keys alone mean a single key compromise can retroactively decrypt every past conversation ever encrypted to it.

**Decision:** Per-launch ephemeral handshake (X3DH-lite), not a full Double Ratchet.

- One ephemeral X25519 keypair is generated at app launch and reused for handshakes with any contact during that session.
- At the start of a session with a contact, a shared session key is derived by combining ephemeral and static keys in both directions (DH(ephemeral_local, static_remote) + DH(static_local, ephemeral_remote)) through HKDF.
- That session key, not the static key, encrypts message content for the session via `nacl/secretbox`.
- The ephemeral private key is discarded when the app quits — sharing its lifecycle with the passphrase-unlocked identity keys (see §1). Compromising a static key later cannot decrypt a session whose ephemeral key is already gone.
- A full Double Ratchet (Signal-style, per-message key rotation) was considered and deliberately not chosen for v1: it's a significantly larger engineering surface, and per-launch granularity was judged sufficient given the server never persists ciphertext in the first place — the real-world exposure window this protects against is already narrow.

## 4. Ephemeral message cache

**Problem:** The server holds no message state, but the client caches locally "for offline reading" — these two facts needed reconciling into one coherent, explicit rule.

**Decision:** Session-scoped ephemerality, not TTL- or read-based.

- While Synq is running, an open chat thread's history is available across tab switches — closing and reopening a *view* of a thread does not lose anything, as long as the app itself stays running. This is what "offline reading" actually means: the ability to still see already-received messages if the network drops mid-session, not the ability to reopen history after a restart.
- Attachments (files, images, videos) received in-chat are not part of the ephemeral store. Saving one is an explicit user action that writes it to permanent storage outside Synq's ephemeral cache.
- **Quitting Synq purges everything.** All thread caches for every contact are wiped on exit. Relaunching and reopening a thread with the same contact starts from a completely blank slate — no history, no indication a conversation ever happened.
- There is no soft-delete or "restore last session" feature. This is a hard, total purge by design, not an accident of implementation.

## 5. Multi-device

**Decision:** Single-device only, for now, and explicitly documented as a limitation rather than an oversight.

- Each install generates its own independent identity keypair. There is no sync, linking, or shared-account mechanism between devices.
- Running Synq on a second machine creates a second, unrelated identity — contacts have no way to know the two belong to the same person unless told out-of-band.
- Building real multi-device support (securely linking or transferring identity keys across devices without ever exposing them to the server) is a substantial project in its own right and is deferred rather than half-built.

## 6. Piped input handling

**Decision:** Sanitize before storing or rendering.

- Content piped into `synq post` (e.g. `cat error.log | synq post`) is stripped of raw ANSI/control escape sequences before it touches storage or the Bubble Tea render loop.
- Syntax highlighting via Chroma is applied to the sanitized text, not the raw input — piped content should never be able to manipulate the terminal or the TUI's rendering state.

## 7. Theming (Lip Gloss v2)

**Decision:** No automatic detection — Lip Gloss v2 removed automatic background/adaptive-color detection, so this must be handled explicitly.

- On first launch, either prompt the user to pick a theme directly, or issue a one-time `tea.RequestBackgroundColor` and choose a sensible light/dark default from the result.
- The chosen theme is always overridable later via the command palette: `:theme <name>` sets one directly, and `:theme` with no argument opens an interactive picker.
- The picker is a live preview, not just a list: moving the highlighted entry immediately re-derives `Model.theme` from that entry's palette, so the whole running UI (tab bar, borders, status bar) re-skins itself before anything is confirmed. Nothing is persisted to the preferences store until `Enter`; `Esc` restores whichever theme was active before the picker opened, discarding the preview. This reuses the same `styles.All`/`styles.Names()` machinery `:theme <name>` already used, rather than introducing a second, separate theme-listing mechanic - see `internal/app/theme_picker.go`.
- The `system` palette is a deliberate exception to "no automatic detection": instead of hardcoded hex colors, every field is a base ANSI (0-15) color index, so it renders using whatever colors the user's terminal emulator itself defines for those slots rather than a color Synq ships. It won't look identical across terminals - that's the trade-off the name is describing, not a bug.

## 8. `$EDITOR` integration

**Decision:**

- If `$EDITOR` is unset when the user presses `Ctrl+O`, Synq falls back to its own built-in text input rather than erroring out.
- If the external editor exits with a non-zero status, or the user quits it without saving, the draft is discarded and the user returns to a blank post. There is no partial-save recovery — this keeps the failure mode simple and predictable rather than introducing ambiguous partial-draft states.

## 9. GitHub verification

**Decision:** OAuth Device Flow (the same mechanism `gh auth login` uses).

- Synq displays a short code and a URL; the user completes the approval in any browser, on any device; Synq polls until authorized.
- This avoids the need for a local HTTP callback server, which a browser-redirect OAuth flow would require and which doesn't fit a terminal-native tool.
- Verification is optional and separate from account registration (see the `synq-server` design doc, §1) — it grants a "verified" badge, not access.

## 10. Onboarding and the landing menu

**Problem:** creating an identity is a one-way, no-recovery commitment (section 1) — the original flow forced this choice on every brand-new user immediately, with no way to look around first. It also meant purely local, identity-free features (theme selection) were structurally unreachable before creating an identity, since the whole TUI only ever launched after identity creation or unlock.

**Decision:** a device with no identity yet is shown a menu before anything else happens, rather than being walked straight into passphrase creation:

- **Browse the public feed as a guest** — launches the same Bubble Tea app used for everything else, with a `nil` identity. This is not a separate, stripped-down "guest UI" — it's the real TUI, with real theme switching (`:theme`, fully functional, since it only depends on the local preferences store, not an identity) and a real Feed tab. Nodes, Chat, and the identity-dependent parts of Profile show a clear call-to-action explaining an identity is needed, rather than being hidden or disabled outright. `:verify` and `:github` are blocked with a clear message, since both fundamentally require an identity to attach their result to.
- **Create your identity** — the existing, already-tested passphrase creation flow, unchanged.
- **Quit.**

**Why guest mode reuses the real Model instead of a separate pre-TUI menu for feed/theme browsing:** theme selection already lives entirely in the command palette (`:theme`), backed by the same preferences table already proven independent of the identity vault (see the `TestPreferencesAreIndependentOfIdentityVault` test in `internal/db`). Building a second, separate theme-picking mechanic for guests would duplicate an already-working feature for no reason. The one thing guest mode deliberately does NOT support is creating an identity mid-session — that still requires quitting and choosing "Create your identity" from the menu, keeping passphrase entry in the single, already-tested, non-interactive terminal flow rather than adding a new masked-input widget inside Bubble Tea. In-session signup (no restart required) is a reasonable future enhancement once there's appetite for building that input widget.

**How this connects to the Feed's anonymization rule (synq-server-DESIGN.md section 5):** the server decides whether to send real usernames or a flat `"node"` placeholder based on whether the request carries a valid, authenticated session — not on whether the client happens to have a local identity. A guest who has never registered/logged in and someone who has generated a local identity but never connected to a server look identical to the server. The client itself does no redaction; it only ever displays whatever `author` field the server actually sent.

## Summary table

| Area | Decision |
|---|---|
| Keys at rest | Argon2id-derived passphrase encrypts secret keys via `nacl/secretbox`; prompted every launch, memory-only, no recovery |
| Key authenticity | TOFU by default, hard-warn on key change, optional `:verify` fingerprint command tied to GitHub identity |
| Forward secrecy | Per-launch ephemeral X25519 keypair, X3DH-style session key derivation via HKDF |
| Ephemeral cache | Thread history persists across tab switches within a session; quitting Synq purges everything unsaved; explicit save for attachments |
| Multi-device | Single-device only, documented limitation, no sync |
| Piped input | ANSI/control sequences stripped before storage or rendering |
| Theming | Explicit theme choice or one-time `RequestBackgroundColor`, no auto-detection |
| `$EDITOR` | Falls back to built-in input if unset; any editor error/abort discards the draft |
| GitHub verification | OAuth Device Flow, optional, separate from registration |

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
- The same encrypted blob also holds `synq-server`'s refresh token, once this identity has registered a username (`crypto.SealIdentity`'s `refreshToken` parameter) — not a separate field needing its own encryption scheme, and deliberately not stored in `db.KeyStore`'s plaintext preferences table the way a theme choice or username is: unlike those, a refresh token is a real bearer credential, so it gets the same protection as the identity keys it's sealed alongside. One consequence worth knowing: only `cmd/synq/main.go` ever briefly holds the passphrase, so only code running there (vault creation, unlock, and the automatic login-fallback path) can write an updated refresh token back to the vault. The running TUI (`internal/app`) never receives the passphrase at all and can hold a session's access token only in memory for that run - see `app.Session`'s doc comment for the full consequence chain.

## 2. Key authenticity

**Problem:** Nothing in the base design confirms that a public key returned for a username actually belongs to that person. Since the server is otherwise zero-knowledge, a compromised or malicious server could substitute a key during onboarding and sit in the middle of a conversation undetected.

**Decision:** Trust-on-first-use (TOFU), with optional manual verification.

- The first time a client sees a public key for a given username, it pins that key locally against the username.
- If a pinned key ever changes for an existing contact, this is treated as a hard warning — new messages to that contact are blocked from encrypting until the user acknowledges the change. This is the case that actually matters: routine first-contact trust is low-stakes, but an existing contact's key changing is the signature of a substitution attack.
- **The box key is bound to the identity key.** The fingerprint covers the Ed25519 identity keys, but messages are encrypted to the X25519 box key, which the server hands out. Each account's identity key therefore signs `(username, box key)` at registration (`crypto.Identity.SignBoxKey`); the server stores and serves the signature, and this client verifies it (`crypto.VerifyBoxKeyBinding`) before pinning a key or opening a thread. A missing or invalid signature means the key is not trusted, so a server cannot substitute a box key of its own while leaving the identity key alone, and a verified fingerprint now vouches for the box key too. Accounts that predate the signature upload one automatically at their next login.
- A `:verify <username>` command (or equivalent action in Profile/Chat) computes a short numeric fingerprint from both parties' public keys, in the same spirit as Signal's safety numbers, for out-of-band comparison.
- Since GitHub identity is already surfaced in the product, the fingerprint can optionally be published on a user's GitHub profile (bio, gist, pinned repo), giving verifiers a ready-made out-of-band channel.
- The `username` referred to throughout this section is assigned by `synq-server` at registration (`:register <username>`, stored locally as `db.PrefUsername`) and is what other users actually see as a post's author and look up via `/users/{username}/keys`. It's permanent - there's no rename endpoint - and distinct from the client's separate `:name` command (`db.PrefDisplayName`), a purely local, freely-changeable label shown only in your own Profile tab, never sent to `synq-server` or seen by anyone else. Conflating the two would be a real bug: see each constant's doc comment in `internal/db/preferences.go`.

## 3. Forward secrecy

**Problem:** Static X25519 keys alone mean a single key compromise can retroactively decrypt every past conversation ever encrypted to it.

**Decision:** Per-launch ephemeral handshake (X3DH-lite), not a full Double Ratchet.

- One ephemeral X25519 keypair is generated at app launch and reused for handshakes with any contact during that session.
- At the start of a session with a contact, a shared session key is derived by combining ephemeral and static keys in both directions (DH(ephemeral_local, static_remote) + DH(static_local, ephemeral_remote)) through HKDF.
- That session key, not the static key, encrypts message content for the session via `nacl/secretbox`.
- The ephemeral private key is discarded when the app quits — sharing its lifecycle with the passphrase-unlocked identity keys (see §1). Compromising a static key later cannot decrypt a session whose ephemeral key is already gone.
- **Replay and reflection (protocol v2).** The server relays opaque bytes and is not trusted, so it could record a sealed frame and deliver it again, or hand a client its own message back as if the contact had sent it. Two measures close that:
  - The session key is split into one subkey per direction (HKDF, labelled low-to-high / high-to-low by comparing the two static keys, and bound to both). A client seals with its send key and opens with its receive key, so a reflected message fails authentication.
  - Every sealed message carries a counter inside the authenticated plaintext. The receiver keeps a 1024-entry sliding window per session key and accepts each counter once; the window tolerates reordering (queued messages are sent concurrently) but refuses repeats and anything older than the window. The counter is only checked after the message authenticates, so forged frames can't move it.
  - Counter state is keyed by the pair of ephemeral keys the session key came from and is never reset, so repeating a handshake can't reopen the window. A contact's superseded ephemeral key is retired, and a handshake frame carrying a retired key is ignored, so replaying an old handshake can't step the session back either.
  - A frame that fails authentication is dropped and reported; the session is not torn down, since otherwise one injected frame could wedge a thread.
  - Handshake version 2 and sealed payload kind `0x03` are not compatible with earlier clients, which are refused with a message asking the sender to update.

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

- Content piped into `synq post` (e.g. `cat error.log | synq post`) is stripped of raw ANSI/control escape sequences before it touches storage or the Bubble Tea render loop. Implemented by `internal/sanitize` (whole CSI/OSC/DCS/APC sequences, C1 forms, control and bidi-override characters) and `internal/postcli`; `synq post` reads its passphrase from the controlling terminal because stdin is the pipe.
- Syntax highlighting via Chroma is applied to the sanitized text, not the raw input — piped content should never be able to manipulate the terminal or the TUI's rendering state.

## 7. Theming (Lip Gloss v2)

**Decision:** No automatic detection — Lip Gloss v2 removed automatic background/adaptive-color detection, so this must be handled explicitly.

- On first launch, either prompt the user to pick a theme directly, or issue a one-time `tea.RequestBackgroundColor` and choose a sensible light/dark default from the result.
- The chosen theme is always overridable later via the command palette: `:theme <name>` sets one directly, and `:theme` with no argument opens an interactive picker.
- The picker is a live preview, not just a list: moving the highlighted entry immediately re-derives `Model.theme` from that entry's palette, so the whole running UI (tab bar, borders, status bar) re-skins itself before anything is confirmed. Nothing is persisted to the preferences store until `Enter`; `Esc` restores whichever theme was active before the picker opened, discarding the preview. This reuses the same `styles.All`/`styles.Names()` machinery `:theme <name>` already used, rather than introducing a second, separate theme-listing mechanic - see `internal/app/theme_picker.go`.
- The `system` palette is a deliberate exception to "no automatic detection": instead of hardcoded hex colors, every field is a base ANSI (0-15) color index, so it renders using whatever colors the user's terminal emulator itself defines for those slots rather than a color Synq ships. It won't look identical across terminals - that's the trade-off the name is describing, not a bug. Synq also starts in it, automatically, when the terminal can show only the 16 base colours (older Windows consoles, `NO_COLOR`) and you have not picked a theme yourself: the hex themes get squeezed into those 16 slots there and look wrong, while `system` is made of them. A theme you chose is never overridden, and `:theme` still changes it.

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
- The menu is its own small Bubble Tea screen, centred in the terminal and drawn in the theme the app will open with (saved theme, else the default, else `system` on a 16-colour terminal). A little motion - the wordmark draws in, a highlight sweeps across it, the tagline types itself out - stays out of the way of the keys (1-3, arrows and Enter all work at once); `SYNQ_NO_ANIMATION=1` shows it still. It drops detail (descriptions, blurb, big wordmark) as the terminal shrinks, and falls back to a plain numbered prompt when input or output is not a terminal.
- **Create your identity** — the existing, already-tested passphrase creation flow, unchanged.
- **Quit.**

**Why guest mode reuses the real Model instead of a separate pre-TUI menu for feed/theme browsing:** theme selection already lives entirely in the command palette (`:theme`), backed by the same preferences table already proven independent of the identity vault (see the `TestPreferencesAreIndependentOfIdentityVault` test in `internal/db`). Building a second, separate theme-picking mechanic for guests would duplicate an already-working feature for no reason. The one thing guest mode deliberately does NOT support is creating an identity mid-session — that still requires quitting and choosing "Create your identity" from the menu, keeping passphrase entry in the single, already-tested, non-interactive terminal flow rather than adding a new masked-input widget inside Bubble Tea. In-session signup (no restart required) is a reasonable future enhancement once there's appetite for building that input widget.

**How this connects to the Feed's anonymization rule (synq-server-DESIGN.md section 5):** the server decides whether to send real usernames or a flat `"node"` placeholder based on whether the request carries a valid, authenticated session — not on whether the client happens to have a local identity. A guest who has never registered/logged in and someone who has generated a local identity but never connected to a server look identical to the server. The client itself does no redaction; it only ever displays whatever `author` field the server actually sent.

## 11. File transfer

Files move over synq-server's HTTP Claim Check endpoints (never the WebSocket), up to 100 MB, and only between active nodes. The server stores ciphertext and nothing else; everything that identifies or opens a file stays between the two clients.

**Encryption.** Each file gets a fresh random 256-bit key, used for that file only. The file is sealed in 64 KiB chunks with AES-256-GCM (`internal/filecrypt`). A chunk's nonce is its index, and its authenticated data marks whether it is the last chunk, so reordering, duplicating, truncating (even at a chunk boundary) or extending the ciphertext all fail authentication. The sender writes the ciphertext to a scratch file first, because the offer must commit to its exact size and SHA-256 before the upload begins.

**The key and filename never touch the server.** After the upload the sender sends an *offer* - attachment id, filename, size, ciphertext hash and the file key - as an ordinary sealed chat message, so it gets the same forward secrecy, sender authentication and replay protection as text (a leading NUL byte, which can't be typed, marks the body as an offer). The server's own metadata is just the size, the hash and a non-secret algorithm label. The consequence is that an offer only reaches a contact who is online and past the handshake, exactly like a chat message: the server keeps no queue, and nothing of a file outlives the sender's session (quitting terminates it, which deletes the blobs).

**Receiving.** An offer is listed in `:files`; nothing is downloaded until the user chooses. The client then claims, downloads, and checks, in order: the size matches what the offer implies, the SHA-256 matches the sender's committed hash (carried in the encrypted offer, so the server could not have chosen it), every chunk authenticates, and the decrypted length matches. Only then does it tell the server the download is complete - that report asserts the file is authenticated, so it is never sent earlier - and the plaintext appears under its real name in a private (0700) cache directory. Any failure cancels the download and deletes what was written.

**View, save, ignore.**
- *View* opens the cached file in the operating system's default application for its type. That hands plaintext to another program, which is what viewing is, so it is refused for anything an OS would run (executables, scripts, shortcuts, macro documents, installers, and files with no extension); those can only be saved. The opener is started without a shell, with the path as one argument.
- *Save* copies the file to the directory set with `:savedir`, never overwriting (`name (1).ext`), created 0600 so it is never executable. Nothing is saved anywhere until the user asks.
- *Ignore* deletes the decrypted copy and tells the server.
- Choosing view or save on a file that hasn't been downloaded downloads it first and then does it.

The cache is wiped at startup and deleted on quit and on `:logout`, so a file that was viewed but not saved does not outlive the session - save is what keeps it (section 4).

**Untrusted input.** A contact controls the filename, size and every other field of an offer. Filenames are reduced to a bare, bounded, platform-safe name (no directory parts, control or bidirectional-override characters, or Windows reserved names); ids are checked before they become a path; sizes are range-checked. Chat message text is stripped of terminal control characters before drawing, so a contact can't send escape sequences through the chat view.

**Server session.** Attachments hang off a server-side session with a 60-second lease. The client creates one when it has a token, renews it every 20 seconds, starts a new one if the server reports the old one gone, and terminates it on quit.

## 12. Synapses (rooms)

A room is a small (16 at most), invite-only, ephemeral group chat. The server keeps membership and a message counter and relays sealed bytes; it never holds message text (see `ROOMS.md` in synq-server).

- **Key.** The owner makes a random 256-bit room key and sends it to each member as a *direct room frame* the server relays between two joined members, with the roster (user id, username, Ed25519 key, X25519 key). Each payload is sealed with the two ends' static X25519 keys (HKDF-SHA256 over the shared secret, AES-256-GCM, room id and both public keys bound in), so only the owner could have made it and only that member can read it. The owner's box key is the one the invitee looked up and verified (binding signature + pin) when accepting, and the first state must describe the owner by exactly those keys; every other member's box key was verified by the owner at invitation time. No earlier chat is needed between sender and recipient, which is what lets a new owner serve members the old owner knew. Members need no relationship with each other, only with the owner - which is also the rule for who can be invited.
- **Messages.** Each is signed by its author's identity key, then sealed under the room key with AES-256-GCM (room id and key generation as associated data). The receiver verifies against the roster key of the sender the server names, so a relay that relabels a message, or a member who holds the room key, cannot pass something off as someone else's. Replies point at a message id inside the signed payload. A message id already shown is ignored.
- **Order.** The server numbers messages per room; the client places them by that number, so everyone sees one order.
- **Lifetime.** Everything is memory-only. The room ends when the owner ends it, when the owner quits (a prompt asks first), or when the owner has been disconnected for `ROOM_OWNER_GRACE_SECS` (default 90). Ending deletes the room server-side and wipes keys and messages client-side. A room can't be rejoined after a restart: the key died with the process.
- **Removal and rotation.** When anyone leaves or is removed (`:synapse kick`), the owner rotates to a fresh key (generation + 1) and sends it to those still in, so the person who went can read nothing sent afterwards. The previous generation is kept until the next rotation so messages already in flight still open. A message sent under a key a member hasn't received yet is dropped.
- **Handover.** The owner offers the room to a joined member (`:synapse transfer`), who accepts or declines; the switch is one atomic server update and pending invitations are withdrawn, so the new owner re-invites whom it chooses. The new owner already holds the key and roster, so nothing needs sending. Members adopt the server's ownership announcement only if the new owner is already on their roster, and a member whose key had not yet arrived leaves instead. A malicious server colluding with a member could still install that member as owner.
- **Files.** `:send` in a synapse encrypts the file under a fresh key (the same chunked AES-256-GCM as 1:1 files) and uploads the ciphertext as a room-owned blob; the file id, name, size, ciphertext hash and key go in a signed, room-sealed message, so the server never sees them and the author's signature covers the hash a download is checked against. Any joined member downloads on demand (no per-recipient offer or claim), the hash and chunk authentication are checked, and the decrypted copy lives in the ephemeral cache until saved with `s` in `:files`. Ending the synapse deletes every blob on the server and wipes every client's unsaved copies; a removed member can no longer download. A synapse holds 20 files and 300 MB.
- **Limits, stated plainly.** A member running a modified client can keep what they receive. The server can drop or delay messages and could end a room early. Members trust the owner for the roster. A member who has opened a shared file can keep it, like any other message.

## 13. Online status

The Nodes tab marks each connected node `●` (online) or `○` (offline), and the command bar says "bob is online." when one arrives. This is deliberate metadata the user can switch off: it is **on by default, symmetric and opt-out**. `:presence off` hides you from your nodes *and* hides them from you, so nobody can watch others while staying invisible. Only active nodes who both share are ever told; strangers, pending requests and blocked users never see or receive anything, and the server still gives a sender no delivery or "recipient offline" signal for messages. Presence is per user, not per device (see section 5).

## 14. Deleting an account

There was no way to leave: usernames were permanent, so a forgotten identity, a test account or an abandoned registration held its name forever. Now `:delete-account <username> confirm` calls `DELETE /users/me` and the server removes the account, its nodes, posts, files and the synapses it owns (which end for everyone), freeing the name. The local identity is kept - the keys are yours - so `:register` can claim a name again. The username you type must be your own, a guard against a stray command, since there is no undo.

`synq --forget` cannot do this for you, because it exists for the case where the passphrase is lost and nothing can be signed. It prints the username that stays claimed. The server also deletes accounts that have gone unused for a while (default 60 days) and have no active node, post, comment or synapse, so an abandoned name is not held forever.

## Summary table

| Area | Decision |
|---|---|
| Keys at rest | Argon2id-derived passphrase encrypts secret keys via `nacl/secretbox`; prompted every launch, memory-only, no recovery |
| Key authenticity | TOFU by default, hard-warn on key change, optional `:verify` fingerprint command tied to GitHub identity |
| Forward secrecy | Per-launch ephemeral X25519 keypair, X3DH-style session key derivation via HKDF |
| Ephemeral cache | Thread history persists across tab switches within a session; quitting Synq purges everything unsaved; explicit save for attachments |
| Multi-device | Single-device only, documented limitation, no sync |
| Piped input | ANSI/control sequences stripped before storage or rendering |
| File transfer | Per-file key, chunked AES-256-GCM; key and name travel in the encrypted chat channel, not to the server; view (default app, never for runnable types) or save (to `:savedir`) after verification |
| Theming | Explicit theme choice or one-time `RequestBackgroundColor`, no auto-detection |
| `$EDITOR` | Falls back to built-in input if unset; any editor error/abort discards the draft |
| Synapses | Invite-only groups of up to 16; owner-distributed room key over sealed direct frames, re-keyed on removal, transferable, signed AES-256-GCM messages, memory-only, files shared room-wide and wiped when it ends, ends with its owner |
| GitHub verification | OAuth Device Flow, optional, separate from registration |
| Online status | Dot on the Nodes tab; on by default, symmetric, opt-out with `:presence off`; active nodes only |
| Account deletion | `:delete-account <username> confirm`; server also reaps unused accounts after a while |

# Synq

Synq is a terminal-native, keyboard-driven client for the Synq developer network — a place to post, chat, and connect with other developers without leaving the terminal. All private messaging is end-to-end encrypted on the client before it ever reaches the network.

This repository contains the Go client. The companion backend, [`synq-server`](https://github.com/HomieB-tt/synq-server), is a private repository — the link is provided for context and will only resolve for those with access.

## What it does

Synq gives you four core views, all reachable without touching a mouse:

- **Feed** — a global stream of posts tagged `[PROJECT]`, `[HIRING]`, or `[GENERAL]`. Pipe command output straight into a post (`cat error.log | synq post`), or open your `$EDITOR` mid-draft with `Ctrl+O`.
- **Nodes** — your developer network. Connections move through `PENDING`, `ACTIVE`, and `BLOCKED` states.
- **Chat** — ephemeral, end-to-end encrypted direct messages. The server relays bytes; it never sees plaintext.
- **Profile** — your identity, including a local display name (`:name`, see below), GitHub verification status (`[✓ GitHub: @username]`) and the active local Git repository, shown for context as you work.

## Why terminal-native

Synq is built for developers who live in the terminal/CLI. Every interaction ( navigation, posting, messaging, command invocation) is keyboard-first, fast, and scriptable. It's meant to sit alongside `vim`, `tmux`, and `git`, not replace a browser tab.

## Security model

- On first launch, Synq generates an Ed25519 keypair (identity and signing) and an X25519 keypair (key exchange) locally.
- Secret keys are stored in the local SQLite key store and never transmitted.
- Private messages are encrypted client-side, per recipient, before being sent over the WebSocket connection. The server only ever handles ciphertext.
- There is no server-side plaintext, no server-side decryption capability, and no message persistence beyond the sender and recipient's own local caches.

## Tech stack

| Purpose | Library |
| --- | --- |
| TUI / Elm-architecture state engine | `charm.land/bubbletea/v2` |
| Styling & layout | `charm.land/lipgloss/v2` |
| Form components & inputs | `charm.land/bubbles/v2` |
| Syntax highlighting | `github.com/alecthomas/chroma/v2` |
| Local cache & key store | `modernc.org/sqlite` (CGO-free) |
| Identity & encryption | `crypto/ed25519`, `golang.org/x/crypto/nacl/box` (X25519) |
| Networking | WebSocket client with automatic reconnect |

Requires Go 1.23+.

## Keyboard reference

| Key | Action |
| --- | --- |
| `1` `2` `3` `4` | Switch to Feed / Nodes / Chat / Profile |
| `Tab` / `Shift+Tab` | Move focus between panes |
| `:` or `Ctrl+P` | Open command palette |
| `Ctrl+O` | Open `$EDITOR` while composing a post |
| `q` / `Ctrl+C` | Quit |

## Command palette

| Command | Does |
| --- | --- |
| `:theme` | Open the interactive theme picker - scroll with `↑`/`↓` or `j`/`k` to live-preview each theme across the whole UI, `Enter` to apply and persist it, `Esc` to cancel and restore whatever was active before. |
| `:theme <name>` | Set a theme directly, skipping the picker. Persists across restarts. Run `:theme` with no argument to see every available name. |
| `:name <your name>` | Set a local display name, shown in your own Profile tab. Purely a local, free-form, trivially-changeable label - not the same thing as the permanent `:register`ed username below (see `DESIGN.md`'s note on `PrefUsername`/`PrefDisplayName` for why those are never interchangeable). |
| `:name clear` | Clear your local display name. |
| `:verify <hex-pubkey>` | Compute a comparable fingerprint between your identity and a contact's, for out-of-band verification (see `DESIGN.md` section 2). Takes a raw hex-encoded public key directly - unlike `:chat`, this doesn't go through a username lookup yet. |
| `:register <username>` | Register a username with `synq-server`, tied to this identity. **Permanent** - there's no rename endpoint. Only needed once; if you skipped the first-launch prompt, this is the same flow. Requires `SYNQ_SERVER_URL`. |
| `:login` | Manually re-authenticate after `:logout`, without restarting Synq. You won't normally need this - a registered identity logs in automatically and silently on every launch (see Configuration below). |
| `:logout` | Revoke all of this account's sessions and disconnect. |
| `:chat <username>` | Open an existing thread with a contact, or start a new one, looked up by their registered username. Chat history is session-only (see `DESIGN.md` section 4) and, for now, local only - composing and sending a message appends it to your own view, but nothing is actually transmitted yet, since the WS wire message format `synq-server` expects for chat isn't wired up in `internal/ws` yet. Requires being logged in. |
| `:github` | Link your GitHub account via OAuth Device Flow (see Configuration below). Optional - grants a verification badge only, unrelated to Synq's own identity/auth. |
| `:quit` | Same as pressing `q`. |

## Configuration

**`SYNQ_SERVER_URL`** - the base URL of a `synq-server` deployment (e.g. `https://synq-server-production.up.railway.app`), used for both REST calls and the WS connection - the WS endpoint is derived from this automatically (same host, `/ws` path, scheme swapped to `ws`/`wss`). Without this set, Synq never attempts any server connection: guest Feed browsing, `:register`, `:login`, `:chat`, and the live WS connection are all unavailable, and the header's connection indicator stays "offline" permanently. There's no default to fall back to.

Logging in is automatic for a returning, already-registered identity: on every launch, right after your passphrase unlocks the vault, Synq silently tries to refresh your stored session, falling back to a full (but still silent - no prompt) re-login using your already-unlocked identity if that fails. You'll only ever see `:login` needed manually after an explicit `:logout`.

**`SYNQ_GITHUB_CLIENT_ID`** - required only if you want to use `:github`. GitHub verification is off by default; without this set, `:github` just tells you it isn't configured rather than failing partway through.

To set it up:

1. On GitHub, go to **Settings → Developer settings → OAuth Apps → New OAuth App** (a plain OAuth App, not a GitHub App).
2. Fill in a name and homepage URL (anything - GitHub requires a value here, but the Device Flow used by Synq never redirects to it).
3. After creating the app, enable **"Enable Device Flow"** in its settings. This is off by default and the flow will fail without it.
4. Copy the **Client ID** (not the client secret - Device Flow doesn't use one) and set it before running Synq:

   ```
   export SYNQ_GITHUB_CLIENT_ID=your_client_id_here
   ```

Note that right now, a successful `:github` link is verified against GitHub directly and stored locally only - Synq doesn't yet call `synq-server`'s `/auth/github/verify` to record it server-side (see `API.md`), so the badge is currently self-asserted rather than independently confirmed by anyone else who might look you up. See `synq-server-DESIGN.md` section 1 for the eventual full picture.

## Project layout

```
synq/
├── cmd/synq/           entry point
├── internal/
│   ├── app/            Bubble Tea root model, update, and view loops
│   ├── crypto/         key generation, E2EE encrypt/decrypt logic
│   ├── db/             SQLite setup and offline cache queries
│   ├── ui/
│   │   ├── components/ feed, chat input, sidebar, and other custom components
│   │   └── styles/     theme palettes (see internal/ui/styles/styles.go for the full list)
│   └── ws/             WebSocket client connection and reconnect handling
├── go.mod
└── README.md
```

## License

This project is under a **Proprietory Software** license - see the [LICENSE](LICENSE) file for details.

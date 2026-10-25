# Synq

Synq is a terminal-native, keyboard-driven client for the Synq developer network — a place to post, chat, and connect with other developers without leaving the terminal. All private messaging is end-to-end encrypted on the client before it ever reaches the network.

This repository contains the Go client. The companion backend, [`synq-server`](https://github.com/HomieB-tt/synq-server), is a private repository — the link is provided for context and will only resolve for those with access.

## Downloads

Prebuilt binaries for Linux, macOS, and Windows are available from the [GitHub Releases](https://github.com/HomieB-tt/Synq/releases) page. Choose the archive matching your operating system and processor architecture (`amd64` for most Intel/AMD computers, `arm64` for Apple Silicon and ARM computers). Each release includes a `checksums.txt` file for verifying downloads.

### Linux (Debian, Fedora, Arch, and others)

Extract the `.tar.gz` archive, then install the binary into `/usr/local/bin`, which is normally already on your `PATH`:

```sh
tar -xzf synq_VERSION_linux_ARCH.tar.gz
sudo install -m 755 synq /usr/local/bin/synq
```

Replace `VERSION` and `ARCH` with the values in the downloaded archive's filename. Alternatively, install it just for your user:

```sh
mkdir -p "$HOME/.local/bin"
install -m 755 synq "$HOME/.local/bin/synq"
```

If `~/.local/bin` is not already on your `PATH`, add this line to your shell startup file (for example, `~/.bashrc` or `~/.zshrc`) and open a new terminal:

```sh
export PATH="$HOME/.local/bin:$PATH"
```

### macOS

Signed and notarized releases provide a `.zip` archive. Extract it and install Synq into your user-local bin directory:

```sh
unzip synq_VERSION_darwin_ARCH.zip
mkdir -p "$HOME/.local/bin"
install -m 755 synq "$HOME/.local/bin/synq"
```

Older, unsigned releases provide a `.tar.gz` archive instead; extract those with `tar -xzf synq_VERSION_darwin_ARCH.tar.gz`.

The default macOS shell is zsh. If `~/.local/bin` is not already on your `PATH`, add the following line to `~/.zshrc`, then open a new terminal:

```sh
export PATH="$HOME/.local/bin:$PATH"
```

macOS releases are signed and notarized when the repository's Apple signing secrets are configured; otherwise the release workflow publishes the unsigned archives. Older releases published before signing was configured remain unsigned.

### Windows

Extract the `.zip` archive to a folder such as `%LOCALAPPDATA%\Programs\Synq`. To run `synq` from any terminal, add that folder (the one containing `synq.exe`) to your user `Path`:

1. Open **Settings**, search for **Environment Variables**, and choose **Edit environment variables for your account**.
2. Select **Path** under user variables, choose **Edit**, then **New**.
3. Enter the folder containing `synq.exe`, save the changes, and open a new terminal.

You can then run `synq.exe` from PowerShell or Command Prompt.

### macOS release signing (maintainers)

To enable signing and notarization for future tagged releases, configure these repository Actions secrets:

| Secret | Value |
| --- | --- |
| `APPLE_CERTIFICATE` | Base64-encoded Developer ID Application `.p12` certificate |
| `APPLE_CERTIFICATE_PASSWORD` | Password used when exporting the `.p12` certificate |
| `APPLE_SIGNING_IDENTITY` | Full Developer ID Application signing identity |
| `APPLE_API_KEY` | Base64-encoded App Store Connect API `.p8` key |
| `APPLE_API_KEY_ID` | App Store Connect API key ID |
| `APPLE_API_ISSUER_ID` | App Store Connect API issuer ID |

The certificate and App Store Connect API key must belong to an Apple Developer team enrolled in the Apple Developer Program. The release workflow signs each macOS binary, submits its archive to Apple's notary service, and publishes the notarized archives. Releases remain drafts until the release and notarization jobs complete. If none of the secrets are set, notarization is skipped and the unsigned macOS archives are published; if only some are set or notarization fails, the release remains a draft.

## What it does

Synq gives you four core views, all reachable without touching a mouse:

- **Feed** — a global stream of posts tagged `[PROJECT]`, `[HIRING]`, or `[GENERAL]`. Write posts in the TUI (`p` or `:post`) and open your `$EDITOR` mid-draft with `Ctrl+O`. Pipe command output straight into a post without opening the TUI: `tail -n 50 error.log | synq post -c project -t "build broke"` (escape sequences are stripped first; see below).
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
- **There is no passphrase recovery** (`DESIGN.md` section 1): the passphrase is the only thing that can open the vault, and it is never stored anywhere - forgotten passphrase means forgotten identity, by design.
- **Starting over after a forgotten passphrase:** quit and run `synq --forget`. It *renames* the key store beside itself (e.g. `~/.config/synq/synq.db.bak-20261007-150405`) - it is never deleted - and the next launch opens the first-run menu as if the device were new. The archive still holds the old identity plus your local preferences (theme, key pins, GitHub handle); delete it yourself whenever you decide you no longer need it. Synq prints this hint after two failed passphrase attempts, so the escape hatch is discoverable from the prompt that would otherwise loop forever.

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
| `?` | Open the help panel (keys + commands); any key closes it |
| `Ctrl+O` | Open `$EDITOR` while composing a post or comment |
| `c` | Profile tab: browse every command and what it does |
| `←` `→` `Home` `End` | In any text box (chat message, command palette, post/comment): move the cursor, so a typo can be fixed in place. `Backspace` and `Delete` act at the cursor. |
| `Alt+←` `Alt+→` (or `Ctrl+←` `Ctrl+→`) | Jump by word |
| `Ctrl+W` / `Ctrl+U` / `Ctrl+K` | Delete the previous word / back to the start of the line / on to the end of the line |
| `↑` `↓` | In a post or comment body: move between lines |
| `PgUp` `PgDn` | Chat thread: scroll. `Ctrl+Home` / `Ctrl+End` jump to the oldest / newest message |
| `↑`/`↓` `Enter` `p` `r` `m` | Feed tab: select, open a post and its comments, write a post, refresh, load more. `[` / `]` switch the category (All, Project, Hiring, General); posts that arrived since you last looked are marked `●`/`new`. In an open post: `↑`/`↓` `PgUp`/`PgDn` `Home`/`End` scroll, `m` loads the next 50 comments (a long thread loads a page at a time), `c` comment, `r` refresh, `x` x2 delete your own, `Esc` back. |
| `q` / `Ctrl+C` | Quit |

## Receiving messages

Messages from your active nodes arrive on their own - you do not have to run `:chat` first, or have their thread open. Both of you do have to be online: synq-server never stores a message, so one sent to someone who is offline is simply not delivered. A thread with unread messages shows `● N new` in the Chat list, the Chat tab shows `(N new)` in the accent colour, and the bottom bar names the sender. Opening the thread clears them and draws a "N new messages" line above the first unread one, scrolled into view; the line stays where it is while you keep the thread open. Threads with unread messages sort to the top of the list, then by latest message. The Nodes tab shows `(N pending)` while requests are waiting on you.

On the Nodes tab, `↑`/`↓` (also `PgUp`/`PgDn`, `g`/`G`) move through requests, nodes and requests you sent; `a` accepts a request, `Enter` accepts it or opens a chat with a connected node, and `b` explains how to block (it never blocks by itself). GitHub-verified users show `✓ @handle`. The Feed shows each post's category as a coloured tag, and the Profile prints your public key in blocks of four digits so it can be read aloud when you `:verify` a contact.

## Command palette

Run a command that needs an argument with none - `:send`, `:savedir`, `:chat`, `:node`, `:accept`, `:verify`, `:register`, `:name` - and a prompt opens asking for it (as `:theme` opens its picker), so there is no syntax to remember. Typing the argument yourself, as in `:send ~/report.pdf`, still works. Not sure what is available? `:commands` lists everything, and the Profile tab's `c` key opens the same list.

| Command | Does |
| --- | --- |
| `:theme` | Open the interactive theme picker - scroll with `↑`/`↓` or `j`/`k` to live-preview each theme across the whole UI, `Enter` to apply and persist it, `Esc` to cancel and restore whatever was active before. |
| `:theme <name>` | Set a theme directly, skipping the picker. Persists across restarts. Run `:theme` with no argument to see every available name. |
| `:name <your name>` | Set a local display name, shown in your own Profile tab. Purely a local, free-form, trivially-changeable label - not the same thing as the permanent `:register`ed username below (see `DESIGN.md`'s note on `PrefUsername`/`PrefDisplayName` for why those are never interchangeable). |
| `:name clear` | Clear your local display name. |
| `:verify <username>` | Compare your identity's fingerprint against the key pinned for that username (`DESIGN.md` section 2), for out-of-band verification. If a key change is pending, it compares against the **new**, not-yet-accepted key - the one you're about to trust. Requires having looked the username up at least once (`:chat` them first). |
| `:verify <hex-pubkey>` | Same fingerprint, computed against a raw hex-encoded public key instead - for a key someone handed you directly, with no lookup involved. |
| `:accept <username>` | Trust a contact whose public key **changed** since you pinned it (the hard warning of `DESIGN.md` section 2). Until you do, sending to them is blocked; compare fingerprints with `:verify` first if you want to check out-of-band. |
| `:post [project\|hiring\|general]` | Write a Feed post (also `p` on the Feed tab). `Tab` switches between title and content, `Ctrl+G` changes the category, `Ctrl+O` edits the content in `$EDITOR`, `Ctrl+S` sends, `Esc` discards. Title up to 120 characters, content up to 10,000. Needs a login. |
| `:report [reason]` | Report the post you have open (Feed tab, `Enter`). |
| `:node <username>` | Send a node request to someone by their exact username (there's no user search - they have to tell you). Once they accept, you can `:chat` and `:send` to each other. |
| `:nodes` | Open the Nodes tab and refresh it. Also `r` while on that tab. Shows requests waiting for you, your active nodes, and requests you've sent. |
| `:accept <username>` | Accept a node request from that user. (The same command also trusts a contact's changed key - see below.) |
| `:presence [on\|off]` | Show or hide your online status. Connected nodes show a dot on the Nodes tab (`●` online, `○` offline) and a message appears when one comes online. On by default; `off` hides you from them and them from you. |
| `:block <username> confirm` | Block a user. Silent - they aren't told - and there's no undo from the client. Blocked users never appear in the list. |
| `:send <path>` | Send a file (up to 100 MB) to the contact in the open chat - use `Ctrl+P` to reach the command palette from inside a thread. Only works between active nodes. The file is encrypted on your machine; synq-server never sees its contents, name or key. It stays available while you remain connected. |
| `:files` | List files contacts have offered you. `Enter` downloads one and checks it; `v` opens it in your default application; `s` saves it to your save folder; `i` ignores it. Anything not saved is deleted when you quit. |
| `:savedir [path]` | Show or set the folder `s` saves files to (created if missing; `~` works). Files are never overwritten. |
| `:register <username>` | Register a username with `synq-server`, tied to this identity. **Permanent** - there's no rename endpoint. Only needed once; if you skipped the first-launch prompt, this is the same flow. Requires a configured server. |
| `:login` | Manually re-authenticate after `:logout`, without restarting Synq. You won't normally need this - a registered identity logs in automatically and silently on every launch (see Configuration below). |
| `:logout` | Revoke all of this account's sessions and disconnect. |
| `:chat <username>` | Open an existing thread with a contact, or start a new one, looked up by their registered username. Messages go out end-to-end encrypted: a per-launch ephemeral handshake derives a session key (`DESIGN.md` sections 2-3), and the relay only ever sees ciphertext. The first lookup also pins their public key - a later change blocks sends until `:accept`. Chat history is session-only (see `DESIGN.md` section 4). Requires being logged in. |
| `:synapse <new\|invite\|accept\|decline\|leave\|kick\|transfer\|end\|members\|connect\|list> ...` | Synapses: small, invite-only group chats of up to 16 people that vanish when they end. `:synapse new <name>` makes one and you own it; `:synapse invite <username>` invites someone you have an active node with; they join with `:synapse accept <name>`. A synapse is a thread in the Chat tab (marked `#`); `Ctrl+R` there steps through messages to reply to. Messages are end-to-end encrypted and signed, so the server can't read them and one member can't write as another (`DESIGN.md` section 12). `:send <path>` shares a file with everyone in it (up to 100 MB; 20 files and 300 MB per synapse); members get it in `:files` to view or save, and anything not saved is wiped, here and on the server, when the synapse ends. The owner can remove someone (`:synapse kick <username>`; the key is changed so they read nothing new) or hand the synapse to a joined member (`:synapse transfer <username>`; they `:synapse accept` or `:synapse decline`), after which quitting no longer ends it for the old owner. A synapse lasts only while its owner is here: `:synapse end confirm`, quitting Synq (you are asked first), or the owner going offline for about 90 seconds ends it for everyone and wipes it. |
| `:github` | Link your GitHub account via OAuth Device Flow (see Configuration below). Optional - grants a verification badge only, unrelated to Synq's own identity/auth. |
| `:commands` | Open the command list - every command, grouped, with an explanation of what it does. `↑`/`↓` select, `Enter` runs it (commands that need an argument open a prompt for it), `Esc` closes. On the Profile tab, press `c`. |
| `:welcome` | Show the getting-started guide again. It is shown once, on first launch. |
| `:help` | Same as pressing `?` - the full key/command reference. |
| `:server [address\|reset]` | Show or change the synq-server Synq connects to. Takes effect after a restart. |
| `:ping` | Time a few requests to the server (first one includes connection setup) to tell a slow server from a slow link. The header also shows the latest round trip beside `● online` (e.g. `● online · 158 ms`), refreshed every 30 s while logged in, when it fits. |
| `:boot on\|off` | Show (or stop showing) the startup checklist on the next launch. On by default; the splash itself is always skippable with any key. |
| `:quit` | Same as pressing `q`. |

## Configuration

**Server address** - Synq needs to know which `synq-server` to talk to. You normally don't have to do anything: release builds ship with a default server baked in, so you download, run, register and go. If your build has no default, Synq asks for the address on first run and remembers it. Change it any time with `:server <address>` (takes effect after a restart; `:server reset` clears it) and check the connection with `:ping`. The order of precedence is: the `SYNQ_SERVER_URL` environment variable, then the address saved in the app, then the built-in default. Without any of them Synq never attempts a server connection: guest Feed browsing, `:register`, `:login`, `:chat`, and the live WS connection are unavailable, and the header's connection indicator stays "offline".

**`SYNQ_SERVER_URL`** - optional override (e.g. `https://synq-server-production.up.railway.app`), used for both REST calls and the WS connection - the WS endpoint is derived from this automatically (same host, `/ws` path, scheme swapped to `ws`/`wss`).

**Maintainers:** set the `SYNQ_DEFAULT_SERVER_URL` repository variable before tagging a release; GoReleaser links it into the binary (`-X main.defaultServerURL=...`). Local builds: `go build -ldflags "-X main.defaultServerURL=https://..." ./cmd/synq`.

**`SYNQ_ALLOW_INSECURE_HTTP`** - Synq refuses to start with a cleartext `http://` `SYNQ_SERVER_URL` unless the host is loopback (`localhost`, `127.0.0.1`, `::1`), because tokens would cross the network unencrypted. Set this to `1` to override on a network you trust, such as a home LAN.

**`SYNQ_NO_ANIMATION`** - set to `1` to show the first-run screen without its small animations.

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

## Posting from a pipe

```
cat error.log | synq post [-c project|hiring|general] [-t title]
```

`synq post` reads stdin, posts it to the Feed as you, prints the new post's ID on stdout and exits; the TUI never starts. Your passphrase is asked for on the terminal (not stdin, which carries the post), so it needs a terminal to be available. Without `-t`, the title is the first line of the input. Posts are limited to 10000 characters (title: 120), so for long output keep the part that matters, e.g. `tail -n 50`.

Everything piped in is sanitized first: ANSI colour/cursor sequences, OSC titles and links, and other control characters are removed, carriage-return progress bars keep their last state, and tabs become spaces. Piped text cannot drive your terminal or anyone else's.

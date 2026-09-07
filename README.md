# Synq

Synq is a terminal-native, keyboard-driven client for the Synq developer network — a place to post, chat, and connect with other developers without leaving the terminal. All private messaging is end-to-end encrypted on the client before it ever reaches the network.

This repository contains the Go client. The companion backend lives in [`synq-server`](../synq-server).

## What it does

Synq gives you four core views, all reachable without touching a mouse:

- **Feed** — a global stream of posts tagged `[PROJECT]`, `[HIRING]`, or `[GENERAL]`. Pipe command output straight into a post (`cat error.log | synq post`), or open your `$EDITOR` mid-draft with `Ctrl+O`.
- **Nodes** — your developer network. Connections move through `PENDING`, `ACTIVE`, and `BLOCKED` states.
- **Chat** — ephemeral, end-to-end encrypted direct messages. The server relays bytes; it never sees plaintext.
- **Profile** — your identity, including GitHub verification status (`[✓ GitHub: @username]`) and the active local Git repository, shown for context as you work.

## Why terminal-native

Synq is built for developers who live in a terminal. Every interaction — navigation, posting, messaging, command invocation — is keyboard-first, fast, and scriptable. It's meant to sit alongside `vim`, `tmux`, and `git`, not replace a browser tab.

## Security model

- On first launch, Synq generates an Ed25519 keypair (identity and signing) and an X25519 keypair (key exchange) locally.
- Secret keys are stored in the local SQLite key store and never transmitted.
- Private messages are encrypted client-side, per recipient, before being sent over the WebSocket connection. The server only ever handles ciphertext.
- There is no server-side plaintext, no server-side decryption capability, and no message persistence beyond the sender and recipient's own local caches.

## Tech stack

| Purpose | Library |
|---|---|
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
|---|---|
| `1` `2` `3` `4` | Switch to Feed / Nodes / Chat / Profile |
| `Tab` / `Shift+Tab` | Move focus between panes |
| `:` or `Ctrl+P` | Open command palette |
| `Ctrl+O` | Open `$EDITOR` while composing a post |
| `q` / `Ctrl+C` | Quit |

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
│   │   └── styles/     theme palettes (Dracula, Nord, Monokai)
│   └── ws/             WebSocket client connection and reconnect handling
├── go.mod
└── README.md
```

## License

Other.

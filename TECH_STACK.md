# Synq — Tech Stack

Exact dependency versions pinned in `go.mod`, current as of September 2026. A couple of these differ from the original spec document — noted below with why.

| Dependency | Version | Purpose |
|---|---|---|
| Go | 1.23+ | Language/toolchain |
| `charm.land/bubbletea/v2` | v2.0.9 | Elm-architecture TUI state engine |
| `charm.land/bubbles/v2` | v2.2.1 | Form components, text inputs, viewport, etc. |
| `charm.land/lipgloss/v2` | v2.0.6 | Styling & layout engine |
| `github.com/alecthomas/chroma/v2` | v2.14.0 | Syntax highlighting for code blocks |
| `modernc.org/sqlite` | v1.33.1 | CGO-free local SQLite cache and key store |
| `golang.org/x/crypto` | v0.28.0 | `nacl/box`, `nacl/secretbox`, `argon2` |
| `golang.org/x/term` | v0.25.0 | Terminal size/raw-mode helpers |
| `github.com/gorilla/websocket` | v1.5.3 | WebSocket client |
| `crypto/ed25519` | stdlib | Identity/signing keys |

## Note on import paths

The original spec listed `github.com/charmbracelet/bubbletea/v2` and sibling packages. Charm moved their v2 libraries to a vanity domain in early 2026 — the correct current import paths are:

```go
import (
    tea     "charm.land/bubbletea/v2"
    lipgloss "charm.land/lipgloss/v2"
    "charm.land/bubbles/v2/textarea"
    // ...
)
```

Using the old `github.com/charmbracelet/...` path for v2 will fail to resolve.

## Note on crypto

The spec listed `crypto/ed25519` and `golang.org/x/crypto/nacl/box`. Per the design decisions in `DESIGN.md`, the final crypto surface also uses:

- `golang.org/x/crypto/nacl/secretbox` — for encrypting the local key store (§1) and for session-key message encryption after the X3DH-style handshake (§3)
- `golang.org/x/crypto/argon2` — for deriving the key-store encryption key from the user's passphrase (§1)
- `golang.org/x/crypto/hkdf` — for deriving the per-session key from the ephemeral/static Diffie-Hellman outputs (§3)

All three are included in the `golang.org/x/crypto` module already pinned above — no extra dependency needed.

## Note on internal/github

The GitHub Device Flow client (`internal/github/device_flow.go`) uses only the Go standard library (`net/http`, `encoding/json`) - no dependency was added for this. Its test suite (`device_flow_live_test.go`) makes real requests to `github.com` and `api.github.com` with a deliberately invalid client ID, to verify the request-building and response-parsing logic against GitHub's actual API shape rather than an assumption about it. This means `go test ./...` makes real outbound network calls for this package specifically - expect those two tests to fail (not hang) if run somewhere without internet access, such as an offline CI runner.

## Versioning note

These are the latest stable versions as of this scaffold's creation. Before your first build, running `go get -u ./...` followed by `go mod tidy` will pick up any newer patch releases.

# Synq icon and Desktop Integration

Two overlapping chat bubbles: the shared overlap is the synapse. Palette matches
the default (Dracula) theme: `#bd93f9`, `#ff79c6`, `#f8f8f2` on `#282a36`-ish dark.

| File | Use |
| --- | --- |
| `synq.svg` | Master icon (rounded tile, typing dots) |
| `synq-small.svg` | Dot-free variant, used for 16/24/32 px renders |
| `synq-mark.svg` | Transparent mark with no tile (README, docs, banners) |
| `synq-<n>.png` | 16 to 1024 px renders |
| `synq.ico` | Windows (16 to 256 px) |
| `synq.icns` | macOS (128 to 1024 px) |
| `synq.desktop` | Linux desktop launcher entry (`Terminal=true`) |
| `synq-social.png` | 1280x640 GitHub social preview |

---

## Desktop & CLI Integration

Synq ships with support for both command-line use and click-to-launch with the official application icon.

### Linux

* **Debian / Ubuntu / Fedora / RHEL**:
  Releases include `.deb` and `.rpm` packages built with GoReleaser NFPM. Installing the package puts `synq` in `/usr/bin/synq`, installs `synq.desktop`, and installs the icon to `/usr/share/icons/hicolor/`.
* **Tarball (`.tar.gz`)**:
  Run `./scripts/install.sh` (or `./install.sh`) to install `synq` to `~/.local/bin` and register the desktop launcher and icon into `~/.local/share/` without needing root. To uninstall, run `./scripts/uninstall.sh` (or with `--purge` to delete stored data).
* **Click-to-launch**:
  Clicking "Synq" in your application grid launches your desktop's default terminal running `synq`. Exiting `:quit` closes the window.

### macOS

* **CLI**:
  The standalone binary `synq` is included in the release zip. Place it on your PATH (e.g. `/usr/local/bin/synq`).
* **Click-to-launch (`Synq.app`)**:
  Release archives include a self-contained `Synq.app` bundle featuring `synq.icns`. Drag `Synq.app` to `/Applications` to launch Synq from Spotlight, Launchpad, or the Finder. Clicking it opens a Terminal window running `synq`.
* **Automated installer**:
  Run `./scripts/install.sh` to install both the CLI binary and `Synq.app`.

### Windows

* **Embedding icon in `synq.exe`**:
  The GitHub release workflow runs `go-winres` to embed `synq.ico` and metadata directly into `synq.exe`.
  To embed locally:
  ```bash
  go install github.com/tc-hib/go-winres@latest
  go-winres simply --arch amd64,arm64 --icon assets/icon/synq.ico --out cmd/synq/rsrc --product-name "Synq" --file-description "Private, ephemeral terminal messaging"
  ```
* **Click-to-launch & CLI**:
  The resulting `synq.exe` displays the purple chat bubble icon in File Explorer. Double-clicking it opens a console window running Synq. Running `synq.exe` in PowerShell / CMD / Windows Terminal works as standard CLI.

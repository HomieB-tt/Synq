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
| `synq.desktop` | Linux desktop launcher entry (runs `synq-desktop`, which opens your terminal with a Synq window class) |
| `synq-social.png` | 1280x640 GitHub social preview |

---

## Desktop & CLI Integration

Synq ships with support for both command-line use and click-to-launch with the official application icon.

### Linux

* **Debian / Ubuntu / Fedora / RHEL**:
  Releases include `.deb` and `.rpm` packages built with GoReleaser NFPM. Installing the package puts `synq` in `/usr/bin/synq`, installs `synq.desktop`, and installs the icon to `/usr/share/icons/hicolor/`.
* **Tarball (`.tar.gz`)**:
  Run `./scripts/install.sh` (or `./install.sh`) to install `synq` to `~/.local/bin` and register the desktop launcher and icon into `~/.local/share/` without needing root. To uninstall, run `./scripts/uninstall.sh` (add `--purge` to also delete your stored keys and settings; it asks for confirmation first, or pass `--yes`).
* **Click-to-launch**:
  Clicking "Synq" in your application grid runs `synq-desktop`, which opens a terminal window running `synq` and, where the terminal allows it, gives that window the class `synq` so the taskbar can show the Synq icon. `:quit` closes the window.
* **Taskbar icon limits**:
  The launcher icon (menu, search, desktop) always shows. The icon on the taskbar or dock while Synq runs depends on the terminal: Alacritty, kitty, foot and WezTerm accept a window class, but Konsole on KDE Wayland, `gnome-terminal` on Wayland, `ptyxis` and `kgx` do not, so those show the terminal's own icon. Synq itself is unaffected.

### macOS

* **CLI**:
  The standalone binary `synq` is included in the release zip. Place it on your PATH (e.g. `/usr/local/bin/synq`).
* **Click-to-launch (`Synq.app`)**:
  Release archives include a self-contained `Synq.app` bundle featuring `synq.icns`. Drag `Synq.app` to `/Applications` to launch Synq from Spotlight, Launchpad, or the Finder. Clicking it opens a Terminal window running `synq`; while it runs, the Dock shows Terminal's icon, because the running program is Terminal.
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
  The resulting `synq.exe` displays the purple chat bubble icon in File Explorer. Double-clicking it opens a console window running Synq. Running `synq.exe` in PowerShell / CMD / Windows Terminal works as standard CLI. Where Windows Terminal hosts the window, its taskbar entry may show Windows Terminal's icon instead.

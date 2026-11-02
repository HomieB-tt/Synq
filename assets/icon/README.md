# Synq icon

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
| `synq.desktop` | Linux launcher entry |
| `synq-social.png` | 1280x640 GitHub social preview |

## Installing on Linux

    install -Dm644 synq-256.png ~/.local/share/icons/hicolor/256x256/apps/synq.png
    install -Dm644 synq.svg ~/.local/share/icons/hicolor/scalable/apps/synq.svg
    install -Dm644 synq.desktop ~/.local/share/applications/synq.desktop

## Embedding in the Windows exe

    go install github.com/tc-hib/go-winres@latest
    go-winres simply --icon assets/icon/synq.ico

Then build as usual (the generated `.syso` is picked up automatically). The
GitHub social preview is set under Settings, then Social preview.

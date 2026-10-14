package transfer

import (
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxNameBytes = 120

// SafeName turns a filename supplied by a contact into one that is safe
// to create on any platform: no directory parts, no control or
// shell/Windows-special characters, no leading or trailing dots and
// spaces, not a reserved device name, and a bounded length that keeps
// the extension. It never returns an empty string.
func SafeName(name string) string {
	// Drop any directory part, whichever separator the sender's OS uses.
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	name = strings.Map(func(r rune) rune {
		switch {
		case r == utf8.RuneError, unicode.IsControl(r):
			return -1
		case strings.ContainsRune(`<>:"|?*`, r):
			return '_'
		// Bidirectional overrides can make "evil\u202Egpj.exe" display as
		// "evilexe.jpg"; zero-width characters hide what a name says.
		case r == '\u202e', r == '\u202d', r == '\u200b', r == '\u200c', r == '\u200d', r == '\ufeff',
			(r >= '\u2066' && r <= '\u2069'), (r >= '\u202a' && r <= '\u202c'):
			return -1
		}
		return r
	}, name)
	name = strings.Trim(name, ". ")

	if name == "" {
		return "file"
	}

	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	if len(ext) > 16 {
		ext = ext[:16]
	}
	if len(stem)+len(ext) > maxNameBytes {
		limit := maxNameBytes - len(ext)
		if limit < 1 {
			limit = 1
		}
		stem = truncateUTF8(stem, limit)
	}
	stem = strings.TrimRight(stem, ". ")
	if stem == "" {
		stem = "file"
	}
	if reservedWindowsName(stem) {
		stem = "_" + stem
	}
	return stem + ext
}

func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func reservedWindowsName(stem string) bool {
	base := strings.ToUpper(stem)
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	switch base {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return true
	}
	return false
}

// runnable lists extensions an OS will execute, or hand to something
// that executes them, when the file is "opened" with its default
// application. Viewing is refused for these: opening a file a contact
// sent must never be able to run it. Saving them is fine - they land as
// plain, non-executable files the user has to deliberately act on.
var runnable = map[string]bool{
	".exe": true, ".com": true, ".bat": true, ".cmd": true, ".scr": true, ".pif": true,
	".msi": true, ".msp": true, ".dll": true, ".cpl": true, ".lnk": true, ".url": true,
	".ps1": true, ".psm1": true, ".vbs": true, ".vbe": true, ".js": true, ".jse": true,
	".wsf": true, ".wsh": true, ".hta": true, ".reg": true, ".jar": true, ".gadget": true,
	".sh": true, ".bash": true, ".zsh": true, ".fish": true, ".command": true, ".desktop": true,
	".run": true, ".bin": true, ".appimage": true, ".app": true, ".pkg": true, ".dmg": true,
	".workflow": true, ".action": true, ".scpt": true, ".terminal": true, ".py": true,
	".pl": true, ".rb": true, ".php": true, ".apk": true, ".deb": true, ".rpm": true,
	".docm": true, ".xlsm": true, ".pptm": true, ".iso": true, ".vhd": true, ".settingcontent-ms": true,
}

// CanView reports whether a file with this name may be opened in its
// default application. Names with no extension are refused too: on
// Unix an extensionless file is exactly what a script looks like.
func CanView(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	if ext == "" {
		return false
	}
	return !runnable[ext]
}

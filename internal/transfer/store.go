package transfer

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Cache is the temporary directory received files are decrypted into
// while the user decides what to do with them. It exists only for the
// life of the process: Wipe clears it at startup (leftovers from a crash)
// and Close removes it on quit, which is what keeps "ephemeral" true for
// a file the user looked at and chose not to keep.
type Cache struct {
	root string
}

// NewCache creates (or empties) a private cache directory under base -
// pass os.UserCacheDir()'s result, or t.TempDir() in tests.
func NewCache(base string) (*Cache, error) {
	root := filepath.Join(base, "synq", "files")
	if err := os.RemoveAll(root); err != nil {
		return nil, fmt.Errorf("transfer: clear cache: %w", err)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("transfer: create cache: %w", err)
	}
	return &Cache{root: root}, nil
}

// Root is the cache directory.
func (c *Cache) Root() string { return c.root }

// Close deletes everything in the cache.
func (c *Cache) Close() error { return os.RemoveAll(c.root) }

// Dir returns the private directory for one attachment, creating it. id
// must already have passed offer validation (hex and dashes only), so it
// can't escape the cache.
func (c *Cache) Dir(id string) (string, error) {
	if !attachmentIDRe.MatchString(id) {
		return "", errors.New("transfer: bad attachment id")
	}
	d := filepath.Join(c.root, id)
	if err := os.MkdirAll(d, 0o700); err != nil {
		return "", err
	}
	return d, nil
}

// Remove deletes one attachment's cached files.
func (c *Cache) Remove(id string) error {
	if !attachmentIDRe.MatchString(id) {
		return errors.New("transfer: bad attachment id")
	}
	return os.RemoveAll(filepath.Join(c.root, id))
}

// ExpandHome turns a leading "~" or "~/" into the user's home directory.
func ExpandHome(p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, p[1:]), nil
	}
	return p, nil
}

// ValidateSaveDir expands and checks a user-chosen save directory,
// creating it if it doesn't exist. The returned path is absolute.
func ValidateSaveDir(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", errors.New("the save directory is empty")
	}
	p, err := ExpandHome(p)
	if err != nil {
		return "", err
	}
	p, err = filepath.Abs(p)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(p, 0o755); err != nil {
		return "", fmt.Errorf("can't create %s: %w", p, err)
	}
	st, err := os.Stat(p)
	if err != nil {
		return "", err
	}
	if !st.IsDir() {
		return "", fmt.Errorf("%s is not a directory", p)
	}
	// Prove it's writable now rather than at the moment someone saves.
	probe, err := os.CreateTemp(p, ".synq-write-test-*")
	if err != nil {
		return "", fmt.Errorf("can't write to %s: %w", p, err)
	}
	probe.Close()
	os.Remove(probe.Name())
	return p, nil
}

// SaveCopy copies src into dir under name and returns the path it
// wrote. It never overwrites: if name is taken it picks "name (1).ext",
// "name (2).ext" and so on, claiming each candidate atomically with
// O_EXCL so two saves can't race onto the same file. The copy is
// created 0600 - a received file is never executable.
func SaveCopy(dir, src, name string) (string, error) {
	name = SafeName(name)
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()

	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 0; i < 1000; i++ {
		candidate := name
		if i > 0 {
			candidate = fmt.Sprintf("%s (%d)%s", stem, i, ext)
		}
		dst := filepath.Join(dir, candidate)
		out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			os.Remove(dst)
			return "", err
		}
		if err := out.Close(); err != nil {
			os.Remove(dst)
			return "", err
		}
		return dst, nil
	}
	return "", errors.New("transfer: too many files with that name already exist")
}

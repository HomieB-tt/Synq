// Package filecrypt encrypts and decrypts file contents for attachment
// transfer (DESIGN.md §10).
//
// A file is sealed under a fresh random 256-bit key that is used for that
// one file and never again; the key travels to the recipient inside the
// end-to-end encrypted chat channel, never to synq-server, which only
// ever stores the ciphertext.
//
// The ciphertext is a sequence of independently authenticated chunks
// rather than one big seal, so neither side has to hold a 100 MB file in
// memory and a corrupted download is noticed without reading to the end.
// Each chunk is AES-256-GCM with
//
//	nonce = 4 zero bytes || big-endian chunk index
//	aad   = one byte: 1 for the last chunk, 0 for every other
//
// Nonces never repeat because the key is per-file and the index is
// unique within it. Binding the index into the nonce stops chunks being
// reordered or duplicated; binding the last-chunk flag into the
// authenticated data stops the ciphertext being truncated at a chunk
// boundary or extended - either one makes some chunk fail to open.
package filecrypt

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

const (
	// KeySize is the length of a file key in bytes.
	KeySize = 32

	// ChunkSize is how much plaintext goes into each chunk, except
	// possibly the last.
	ChunkSize = 64 * 1024

	// TagSize is the authentication overhead GCM adds to each chunk.
	TagSize = 16

	// HashPrefix is the scheme synq-server expects on ciphertext hashes.
	HashPrefix = "sha256:"
)

// ErrCorrupt means the ciphertext failed authentication: it was
// tampered with, truncated, extended, reordered, or sealed under a
// different key. It deliberately says no more than that.
var ErrCorrupt = errors.New("filecrypt: ciphertext failed authentication")

// NewKey returns a fresh random file key.
func NewKey() (*[KeySize]byte, error) {
	var k [KeySize]byte
	if _, err := io.ReadFull(rand.Reader, k[:]); err != nil {
		return nil, fmt.Errorf("filecrypt: generate key: %w", err)
	}
	return &k, nil
}

// chunks reports how many chunks a plaintext of n bytes occupies. An
// empty file is still one (empty, final) chunk, so even it is
// authenticated.
func chunks(n int64) int64 {
	if n <= 0 {
		return 1
	}
	return (n + ChunkSize - 1) / ChunkSize
}

// CiphertextSize is the exact size of the ciphertext Encrypt produces for
// a plaintext of plainSize bytes. synq-server needs it up front, as the
// offer's blob_size.
func CiphertextSize(plainSize int64) int64 {
	return plainSize + chunks(plainSize)*TagSize
}

// PlaintextSize is the inverse of CiphertextSize. ok is false when
// cipherSize can't be the size of any ciphertext this package produces.
func PlaintextSize(cipherSize int64) (size int64, ok bool) {
	if cipherSize < TagSize {
		return 0, false
	}
	full := int64(ChunkSize + TagSize)
	n := cipherSize / full
	rem := cipherSize % full
	switch {
	case rem == 0 && n > 0:
		return n * ChunkSize, true
	case rem >= TagSize:
		return n*ChunkSize + (rem - TagSize), true
	default:
		return 0, false
	}
}

func newAEAD(key *[KeySize]byte) (cipher.AEAD, error) {
	if key == nil {
		return nil, errors.New("filecrypt: nil key")
	}
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func nonceFor(index uint64) []byte {
	nonce := make([]byte, 12)
	binary.BigEndian.PutUint64(nonce[4:], index)
	return nonce
}

func lastFlag(last bool) []byte {
	if last {
		return []byte{1}
	}
	return []byte{0}
}

// readChunk reads up to len(buf) bytes, returning how many it got. It
// treats a short read at end of input as normal and only reports real
// errors.
func readChunk(r io.Reader, buf []byte) (int, error) {
	n, err := io.ReadFull(r, buf)
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		err = nil
	}
	return n, err
}

// Encrypt seals everything from src under key and writes the ciphertext
// to dst. It returns the plaintext length and the ciphertext's SHA-256 in
// the "sha256:<hex>" form the server wants - the hash the sender commits
// to in the offer, which the recipient and the server both check.
func Encrypt(dst io.Writer, src io.Reader, key *[KeySize]byte) (plainSize int64, hash string, err error) {
	aead, err := newAEAD(key)
	if err != nil {
		return 0, "", err
	}
	h := sha256.New()
	out := io.MultiWriter(dst, h)

	cur := make([]byte, ChunkSize)
	next := make([]byte, ChunkSize)
	n, err := readChunk(src, cur)
	if err != nil {
		return 0, "", err
	}
	var index uint64
	for {
		// Look one chunk ahead: a chunk is the last one exactly when
		// nothing follows it.
		m, err := readChunk(src, next)
		if err != nil {
			return 0, "", err
		}
		last := m == 0
		sealed := aead.Seal(nil, nonceFor(index), cur[:n], lastFlag(last))
		if _, err := out.Write(sealed); err != nil {
			return 0, "", err
		}
		plainSize += int64(n)
		if last {
			break
		}
		cur, next = next, cur
		n = m
		index++
	}
	return plainSize, HashPrefix + hex.EncodeToString(h.Sum(nil)), nil
}

// Decrypt authenticates and opens everything from src under key,
// writing plaintext to dst, and returns how many plaintext bytes it
// wrote.
//
// Chunks are written as soon as each one verifies, so on error dst may
// already hold a prefix of the file. The caller must treat dst's contents
// as unusable unless Decrypt returned nil - in practice, decrypt into a
// temporary file and only move it into place on success. A truncated
// ciphertext is only detectable at its end, which is why a nil error is
// the only proof the whole file is intact.
func Decrypt(dst io.Writer, src io.Reader, key *[KeySize]byte) (int64, error) {
	aead, err := newAEAD(key)
	if err != nil {
		return 0, err
	}
	const sealedChunk = ChunkSize + TagSize

	cur := make([]byte, sealedChunk)
	next := make([]byte, sealedChunk)
	n, err := readChunk(src, cur)
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, ErrCorrupt // even an empty file has one sealed chunk
	}
	var (
		index uint64
		total int64
		plain []byte
	)
	for {
		m, err := readChunk(src, next)
		if err != nil {
			return total, err
		}
		last := m == 0
		if n < TagSize {
			return total, ErrCorrupt
		}
		plain, err = aead.Open(plain[:0], nonceFor(index), cur[:n], lastFlag(last))
		if err != nil {
			return total, ErrCorrupt
		}
		if _, err := dst.Write(plain); err != nil {
			return total, err
		}
		total += int64(len(plain))
		if last {
			return total, nil
		}
		cur, next = next, cur
		n = m
		index++
	}
}

// HashOf computes the "sha256:<hex>" form of r's contents, for checking a
// downloaded ciphertext against the sender's committed hash.
func HashOf(r io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return HashPrefix + hex.EncodeToString(h.Sum(nil)), nil
}

// EqualHash reports whether two "sha256:<hex>" strings are equal.
func EqualHash(a, b string) bool {
	return bytes.EqualFold([]byte(a), []byte(b))
}

package filecrypt

import (
	"bytes"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
)

func testKey(t *testing.T) *[KeySize]byte {
	t.Helper()
	k, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func seal(t *testing.T, key *[KeySize]byte, plain []byte) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	n, hash, err := Encrypt(&buf, bytes.NewReader(plain), key)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if n != int64(len(plain)) {
		t.Fatalf("Encrypt reported %d plaintext bytes, want %d", n, len(plain))
	}
	return buf.Bytes(), hash
}

// Sizes around the chunk boundary are where off-by-ones hide.
var sizes = []int{0, 1, 15, 16, ChunkSize - 1, ChunkSize, ChunkSize + 1, 2 * ChunkSize, 2*ChunkSize + 7, 5*ChunkSize + 123}

func TestRoundTrip(t *testing.T) {
	for _, size := range sizes {
		key := testKey(t)
		plain := make([]byte, size)
		_, _ = rand.Read(plain)

		ct, _ := seal(t, key, plain)
		if got := int64(len(ct)); got != CiphertextSize(int64(size)) {
			t.Errorf("size %d: ciphertext is %d bytes, CiphertextSize says %d", size, got, CiphertextSize(int64(size)))
		}
		if back, ok := PlaintextSize(int64(len(ct))); !ok || back != int64(size) {
			t.Errorf("size %d: PlaintextSize(%d) = %d, %v", size, len(ct), back, ok)
		}

		var out bytes.Buffer
		n, err := Decrypt(&out, bytes.NewReader(ct), key)
		if err != nil {
			t.Fatalf("size %d: Decrypt: %v", size, err)
		}
		if n != int64(size) || !bytes.Equal(out.Bytes(), plain) {
			t.Errorf("size %d: round trip changed the data", size)
		}
	}
}

func TestHashMatchesCiphertext(t *testing.T) {
	key := testKey(t)
	ct, hash := seal(t, key, []byte("hello"))
	if !strings.HasPrefix(hash, HashPrefix) || len(hash) != 71 {
		t.Fatalf("hash %q is not in the sha256:<64 hex> form the server requires", hash)
	}
	got, err := HashOf(bytes.NewReader(ct))
	if err != nil || !EqualHash(got, hash) {
		t.Errorf("HashOf = %q, %v; want %q", got, err, hash)
	}
}

func TestWrongKeyFails(t *testing.T) {
	ct, _ := seal(t, testKey(t), []byte("secret"))
	if _, err := Decrypt(&bytes.Buffer{}, bytes.NewReader(ct), testKey(t)); !errors.Is(err, ErrCorrupt) {
		t.Errorf("wrong key: err = %v, want ErrCorrupt", err)
	}
}

func TestEveryBitFlipIsDetected(t *testing.T) {
	key := testKey(t)
	ct, _ := seal(t, key, bytes.Repeat([]byte("x"), ChunkSize+50))
	for _, pos := range []int{0, 5, ChunkSize, ChunkSize + TagSize, len(ct) - 1} {
		bad := append([]byte(nil), ct...)
		bad[pos] ^= 1
		if _, err := Decrypt(&bytes.Buffer{}, bytes.NewReader(bad), key); !errors.Is(err, ErrCorrupt) {
			t.Errorf("flip at %d: err = %v, want ErrCorrupt", pos, err)
		}
	}
}

func TestTruncationAtAChunkBoundaryIsDetected(t *testing.T) {
	key := testKey(t)
	ct, _ := seal(t, key, bytes.Repeat([]byte("y"), 3*ChunkSize))
	whole := ChunkSize + TagSize
	for _, keep := range []int{0, 5, whole, 2 * whole} {
		if _, err := Decrypt(&bytes.Buffer{}, bytes.NewReader(ct[:keep]), key); !errors.Is(err, ErrCorrupt) {
			t.Errorf("truncated to %d: err = %v, want ErrCorrupt", keep, err)
		}
	}
}

func TestExtensionIsDetected(t *testing.T) {
	key := testKey(t)
	ct, _ := seal(t, key, bytes.Repeat([]byte("z"), ChunkSize))
	// Appending a copy of the final chunk, or junk, must not verify.
	for _, extra := range [][]byte{ct, []byte("junk-junk-junk-junk-junk")} {
		bad := append(append([]byte(nil), ct...), extra...)
		if _, err := Decrypt(&bytes.Buffer{}, bytes.NewReader(bad), key); !errors.Is(err, ErrCorrupt) {
			t.Errorf("extended: err = %v, want ErrCorrupt", err)
		}
	}
}

func TestReorderedChunksAreDetected(t *testing.T) {
	key := testKey(t)
	ct, _ := seal(t, key, bytes.Repeat([]byte("q"), 3*ChunkSize))
	whole := ChunkSize + TagSize
	swapped := append([]byte(nil), ct...)
	copy(swapped[0:whole], ct[whole:2*whole])
	copy(swapped[whole:2*whole], ct[0:whole])
	if _, err := Decrypt(&bytes.Buffer{}, bytes.NewReader(swapped), key); !errors.Is(err, ErrCorrupt) {
		t.Errorf("reordered: err = %v, want ErrCorrupt", err)
	}
}

func TestDifferentFilesGetDifferentCiphertext(t *testing.T) {
	a, _ := seal(t, testKey(t), []byte("same"))
	b, _ := seal(t, testKey(t), []byte("same"))
	if bytes.Equal(a, b) {
		t.Error("two keys produced identical ciphertext")
	}
}

func TestPlaintextSizeRejectsImpossibleSizes(t *testing.T) {
	for _, n := range []int64{-1, 0, 1, TagSize - 1, ChunkSize + TagSize + 1, ChunkSize + TagSize + TagSize - 1} {
		if got, ok := PlaintextSize(n); ok && CiphertextSize(got) != n {
			t.Errorf("PlaintextSize(%d) = %d, ok - but that doesn't round trip", n, got)
		}
	}
	if _, ok := PlaintextSize(0); ok {
		t.Error("PlaintextSize(0) should not be valid: even an empty file is one sealed chunk")
	}
}

func TestNilKey(t *testing.T) {
	if _, _, err := Encrypt(&bytes.Buffer{}, strings.NewReader("x"), nil); err == nil {
		t.Error("Encrypt accepted a nil key")
	}
	if _, err := Decrypt(&bytes.Buffer{}, strings.NewReader("x"), nil); err == nil {
		t.Error("Decrypt accepted a nil key")
	}
}

package transfer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HomieB-tt/synq/internal/api"
	"github.com/HomieB-tt/synq/internal/filecrypt"
)

// fakeRoomServer plays synq-server's room file routes.
type fakeRoomServer struct {
	blob     []byte
	roomID   string
	sha      string
	failUp   bool
	failDown bool
	tamper   func([]byte) []byte
}

func (f *fakeRoomServer) UploadRoomFile(_ context.Context, _, roomID string, body io.Reader, size int64, sha string) (string, error) {
	if f.failUp {
		return "", &api.APIError{StatusCode: 409, Message: "a room holds at most 20 files"}
	}
	b, _ := io.ReadAll(body)
	if int64(len(b)) != size {
		return "", errors.New("upload length differs from declared size")
	}
	f.blob, f.roomID, f.sha = b, roomID, sha
	return goodID, nil
}

func (f *fakeRoomServer) DownloadRoomFile(_ context.Context, _, roomID, fileID string) (io.ReadCloser, error) {
	if f.failDown || roomID != f.roomID || fileID != goodID {
		return nil, &api.APIError{StatusCode: 404, Message: "no such file"}
	}
	b := f.blob
	if f.tamper != nil {
		b = f.tamper(append([]byte(nil), b...))
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func roomSend(t *testing.T, srv *fakeRoomServer, data []byte, name string) (*Offer, *[filecrypt.KeySize]byte, *Cache) {
	t.Helper()
	cache, err := NewCache(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sent, err := SendRoomFile(context.Background(), srv, "tok", "room-1", writeFile(t, name, data), cache.Root())
	if err != nil {
		t.Fatalf("SendRoomFile: %v", err)
	}
	body, err := EncodeOffer(sent.AttachmentID, sent.Name, sent.Size, sent.Hash, sent.Key)
	if err != nil {
		t.Fatal(err)
	}
	offer, key, err := DecodeOffer(body)
	if err != nil {
		t.Fatal(err)
	}
	return offer, key, cache
}

func TestRoomFileRoundTrip(t *testing.T) {
	for _, size := range []int{0, 1, filecrypt.ChunkSize, 3*filecrypt.ChunkSize + 5} {
		srv := &fakeRoomServer{}
		data := bytes.Repeat([]byte{0x5A}, size)
		offer, key, cache := roomSend(t, srv, data, "notes.txt")
		path, err := ReceiveRoomFile(context.Background(), srv, "tok", "room-1", offer, key, cache)
		if err != nil {
			t.Fatalf("size %d: %v", size, err)
		}
		got, _ := os.ReadFile(path)
		if !bytes.Equal(got, data) || filepath.Base(path) != "notes.txt" {
			t.Errorf("size %d: wrong file %q", size, path)
		}
		if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
			t.Errorf("size %d: %d files left in the cache dir", size, len(entries))
		}
	}
}

func TestRoomServerSeesOnlyCiphertextAndItsHash(t *testing.T) {
	srv := &fakeRoomServer{}
	secret := []byte("the-quick-brown-fox-jumps-over-the-lazy-dog")
	offer, key, _ := roomSend(t, srv, secret, "diary.txt")
	if bytes.Contains(srv.blob, secret) || bytes.Contains(srv.blob, key[:]) {
		t.Fatal("plaintext or key reached the server")
	}
	if got, _ := filecrypt.HashOf(bytes.NewReader(srv.blob)); got != srv.sha || got != offer.CiphertextSHA256 {
		t.Fatal("the committed hash doesn't match the upload")
	}
	if strings.Contains(srv.sha+srv.roomID, offer.Name) {
		t.Fatal("filename reached the server")
	}
}

func TestRoomReceiveRefusesTamperingTheWrongKeyAndMissingFiles(t *testing.T) {
	srv := &fakeRoomServer{}
	offer, key, cache := roomSend(t, srv, bytes.Repeat([]byte{1}, 5000), "a.bin")
	ctx := context.Background()

	srv.tamper = func(b []byte) []byte { b[10] ^= 1; return b }
	if _, err := ReceiveRoomFile(ctx, srv, "tok", "room-1", offer, key, cache); err == nil {
		t.Error("a tampered download was accepted")
	}
	srv.tamper = func(b []byte) []byte { return b[:len(b)-1] }
	if _, err := ReceiveRoomFile(ctx, srv, "tok", "room-1", offer, key, cache); err == nil {
		t.Error("a truncated download was accepted")
	}
	srv.tamper = nil
	other, _ := filecrypt.NewKey()
	if _, err := ReceiveRoomFile(ctx, srv, "tok", "room-1", offer, other, cache); err == nil {
		t.Error("the wrong key opened it")
	}
	if _, err := ReceiveRoomFile(ctx, srv, "tok", "room-2", offer, key, cache); !api.IsNotFound(errors.Unwrap(err)) && err == nil {
		t.Error("a file from another room was served")
	}
	srv.failDown = true
	if _, err := ReceiveRoomFile(ctx, srv, "tok", "room-1", offer, key, cache); err == nil {
		t.Error("a refused download succeeded")
	}
	// Nothing half-decrypted is left behind after any failure.
	if entries, _ := os.ReadDir(cache.Root()); len(entries) != 0 {
		t.Errorf("%d leftovers in the cache after failures", len(entries))
	}
}

func TestRoomReceiveRefusesAHashTheSenderDidNotCommitTo(t *testing.T) {
	srv := &fakeRoomServer{}
	offer, key, cache := roomSend(t, srv, []byte("hello"), "a.txt")
	offer.CiphertextSHA256 = "sha256:" + strings.Repeat("0", 64)
	if _, err := ReceiveRoomFile(context.Background(), srv, "tok", "room-1", offer, key, cache); err == nil {
		t.Fatal("a download that doesn't match the signed hash was accepted")
	}
}

func TestRoomSendFailureLeavesNoScratchAndSurfacesTheServersAnswer(t *testing.T) {
	srv := &fakeRoomServer{failUp: true}
	cache, _ := NewCache(t.TempDir())
	_, err := SendRoomFile(context.Background(), srv, "tok", "room-1", writeFile(t, "a.txt", []byte("hi")), cache.Root())
	if !api.IsConflict(errors.Unwrap(err)) && !strings.Contains(err.Error(), "at most 20 files") {
		t.Fatalf("err = %v", err)
	}
	if entries, _ := os.ReadDir(cache.Root()); len(entries) != 0 {
		t.Fatalf("%d scratch files left", len(entries))
	}
	if _, err := SendRoomFile(context.Background(), srv, "t", "r", t.TempDir(), cache.Root()); err == nil {
		t.Error("a directory was accepted")
	}
}

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

// fakeServer plays synq-server for one attachment.
type fakeServer struct {
	offered  api.OfferRequest
	blob     []byte
	calls    []string
	failAt   string // method name to fail on
	tamper   func([]byte) []byte
	blobSize int64 // overrides StartDownload's blob_size when non-zero
}

func (f *fakeServer) fail(name string) error {
	f.calls = append(f.calls, name)
	if f.failAt == name {
		return &api.APIError{StatusCode: 500, Message: name + " failed"}
	}
	return nil
}

func (f *fakeServer) OfferAttachment(_ context.Context, _ string, req api.OfferRequest) (*api.OfferResponse, error) {
	if err := f.fail("offer"); err != nil {
		return nil, err
	}
	f.offered = req
	return &api.OfferResponse{AttachmentID: goodID, BlobID: "blob"}, nil
}

func (f *fakeServer) UploadAttachment(_ context.Context, _, _ string, body io.Reader, size int64) error {
	if err := f.fail("upload"); err != nil {
		return err
	}
	b, _ := io.ReadAll(body)
	if int64(len(b)) != size {
		return errors.New("upload length differs from declared size")
	}
	f.blob = b
	return nil
}

func (f *fakeServer) ClaimAttachment(context.Context, string, string, string) error {
	return f.fail("claim")
}

func (f *fakeServer) StartDownload(context.Context, string, string, string, int) (*api.DownloadStart, error) {
	if err := f.fail("start"); err != nil {
		return nil, err
	}
	size := int64(len(f.blob))
	if f.blobSize != 0 {
		size = f.blobSize
	}
	return &api.DownloadStart{DownloadID: "dl-1", BlobSize: size}, nil
}

func (f *fakeServer) DownloadBlob(context.Context, string, string, string) (io.ReadCloser, error) {
	if err := f.fail("blob"); err != nil {
		return nil, err
	}
	b := f.blob
	if f.tamper != nil {
		b = f.tamper(append([]byte(nil), b...))
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (f *fakeServer) CompleteDownload(_ context.Context, _, _, _, _ string, n int64, hash string) error {
	if err := f.fail("complete"); err != nil {
		return err
	}
	if n != int64(len(f.blob)) || !filecrypt.EqualHash(hash, f.offered.CiphertextSHA256) {
		return errors.New("complete disagrees with the offer")
	}
	return nil
}

func (f *fakeServer) CancelDownload(context.Context, string, string, string, string) error {
	return f.fail("cancel")
}

func (f *fakeServer) called(name string) bool {
	for _, c := range f.calls {
		if c == name {
			return true
		}
	}
	return false
}

func writeFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// sendThenOffer runs the sender half and returns the offer the
// recipient would decode from chat.
func sendThenOffer(t *testing.T, srv *fakeServer, data []byte, name string) (*Offer, *[filecrypt.KeySize]byte, *Cache) {
	t.Helper()
	cache, err := NewCache(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sent, err := SendFile(context.Background(), srv, "tok", "ses-1", "usr-2", writeFile(t, name, data), cache.Root())
	if err != nil {
		t.Fatalf("SendFile: %v", err)
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

func TestSendThenReceiveRoundTrip(t *testing.T) {
	for _, size := range []int{0, 1, filecrypt.ChunkSize, 3*filecrypt.ChunkSize + 5} {
		srv := &fakeServer{}
		data := bytes.Repeat([]byte{0xAB}, size)
		offer, key, cache := sendThenOffer(t, srv, data, "photo.png")

		path, err := ReceiveFile(context.Background(), srv, "tok", "ses-2", offer, key, cache)
		if err != nil {
			t.Fatalf("size %d: ReceiveFile: %v", size, err)
		}
		got, _ := os.ReadFile(path)
		if !bytes.Equal(got, data) {
			t.Errorf("size %d: received file differs", size)
		}
		if filepath.Base(path) != "photo.png" {
			t.Errorf("size %d: file is named %q", size, filepath.Base(path))
		}
		// Only the finished file is left behind.
		entries, _ := os.ReadDir(filepath.Dir(path))
		if len(entries) != 1 {
			t.Errorf("size %d: cache dir holds %d entries, want 1", size, len(entries))
		}
	}
}

func TestServerNeverSeesPlaintextOrKey(t *testing.T) {
	srv := &fakeServer{}
	secret := []byte("the-quick-brown-fox-jumps-over-the-lazy-dog")
	offer, key, _ := sendThenOffer(t, srv, secret, "diary.txt")
	if bytes.Contains(srv.blob, secret) {
		t.Error("plaintext reached the server")
	}
	if bytes.Contains(srv.blob, key[:]) {
		t.Error("key reached the server")
	}
	if strings.Contains(srv.offered.Encryption.Algorithm+srv.offered.RecipientID, offer.Name) {
		t.Error("filename reached the server")
	}
}

func TestSendOfferMatchesTheUpload(t *testing.T) {
	srv := &fakeServer{}
	offer, _, _ := sendThenOffer(t, srv, []byte("hello"), "a.txt")
	if srv.offered.BlobSize != int64(len(srv.blob)) {
		t.Errorf("offer blob_size %d, uploaded %d", srv.offered.BlobSize, len(srv.blob))
	}
	if got, _ := filecrypt.HashOf(bytes.NewReader(srv.blob)); got != srv.offered.CiphertextSHA256 || got != offer.CiphertextSHA256 {
		t.Error("committed hash doesn't match the uploaded bytes")
	}
	if srv.offered.RecipientID != "usr-2" || srv.offered.SessionID != "ses-1" {
		t.Errorf("offer = %+v", srv.offered)
	}
}

func TestSendRejectsWhatItShouldNot(t *testing.T) {
	cache, _ := NewCache(t.TempDir())
	srv := &fakeServer{}
	ctx := context.Background()
	if _, err := SendFile(ctx, srv, "t", "s", "r", t.TempDir(), cache.Root()); err == nil {
		t.Error("a directory was accepted")
	}
	if _, err := SendFile(ctx, srv, "t", "s", "r", filepath.Join(t.TempDir(), "missing"), cache.Root()); err == nil {
		t.Error("a missing file was accepted")
	}
	if len(srv.calls) != 0 {
		t.Errorf("server was contacted: %v", srv.calls)
	}
}

func TestSendCleansUpItsTemporaryCiphertext(t *testing.T) {
	for _, failAt := range []string{"", "offer", "upload"} {
		cache, _ := NewCache(t.TempDir())
		srv := &fakeServer{failAt: failAt}
		_, err := SendFile(context.Background(), srv, "t", "s", "r", writeFile(t, "a.txt", []byte("hi")), cache.Root())
		if (failAt == "") != (err == nil) {
			t.Errorf("failAt %q: err = %v", failAt, err)
		}
		if entries, _ := os.ReadDir(cache.Root()); len(entries) != 0 {
			t.Errorf("failAt %q: scratch left %d file(s) behind", failAt, len(entries))
		}
	}
}

func TestReceiveRefusesTamperedDownloads(t *testing.T) {
	cases := map[string]func([]byte) []byte{
		"flipped bit":  func(b []byte) []byte { b[len(b)/2] ^= 1; return b },
		"truncated":    func(b []byte) []byte { return b[:len(b)-1] },
		"extended":     func(b []byte) []byte { return append(b, 0) },
		"swapped blob": func(b []byte) []byte { return bytes.Repeat([]byte{1}, len(b)) },
	}
	for name, tamper := range cases {
		srv := &fakeServer{}
		offer, key, cache := sendThenOffer(t, srv, bytes.Repeat([]byte("x"), 1000), "a.txt")
		srv.tamper = tamper

		path, err := ReceiveFile(context.Background(), srv, "t", "s", offer, key, cache)
		if err == nil {
			t.Errorf("%s: accepted (%s)", name, path)
			continue
		}
		if srv.called("complete") {
			t.Errorf("%s: reported complete for a bad file", name)
		}
		if !srv.called("cancel") {
			t.Errorf("%s: didn't cancel the download", name)
		}
		if _, err := os.Stat(filepath.Join(cache.Root(), offer.AttachmentID)); !os.IsNotExist(err) {
			t.Errorf("%s: cache dir left behind", name)
		}
	}
}

func TestReceiveRefusesTheWrongKey(t *testing.T) {
	srv := &fakeServer{}
	offer, _, cache := sendThenOffer(t, srv, []byte("secret"), "a.txt")
	other, _ := filecrypt.NewKey()
	if _, err := ReceiveFile(context.Background(), srv, "t", "s", offer, other, cache); err == nil {
		t.Error("decrypted with the wrong key")
	}
	if srv.called("complete") {
		t.Error("reported complete")
	}
}

func TestReceiveChecksTheServerAgainstTheOffer(t *testing.T) {
	srv := &fakeServer{}
	offer, key, cache := sendThenOffer(t, srv, []byte("hello world"), "a.txt")
	srv.blobSize = 12345
	if _, err := ReceiveFile(context.Background(), srv, "t", "s", offer, key, cache); err == nil {
		t.Error("accepted a size the offer contradicts")
	}

	// A lying offer size is caught by the same check.
	srv = &fakeServer{}
	offer, key, cache = sendThenOffer(t, srv, []byte("hello world"), "a.txt")
	offer.Size++
	if _, err := ReceiveFile(context.Background(), srv, "t", "s", offer, key, cache); err == nil {
		t.Error("accepted an offer whose size doesn't match")
	}
}

func TestReceiveStopsAtEachServerFailure(t *testing.T) {
	for _, failAt := range []string{"claim", "start", "blob", "complete"} {
		srv := &fakeServer{}
		offer, key, cache := sendThenOffer(t, srv, []byte("hi"), "a.txt")
		srv.failAt = failAt
		if _, err := ReceiveFile(context.Background(), srv, "t", "s", offer, key, cache); err == nil {
			t.Errorf("%s failure ignored", failAt)
		}
		if _, err := os.Stat(filepath.Join(cache.Root(), offer.AttachmentID)); !os.IsNotExist(err) {
			t.Errorf("%s: cache dir left behind", failAt)
		}
	}
}

// A UI is watching: sending and receiving must report each phase and
// finish with the byte counts the protocol implies.
func TestTransfersReportProgress(t *testing.T) {
	data := bytes.Repeat([]byte{0x5A}, 3*filecrypt.ChunkSize+7)
	srv := &fakeServer{}
	cache, err := NewCache(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	sendProg := NewProgress()
	sent, err := SendFile(WithProgress(context.Background(), sendProg), srv, "tok", "ses-1", "usr-2",
		writeFile(t, "big.bin", data), cache.Root())
	if err != nil {
		t.Fatal(err)
	}
	ph, done, total := sendProg.Snapshot()
	want := filecrypt.CiphertextSize(int64(len(data)))
	if ph != PhaseUploading || done != want || total != want {
		t.Fatalf("after send: %v %d/%d, want uploading %d/%d", ph, done, total, want, want)
	}

	body, _ := EncodeOffer(sent.AttachmentID, sent.Name, sent.Size, sent.Hash, sent.Key)
	offer, key, _ := DecodeOffer(body)

	recvProg := NewProgress()
	if _, err := ReceiveFile(WithProgress(context.Background(), recvProg), srv, "tok", "ses-2", offer, key, cache); err != nil {
		t.Fatal(err)
	}
	ph, done, total = recvProg.Snapshot()
	if ph != PhaseDecrypting || done != want || total != want {
		t.Fatalf("after receive: %v %d/%d, want decrypting %d/%d", ph, done, total, want, want)
	}
}

// No Progress in the context is the ordinary case and must change nothing.
func TestTransfersWorkWithoutProgress(t *testing.T) {
	srv := &fakeServer{}
	offer, key, cache := sendThenOffer(t, srv, []byte("hi"), "a.txt")
	if _, err := ReceiveFile(context.Background(), srv, "t", "s", offer, key, cache); err != nil {
		t.Fatal(err)
	}
}

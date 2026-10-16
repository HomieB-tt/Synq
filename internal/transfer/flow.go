package transfer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/HomieB-tt/synq/internal/api"
	"github.com/HomieB-tt/synq/internal/filecrypt"
)

// Server is the slice of synq-server's API that file transfer uses.
// *api.Client satisfies it; tests substitute a fake.
type Server interface {
	OfferAttachment(ctx context.Context, token string, req api.OfferRequest) (*api.OfferResponse, error)
	UploadAttachment(ctx context.Context, token, attachmentID string, body io.Reader, size int64) error
	ClaimAttachment(ctx context.Context, token, attachmentID, sessionID string) error
	StartDownload(ctx context.Context, token, attachmentID, sessionID string, attempt int) (*api.DownloadStart, error)
	DownloadBlob(ctx context.Context, token, attachmentID, downloadID string) (io.ReadCloser, error)
	CompleteDownload(ctx context.Context, token, attachmentID, sessionID, downloadID string, receivedBytes int64, hash string) error
	CancelDownload(ctx context.Context, token, attachmentID, sessionID, downloadID string) error
}

var _ Server = (*api.Client)(nil)

// Sent is the result of a successful SendFile: everything the sender
// must now tell the recipient over the encrypted chat channel.
type Sent struct {
	AttachmentID string
	Name         string
	Size         int64
	Hash         string
	Key          *[filecrypt.KeySize]byte
}

// SendFile encrypts the file at path under a fresh key, registers it with
// the server as an offer to recipientID, and uploads the ciphertext.
// scratch is a private directory for the temporary ciphertext (the
// cache's directory); the temporary file is removed before returning.
//
// The ciphertext is written to disk first because the offer must commit
// to its size and hash before the upload starts.
func SendFile(ctx context.Context, srv Server, token, sessionID, recipientID, path, scratch string) (*Sent, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if st.Size() > MaxPlainBytes() {
		return nil, fmt.Errorf("file is %d bytes; the limit is %d (100 MB after encryption)", st.Size(), MaxPlainBytes())
	}

	prog := progressFrom(ctx)

	key, err := filecrypt.NewKey()
	if err != nil {
		return nil, err
	}

	in, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer in.Close()

	tmp, err := os.CreateTemp(scratch, "upload-*.part")
	if err != nil {
		return nil, err
	}
	defer func() {
		tmp.Close()
		os.Remove(tmp.Name())
	}()

	prog.Start(PhaseEncrypting, st.Size())
	plainSize, hash, err := filecrypt.Encrypt(tmp, prog.reader(in), key)
	if err != nil {
		return nil, fmt.Errorf("encrypt: %w", err)
	}
	if plainSize > MaxPlainBytes() {
		// The file grew between the stat and the read.
		return nil, errors.New("file changed size while it was being read")
	}
	blobSize := filecrypt.CiphertextSize(plainSize)
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	offer, err := srv.OfferAttachment(ctx, token, api.OfferRequest{
		RecipientID:      recipientID,
		SessionID:        sessionID,
		BlobSize:         blobSize,
		CiphertextSHA256: hash,
		Encryption:       api.EncryptionMetadata{Algorithm: "AES-256-GCM-chunked", Chunk: filecrypt.ChunkSize},
	})
	if err != nil {
		return nil, fmt.Errorf("offer: %w", err)
	}
	prog.Start(PhaseUploading, blobSize)
	if err := srv.UploadAttachment(ctx, token, offer.AttachmentID, prog.reader(tmp), blobSize); err != nil {
		return nil, fmt.Errorf("upload: %w", err)
	}
	return &Sent{
		AttachmentID: offer.AttachmentID,
		Name:         SafeName(filepath.Base(path)),
		Size:         plainSize,
		Hash:         hash,
		Key:          key,
	}, nil
}

// ReceiveFile claims and downloads an offered attachment, checks it
// against what the sender committed to, decrypts it into the cache, and
// reports it to the server as complete. It returns the path of the
// plaintext file, named offer.Name inside the attachment's own cache
// directory - so the OS picks the right default application by its
// extension, and nothing else is ever in that directory.
//
// The order matters: the file is only reported complete after it has
// been fully decrypted and authenticated, because that report asserts
// exactly that; and it only becomes visible under its real name after
// every check passed.
func ReceiveFile(ctx context.Context, srv Server, token, sessionID string, offer *Offer, key *[filecrypt.KeySize]byte, cache *Cache) (string, error) {
	prog := progressFrom(ctx)
	dir, err := cache.Dir(offer.AttachmentID)
	if err != nil {
		return "", err
	}
	ok := false
	defer func() {
		if !ok {
			_ = cache.Remove(offer.AttachmentID)
		}
	}()

	if err := srv.ClaimAttachment(ctx, token, offer.AttachmentID, sessionID); err != nil {
		return "", fmt.Errorf("claim: %w", err)
	}
	ds, err := srv.StartDownload(ctx, token, offer.AttachmentID, sessionID, 1)
	if err != nil {
		return "", fmt.Errorf("start download: %w", err)
	}
	cancel := func() {
		// Best effort, on a context that survives the caller's own
		// cancellation: leaving the attachment stuck DOWNLOADING would
		// block a retry.
		cctx, stop := context.WithTimeout(context.Background(), 10_000_000_000)
		defer stop()
		_ = srv.CancelDownload(cctx, token, offer.AttachmentID, sessionID, ds.DownloadID)
	}

	want := filecrypt.CiphertextSize(offer.Size)
	if ds.BlobSize != want {
		cancel()
		return "", fmt.Errorf("the server's copy is %d bytes but the sender's offer implies %d", ds.BlobSize, want)
	}

	body, err := srv.DownloadBlob(ctx, token, offer.AttachmentID, ds.DownloadID)
	if err != nil {
		cancel()
		return "", fmt.Errorf("download: %w", err)
	}
	defer body.Close()

	ctPath := filepath.Join(dir, "download.part")
	ct, err := os.OpenFile(ctPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		cancel()
		return "", err
	}
	defer os.Remove(ctPath)
	defer ct.Close()

	// Read one byte past the expected size so an over-long stream is
	// noticed instead of silently cut.
	prog.Start(PhaseDownloading, want)
	n, err := io.Copy(ct, io.LimitReader(prog.reader(body), want+1))
	if err != nil {
		cancel()
		return "", fmt.Errorf("download: %w", err)
	}
	if n != want {
		cancel()
		return "", fmt.Errorf("download was %d bytes, expected %d", n, want)
	}
	if _, err := ct.Seek(0, io.SeekStart); err != nil {
		cancel()
		return "", err
	}
	prog.Start(PhaseVerifying, 0)
	got, err := filecrypt.HashOf(ct)
	if err != nil {
		cancel()
		return "", err
	}
	if !filecrypt.EqualHash(got, offer.CiphertextSHA256) {
		cancel()
		return "", errors.New("the download doesn't match the hash the sender committed to")
	}

	if _, err := ct.Seek(0, io.SeekStart); err != nil {
		cancel()
		return "", err
	}
	plainPath := filepath.Join(dir, "plain.part")
	plain, err := os.OpenFile(plainPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		cancel()
		return "", err
	}
	prog.Start(PhaseDecrypting, want)
	written, err := filecrypt.Decrypt(plain, prog.reader(ct), key)
	if cerr := plain.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		cancel()
		if errors.Is(err, filecrypt.ErrCorrupt) {
			return "", errors.New("the file failed authentication - it was corrupted or tampered with")
		}
		return "", fmt.Errorf("decrypt: %w", err)
	}
	if written != offer.Size {
		cancel()
		return "", fmt.Errorf("the file decrypted to %d bytes, but the offer said %d", written, offer.Size)
	}

	final := filepath.Join(dir, offer.Name)
	if err := os.Rename(plainPath, final); err != nil {
		cancel()
		return "", err
	}
	if err := srv.CompleteDownload(ctx, token, offer.AttachmentID, sessionID, ds.DownloadID, n, got); err != nil {
		return "", fmt.Errorf("complete: %w", err)
	}
	ok = true
	return final, nil
}

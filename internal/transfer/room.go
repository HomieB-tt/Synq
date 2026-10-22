package transfer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/HomieB-tt/synq/internal/filecrypt"
)

// RoomServer is the slice of synq-server's API that room files use. A room
// file is owned by the room: one upload, readable by every joined member,
// so there is no per-recipient offer, claim or download session - just
// upload and download. *api.Client satisfies it; tests substitute a fake.
type RoomServer interface {
	UploadRoomFile(ctx context.Context, token, roomID string, body io.Reader, size int64, sha256 string) (fileID string, err error)
	DownloadRoomFile(ctx context.Context, token, roomID, fileID string) (io.ReadCloser, error)
}

// SendRoomFile encrypts the file at path under a fresh key and uploads the
// ciphertext into the room. The caller then tells the other members what
// it is, and the key, inside a sealed room message (room.FileRef) - the
// server never sees the name or the key. Sent.AttachmentID is the room
// file's id.
func SendRoomFile(ctx context.Context, srv RoomServer, token, roomID, path, scratch string) (*Sent, error) {
	p, err := encryptForUpload(ctx, path, scratch)
	if err != nil {
		return nil, err
	}
	defer p.close()

	prog := progressFrom(ctx)
	prog.Start(PhaseUploading, p.blobSize)
	id, err := srv.UploadRoomFile(ctx, token, roomID, prog.reader(p.tmp), p.blobSize, p.hash)
	if err != nil {
		return nil, fmt.Errorf("upload: %w", err)
	}
	return &Sent{
		AttachmentID: id,
		Name:         SafeName(filepath.Base(path)),
		Size:         p.plainSize,
		Hash:         p.hash,
		Key:          p.key,
	}, nil
}

// ReceiveRoomFile downloads a room file, checks it against the hash the
// sender committed to inside the signed message, decrypts it into the
// cache, and returns the plaintext path (named offer.Name inside the
// file's own directory). offer.AttachmentID is the room file's id.
func ReceiveRoomFile(ctx context.Context, srv RoomServer, token, roomID string, offer *Offer, key *[filecrypt.KeySize]byte, cache *Cache) (string, error) {
	if offer == nil || key == nil {
		return "", errors.New("transfer: missing offer or key")
	}
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

	body, err := srv.DownloadRoomFile(ctx, token, roomID, offer.AttachmentID)
	if err != nil {
		return "", fmt.Errorf("download: %w", err)
	}
	defer body.Close()

	final, _, _, err := materialize(prog, dir, body, offer, key)
	if err != nil {
		return "", err
	}
	ok = true
	return final, nil
}

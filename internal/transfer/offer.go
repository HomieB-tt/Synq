// Package transfer holds the client-side pieces of file transfer that
// don't touch the network or the UI: the offer message that carries a
// file's key to the recipient, filename sanitising, the temporary cache
// received files are decrypted into, copying a kept file out to the
// user's save directory, and handing a file to the OS to open.
//
// The network half is internal/api (Claim Check endpoints) and the
// cryptography is internal/filecrypt; internal/app strings them together.
package transfer

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/HomieB-tt/synq/internal/filecrypt"
)

// MaxFileBytes is the largest file synq-server accepts: 100 MB of
// ciphertext (API.md, "Attachments"). MaxPlainBytes is the largest
// plaintext that still fits once per-chunk overhead is added.
const (
	MaxBlobBytes = 100 * 1024 * 1024
)

// MaxPlainBytes returns the largest plaintext size whose ciphertext fits
// under MaxBlobBytes.
func MaxPlainBytes() int64 {
	// CiphertextSize is monotonic; walk down from the ceiling.
	n := int64(MaxBlobBytes)
	for filecrypt.CiphertextSize(n) > MaxBlobBytes {
		n -= filecrypt.TagSize
	}
	return n
}

// offerMagic marks a sealed chat body as a file offer rather than text.
// A leading NUL can't be typed into the composer, so ordinary messages
// never collide with it; a contact can still send one, which is how
// offers arrive - and why DecodeOffer treats everything in it as
// untrusted.
var offerMagic = []byte("\x00synq-file-v1\x00")

// Offer is what the sender tells the recipient, inside the end-to-end
// encrypted chat channel. synq-server never sees any of it: the filename
// and the key stay between the two clients, and the server's own record
// of the attachment is just an opaque blob.
type Offer struct {
	// AttachmentID is the server-side id to claim and download.
	AttachmentID string `json:"id"`
	// Name is the sender's filename. Untrusted: use SafeName before it
	// goes anywhere near a path.
	Name string `json:"name"`
	// Size is the plaintext size in bytes.
	Size int64 `json:"size"`
	// CiphertextSHA256 is the hash the sender committed to at offer time,
	// "sha256:<hex>". Carrying it here as well as at the server lets the
	// recipient verify the download against something the server could
	// not have chosen.
	CiphertextSHA256 string `json:"sha256"`
	// Key is the file key, base64.
	Key string `json:"key"`
}

var (
	attachmentIDRe = regexp.MustCompile(`^[0-9a-fA-F-]{32,40}$`)
	hashRe         = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// IsOffer reports whether a decrypted chat body is a file offer.
func IsOffer(body []byte) bool { return bytes.HasPrefix(body, offerMagic) }

// EncodeOffer builds the chat body for an offer. key is the file key.
func EncodeOffer(id, name string, size int64, cipherHash string, key *[filecrypt.KeySize]byte) ([]byte, error) {
	if key == nil {
		return nil, errors.New("transfer: nil file key")
	}
	o := Offer{
		AttachmentID:     id,
		Name:             name,
		Size:             size,
		CiphertextSHA256: cipherHash,
		Key:              base64.StdEncoding.EncodeToString(key[:]),
	}
	if _, err := o.validate(); err != nil {
		return nil, err
	}
	j, err := json.Marshal(o)
	if err != nil {
		return nil, err
	}
	return append(append([]byte(nil), offerMagic...), j...), nil
}

// DecodeOffer parses and validates an offer body. It returns the offer
// and the decoded file key. Every field is checked: the sender is a
// contact, not an authority.
func DecodeOffer(body []byte) (*Offer, *[filecrypt.KeySize]byte, error) {
	if !IsOffer(body) {
		return nil, nil, errors.New("transfer: not a file offer")
	}
	var o Offer
	dec := json.NewDecoder(bytes.NewReader(body[len(offerMagic):]))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&o); err != nil {
		return nil, nil, fmt.Errorf("transfer: malformed offer: %w", err)
	}
	key, err := o.validate()
	if err != nil {
		return nil, nil, err
	}
	o.Name = SafeName(o.Name)
	return &o, key, nil
}

func (o *Offer) validate() (*[filecrypt.KeySize]byte, error) {
	if !attachmentIDRe.MatchString(o.AttachmentID) {
		return nil, errors.New("transfer: offer has a bad attachment id")
	}
	if o.Size < 0 || o.Size > MaxPlainBytes() {
		return nil, fmt.Errorf("transfer: offer size %d is out of range", o.Size)
	}
	if !hashRe.MatchString(o.CiphertextSHA256) {
		return nil, errors.New("transfer: offer has a bad hash")
	}
	raw, err := base64.StdEncoding.DecodeString(o.Key)
	if err != nil || len(raw) != filecrypt.KeySize {
		return nil, errors.New("transfer: offer has a bad key")
	}
	var key [filecrypt.KeySize]byte
	copy(key[:], raw)
	return &key, nil
}

// Package room is the client-side core of Synq rooms: the sealed message
// format, the control message the owner uses to hand out the room key, and
// the per-room state machine that decides what to do with each incoming
// message. It has no UI, network or Bubble Tea code, so all of it is
// testable on its own. See ROOMS.md in synq-server for the model.
//
// Crypto, in one paragraph. The owner makes a random 256-bit room key and
// sends it to each member over a server-relayed direct channel, sealed with
// the two ends' static X25519 keys (so it is authenticated as coming from
// the owner, and needs no earlier chat between them), together with the
// roster of members' Ed25519 and X25519 keys. A room message is signed by its author
// and then sealed under the room key with AES-256-GCM. The server relays
// the sealed bytes and names the sender; the receiver checks the signature
// against the roster key of *that* sender, so the server can neither read
// a message nor make it look like it came from someone else, and one
// member cannot write as another.
package room

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"unicode"
	"unicode/utf8"
)

const (
	// MaxMembers matches the server's cap (owner and invitees included).
	MaxMembers = 16
	// MaxNameRunes matches the server's room name limit.
	MaxNameRunes = 48
	// MaxBodyBytes is the longest message body. It keeps one sealed
	// message, as base64 inside a frame, far below the server's 64 KiB
	// WebSocket limit.
	MaxBodyBytes = 4000
	// MaxHeld is how many messages that can't be checked yet (no key, or
	// a sender not on the roster yet) are kept waiting per room.
	MaxHeld = 64
)

// Key is a room's symmetric key.
type Key [32]byte

// NewKey returns a fresh random room key.
func NewKey() (Key, error) {
	var k Key
	if _, err := rand.Read(k[:]); err != nil {
		return k, fmt.Errorf("room: generate key: %w", err)
	}
	return k, nil
}

// NewMessageID returns a random message id. It is hex, so it is also a
// valid client id for the server's room_ack.
func NewMessageID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing is not recoverable and not a state a chat
		// client should limp on in.
		panic("room: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// ValidID reports whether s looks like a message id this package makes.
func ValidID(s string) bool {
	if len(s) != 32 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// CleanName trims and validates a room name; ok is false if it is empty,
// too long, or contains control characters.
func CleanName(raw string) (string, bool) {
	name := string(bytes.TrimSpace([]byte(raw)))
	n := utf8.RuneCountInString(name)
	if n == 0 || n > MaxNameRunes || !utf8.ValidString(name) {
		return "", false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", false
		}
	}
	return name, true
}

// --- messages ---

// Message is one room message as the author wrote it.
type Message struct {
	V    int    `json:"v"`
	ID   string `json:"id"`
	Re   string `json:"re,omitempty"` // id of the message this replies to
	Body string `json:"body"`
	TS   int64  `json:"ts"`
	Sig  []byte `json:"sig"`
}

const messageVersion = 1

func (m *Message) signingBytes(roomID string) []byte {
	var b bytes.Buffer
	b.WriteString("synq-room-msg-v1\x00")
	b.WriteString(roomID)
	b.WriteByte(0)
	b.WriteString(m.ID)
	b.WriteByte(0)
	b.WriteString(m.Re)
	b.WriteByte(0)
	fmt.Fprintf(&b, "%d", m.TS)
	b.WriteByte(0)
	b.WriteString(m.Body)
	return b.Bytes()
}

// Sign fills in m's version and signature.
func (m *Message) Sign(priv ed25519.PrivateKey, roomID string) {
	m.V = messageVersion
	m.Sig = ed25519.Sign(priv, m.signingBytes(roomID))
}

// Verify checks m's signature against pub.
func (m *Message) Verify(pub ed25519.PublicKey, roomID string) bool {
	if len(pub) != ed25519.PublicKeySize || len(m.Sig) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(pub, m.signingBytes(roomID), m.Sig)
}

// validate checks a decoded message's shape. It does not check the
// signature.
func (m *Message) validate() error {
	switch {
	case m.V != messageVersion:
		return fmt.Errorf("room: unsupported message version %d", m.V)
	case !ValidID(m.ID):
		return errors.New("room: bad message id")
	case m.Re != "" && !ValidID(m.Re):
		return errors.New("room: bad reply id")
	case len(m.Body) == 0 || len(m.Body) > MaxBodyBytes:
		return errors.New("room: message body empty or too long")
	case !utf8.ValidString(m.Body):
		return errors.New("room: message body is not valid UTF-8")
	}
	return nil
}

func aead(key *Key) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func aad(roomID string, gen int) []byte {
	return []byte(fmt.Sprintf("synq-room-seal-v1\x00%s\x00%d", roomID, gen))
}

// Seal signs m with priv and seals it under key. The result is
// nonce || ciphertext, ready to be base64'd into a room frame.
func Seal(key *Key, roomID string, gen int, priv ed25519.PrivateKey, m Message) ([]byte, error) {
	if key == nil {
		return nil, errors.New("room: no room key yet")
	}
	m.Sign(priv, roomID)
	if err := m.validate(); err != nil {
		return nil, err
	}
	plain, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	g, err := aead(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return g.Seal(nonce, nonce, plain, aad(roomID, gen)), nil
}

// ErrUndecryptable means the bytes did not open under the room key - not
// sealed under it, or damaged.
var ErrUndecryptable = errors.New("room: message did not decrypt")

// Open decrypts and decodes a sealed message. It does not check the
// signature; Receive does, once it knows who the sender is.
func Open(key *Key, roomID string, gen int, sealed []byte) (Message, error) {
	var m Message
	g, err := aead(key)
	if err != nil {
		return m, err
	}
	if len(sealed) < g.NonceSize()+g.Overhead() {
		return m, ErrUndecryptable
	}
	nonce, ct := sealed[:g.NonceSize()], sealed[g.NonceSize():]
	plain, err := g.Open(nil, nonce, ct, aad(roomID, gen))
	if err != nil {
		return m, ErrUndecryptable
	}
	if err := json.Unmarshal(plain, &m); err != nil {
		return m, fmt.Errorf("room: bad message: %w", err)
	}
	if err := m.validate(); err != nil {
		return m, err
	}
	return m, nil
}

// --- the owner's control message ---

var controlMagic = []byte("synq-room\x00")

// Peer is one member as the owner describes them.
type Peer struct {
	ID       string `json:"id"`
	Username string `json:"u"`
	SignKey  string `json:"k"` // hex Ed25519 public key
	Box      string `json:"b"` // hex X25519 public key (direct channel)
}

// State is everything a member needs to take part: sent by the owner,
// over the direct channel, whenever the key or the roster changes.
type State struct {
	Room   string `json:"room"`
	Name   string `json:"name"`
	Owner  string `json:"owner"`
	Gen    int    `json:"gen"`
	Key    []byte `json:"key"`
	Roster []Peer `json:"roster"`
}

// IsControl reports whether a decrypted direct body is a room control
// message.
func IsControl(body []byte) bool { return bytes.HasPrefix(body, controlMagic) }

// EncodeState builds the control message for st.
func EncodeState(st State) ([]byte, error) {
	j, err := json.Marshal(st)
	if err != nil {
		return nil, err
	}
	return append(append([]byte(nil), controlMagic...), j...), nil
}

// DecodeState parses and validates a control message.
func DecodeState(body []byte) (State, error) {
	var st State
	if !IsControl(body) {
		return st, errors.New("room: not a control message")
	}
	if err := json.Unmarshal(body[len(controlMagic):], &st); err != nil {
		return st, fmt.Errorf("room: bad control message: %w", err)
	}
	if st.Room == "" || st.Owner == "" {
		return st, errors.New("room: control message without room or owner")
	}
	if _, ok := CleanName(st.Name); !ok {
		return st, errors.New("room: control message with a bad room name")
	}
	if st.Gen < 1 {
		return st, errors.New("room: control message with a bad generation")
	}
	if len(st.Key) != len(Key{}) {
		return st, errors.New("room: control message with a bad key")
	}
	if len(st.Roster) == 0 || len(st.Roster) > MaxMembers {
		return st, errors.New("room: control message with a bad roster size")
	}
	seen := map[string]bool{}
	ownerListed := false
	for _, p := range st.Roster {
		if p.ID == "" || seen[p.ID] {
			return st, errors.New("room: roster has a missing or repeated id")
		}
		seen[p.ID] = true
		if _, ok := CleanName(p.Username); !ok {
			return st, errors.New("room: roster has a bad username")
		}
		if k, err := hex.DecodeString(p.SignKey); err != nil || len(k) != ed25519.PublicKeySize {
			return st, errors.New("room: roster has a bad signing key")
		}
		if k, err := hex.DecodeString(p.Box); err != nil || len(k) != 32 {
			return st, errors.New("room: roster has a bad box key")
		}
		if p.ID == st.Owner {
			ownerListed = true
		}
	}
	if !ownerListed {
		return st, errors.New("room: roster does not include the owner")
	}
	return st, nil
}

// --- the direct channel ---

// A direct payload is sealed under a key both ends can compute from their
// static X25519 keys: HKDF-SHA256 of the shared secret, with the room id and
// both public keys (sorted, so both ends agree) as context. The additional
// data names the room, the sender and the recipient, so a payload can't be
// replayed into another room or reflected back at its sender. Only the two
// holders of the private keys can make or read one, which is what makes the
// owner's state authentic without any signature of its own.

const directDomain = "synq-room-direct-v1\x00"

// ErrDirectUndecryptable means a direct payload didn't open.
var ErrDirectUndecryptable = errors.New("room: direct payload did not open")

func directAEAD(myPriv, theirPub *[32]byte, roomID string) (cipher.AEAD, [32]byte, error) {
	var myPub [32]byte
	priv, err := ecdh.X25519().NewPrivateKey(myPriv[:])
	if err != nil {
		return nil, myPub, err
	}
	copy(myPub[:], priv.PublicKey().Bytes())
	peer, err := ecdh.X25519().NewPublicKey(theirPub[:])
	if err != nil {
		return nil, myPub, err
	}
	secret, err := priv.ECDH(peer)
	if err != nil {
		return nil, myPub, err
	}
	pubs := [][]byte{myPub[:], theirPub[:]}
	sort.Slice(pubs, func(i, j int) bool { return bytes.Compare(pubs[i], pubs[j]) < 0 })
	info := directDomain + roomID + "\x00" + string(pubs[0]) + string(pubs[1])
	key, err := hkdf.Key(sha256.New, secret, nil, info, 32)
	if err != nil {
		return nil, myPub, err
	}
	var k Key
	copy(k[:], key)
	g, err := aead(&k)
	return g, myPub, err
}

func directAAD(roomID string, sender, recipient *[32]byte) []byte {
	b := []byte(directDomain + roomID + "\x00")
	b = append(b, sender[:]...)
	return append(b, recipient[:]...)
}

// SealDirect seals plain for the holder of theirPub. The result is
// nonce || ciphertext.
func SealDirect(myPriv, theirPub *[32]byte, roomID string, plain []byte) ([]byte, error) {
	g, myPub, err := directAEAD(myPriv, theirPub, roomID)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return g.Seal(nonce, nonce, plain, directAAD(roomID, &myPub, theirPub)), nil
}

// OpenDirect opens a payload that theirPub's holder sealed for us.
func OpenDirect(myPriv, theirPub *[32]byte, roomID string, sealed []byte) ([]byte, error) {
	g, myPub, err := directAEAD(myPriv, theirPub, roomID)
	if err != nil {
		return nil, err
	}
	if len(sealed) < g.NonceSize()+g.Overhead() {
		return nil, ErrDirectUndecryptable
	}
	plain, err := g.Open(nil, sealed[:g.NonceSize()], sealed[g.NonceSize():], directAAD(roomID, theirPub, &myPub))
	if err != nil {
		return nil, ErrDirectUndecryptable
	}
	return plain, nil
}

// --- per-room state machine ---

// Result is what Receive did with a message.
type Result int

const (
	// Accepted: decrypted, signature good, new.
	Accepted Result = iota
	// Duplicate: a message id already shown (a replay or a double relay).
	Duplicate
	// Held: can't be checked yet (no key, or the sender isn't on the
	// roster yet); kept to try again when the state changes.
	Held
	// Rejected: failed to decrypt or verify, or malformed. Dropped.
	Rejected
)

type heldFrame struct {
	from   string
	seq    int64
	sealed []byte
}

// Received is an accepted message together with who sent it.
type Received struct {
	From string
	Seq  int64
	Msg  Message
}

// Room is one room as this client sees it.
type Room struct {
	ID      string
	Name    string
	OwnerID string

	// Key is nil until the owner's state arrives (the owner has it from
	// the start).
	Key    *Key
	Gen    int
	Roster map[string]Peer

	// prev is the key of the generation before the current one. Messages
	// sent just before a rotation are still in flight when it happens, so
	// they are still read; it is dropped at the next rotation.
	prev    *Key
	prevGen int

	seen map[string]bool
	held []heldFrame
}

// New makes an empty room record.
func New(id, name, ownerID string) *Room {
	return &Room{ID: id, Name: name, OwnerID: ownerID, Roster: map[string]Peer{}, seen: map[string]bool{}}
}

// Ready reports whether this client can send and read messages.
func (r *Room) Ready() bool { return r.Key != nil }

// ApplyState installs the owner's state. It is refused unless `from` is
// the owner (the direct channel authenticates the sender as `from`), it is
// for this room, and it is not older than what is held. Messages held while
// waiting are returned if they can now be accepted.
func (r *Room) ApplyState(from string, st State) ([]Received, error) {
	if from != r.OwnerID || st.Owner != r.OwnerID {
		return nil, errors.New("room: state did not come from the owner")
	}
	if st.Room != r.ID {
		return nil, errors.New("room: state is for a different room")
	}
	if st.Gen < r.Gen {
		return nil, errors.New("room: state is older than the one held")
	}
	var k Key
	copy(k[:], st.Key)
	if r.Key != nil && st.Gen > r.Gen {
		old := *r.Key
		r.prev, r.prevGen = &old, r.Gen
	}
	r.Key = &k
	r.Gen = st.Gen
	r.Name = st.Name
	roster := make(map[string]Peer, len(st.Roster))
	for _, p := range st.Roster {
		roster[p.ID] = p
	}
	r.Roster = roster
	return r.retryHeld(), nil
}

// SetOwnerState is what the owner calls on creating a room: it installs a
// key and itself as the roster.
func (r *Room) SetOwnerState(key Key, self Peer) {
	r.Key = &key
	r.Gen = 1
	r.Roster = map[string]Peer{self.ID: self}
}

// Rekey rotates to a fresh key (owner side), so that someone who has just
// left or been removed can't read anything sent from now on. The old key
// is kept for messages already in flight.
func (r *Room) Rekey() error {
	k, err := NewKey()
	if err != nil {
		return err
	}
	if r.Key != nil {
		old := *r.Key
		r.prev, r.prevGen = &old, r.Gen
	}
	r.Key = &k
	r.Gen++
	return nil
}

// SetOwner records a change of owner. It is refused unless the new owner is
// already on the roster: the server announces handovers, and this is what
// stops a bare announcement from installing a stranger.
func (r *Room) SetOwner(id string) error {
	if _, ok := r.Roster[id]; !ok {
		return errors.New("room: new owner is not on the roster")
	}
	r.OwnerID = id
	return nil
}

// AddPeer adds or replaces a roster entry (owner side).
func (r *Room) AddPeer(p Peer) { r.Roster[p.ID] = p }

// RemovePeer drops a roster entry (owner side).
func (r *Room) RemovePeer(id string) { delete(r.Roster, id) }

// State builds the control message content for the current key and roster,
// roster in a stable order (owner first).
func (r *Room) State() (State, error) {
	if r.Key == nil {
		return State{}, errors.New("room: no key")
	}
	st := State{Room: r.ID, Name: r.Name, Owner: r.OwnerID, Gen: r.Gen, Key: append([]byte(nil), r.Key[:]...)}
	if o, ok := r.Roster[r.OwnerID]; ok {
		st.Roster = append(st.Roster, o)
	}
	for id, p := range r.Roster {
		if id != r.OwnerID {
			st.Roster = append(st.Roster, p)
		}
	}
	return st, nil
}

// Receive handles one relayed room message. `from` is the sender the
// server named.
func (r *Room) Receive(from string, seq int64, sealed []byte) (Received, Result) {
	if r.Key == nil {
		r.hold(from, seq, sealed)
		return Received{}, Held
	}
	peer, ok := r.Roster[from]
	if !ok {
		r.hold(from, seq, sealed)
		return Received{}, Held
	}
	msg, err := Open(r.Key, r.ID, r.Gen, sealed)
	if err != nil && r.prev != nil {
		msg, err = Open(r.prev, r.ID, r.prevGen, sealed)
	}
	if err != nil {
		return Received{}, Rejected
	}
	pub, err := hex.DecodeString(peer.SignKey)
	if err != nil || !msg.Verify(ed25519.PublicKey(pub), r.ID) {
		return Received{}, Rejected
	}
	if r.seen[msg.ID] {
		return Received{}, Duplicate
	}
	r.seen[msg.ID] = true
	return Received{From: from, Seq: seq, Msg: msg}, Accepted
}

// MarkSent records the id of a message this client sent, so a copy
// relayed back is not shown twice.
func (r *Room) MarkSent(id string) { r.seen[id] = true }

func (r *Room) hold(from string, seq int64, sealed []byte) {
	if len(r.held) >= MaxHeld {
		r.held = r.held[1:]
	}
	r.held = append(r.held, heldFrame{from: from, seq: seq, sealed: append([]byte(nil), sealed...)})
}

func (r *Room) retryHeld() []Received {
	pending := r.held
	r.held = nil
	var out []Received
	for _, h := range pending {
		if got, res := r.Receive(h.from, h.seq, h.sealed); res == Accepted {
			out = append(out, got)
		}
	}
	return out
}

// Wipe forgets the key and everything held, best effort.
func (r *Room) Wipe() {
	if r.Key != nil {
		for i := range r.Key {
			r.Key[i] = 0
		}
		r.Key = nil
	}
	if r.prev != nil {
		for i := range r.prev {
			r.prev[i] = 0
		}
		r.prev = nil
	}
	r.held = nil
	r.seen = map[string]bool{}
	r.Roster = map[string]Peer{}
}

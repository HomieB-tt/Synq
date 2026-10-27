package room

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"reflect"
	"strings"
	"testing"
)

type member struct {
	id      string
	priv    ed25519.PrivateKey
	boxPriv [32]byte
	boxPub  [32]byte
	peer    Peer
}

func newMember(t *testing.T, id, name string) member {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	bp, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	m := member{id: id, priv: priv}
	copy(m.boxPriv[:], bp.Bytes())
	copy(m.boxPub[:], bp.PublicKey().Bytes())
	m.peer = Peer{ID: id, Username: name, SignKey: hex.EncodeToString(pub), Box: hex.EncodeToString(m.boxPub[:])}
	return m
}

// setup makes an owner's room with two members who have received its state.
func setup(t *testing.T) (owner, alice, bob member, ro, ra, rb *Room) {
	t.Helper()
	owner = newMember(t, "owner-id", "olive")
	alice = newMember(t, "alice-id", "alice")
	bob = newMember(t, "bob-id", "bob")

	ro = New("room1", "standup", owner.id)
	k, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	ro.SetOwnerState(k, owner.peer)
	ro.AddPeer(alice.peer)
	ro.AddPeer(bob.peer)

	st, err := ro.State()
	if err != nil {
		t.Fatal(err)
	}
	wire, err := EncodeState(st)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []**Room{&ra, &rb} {
		*r = New("room1", "standup", owner.id)
		got, err := DecodeState(wire)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := (*r).ApplyState(owner.id, got); err != nil {
			t.Fatal(err)
		}
	}
	return
}

func seal(t *testing.T, r *Room, from member, re, body string) []byte {
	t.Helper()
	s, err := Seal(r.Key, r.ID, r.Gen, from.priv, Message{ID: NewMessageID(), Re: re, Body: body, TS: 1})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAMessageRoundTripsBetweenMembers(t *testing.T) {
	_, alice, _, _, ra, rb := setup(t)
	got, res := rb.Receive(alice.id, 1, seal(t, ra, alice, "", "hello room"))
	if res != Accepted || got.Msg.Body != "hello room" || got.From != alice.id || got.Seq != 1 {
		t.Fatalf("got %+v, %v", got, res)
	}
}

func TestRepliesCarryTheirTarget(t *testing.T) {
	_, alice, bob, _, ra, rb := setup(t)
	first := seal(t, ra, alice, "", "lunch?")
	m1, _ := rb.Receive(alice.id, 1, first)
	reply := seal(t, rb, bob, m1.Msg.ID, "yes")
	m2, res := ra.Receive(bob.id, 2, reply)
	if res != Accepted || m2.Msg.Re != m1.Msg.ID {
		t.Fatalf("reply not linked: %+v %v", m2, res)
	}
}

func TestADuplicateIsIgnored(t *testing.T) {
	_, alice, _, _, ra, rb := setup(t)
	s := seal(t, ra, alice, "", "once")
	if _, res := rb.Receive(alice.id, 1, s); res != Accepted {
		t.Fatal(res)
	}
	if _, res := rb.Receive(alice.id, 2, s); res != Duplicate {
		t.Fatalf("replay not caught: %v", res)
	}
}

func TestOwnMessagesRelayedBackAreNotShownTwice(t *testing.T) {
	_, alice, _, _, ra, _ := setup(t)
	m := Message{ID: NewMessageID(), Body: "mine", TS: 1}
	s, _ := Seal(ra.Key, ra.ID, ra.Gen, alice.priv, m)
	ra.MarkSent(m.ID)
	if _, res := ra.Receive(alice.id, 1, s); res != Duplicate {
		t.Fatalf("got %v", res)
	}
}

func TestTheServerCannotMakeOneMemberSpeakAsAnother(t *testing.T) {
	_, alice, bob, _, ra, rb := setup(t)
	s := seal(t, ra, alice, "", "I am alice")
	// The relay claims bob sent alice's (validly signed) message.
	if _, res := rb.Receive(bob.id, 1, s); res != Rejected {
		t.Fatalf("forged sender accepted: %v", res)
	}
}

func TestAMemberCannotForgeAnotherMembersSignature(t *testing.T) {
	_, alice, bob, ro, _, rb := setup(t)
	// bob has the room key, but signs with his own key while the server
	// names alice as the sender.
	s := seal(t, ro, bob, "", "pretend")
	if _, res := rb.Receive(alice.id, 1, s); res != Rejected {
		t.Fatalf("got %v", res)
	}
}

func TestTamperedCiphertextIsRejected(t *testing.T) {
	_, alice, _, _, ra, rb := setup(t)
	s := seal(t, ra, alice, "", "x")
	s[len(s)-1] ^= 1
	if _, res := rb.Receive(alice.id, 1, s); res != Rejected {
		t.Fatalf("got %v", res)
	}
}

func TestAMessageFromAnotherRoomIsRejected(t *testing.T) {
	_, alice, _, _, ra, rb := setup(t)
	s := seal(t, ra, alice, "", "x")
	rb.ID = "room2" // same key, different room id: AAD and signature differ
	rb.Roster = ra.Roster
	if _, res := rb.Receive(alice.id, 1, s); res != Rejected {
		t.Fatalf("got %v", res)
	}
}

func TestMessagesWaitForTheKeyAndThenAppear(t *testing.T) {
	owner, alice, _, ro, ra, _ := setup(t)
	late := New("room1", "standup", owner.id)
	s := seal(t, ra, alice, "", "early bird")
	if _, res := late.Receive(alice.id, 1, s); res != Held {
		t.Fatalf("got %v", res)
	}
	st, _ := ro.State()
	got, err := late.ApplyState(owner.id, st)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Msg.Body != "early bird" {
		t.Fatalf("held message not released: %+v", got)
	}
}

func TestAMessageFromSomeoneNotYetOnTheRosterIsHeld(t *testing.T) {
	owner, _, _, ro, ra, rb := setup(t)
	carol := newMember(t, "carol-id", "carol")
	ro.AddPeer(carol.peer)
	s := seal(t, ro, carol, "", "hi from carol")
	if _, res := rb.Receive(carol.id, 5, s); res != Held {
		t.Fatalf("got %v", res)
	}
	st, _ := ro.State()
	got, err := rb.ApplyState(owner.id, st)
	if err != nil || len(got) != 1 || got[0].From != carol.id || got[0].Seq != 5 {
		t.Fatalf("got %+v, %v", got, err)
	}
	_ = ra
}

func TestHeldMessagesAreBounded(t *testing.T) {
	r := New("room1", "x", "o")
	for i := 0; i < MaxHeld*2; i++ {
		r.Receive("someone", int64(i), []byte("junk"))
	}
	if len(r.held) != MaxHeld {
		t.Fatalf("held %d, want %d", len(r.held), MaxHeld)
	}
}

func TestStateOnlyCountsFromTheOwner(t *testing.T) {
	owner, alice, _, ro, _, _ := setup(t)
	st, _ := ro.State()
	r := New("room1", "standup", owner.id)
	if _, err := r.ApplyState(alice.id, st); err == nil {
		t.Fatal("a member's state was accepted")
	}
	if r.Ready() {
		t.Fatal("room became ready from a refused state")
	}
	other := New("room9", "standup", owner.id)
	if _, err := other.ApplyState(owner.id, st); err == nil {
		t.Fatal("state for a different room was accepted")
	}
}

func TestOlderStateIsRefused(t *testing.T) {
	owner, _, _, ro, ra, _ := setup(t)
	ro.Gen = 3
	st, _ := ro.State()
	if _, err := ra.ApplyState(owner.id, st); err != nil {
		t.Fatal(err)
	}
	ro.Gen = 2
	old, _ := ro.State()
	if _, err := ra.ApplyState(owner.id, old); err == nil {
		t.Fatal("older state accepted")
	}
}

func TestDecodeStateValidates(t *testing.T) {
	owner := newMember(t, "o", "olive")
	good := State{Room: "r", Name: "n", Owner: "o", Gen: 1, Key: make([]byte, 32), Roster: []Peer{owner.peer}}
	if _, err := EncodeState(good); err != nil {
		t.Fatal(err)
	}
	bad := map[string]func(*State){
		"no room":      func(s *State) { s.Room = "" },
		"bad name":     func(s *State) { s.Name = "x\x1b[0m" },
		"gen zero":     func(s *State) { s.Gen = 0 },
		"short key":    func(s *State) { s.Key = make([]byte, 16) },
		"empty roster": func(s *State) { s.Roster = nil },
		"no owner":     func(s *State) { s.Owner = "someone-else" },
		"bad sign key": func(s *State) { s.Roster = []Peer{{ID: "o", Username: "olive", SignKey: "zz", Box: owner.peer.Box}} },
		"bad box key": func(s *State) {
			s.Roster = []Peer{{ID: "o", Username: "olive", SignKey: owner.peer.SignKey, Box: "zz"}}
		},
		"no box key": func(s *State) { s.Roster = []Peer{{ID: "o", Username: "olive", SignKey: owner.peer.SignKey}} },
		"dup id":     func(s *State) { s.Roster = []Peer{owner.peer, owner.peer} },
		"ctrl name": func(s *State) {
			s.Roster = []Peer{{ID: "o", Username: "a\nb", SignKey: owner.peer.SignKey, Box: owner.peer.Box}}
		},
	}
	for name, mutate := range bad {
		s := good
		s.Roster = append([]Peer(nil), good.Roster...)
		mutate(&s)
		wire, _ := EncodeState(s)
		if _, err := DecodeState(wire); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	wire, _ := EncodeState(good)
	if _, err := DecodeState(wire); err != nil {
		t.Errorf("good state refused: %v", err)
	}
}

func TestRosterTooBigIsRefused(t *testing.T) {
	owner := newMember(t, "o", "olive")
	roster := []Peer{owner.peer}
	for i := 0; i < MaxMembers; i++ {
		roster = append(roster, Peer{ID: string(rune('a' + i)), Username: "u", SignKey: owner.peer.SignKey, Box: owner.peer.Box})
	}
	wire, _ := EncodeState(State{Room: "r", Name: "n", Owner: "o", Gen: 1, Key: make([]byte, 32), Roster: roster})
	if _, err := DecodeState(wire); err == nil {
		t.Fatal("17-person roster accepted")
	}
}

func TestControlMessagesAreRecognisedAndChatIsNot(t *testing.T) {
	owner := newMember(t, "o", "olive")
	wire, _ := EncodeState(State{Room: "r", Name: "n", Owner: "o", Gen: 1, Key: make([]byte, 32), Roster: []Peer{owner.peer}})
	if !IsControl(wire) {
		t.Fatal("control message not recognised")
	}
	if IsControl([]byte("hello synq-room")) || IsControl(nil) {
		t.Fatal("chat text taken for control")
	}
}

func TestMessageValidation(t *testing.T) {
	k, _ := NewKey()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	cases := map[string]Message{
		"empty body":   {ID: NewMessageID(), Body: ""},
		"huge body":    {ID: NewMessageID(), Body: strings.Repeat("x", MaxBodyBytes+1)},
		"bad id":       {ID: "nope", Body: "x"},
		"bad reply id": {ID: NewMessageID(), Re: "nope", Body: "x"},
		"bad utf8":     {ID: NewMessageID(), Body: "\xff\xfe"},
	}
	for name, m := range cases {
		if _, err := Seal(&k, "r", 1, priv, m); err == nil {
			t.Errorf("%s: sealed", name)
		}
	}
	if _, err := Seal(&k, "r", 1, priv, Message{ID: NewMessageID(), Body: strings.Repeat("x", MaxBodyBytes)}); err != nil {
		t.Errorf("a full-size body was refused: %v", err)
	}
	if _, err := Seal(nil, "r", 1, priv, Message{ID: NewMessageID(), Body: "x"}); err == nil {
		t.Error("sealed without a key")
	}
}

func TestSealedMessagesDoNotRevealTheText(t *testing.T) {
	_, alice, _, _, ra, _ := setup(t)
	s := seal(t, ra, alice, "", "attack at dawn")
	if bytes.Contains(s, []byte("attack")) {
		t.Fatal("plaintext visible in the sealed bytes")
	}
	// Two seals of the same text differ (random nonce, fresh id).
	if bytes.Equal(s, seal(t, ra, alice, "", "attack at dawn")) {
		t.Fatal("identical ciphertexts")
	}
}

func TestWipeForgetsTheKey(t *testing.T) {
	_, alice, _, _, ra, rb := setup(t)
	s := seal(t, ra, alice, "", "x")
	rb.Wipe()
	if rb.Ready() || len(rb.Roster) != 0 {
		t.Fatal("state survived Wipe")
	}
	if _, res := rb.Receive(alice.id, 1, s); res != Held {
		t.Fatalf("got %v", res)
	}
}

func TestCleanName(t *testing.T) {
	if n, ok := CleanName("  team sync "); !ok || n != "team sync" {
		t.Fatalf("%q %v", n, ok)
	}
	for _, bad := range []string{"", "   ", strings.Repeat("a", MaxNameRunes+1), "a\x1bb", "a\nb"} {
		if _, ok := CleanName(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, ok := CleanName(strings.Repeat("é", MaxNameRunes)); !ok {
		t.Error("48 multi-byte runes refused")
	}
}

func TestMessageIDsAreValidAndUnique(t *testing.T) {
	a, b := NewMessageID(), NewMessageID()
	if !ValidID(a) || a == b {
		t.Fatalf("%q %q", a, b)
	}
	if ValidID("") || ValidID(strings.Repeat("g", 32)) || ValidID(a+"0") {
		t.Fatal("bad ids accepted")
	}
}

// --- direct channel, rekey, handover ---

func TestDirectPayloadsRoundTripBetweenTwoKeyHolders(t *testing.T) {
	owner := newMember(t, "o", "olive")
	alice := newMember(t, "a", "alice")
	sealed, err := SealDirect(&owner.boxPriv, &alice.boxPub, "room1", []byte("the key"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := OpenDirect(&alice.boxPriv, &owner.boxPub, "room1", sealed)
	if err != nil || string(got) != "the key" {
		t.Fatalf("got %q, %v", got, err)
	}
	if bytes.Contains(sealed, []byte("the key")) {
		t.Fatal("payload is not encrypted")
	}
}

func TestDirectPayloadsAreBoundToRoomAndDirection(t *testing.T) {
	owner := newMember(t, "o", "olive")
	alice := newMember(t, "a", "alice")
	mallory := newMember(t, "m", "mallory")
	sealed, _ := SealDirect(&owner.boxPriv, &alice.boxPub, "room1", []byte("x"))

	if _, err := OpenDirect(&alice.boxPriv, &owner.boxPub, "room2", sealed); err == nil {
		t.Fatal("opened under another room id")
	}
	// Reflected back at the sender as if alice had sent it.
	if _, err := OpenDirect(&owner.boxPriv, &alice.boxPub, "room1", sealed); err == nil {
		t.Fatal("reflected payload opened")
	}
	// Someone else can't open it, nor claim to be the sender.
	if _, err := OpenDirect(&mallory.boxPriv, &owner.boxPub, "room1", sealed); err == nil {
		t.Fatal("a third party opened it")
	}
	if _, err := OpenDirect(&alice.boxPriv, &mallory.boxPub, "room1", sealed); err == nil {
		t.Fatal("opened as if from someone else")
	}
	sealed[len(sealed)-1] ^= 1
	if _, err := OpenDirect(&alice.boxPriv, &owner.boxPub, "room1", sealed); err == nil {
		t.Fatal("tampered payload opened")
	}
	if _, err := OpenDirect(&alice.boxPriv, &owner.boxPub, "room1", []byte("short")); err == nil {
		t.Fatal("short payload opened")
	}
}

func TestRekeyLocksOutSomeoneWhoLeft(t *testing.T) {
	owner, alice, bob, ro, ra, rb := setup(t)
	before := seal(t, ro, owner, "", "before")

	// bob is removed; the owner rotates and tells alice only.
	ro.RemovePeer(bob.id)
	if err := ro.Rekey(); err != nil {
		t.Fatal(err)
	}
	if ro.Gen != 2 {
		t.Fatalf("gen = %d", ro.Gen)
	}
	st, _ := ro.State()
	if _, err := ra.ApplyState(owner.id, st); err != nil {
		t.Fatal(err)
	}

	after := seal(t, ro, owner, "", "after")
	if _, res := ra.Receive(owner.id, 2, after); res != Accepted {
		t.Fatalf("alice could not read the new key's message: %v", res)
	}
	if _, res := rb.Receive(owner.id, 2, after); res != Rejected {
		t.Fatalf("bob read a message sent after he was removed: %v", res)
	}
	// A message sealed just before the rotation still reads for alice.
	if _, res := ra.Receive(owner.id, 1, before); res != Accepted {
		t.Fatalf("in-flight message lost across the rotation: %v", res)
	}
	_ = alice
}

func TestThePreviousKeyIsDroppedAtTheNextRotation(t *testing.T) {
	owner, _, _, ro, ra, _ := setup(t)
	old := seal(t, ro, owner, "", "old")
	for i := 0; i < 2; i++ {
		if err := ro.Rekey(); err != nil {
			t.Fatal(err)
		}
		st, _ := ro.State()
		if _, err := ra.ApplyState(owner.id, st); err != nil {
			t.Fatal(err)
		}
	}
	if _, res := ra.Receive(owner.id, 1, old); res != Rejected {
		t.Fatalf("two generations back still opened: %v", res)
	}
}

func TestSetOwnerOnlyAcceptsSomeoneOnTheRoster(t *testing.T) {
	owner, alice, _, _, ra, _ := setup(t)
	if err := ra.SetOwner("stranger"); err == nil {
		t.Fatal("a stranger became owner")
	}
	if ra.OwnerID != owner.id {
		t.Fatal("owner changed despite refusal")
	}
	if err := ra.SetOwner(alice.id); err != nil {
		t.Fatal(err)
	}
	if ra.OwnerID != alice.id {
		t.Fatal("owner not updated")
	}
}

func TestANewOwnerCanRekeyAndServeTheRoomItAlreadyHolds(t *testing.T) {
	owner, alice, bob, _, ra, rb := setup(t)
	// The handover: everyone adopts alice as owner.
	for _, r := range []*Room{ra, rb} {
		if err := r.SetOwner(alice.id); err != nil {
			t.Fatal(err)
		}
	}
	// alice already holds key and roster, so she can hand out a new key
	// without any help from the old owner.
	if err := ra.Rekey(); err != nil {
		t.Fatal(err)
	}
	ra.RemovePeer(owner.id)
	st, err := ra.State()
	if err != nil {
		t.Fatal(err)
	}
	if st.Owner != alice.id {
		t.Fatalf("state names owner %q", st.Owner)
	}
	if _, err := rb.ApplyState(alice.id, st); err != nil {
		t.Fatal(err)
	}
	if _, res := rb.Receive(alice.id, 1, seal(t, ra, alice, "", "hi from the new owner")); res != Accepted {
		t.Fatalf("bob could not read the new owner: %v", res)
	}
	// The old owner's state no longer counts.
	if _, err := rb.ApplyState(owner.id, st); err == nil {
		t.Fatal("the former owner's state was accepted")
	}
	_ = bob
}

// --- files ---

func fileRef() *FileRef {
	return &FileRef{
		ID:   "0a1b2c3d-4e5f-6071-8293-a4b5c6d7e8f9",
		Name: "plan.pdf",
		Size: 1234,
		SHA:  "sha256:" + strings.Repeat("ab", 32),
		Key:  bytes.Repeat([]byte{7}, 32),
	}
}

func sealFile(t *testing.T, r *Room, from member, f *FileRef) []byte {
	t.Helper()
	s, err := Seal(r.Key, r.ID, r.Gen, from.priv, Message{ID: NewMessageID(), Body: "📎 plan.pdf", File: f, TS: 1})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAFileMessageRoundTripsAndKeepsItsFileDetails(t *testing.T) {
	owner, _, _, ro, ra, _ := setup(t)
	got, res := ra.Receive(owner.id, 1, sealFile(t, ro, owner, fileRef()))
	if res != Accepted || got.Msg.File == nil {
		t.Fatalf("res=%v file=%v", res, got.Msg.File)
	}
	if !reflect.DeepEqual(got.Msg.File, fileRef()) {
		t.Fatal("file details changed in transit")
	}
}

func TestTheSignatureCoversTheFile(t *testing.T) {
	owner, _, _, ro, _, _ := setup(t)
	m := Message{ID: NewMessageID(), Body: "📎", File: fileRef(), TS: 1}
	m.Sign(owner.priv, ro.ID)
	pub, _ := hex.DecodeString(owner.peer.SignKey)
	if !m.Verify(pub, ro.ID) {
		t.Fatal("a good file message didn't verify")
	}
	for name, mutate := range map[string]func(*Message){
		"id":   func(m *Message) { m.File.ID = "ffffffff-4e5f-6071-8293-a4b5c6d7e8f9" },
		"name": func(m *Message) { m.File.Name = "evil.exe" },
		"size": func(m *Message) { m.File.Size++ },
		"sha":  func(m *Message) { m.File.SHA = "sha256:" + strings.Repeat("cd", 32) },
		"key":  func(m *Message) { m.File.Key = bytes.Repeat([]byte{9}, 32) },
		"drop": func(m *Message) { m.File = nil },
	} {
		c := m
		f := *m.File
		c.File = &f
		mutate(&c)
		if c.Verify(pub, ro.ID) {
			t.Errorf("changing the file %s still verified", name)
		}
	}
}

func TestBadFileDetailsAreRefused(t *testing.T) {
	owner, _, _, ro, _, _ := setup(t)
	bad := map[string]func(*FileRef){
		"id":           func(f *FileRef) { f.ID = "../../etc/passwd" },
		"uppercase id": func(f *FileRef) { f.ID = strings.ToUpper(f.ID) },
		"neg size":     func(f *FileRef) { f.Size = -1 },
		"huge":         func(f *FileRef) { f.Size = MaxFileBytes + 1 },
		"sha":          func(f *FileRef) { f.SHA = "md5:00" },
		"short key":    func(f *FileRef) { f.Key = []byte{1} },
		"no name":      func(f *FileRef) { f.Name = "" },
		"long name":    func(f *FileRef) { f.Name = strings.Repeat("a", 256) },
	}
	for name, mutate := range bad {
		f := fileRef()
		mutate(f)
		if _, err := Seal(ro.Key, ro.ID, ro.Gen, owner.priv, Message{ID: NewMessageID(), Body: "x", File: f, TS: 1}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

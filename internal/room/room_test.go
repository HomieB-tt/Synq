package room

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"testing"
)

type member struct {
	id   string
	priv ed25519.PrivateKey
	peer Peer
}

func newMember(t *testing.T, id, name string) member {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return member{id: id, priv: priv, peer: Peer{ID: id, Username: name, SignKey: hex.EncodeToString(pub)}}
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
		"bad sign key": func(s *State) { s.Roster = []Peer{{ID: "o", Username: "olive", SignKey: "zz"}} },
		"dup id":       func(s *State) { s.Roster = []Peer{owner.peer, owner.peer} },
		"ctrl name":    func(s *State) { s.Roster = []Peer{{ID: "o", Username: "a\nb", SignKey: owner.peer.SignKey}} },
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
		roster = append(roster, Peer{ID: string(rune('a' + i)), Username: "u", SignKey: owner.peer.SignKey})
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

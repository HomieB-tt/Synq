package app

import (
	"encoding/hex"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/HomieB-tt/synq/internal/api"
	"github.com/HomieB-tt/synq/internal/chat"
	"github.com/HomieB-tt/synq/internal/crypto"
	"github.com/HomieB-tt/synq/internal/room"
	"github.com/HomieB-tt/synq/internal/ui/styles"
	"github.com/HomieB-tt/synq/internal/ws"
)

const (
	testRoomID  = "r1"
	testOwnerID = "owner-id"
	ownerThread = chat.ContactKey("owner-contact")
)

func roomTestModel(t *testing.T, username string) Model {
	t.Helper()
	id, err := crypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	return Model{
		theme:     styles.Default(),
		chatStore: chat.NewStore(),
		identity:  id,
		username:  username,
		width:     100,
		height:    30,
	}
}

func withClient(t *testing.T, m Model) Model {
	t.Helper()
	c, err := ws.NewClient("ws://example.invalid/ws")
	if err != nil {
		t.Fatalf("ws.NewClient: %v", err)
	}
	m.wsClient = c
	return m
}

// ownerModel is an owner who has just created room r1.
func ownerModel(t *testing.T) Model {
	t.Helper()
	m := roomTestModel(t, "olive")
	next, _ := m.handleRoomCreated(roomCreatedMsg{room: &api.Room{ID: testRoomID, Name: "standup", OwnerID: testOwnerID}})
	return next.(Model)
}

// memberModel is a member who accepted an invitation to r1 and knows the
// owner as a contact.
func memberModel(t *testing.T, username string) Model {
	t.Helper()
	m := roomTestModel(t, username)
	m.rememberContact(ownerThread, "olive", testOwnerID)
	m.putRoom(&roomEntry{r: room.New(testRoomID, "standup", testOwnerID), joined: true})
	return m
}

// admit adds a member to the owner's roster and returns the control
// message the owner would now send everyone.
func admit(t *testing.T, owner *Model, id, name string, m Model) []byte {
	t.Helper()
	e := owner.roomEntryFor(testRoomID)
	e.r.AddPeer(room.Peer{ID: id, Username: name, SignKey: hex.EncodeToString(m.identity.SigningPublic)})
	st, err := e.r.State()
	if err != nil {
		t.Fatal(err)
	}
	wire, err := room.EncodeState(st)
	if err != nil {
		t.Fatal(err)
	}
	return wire
}

func lastBody(m Model, th chat.ContactKey) string {
	n := m.chatStore.Len(th)
	if s := m.chatStore.Slice(th, n-1, n); len(s) == 1 {
		return string(s[0].Body)
	}
	return ""
}

func TestRoomThreadKeysNeverLookLikeContacts(t *testing.T) {
	if id, ok := roomIDOf(roomThread("abc")); !ok || id != "abc" {
		t.Fatalf("round trip failed: %q %v", id, ok)
	}
	for _, c := range []chat.ContactKey{"", "deadbeef", chat.NewContactKey(&[32]byte{1})} {
		if _, ok := roomIDOf(c); ok {
			t.Errorf("%q taken for a room", c)
		}
	}
}

func TestCreatingARoomOpensItsThreadWithTheKeyHeld(t *testing.T) {
	m := ownerModel(t)
	e := m.roomEntryFor(testRoomID)
	if e == nil || !e.owner || !e.joined || !e.r.Ready() {
		t.Fatalf("entry = %+v", e)
	}
	th := roomThread(testRoomID)
	if m.chatActive != th || m.activeTab != tabChat {
		t.Fatalf("thread not opened: active %q tab %v", m.chatActive, m.activeTab)
	}
	if !m.chatStore.HasThread(th) {
		t.Fatal("the room has no thread, so it would not show in the list")
	}
	if m.chatContactLabel(th) != "# standup" {
		t.Fatalf("label = %q", m.chatContactLabel(th))
	}
}

func TestAMemberGetsReadyWhenTheOwnersKeyArrives(t *testing.T) {
	owner := ownerModel(t)
	bob := memberModel(t, "bob")
	wire := admit(t, &owner, "bob-id", "bob", bob)

	if bob.roomEntryFor(testRoomID).r.Ready() {
		t.Fatal("ready before any key")
	}
	bob.receiveRoomControl(ownerThread, wire)
	e := bob.roomEntryFor(testRoomID)
	if !e.r.Ready() || !e.ready {
		t.Fatal("key did not arrive")
	}
	if !strings.Contains(lastBody(bob, roomThread(testRoomID)), "Connected") {
		t.Fatalf("no connected line: %q", lastBody(bob, roomThread(testRoomID)))
	}
}

func TestRoomStateFromANonOwnerIsIgnored(t *testing.T) {
	owner := ownerModel(t)
	bob := memberModel(t, "bob")
	wire := admit(t, &owner, "bob-id", "bob", bob)

	mallory := chat.ContactKey("mallory-contact")
	bob.rememberContact(mallory, "mallory", "mallory-id")
	bob.receiveRoomControl(mallory, wire)
	if bob.roomEntryFor(testRoomID).r.Ready() {
		t.Fatal("a stranger handed this member a room key")
	}
}

func TestRoomStateForARoomWeAreNotInIsIgnored(t *testing.T) {
	owner := ownerModel(t)
	carol := roomTestModel(t, "carol")
	carol.rememberContact(ownerThread, "olive", testOwnerID)
	wire := admit(t, &owner, "carol-id", "carol", carol)
	carol.receiveRoomControl(ownerThread, wire)
	if len(carol.rooms) != 0 || carol.chatStore.HasThread(roomThread(testRoomID)) {
		t.Fatal("unsolicited room state created a room")
	}
}

// twoMembers sets up the owner, alice and bob all holding the key.
func twoMembers(t *testing.T) (owner, alice, bob Model) {
	t.Helper()
	owner = ownerModel(t)
	alice, bob = memberModel(t, "alice"), memberModel(t, "bob")
	admit(t, &owner, "alice-id", "alice", alice)
	wire := admit(t, &owner, "bob-id", "bob", bob)
	alice.receiveRoomControl(ownerThread, wire)
	bob.receiveRoomControl(ownerThread, wire)
	return
}

func sealFrom(t *testing.T, m Model, re, body string) (ws.Event, string) {
	t.Helper()
	e := m.roomEntryFor(testRoomID)
	id := room.NewMessageID()
	sealed, err := room.Seal(e.r.Key, testRoomID, e.r.Gen, m.identity.SigningPrivate, room.Message{ID: id, Re: re, Body: body, TS: 1})
	if err != nil {
		t.Fatal(err)
	}
	return ws.Event{Type: ws.EventTypeRoomMessage, Room: testRoomID, Payload: sealed}, id
}

func TestARoomMessageShowsUnderItsAuthor(t *testing.T) {
	_, alice, bob := twoMembers(t)
	ev, _ := sealFrom(t, alice, "", "hello everyone")
	ev.From, ev.Seq = "alice-id", 1
	bob.handleRoomEvent(ev)

	msgs := bob.chatStore.Messages(roomThread(testRoomID))
	last := msgs[len(msgs)-1]
	if string(last.Body) != "hello everyone" || last.Author != "alice" || last.Outgoing || last.Seq != 1 {
		t.Fatalf("message = %+v", last)
	}
	if bob.unread[roomThread(testRoomID)] != 1 {
		t.Fatalf("unread = %d", bob.unread[roomThread(testRoomID)])
	}
}

func TestARepeatedRoomMessageIsShownOnce(t *testing.T) {
	_, alice, bob := twoMembers(t)
	ev, _ := sealFrom(t, alice, "", "once")
	ev.From, ev.Seq = "alice-id", 1
	bob.handleRoomEvent(ev)
	before := bob.chatStore.Len(roomThread(testRoomID))
	ev.Seq = 2
	bob.handleRoomEvent(ev)
	if bob.chatStore.Len(roomThread(testRoomID)) != before {
		t.Fatal("a replayed message was shown twice")
	}
}

func TestAMessageClaimedByTheWrongSenderIsDropped(t *testing.T) {
	owner, alice, bob := twoMembers(t)
	ev, _ := sealFrom(t, alice, "", "I am the owner")
	ev.From, ev.Seq = testOwnerID, 1 // the relay lies about who sent it
	before := bob.chatStore.Len(roomThread(testRoomID))
	bob.handleRoomEvent(ev)
	if bob.chatStore.Len(roomThread(testRoomID)) != before {
		t.Fatal("a forged sender was accepted")
	}
	if !strings.Contains(bob.commandMsg, "didn't verify") {
		t.Fatalf("no warning: %q", bob.commandMsg)
	}
	_ = owner
}

func TestARoomMessageForAnUnknownRoomIsIgnored(t *testing.T) {
	_, alice, bob := twoMembers(t)
	ev, _ := sealFrom(t, alice, "", "x")
	ev.From, ev.Seq, ev.Room = "alice-id", 1, "other-room"
	bob.handleRoomEvent(ev)
	if bob.chatStore.HasThread(roomThread("other-room")) {
		t.Fatal("a thread was made for a room we are not in")
	}
}

func TestARepliesShowTheMessageTheyAnswer(t *testing.T) {
	_, alice, bob := twoMembers(t)
	first, firstID := sealFrom(t, alice, "", "lunch at noon?")
	first.From, first.Seq = "alice-id", 1
	bob.handleRoomEvent(first)

	reply, _ := sealFrom(t, alice, firstID, "or one o'clock")
	reply.From, reply.Seq = "alice-id", 2
	bob.handleRoomEvent(reply)

	msgs := bob.chatStore.Messages(roomThread(testRoomID))
	last := msgs[len(msgs)-1]
	if !strings.Contains(last.Quote, "alice") || !strings.Contains(last.Quote, "lunch at noon?") {
		t.Fatalf("quote = %q", last.Quote)
	}
}

func TestSendingPutsYourMessageInTheThreadAndTheAckNumbersIt(t *testing.T) {
	_, alice, _ := twoMembers(t)
	alice = withClient(t, alice)
	th := roomThread(testRoomID)
	alice.chatActive = th

	next, cmd := alice.sendRoomText(testRoomID, "my words")
	alice = next.(Model)
	if cmd == nil {
		t.Fatal("nothing was queued to send")
	}
	msgs := alice.chatStore.Messages(th)
	mine := msgs[len(msgs)-1]
	if !mine.Outgoing || string(mine.Body) != "my words" || mine.ID == "" || mine.Author != "alice" || mine.Seq != 0 {
		t.Fatalf("echo = %+v", mine)
	}

	alice.handleRoomEvent(ws.Event{Type: ws.EventTypeRoomAck, Room: testRoomID, Cid: mine.ID, Seq: 9})
	got, _ := alice.chatStore.FindByID(th, mine.ID)
	if got.Seq != 9 {
		t.Fatalf("seq after ack = %d", got.Seq)
	}
}

func TestSendingBeforeTheKeyArrivesIsRefusedAndKeepsTheDraft(t *testing.T) {
	bob := withClient(t, memberModel(t, "bob"))
	bob.chatActive = roomThread(testRoomID)
	bob.chatInput = "too early"
	next, cmd := bob.sendRoomText(testRoomID, "too early")
	bob = next.(Model)
	if cmd != nil || bob.chatInput != "too early" || !strings.Contains(bob.commandMsg, "Waiting") {
		t.Fatalf("cmd=%v input=%q msg=%q", cmd != nil, bob.chatInput, bob.commandMsg)
	}
}

func TestAnOversizedRoomMessageIsRefused(t *testing.T) {
	_, alice, _ := twoMembers(t)
	alice = withClient(t, alice)
	next, cmd := alice.sendRoomText(testRoomID, strings.Repeat("x", room.MaxBodyBytes+1))
	alice = next.(Model)
	if cmd != nil || !strings.Contains(alice.commandMsg, "at most") {
		t.Fatalf("cmd=%v msg=%q", cmd != nil, alice.commandMsg)
	}
}

func TestReplySelectionStepsThroughMessagesAndSendsTheLink(t *testing.T) {
	_, alice, bob := twoMembers(t)
	th := roomThread(testRoomID)
	var ids []string
	for i, body := range []string{"one", "two", "three"} {
		ev, id := sealFrom(t, alice, "", body)
		ev.From, ev.Seq = "alice-id", int64(i+1)
		bob.handleRoomEvent(ev)
		ids = append(ids, id)
	}
	bob = withClient(t, bob)
	bob.chatActive = th

	bob.cycleReply(testRoomID) // newest
	if tgt, ok := bob.replyTarget(bob.roomEntryFor(testRoomID)); !ok || string(tgt.Body) != "three" {
		t.Fatalf("first step = %v %q", ok, tgt.Body)
	}
	bob.cycleReply(testRoomID)
	if tgt, _ := bob.replyTarget(bob.roomEntryFor(testRoomID)); string(tgt.Body) != "two" {
		t.Fatalf("second step = %q", tgt.Body)
	}
	if h := bob.roomHint(testRoomID); !strings.Contains(h, "replying to alice") || !strings.Contains(h, "two") {
		t.Fatalf("hint = %q", h)
	}

	next, _ := bob.sendRoomText(testRoomID, "agreed")
	bob = next.(Model)
	msgs := bob.chatStore.Messages(th)
	sent := msgs[len(msgs)-1]
	if !strings.Contains(sent.Quote, "two") {
		t.Fatalf("sent quote = %q", sent.Quote)
	}
	if bob.roomEntryFor(testRoomID).replyBack != 0 {
		t.Fatal("the reply selection survived sending")
	}
	_ = ids
}

func TestReplySelectionWrapsOffAndEscCancelsIt(t *testing.T) {
	_, alice, bob := twoMembers(t)
	ev, _ := sealFrom(t, alice, "", "only one")
	ev.From, ev.Seq = "alice-id", 1
	bob.handleRoomEvent(ev)

	bob.cycleReply(testRoomID)
	bob.cycleReply(testRoomID) // past the last message: off again
	if bob.roomEntryFor(testRoomID).replyBack != 0 {
		t.Fatal("selection did not wrap off")
	}
	bob.cycleReply(testRoomID)
	if !bob.cancelReply(testRoomID) || bob.roomEntryFor(testRoomID).replyBack != 0 {
		t.Fatal("cancel did not clear the selection")
	}
	if bob.cancelReply(testRoomID) {
		t.Fatal("cancel reported a selection that wasn't there")
	}
}

func TestEndingARoomWipesItAndLeavesTheThreadList(t *testing.T) {
	_, _, bob := twoMembers(t)
	th := roomThread(testRoomID)
	bob.chatActive = th
	bob.handleRoomEvent(ws.Event{Type: ws.EventTypeRoomEnded, Room: testRoomID, Reason: "owner_gone"})

	if bob.roomEntryFor(testRoomID) != nil || bob.chatStore.HasThread(th) {
		t.Fatal("the room survived ending")
	}
	if bob.chatActive != "" {
		t.Fatal("the ended room is still open on screen")
	}
	if !strings.Contains(bob.commandMsg, "went offline") {
		t.Fatalf("msg = %q", bob.commandMsg)
	}
}

func TestAnInvitationBecomesAThreadAndAcceptingOpensIt(t *testing.T) {
	m := roomTestModel(t, "bob")
	m.nodes = []api.NodeRelationship{{RequesterID: testOwnerID, RequesterUsername: "olive", TargetID: "bob-id", TargetUsername: "bob", Status: api.NodeStatusActive}}
	m.handleRoomEvent(ws.Event{Type: ws.EventTypeRoomInvite, Room: testRoomID, Name: "standup", From: testOwnerID})

	th := roomThread(testRoomID)
	e := m.roomEntryFor(testRoomID)
	if e == nil || e.joined {
		t.Fatalf("entry = %+v", e)
	}
	if !strings.Contains(lastBody(m, th), "olive invited you") {
		t.Fatalf("thread says %q", lastBody(m, th))
	}
	if m.unread[th] != 1 {
		t.Fatalf("unread = %d", m.unread[th])
	}

	next, _ := m.handleRoomOp(roomOpMsg{op: "accept", id: testRoomID})
	m = next.(Model)
	if !m.roomEntryFor(testRoomID).joined || m.chatActive != th {
		t.Fatal("accepting did not join and open the room")
	}
}

func TestOnlyTheOwnerHandsOutKeysOnJoin(t *testing.T) {
	m := memberModel(t, "bob")
	if cmd := m.handleRoomEvent(ws.Event{Type: ws.EventTypeRoomJoined, Room: testRoomID, User: "someone"}); cmd != nil {
		t.Fatal("a member tried to distribute a key")
	}
}

func TestAJoinFromSomeoneNotInvitedHereGetsNoKey(t *testing.T) {
	owner := withClient(t, ownerModel(t))
	cmd := owner.handleRoomEvent(ws.Event{Type: ws.EventTypeRoomJoined, Room: testRoomID, User: "stranger-id"})
	if cmd != nil {
		t.Fatal("the owner addressed a key to someone it never looked up")
	}
	if len(owner.roomEntryFor(testRoomID).r.Roster) != 1 {
		t.Fatal("a stranger was added to the roster")
	}
}

func TestAnInvitedPersonJoiningJoinsTheRosterAndTriggersKeyDelivery(t *testing.T) {
	owner := withClient(t, ownerModel(t))
	bob := roomTestModel(t, "bob")
	contact := chat.ContactKey("bob-contact")
	owner.rememberContact(contact, "bob", "bob-id")
	e := owner.roomEntryFor(testRoomID)
	e.peers = map[string]roomPeer{"bob-id": {
		userID: "bob-id", username: "bob", contact: contact,
		signKey: hex.EncodeToString(bob.identity.SigningPublic),
	}}

	cmd := owner.handleRoomEvent(ws.Event{Type: ws.EventTypeRoomJoined, Room: testRoomID, User: "bob-id"})
	if _, in := e.r.Roster["bob-id"]; !in {
		t.Fatal("bob was not added to the roster")
	}
	if cmd == nil {
		t.Fatal("no key delivery was queued")
	}
	if !strings.Contains(lastBody(owner, roomThread(testRoomID)), "bob joined") {
		t.Fatalf("thread says %q", lastBody(owner, roomThread(testRoomID)))
	}
}

func TestLeavingRemovesAMemberFromTheOwnersRoster(t *testing.T) {
	owner, _, _ := twoMembers(t)
	owner = withClient(t, owner)
	e := owner.roomEntryFor(testRoomID)
	e.peers = map[string]roomPeer{"bob-id": {userID: "bob-id", username: "bob", contact: "bob-contact"}}
	owner.rememberContact("bob-contact", "bob", "bob-id")

	owner.handleRoomEvent(ws.Event{Type: ws.EventTypeRoomLeft, Room: testRoomID, User: "bob-id"})
	if _, in := e.r.Roster["bob-id"]; in {
		t.Fatal("bob is still on the roster")
	}
	if !strings.Contains(lastBody(owner, roomThread(testRoomID)), "bob left") {
		t.Fatalf("thread says %q", lastBody(owner, roomThread(testRoomID)))
	}
}

// --- quitting ---

func TestQuittingWithAnOpenRoomAsksFirst(t *testing.T) {
	m := ownerModel(t)
	next, cmd := m.requestQuit()
	m = next.(Model)
	if m.quitting || cmd != nil || !m.quitPrompt {
		t.Fatalf("quitting=%v prompt=%v cmd=%v", m.quitting, m.quitPrompt, cmd != nil)
	}
	if !strings.Contains(m.commandMsg, "standup") || !strings.Contains(m.commandMsg, "ends it for everyone") {
		t.Fatalf("prompt = %q", m.commandMsg)
	}
}

func TestDecliningTheQuitPromptStaysAndKeepsTheRoom(t *testing.T) {
	m := ownerModel(t)
	next, _ := m.requestQuit()
	m = next.(Model)
	for _, key := range []string{"n", "esc"} {
		m.quitPrompt = true
		var k tea.KeyPressMsg
		if key == "esc" {
			k = tea.KeyPressMsg{Code: tea.KeyEsc}
		} else {
			k = keyPressWith(key)
		}
		next, _ = m.updateQuitPrompt(k)
		m = next.(Model)
		if m.quitPrompt || m.quitting || m.roomEntryFor(testRoomID) == nil {
			t.Fatalf("%s: prompt=%v quitting=%v", key, m.quitPrompt, m.quitting)
		}
	}
}

func TestConfirmingTheQuitPromptQuitsAndWipesTheRoom(t *testing.T) {
	m := ownerModel(t)
	th := roomThread(testRoomID)
	next, _ := m.requestQuit()
	m = next.(Model)
	next, cmd := m.updateQuitPrompt(keyPressWith("y"))
	m = next.(Model)
	if !m.quitting || cmd == nil {
		t.Fatalf("quitting=%v cmd=%v", m.quitting, cmd != nil)
	}
	if len(m.rooms) != 0 || m.chatStore.HasThread(th) {
		t.Fatal("the room survived quitting")
	}
}

func TestOtherKeysAtTheQuitPromptJustAskAgain(t *testing.T) {
	m := ownerModel(t)
	next, _ := m.requestQuit()
	m = next.(Model)
	next, _ = m.updateQuitPrompt(keyPressWith("x"))
	m = next.(Model)
	if !m.quitPrompt || m.quitting {
		t.Fatal("a stray key dismissed or confirmed the prompt")
	}
}

func TestQuittingAsAMemberDoesNotAsk(t *testing.T) {
	m := memberModel(t, "bob")
	next, cmd := m.requestQuit()
	m = next.(Model)
	if !m.quitting || m.quitPrompt || cmd == nil {
		t.Fatalf("quitting=%v prompt=%v cmd=%v", m.quitting, m.quitPrompt, cmd != nil)
	}
}

func TestQuittingWithNoRoomsIsUnchanged(t *testing.T) {
	m := roomTestModel(t, "bob")
	next, cmd := m.requestQuit()
	m = next.(Model)
	if !m.quitting || cmd == nil {
		t.Fatalf("quitting=%v cmd=%v", m.quitting, cmd != nil)
	}
}

// --- commands ---

func TestRoomCommandNeedsAnIdentityAndALogin(t *testing.T) {
	m := Model{chatStore: chat.NewStore()}
	if res, _ := m.roomCommand([]string{"new", "x"}); !strings.Contains(res, "identity") {
		t.Errorf("no identity: %q", res)
	}
	m = roomTestModel(t, "bob")
	if res, _ := m.roomCommand([]string{"new", "x"}); !strings.Contains(res, "SYNQ_SERVER_URL") {
		t.Errorf("no server: %q", res)
	}
	m.apiClient = api.NewClient("http://example.invalid")
	if res, _ := m.roomCommand([]string{"new", "x"}); !strings.Contains(res, "Log in") {
		t.Errorf("not logged in: %q", res)
	}
}

func loggedIn(t *testing.T, m Model) Model {
	t.Helper()
	m.apiClient = api.NewClient("http://example.invalid")
	m.accessToken = "tok"
	return m
}

func TestRoomCommandValidatesNamesAndArguments(t *testing.T) {
	m := loggedIn(t, roomTestModel(t, "bob"))
	for _, args := range [][]string{{"new"}, {"new", "bad\x1bname"}, {"invite"}, {"bogus"}} {
		res, cmd := m.roomCommand(args)
		if cmd != nil || res == "" {
			t.Errorf("%v: res=%q cmd=%v", args, res, cmd != nil)
		}
	}
	if res, cmd := m.roomCommand([]string{"new", "standup"}); cmd == nil || !strings.Contains(res, "standup") {
		t.Errorf("good name refused: %q", res)
	}
}

func TestOnlyTheOwnerCanInviteOrEnd(t *testing.T) {
	m := loggedIn(t, memberModel(t, "bob"))
	m.chatActive = roomThread(testRoomID)
	if res, cmd := m.roomCommand([]string{"invite", "carol"}); cmd != nil || !strings.Contains(res, "owner") {
		t.Errorf("member invite: %q", res)
	}
	if res, cmd := m.roomCommand([]string{"end", "confirm"}); cmd != nil || !strings.Contains(res, "owner") {
		t.Errorf("member end: %q", res)
	}
}

func TestEndingNeedsConfirmation(t *testing.T) {
	m := loggedIn(t, ownerModel(t))
	res, cmd := m.roomCommand([]string{"end"})
	if cmd != nil || !strings.Contains(res, "end confirm") {
		t.Fatalf("unconfirmed end: %q cmd=%v", res, cmd != nil)
	}
	if _, cmd := m.roomCommand([]string{"end", "confirm"}); cmd == nil {
		t.Fatal("confirmed end did nothing")
	}
}

func TestTheOwnerCannotJustLeave(t *testing.T) {
	m := loggedIn(t, ownerModel(t))
	res, cmd := m.roomCommand([]string{"leave"})
	if cmd != nil || !strings.Contains(res, "end confirm") {
		t.Fatalf("owner leave: %q", res)
	}
}

func TestInvitingStopsAtTheCap(t *testing.T) {
	m := loggedIn(t, ownerModel(t))
	e := m.roomEntryFor(testRoomID)
	for i := 0; i < room.MaxMembers-1; i++ {
		id := string(rune('a' + i))
		e.r.AddPeer(room.Peer{ID: id, Username: id, SignKey: hex.EncodeToString(m.identity.SigningPublic)})
	}
	res, cmd := m.roomCommand([]string{"invite", "one-too-many"})
	if cmd != nil || !strings.Contains(res, "at most") {
		t.Fatalf("over the cap: %q", res)
	}
}

func TestFindRoomMatchesNamesAndComplainsWhenAmbiguous(t *testing.T) {
	m := roomTestModel(t, "bob")
	m.putRoom(&roomEntry{r: room.New("1", "standup", "o"), joined: true})
	m.putRoom(&roomEntry{r: room.New("2", "stargazers", "o"), joined: true})

	if e, _ := m.findRoom("STANDUP"); e == nil || e.r.ID != "1" {
		t.Error("exact match ignoring case failed")
	}
	if e, _ := m.findRoom("stan"); e == nil || e.r.ID != "1" {
		t.Error("unique prefix failed")
	}
	if e, why := m.findRoom("st"); e != nil || !strings.Contains(why, "More than one") {
		t.Errorf("ambiguous prefix: %v %q", e, why)
	}
	if e, why := m.findRoom("nope"); e != nil || !strings.Contains(why, "No room") {
		t.Errorf("unknown name: %v %q", e, why)
	}
	if e, why := m.findRoom(""); e != nil || !strings.Contains(why, "Open a room") {
		t.Errorf("no name and no open room: %v %q", e, why)
	}
	m.chatActive = roomThread("2")
	if e, _ := m.findRoom(""); e == nil || e.r.ID != "2" {
		t.Error("the open room was not the default")
	}
}

func TestFilesAreRefusedInARoom(t *testing.T) {
	m := loggedIn(t, ownerModel(t))
	m.activeTab = tabChat
	m.fileSession = "sess"
	res, cmd := m.sendFileCmd("/tmp/x")
	if cmd != nil || !strings.Contains(res, "room") {
		t.Fatalf("res=%q cmd=%v", res, cmd != nil)
	}
}

// --- reconciling with the server ---

func TestTheRoomListBringsInMissedInvitationsAndDropsEndedRooms(t *testing.T) {
	m := memberModel(t, "bob")
	m.putRoom(&roomEntry{r: room.New("gone", "oldroom", "o"), joined: true})
	m.chatStore.Append(roomThread("gone"), chat.Message{Body: []byte("x"), System: true})

	next, _ := m.handleRoomList(roomListMsg{rows: []api.RoomSummary{
		{ID: testRoomID, Name: "standup", OwnerID: testOwnerID, Status: "JOINED"},
		{ID: "new", Name: "fresh", OwnerID: "o2", Status: "INVITED"},
	}})
	m = next.(Model)
	if m.roomEntryFor("gone") != nil || m.chatStore.HasThread(roomThread("gone")) {
		t.Error("a room the server no longer has was kept")
	}
	if e := m.roomEntryFor("new"); e == nil || e.joined {
		t.Error("a missed invitation was not picked up")
	}
	if m.roomEntryFor(testRoomID) == nil {
		t.Error("a live room was dropped")
	}
}

func TestLeftoverRoomsFromACrashedLaunchAreDropped(t *testing.T) {
	m := loggedIn(t, roomTestModel(t, "bob"))
	_, cmd := m.handleRoomList(roomListMsg{rows: []api.RoomSummary{
		{ID: "stale", Name: "stale", OwnerID: "o", Status: "JOINED"},
	}})
	if cmd == nil {
		t.Fatal("no cleanup was queued for a room this launch holds no key for")
	}
	if m.roomEntryFor("stale") != nil {
		t.Fatal("a keyless room was adopted")
	}
}

// --- drawing ---

func TestRoomThreadDrawsAuthorsQuotesAndSystemLines(t *testing.T) {
	_, alice, bob := twoMembers(t)
	first, firstID := sealFrom(t, alice, "", "shall we begin")
	first.From, first.Seq = "alice-id", 1
	bob.handleRoomEvent(first)
	reply, _ := sealFrom(t, alice, firstID, "yes please")
	reply.From, reply.Seq = "alice-id", 2
	bob.handleRoomEvent(reply)
	bob = withClient(t, bob)
	bob.chatActive = roomThread(testRoomID)
	bob.activeTab = tabChat

	out := plain(bob.renderChatThread())
	for _, want := range []string{"alice", "shall we begin", "↪", "yes please", "Connected", "end-to-end encrypted", "# standup"} {
		if !strings.Contains(out, want) {
			t.Errorf("thread view is missing %q:\n%s", want, out)
		}
	}
	w, h := bob.innerSize()
	lines := strings.Split(out, "\n")
	if len(lines) > h {
		t.Errorf("thread is %d rows in a pane of %d", len(lines), h)
	}
	for _, l := range lines {
		if len([]rune(l)) > w+4 {
			t.Errorf("a row is wider than the pane (%d > %d): %q", len([]rune(l)), w, l)
		}
	}
}

func TestRoomStatusWaitsForTheKeyAndSaysInvitedBeforeJoining(t *testing.T) {
	m := withClient(t, memberModel(t, "bob"))
	m.chatActive = roomThread(testRoomID)
	if s := plain(strings.Join(m.chatStatusLines(80, ""), "\n")); !strings.Contains(s, "waiting for the owner's key") {
		t.Errorf("status = %q", s)
	}
	m.roomEntryFor(testRoomID).joined = false
	if s := plain(strings.Join(m.chatStatusLines(80, ""), "\n")); !strings.Contains(s, "you're invited") {
		t.Errorf("status = %q", s)
	}
}

func TestRoomsAppearInTheChatListWithAuthorPreviews(t *testing.T) {
	_, alice, bob := twoMembers(t)
	ev, _ := sealFrom(t, alice, "", "see you there")
	ev.From, ev.Seq = "alice-id", 1
	bob.handleRoomEvent(ev)
	bob.activeTab = tabChat

	out := plain(bob.renderChatList())
	if !strings.Contains(out, "# standup") || !strings.Contains(out, "alice: see you there") {
		t.Fatalf("list view:\n%s", out)
	}
}

func TestRoomCommandIsDocumented(t *testing.T) {
	if _, ok := specByName(roomCommandName); !ok {
		t.Fatalf("no command spec for :%s", roomCommandName)
	}
}

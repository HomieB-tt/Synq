package app

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/HomieB-tt/synq/internal/api"
	"github.com/HomieB-tt/synq/internal/chat"
	"github.com/HomieB-tt/synq/internal/ws"
)

func nodeWith(status api.NodeStatus, otherID, otherName string, theyRequested bool) api.NodeRelationship {
	n := api.NodeRelationship{Status: status}
	if theyRequested {
		n.RequesterID, n.RequesterUsername = otherID, otherName
		n.TargetID, n.TargetUsername = "me-id", "me"
	} else {
		n.RequesterID, n.RequesterUsername = "me-id", "me"
		n.TargetID, n.TargetUsername = otherID, otherName
	}
	return n
}

func senderModel(t *testing.T, nodes ...api.NodeRelationship) Model {
	t.Helper()
	m := chatViewModel(t, 100, 30)
	m.username = "me"
	m.apiClient = api.NewClient("https://example.invalid")
	m.accessToken = "tok"
	m.nodes = nodes
	return m
}

func frameFrom(id string) ws.Event {
	return ws.Event{Type: ws.EventTypeMessage, From: id, Payload: []byte("x")}
}

func TestActiveNodeUsernameReadsEitherSideOfARelationship(t *testing.T) {
	m := senderModel(t,
		nodeWith(api.NodeStatusActive, "bob-id", "bob", true),  // they asked me
		nodeWith(api.NodeStatusActive, "cy-id", "cy", false),   // I asked them
		nodeWith(api.NodeStatusPending, "pat-id", "pat", true), // not accepted
		nodeWith(api.NodeStatusBlocked, "ed-id", "ed", false),  // blocked
	)
	for id, want := range map[string]string{"bob-id": "bob", "cy-id": "cy"} {
		if got, ok := m.activeNodeUsername(id); !ok || got != want {
			t.Errorf("activeNodeUsername(%q) = %q, %v; want %q", id, got, ok, want)
		}
	}
	for _, id := range []string{"pat-id", "ed-id", "stranger", "", "me-id"} {
		if got, ok := m.activeNodeUsername(id); ok {
			t.Errorf("activeNodeUsername(%q) = %q, want no match", id, got)
		}
	}
}

// The heart of the fix: a frame from an active node is held and a lookup
// starts; a second frame is held without a second lookup.
func TestFrameFromActiveNodeIsHeldAndLookedUpOnce(t *testing.T) {
	m := senderModel(t, nodeWith(api.NodeStatusActive, "bob-id", "bob", true))

	if cmd := m.deferFromActiveNode(frameFrom("bob-id")); cmd == nil {
		t.Fatal("first frame should start a lookup")
	}
	if cmd := m.deferFromActiveNode(frameFrom("bob-id")); cmd != nil {
		t.Fatal("second frame must not start a second lookup")
	}
	if got := len(m.deferredFrames["bob-id"]); got != 2 {
		t.Fatalf("held %d frames, want 2", got)
	}
}

// Everyone else is still dropped silently, as before.
func TestFramesFromStrangersAreStillDropped(t *testing.T) {
	m := senderModel(t,
		nodeWith(api.NodeStatusPending, "pat-id", "pat", true),
		nodeWith(api.NodeStatusBlocked, "ed-id", "ed", true),
	)
	for _, id := range []string{"pat-id", "ed-id", "nobody", ""} {
		if cmd := m.deferFromActiveNode(frameFrom(id)); cmd != nil {
			t.Errorf("a frame from %q started a lookup", id)
		}
	}
	if len(m.deferredFrames) != 0 {
		t.Errorf("held frames from strangers: %v", m.deferredFrames)
	}

	m = senderModel(t, nodeWith(api.NodeStatusActive, "bob-id", "bob", true))
	m.accessToken = ""
	if cmd := m.deferFromActiveNode(frameFrom("bob-id")); cmd != nil {
		t.Error("with no session there is nobody to ask")
	}
}

func TestHeldFramesAreBounded(t *testing.T) {
	m := senderModel(t, nodeWith(api.NodeStatusActive, "bob-id", "bob", true))
	for i := 0; i < maxDeferredPerSender+10; i++ {
		m.deferFromActiveNode(frameFrom("bob-id"))
	}
	if got := len(m.deferredFrames["bob-id"]); got != maxDeferredPerSender {
		t.Errorf("held %d frames for one sender, cap is %d", got, maxDeferredPerSender)
	}

	var nodes []api.NodeRelationship
	for i := 0; i < maxDeferredSenders+5; i++ {
		nodes = append(nodes, nodeWith(api.NodeStatusActive, fmt.Sprintf("u%d", i), fmt.Sprintf("user%d", i), true))
	}
	m = senderModel(t, nodes...)
	started := 0
	for i := 0; i < maxDeferredSenders+5; i++ {
		if m.deferFromActiveNode(frameFrom(fmt.Sprintf("u%d", i))) != nil {
			started++
		}
	}
	if started != maxDeferredSenders {
		t.Errorf("%d lookups started at once, cap is %d", started, maxDeferredSenders)
	}
}

// A lookup that lands on a different user id than the frames came from
// means the server is serving one user's keys under another's name.
func TestBackgroundLookupMustMatchTheSenderID(t *testing.T) {
	m := senderModel(t, nodeWith(api.NodeStatusActive, "bob-id", "bob", true))
	m.deferFromActiveNode(frameFrom("bob-id"))

	res := lookupResult("bob", randomKeyHex(t, 32), newSigner(t)) // carries UserID "user-1"
	res.background, res.senderID = true, "bob-id"
	next, _ := m.handleSenderResolved(res)
	m = next.(Model)

	if _, ok := m.contactForUserID("bob-id"); ok {
		t.Error("remembered a contact whose lookup returned a different user id")
	}
	if _, ok := m.contactForUserID("user-1"); ok {
		t.Error("remembered the mismatched id")
	}
	if len(m.deferredFrames["bob-id"]) != 0 || m.resolvingSenders["bob-id"] {
		t.Error("held frames or the in-flight mark survived the lookup")
	}
}

func TestFailedBackgroundLookupClearsAndStaysQuiet(t *testing.T) {
	m := senderModel(t, nodeWith(api.NodeStatusActive, "bob-id", "bob", true))
	m.deferFromActiveNode(frameFrom("bob-id"))
	m.commandMsg = ""
	next, _ := m.handleSenderResolved(chatResolveResultMsg{
		username: "bob", err: fmt.Errorf("boom"), background: true, senderID: "bob-id",
	})
	m = next.(Model)
	if m.commandMsg != "" {
		t.Errorf("a lookup nobody asked for shouldn't announce its failure: %q", m.commandMsg)
	}
	if len(m.deferredFrames["bob-id"]) != 0 || m.resolvingSenders["bob-id"] {
		t.Error("state not cleared after a failed lookup")
	}
	// ...and the next frame can try again.
	if cmd := m.deferFromActiveNode(frameFrom("bob-id")); cmd == nil {
		t.Error("a later frame should be able to retry the lookup")
	}
}

// A good background lookup registers the contact but must not take over
// the screen: the user is somewhere else.
func TestBackgroundLookupRegistersContactWithoutOpeningIt(t *testing.T) {
	m := newPinModel(t)
	m.username = "me"
	m.theme = chatViewModel(t, 100, 30).theme
	m.activeTab = tabFeed
	m.nodes = []api.NodeRelationship{nodeWith(api.NodeStatusActive, "user-1", "alice", true)}
	m.apiClient = api.NewClient("https://example.invalid")
	m.accessToken = "tok"
	m.deferFromActiveNode(frameFrom("user-1"))

	res := lookupResult("alice", randomKeyHex(t, 32), newSigner(t)) // UserID "user-1"
	res.background, res.senderID = true, "user-1"
	next, _ := m.handleSenderResolved(res)
	m = next.(Model)

	if _, ok := m.contactForUserID("user-1"); !ok {
		t.Fatal("the sender should now be a known contact")
	}
	if m.activeTab != tabFeed || m.chatActive != "" {
		t.Errorf("a background lookup changed the screen: tab=%v chatActive=%q", m.activeTab, m.chatActive)
	}
}

func TestBackgroundAndTypedLookupsShareTheSameChecks(t *testing.T) {
	// An unsigned box key is refused for :chat, and equally in the background.
	m := newPinModel(t)
	s := newSigner(t)
	res := lookupResult("alice", randomKeyHex(t, 32), s)
	res.keys.BoxKeySig = ""
	_, _, fail := m.acceptResolved(res)
	if !strings.Contains(fail, "isn't signed yet") {
		t.Fatalf("fail = %q", fail)
	}
}

// --- unread ---

func TestMessageInAnotherThreadIsCountedAndAnnounced(t *testing.T) {
	m := fill(chatViewModel(t, 100, 30), 0, nil) // thread "alice" open on... no tab yet
	m.activeTab = tabFeed
	m.chatContacts = map[chat.ContactKey]contactInfo{alice: {Username: "alice", UserID: "a"}}

	m.noteUnread(alice, true)
	m.noteUnread(alice, true)

	if m.unreadIn(alice) != 2 || m.unreadTotal() != 2 {
		t.Fatalf("unread = %d (total %d), want 2", m.unreadIn(alice), m.unreadTotal())
	}
	if !strings.Contains(m.commandMsg, "alice") || !strings.Contains(m.commandMsg, "press 3") {
		t.Errorf("notice = %q", m.commandMsg)
	}
}

func TestMessageInTheThreadOnScreenIsNotUnread(t *testing.T) {
	m := fill(chatViewModel(t, 100, 30), 0, nil)
	m.activeTab = tabChat // alice is open
	m.commandMsg = ""
	m.noteUnread(alice, true)
	if m.unreadTotal() != 0 || m.commandMsg != "" {
		t.Errorf("unread=%d notice=%q for a message you are looking at", m.unreadTotal(), m.commandMsg)
	}
}

// Whatever brings a thread on screen - the list, :chat, anything - clears
// it, because the clearing is in Update, not in each of those paths.
func TestOpeningTheThreadClearsUnreadViaUpdate(t *testing.T) {
	m := fill(chatViewModel(t, 100, 30), 0, nil)
	m.activeTab = tabFeed
	m.noteUnread(alice, false)
	if m.unreadTotal() != 1 {
		t.Fatal("setup")
	}
	m.activeTab = tabChat // as :chat or Enter in the list would
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if got := next.(Model).unreadTotal(); got != 0 {
		t.Fatalf("unread = %d after the thread came on screen", got)
	}
}

func TestUnreadShowsOnTabBarAndThreadList(t *testing.T) {
	m := fill(chatViewModel(t, 100, 30), 1, func(int) string { return "hello" })
	m.activeTab = tabFeed
	m.noteUnread(alice, false)
	m.noteUnread(alice, false)

	if bar := plain(m.renderTabBar()); !strings.Contains(bar, "Chat (2 new)") {
		t.Errorf("tab bar = %q", bar)
	}
	if row := plain(m.chatListRow(alice, false, 80)); !strings.Contains(row, "2 new") {
		t.Errorf("thread row = %q", row)
	}

	m.unread = nil
	if bar := plain(m.renderTabBar()); strings.Contains(bar, "new") {
		t.Errorf("tab bar shows unread with none: %q", bar)
	}
}

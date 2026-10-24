package app

import (
	"errors"
	"strings"
	"testing"

	"github.com/HomieB-tt/synq/internal/api"
	"github.com/HomieB-tt/synq/internal/ws"
)

func presenceModel() *Model {
	m := nodeModel()
	r := rel("me", "bob", api.NodeStatusActive)
	r.TargetID = "u-bob"
	m.nodes = []api.NodeRelationship{r}
	m.nodesLoaded = true
	m.applyPresence(api.Presence{Share: true})
	return m
}

func TestPresenceEventsUpdateTheSetAndAnnounceArrivals(t *testing.T) {
	m := presenceModel()
	m.handlePresenceEvent(ws.Event{Type: ws.EventTypePresence, User: "u-bob", Online: true})
	if !m.online["u-bob"] || !strings.Contains(m.commandMsg, "bob is online") {
		t.Fatalf("online=%v msg=%q", m.online, m.commandMsg)
	}
	m.commandMsg = ""
	m.handlePresenceEvent(ws.Event{Type: ws.EventTypePresence, User: "u-bob", Online: true})
	if m.commandMsg != "" {
		t.Errorf("announced a repeat: %q", m.commandMsg)
	}
	m.handlePresenceEvent(ws.Event{Type: ws.EventTypePresence, User: "u-bob", Online: false})
	if m.online["u-bob"] || m.commandMsg != "" {
		t.Errorf("offline: online=%v msg=%q", m.online, m.commandMsg)
	}
}

func TestPresenceEventsFromStrangersOrWhileHiddenAreIgnored(t *testing.T) {
	m := presenceModel()
	m.handlePresenceEvent(ws.Event{Type: ws.EventTypePresence, User: "u-stranger", Online: true})
	if len(m.online) != 0 || m.commandMsg != "" {
		t.Errorf("stranger shown: %v %q", m.online, m.commandMsg)
	}
	m.applyPresence(api.Presence{Share: false, Online: []string{"u-bob"}})
	m.handlePresenceEvent(ws.Event{Type: ws.EventTypePresence, User: "u-bob", Online: true})
	if len(m.online) != 0 || m.presenceVisible() {
		t.Errorf("shown while hidden: %v", m.online)
	}
}

func TestNodesRowShowsDotsOnlyForActiveNodesWhenVisible(t *testing.T) {
	m := presenceModel()
	m.online["u-bob"] = true
	row := nodeEntry{nodeRow{Username: "bob", UserID: "u-bob"}, nodeActive}
	if got := plain(m.nodeRowLine(row, false, 40)); !strings.Contains(got, "● bob") || len([]rune(got)) != 40 {
		t.Errorf("online row = %q", got)
	}
	m.online = map[string]bool{}
	if got := plain(m.nodeRowLine(row, false, 40)); !strings.Contains(got, "○ bob") {
		t.Errorf("offline row = %q", got)
	}
	m.applyPresence(api.Presence{Share: false})
	if got := plain(m.nodeRowLine(row, false, 40)); strings.ContainsAny(got, "●○") {
		t.Errorf("hidden row has a dot: %q", got)
	}
	pending := nodeEntry{nodeRow{Username: "carl", UserID: "u-carl"}, nodeOutgoing}
	m.applyPresence(api.Presence{Share: true})
	if got := plain(m.nodeRowLine(pending, false, 40)); strings.ContainsAny(got, "●○") {
		t.Errorf("pending row has a dot: %q", got)
	}
}

func TestNodesLoadedAppliesPresenceAndFailureKeepsTheList(t *testing.T) {
	m := *nodeModel()
	next, _ := m.handleNodesLoaded(nodesLoadedMsg{
		nodes:    []api.NodeRelationship{rel("me", "bob", api.NodeStatusActive)},
		presence: &api.Presence{Share: true, Online: []string{"u1"}},
	})
	got := next.(Model)
	if !got.presenceVisible() || !got.online["u1"] {
		t.Errorf("presence not applied: %+v", got.online)
	}
	next, _ = got.handleNodesLoaded(nodesLoadedMsg{nodes: got.nodes}) // presence fetch failed
	if again := next.(Model); !again.online["u1"] || len(again.nodes) != 1 {
		t.Errorf("a missing presence answer wiped state: %v", again.online)
	}
}

func TestPresenceCommand(t *testing.T) {
	m := presenceModel()
	if msg, cmd := m.presenceCommand(nil); cmd != nil || !strings.Contains(msg, "visible") {
		t.Errorf("status: %q", msg)
	}
	if msg, cmd := m.presenceCommand([]string{"maybe"}); cmd != nil || !strings.Contains(msg, "Usage") {
		t.Errorf("bad arg: %q", msg)
	}
	if _, cmd := m.presenceCommand([]string{"OFF"}); cmd == nil {
		t.Error("off sent nothing")
	}
	bare := &Model{}
	if msg, cmd := bare.presenceCommand([]string{"off"}); cmd != nil || msg == "" {
		t.Errorf("no server: %q", msg)
	}
}

func TestPresenceSetResult(t *testing.T) {
	m := *presenceModel()
	m.online["u-bob"] = true
	next, cmd := m.handlePresenceSet(presenceSetMsg{share: false})
	got := next.(Model)
	if cmd != nil || got.presenceVisible() || len(got.online) != 0 {
		t.Errorf("off: visible=%v online=%v", got.presenceVisible(), got.online)
	}
	next, cmd = got.handlePresenceSet(presenceSetMsg{share: true})
	if got = next.(Model); cmd == nil || !got.presenceVisible() {
		t.Errorf("on: cmd=%v visible=%v", cmd != nil, got.presenceVisible())
	}
	next, _ = got.handlePresenceSet(presenceSetMsg{share: false, err: errors.New("boom")})
	if got = next.(Model); !got.presenceVisible() || !strings.Contains(got.commandMsg, "boom") {
		t.Errorf("failure changed state: %q", got.commandMsg)
	}
}

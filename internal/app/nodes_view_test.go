package app

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/HomieB-tt/synq/internal/api"
	"github.com/HomieB-tt/synq/internal/chat"
	"github.com/HomieB-tt/synq/internal/ui/styles"
)

// nodesViewModel is a logged-in Model on the Nodes tab with one of each
// kind of relationship: bob asked you, amy is a node, cat hasn't
// answered, and dan is blocked (and must never appear).
func nodesViewModel(t *testing.T, w, h int) Model {
	t.Helper()
	m := *nodeModel()
	m.theme = styles.Default()
	m.chatStore = chat.NewStore()
	m.width, m.height = w, h
	m.identity = testIdentityForRender(t)
	m.activeTab = tabNodes
	m.nodesLoaded = true
	m.nodes = []api.NodeRelationship{
		rel("bob", "me", api.NodeStatusPending),
		rel("me", "amy", api.NodeStatusActive),
		rel("me", "cat", api.NodeStatusPending),
		rel("me", "dan", api.NodeStatusBlocked),
	}
	return m
}

func TestNodeEntriesAreInDisplayOrder(t *testing.T) {
	m := nodesViewModel(t, 80, 24)
	var got []string
	for _, e := range groupNodes(m.nodes, m.username).entries() {
		got = append(got, fmt.Sprintf("%s:%d", e.Username, e.kind))
	}
	// requests for you, then nodes, then waiting-on-them
	if want := "bob:0 amy:1 cat:2"; strings.Join(got, " ") != want {
		t.Errorf("entries = %q, want %q", strings.Join(got, " "), want)
	}
}

func TestNodeRowsCarryGitHubBadges(t *testing.T) {
	rels := []api.NodeRelationship{
		{RequesterUsername: "me", TargetUsername: "amy", TargetID: "id-amy", Status: api.NodeStatusActive,
			TargetGitHubVerified: true, TargetGitHubHandle: "amy-gh"},
		{RequesterUsername: "bob", RequesterID: "id-bob", TargetUsername: "me", Status: api.NodeStatusActive,
			RequesterGitHubVerified: true, RequesterGitHubHandle: "bob-gh"},
	}
	for _, r := range groupNodes(rels, "me").Active {
		if !r.Verified || !strings.HasSuffix(r.Handle, "-gh") {
			t.Errorf("%s lost its GitHub badge: %+v", r.Username, r)
		}
	}
}

func TestNodeKeysMoveAndClamp(t *testing.T) {
	m := nodesViewModel(t, 80, 24)
	for i := 0; i < 10; i++ {
		m.updateNodesKey("down")
	}
	if m.nodesSel != 2 {
		t.Errorf("down past the end: sel = %d, want 2", m.nodesSel)
	}
	m.updateNodesKey("g")
	m.updateNodesKey("up")
	if m.nodesSel != 0 {
		t.Errorf("up past the top: sel = %d, want 0", m.nodesSel)
	}
	m.updateNodesKey("G")
	if m.nodesSel != 2 {
		t.Errorf("G: sel = %d, want 2", m.nodesSel)
	}
}

func TestNodeKeysAreGlobalWhenThereIsNothingToSelect(t *testing.T) {
	m := nodesViewModel(t, 80, 24)

	m.nodesLoaded = false
	if handled, _ := m.updateNodesKey("down"); handled {
		t.Error("keys must pass through before the list has loaded")
	}
	m.nodesLoaded = true
	m.nodes = nil
	if handled, _ := m.updateNodesKey("down"); handled {
		t.Error("keys must pass through with an empty list")
	}
	m = nodesViewModel(t, 80, 24)
	m.accessToken = ""
	if handled, _ := m.updateNodesKey("down"); handled {
		t.Error("keys must pass through when logged out")
	}
	// Unowned keys always fall through: tab switching, quit, refresh.
	m = nodesViewModel(t, 80, 24)
	for _, k := range []string{"q", "1", "2", "r", "tab", "?"} {
		if handled, _ := m.updateNodesKey(k); handled {
			t.Errorf("%q must stay a global key", k)
		}
	}
}

func TestNodeAcceptShortcut(t *testing.T) {
	m := nodesViewModel(t, 80, 24) // sel 0 = bob, an incoming request
	handled, cmd := m.updateNodesKey("a")
	if !handled || cmd == nil || !strings.Contains(m.commandMsg, "Accepting bob") {
		t.Errorf("a on a request: handled=%v cmd=%v msg=%q", handled, cmd != nil, m.commandMsg)
	}

	// Enter does the same on a request...
	m = nodesViewModel(t, 80, 24)
	if _, cmd := m.updateNodesKey("enter"); cmd == nil {
		t.Error("enter on a request should accept it")
	}

	// ...but a on a row with nothing to accept does nothing, loudly.
	m = nodesViewModel(t, 80, 24)
	m.nodesSel = 1 // amy, already a node
	if _, cmd := m.updateNodesKey("a"); cmd != nil || !strings.Contains(m.commandMsg, "Nothing to accept") {
		t.Errorf("a on a node: cmd=%v msg=%q", cmd != nil, m.commandMsg)
	}
}

func TestNodeEnterStartsAChatWithAnActiveNode(t *testing.T) {
	m := nodesViewModel(t, 80, 24)
	m.nodesSel = 1 // amy
	handled, cmd := m.updateNodesKey("enter")
	if !handled || cmd == nil || !strings.Contains(m.commandMsg, "Looking up amy") {
		t.Errorf("enter on a node: handled=%v cmd=%v msg=%q", handled, cmd != nil, m.commandMsg)
	}

	m = nodesViewModel(t, 80, 24)
	m.nodesSel = 2 // cat, hasn't answered
	if _, cmd := m.updateNodesKey("enter"); cmd != nil || !strings.Contains(m.commandMsg, "Waiting for cat") {
		t.Errorf("enter on an unanswered request: cmd=%v msg=%q", cmd != nil, m.commandMsg)
	}
}

// Blocking can't be undone and the other person isn't told, so the key
// only explains how - it never fires the block itself.
func TestNodeBlockKeyOnlyExplains(t *testing.T) {
	m := nodesViewModel(t, 80, 24)
	handled, cmd := m.updateNodesKey("b")
	if !handled || cmd != nil {
		t.Fatalf("b must not start a block (handled=%v cmd=%v)", handled, cmd != nil)
	}
	if !strings.Contains(m.commandMsg, ":block bob confirm") {
		t.Errorf("b should say how to block: %q", m.commandMsg)
	}
}

func TestNodeListFitsPaneAndNeverShowsBlocked(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {120, 40}, {50, 14}, {40, 10}} {
		for _, many := range []bool{false, true} {
			m := nodesViewModel(t, size[0], size[1])
			if many {
				for i := 0; i < 40; i++ {
					m.nodes = append(m.nodes, rel("me", fmt.Sprintf("friend%02d", i), api.NodeStatusActive))
				}
				m.nodesSel = 30
			}
			w, h := m.innerSize()
			out := m.renderNodes()
			lines := strings.Split(plain(out), "\n")
			if len(lines) > h {
				t.Errorf("%dx%d many=%v: %d rows, pane holds %d", size[0], size[1], many, len(lines), h)
			}
			for _, l := range lines {
				if got := lipgloss.Width(l); got > w {
					t.Errorf("%dx%d many=%v: row is %d wide, pane holds %d: %q", size[0], size[1], many, got, w, l)
					break
				}
			}
			if strings.Contains(plain(out), "dan") {
				t.Error("a blocked user is listed")
			}
		}
	}
}

// With more rows than fit, the window follows the selection.
func TestNodeListScrollsToTheSelection(t *testing.T) {
	m := nodesViewModel(t, 80, 20)
	m.nodes = nil
	for i := 0; i < 40; i++ {
		m.nodes = append(m.nodes, rel("me", fmt.Sprintf("friend%02d", i), api.NodeStatusActive))
	}
	for _, sel := range []int{0, 20, 39} {
		m.nodesSel = sel
		want := fmt.Sprintf("friend%02d", sel) // active rows sort by name
		if out := plain(m.renderNodes()); !strings.Contains(out, want) {
			t.Errorf("sel=%d: the selected row %s is off-screen:\n%s", sel, want, out)
		}
	}
}

func TestNodeRowsAreExactlyPaneWide(t *testing.T) {
	for _, w := range []int{30, 60, 100} {
		for _, sel := range []bool{false, true} {
			for _, e := range []nodeEntry{
				{nodeRow{Username: "amy", Verified: true, Handle: "amy-gh"}, nodeActive},
				{nodeRow{Username: "a-very-long-username-indeed-yes", Verified: true, Handle: "handle"}, nodeIncoming},
				{nodeRow{Username: "cat"}, nodeOutgoing},
			} {
				m := nodesViewModel(t, w+6, 24)
				if got := lipgloss.Width(plain(m.nodeRowLine(e, sel, w))); got != w {
					t.Errorf("w=%d sel=%v %s: row is %d wide", w, sel, e.Username, got)
				}
			}
		}
	}
}

func TestNodeUsernamesCannotInjectEscapes(t *testing.T) {
	m := nodesViewModel(t, 80, 24)
	m.nodes = []api.NodeRelationship{rel("me", "evil\x1b[2J\x1b]0;pwned\x07", api.NodeStatusActive)}
	out := m.renderNodes()
	for _, seq := range []string{"\x1b[2J", "\x1b]0;", "\x07"} {
		if strings.Contains(out, seq) {
			t.Errorf("output contains the username's %q", seq)
		}
	}
}

func TestNodePageKeysJumpAFullPage(t *testing.T) {
	m := nodesViewModel(t, 80, 20)
	m.nodes = nil
	for i := 0; i < 60; i++ {
		m.nodes = append(m.nodes, rel("me", fmt.Sprintf("friend%02d", i), api.NodeStatusActive))
	}
	page := m.listPage()
	if page < 2 {
		t.Fatalf("page = %d, too small to test", page)
	}
	m.updateNodesKey("pgdown")
	if m.nodesSel != page {
		t.Errorf("pgdown: sel = %d, want %d", m.nodesSel, page)
	}
	m.updateNodesKey("pgup")
	m.updateNodesKey("pgup") // past the top: clamped
	if m.nodesSel != 0 {
		t.Errorf("pgup past the top: sel = %d", m.nodesSel)
	}
	for i := 0; i < 20; i++ {
		m.updateNodesKey("pgdown")
	}
	if m.nodesSel != 59 {
		t.Errorf("pgdown past the end: sel = %d, want 59", m.nodesSel)
	}
}

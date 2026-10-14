package app

import (
	"errors"
	"strings"
	"testing"

	"github.com/HomieB-tt/synq/internal/api"
	"github.com/HomieB-tt/synq/internal/crypto"
)

func testIdentityForRender(t *testing.T) *crypto.Identity {
	t.Helper()
	id, err := crypto.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func rel(requester, target string, status api.NodeStatus) api.NodeRelationship {
	return api.NodeRelationship{
		RequesterID: "id-" + requester, RequesterUsername: requester,
		TargetID: "id-" + target, TargetUsername: target,
		Status: status,
	}
}

func names(rows []nodeRow) string {
	var out []string
	for _, r := range rows {
		out = append(out, r.Username)
	}
	return strings.Join(out, ",")
}

func TestGroupNodesSortsByStatusAndDirection(t *testing.T) {
	rels := []api.NodeRelationship{
		rel("me", "zed", api.NodeStatusActive),  // I asked, accepted
		rel("amy", "me", api.NodeStatusActive),  // they asked, accepted
		rel("bob", "me", api.NodeStatusPending), // incoming
		rel("me", "cat", api.NodeStatusPending), // outgoing
		rel("me", "dan", api.NodeStatusBlocked), // hidden
		rel("eve", "me", api.NodeStatusBlocked), // hidden
		rel("x", "y", api.NodeStatusActive),     // not mine
	}
	got := groupNodes(rels, "me")
	if names(got.Active) != "amy,zed" {
		t.Errorf("Active = %q, want amy,zed", names(got.Active))
	}
	if names(got.Incoming) != "bob" || got.Incoming[0].UserID != "id-bob" {
		t.Errorf("Incoming = %+v", got.Incoming)
	}
	if names(got.Outgoing) != "cat" {
		t.Errorf("Outgoing = %q", names(got.Outgoing))
	}
}

func TestGroupNodesWithoutAUsernameShowsNothing(t *testing.T) {
	got := groupNodes([]api.NodeRelationship{rel("a", "b", api.NodeStatusActive)}, "")
	if len(got.Active)+len(got.Incoming)+len(got.Outgoing) != 0 {
		t.Errorf("listed relationships for an unknown user: %+v", got)
	}
}

func TestGroupNodesUsernamesAreCaseSensitive(t *testing.T) {
	got := groupNodes([]api.NodeRelationship{rel("Me", "bob", api.NodeStatusActive)}, "me")
	if len(got.Active) != 0 {
		t.Error("matched a different account by folding case")
	}
}

func nodeModel() *Model {
	return &Model{apiClient: api.NewClient("https://example.invalid"), accessToken: "tok", username: "me"}
}

func TestNodeCommandsNeedAServerAndALogin(t *testing.T) {
	m := &Model{}
	if msg, cmd := m.requestNode("bob"); cmd != nil || !strings.Contains(msg, "SYNQ_SERVER_URL") {
		t.Errorf("no server: %q", msg)
	}
	m = &Model{apiClient: api.NewClient("https://example.invalid")}
	if msg, cmd := m.requestNode("bob"); cmd != nil || !strings.Contains(msg, "Log in") {
		t.Errorf("no login: %q", msg)
	}
}

func TestRequestNodeRefusesYourselfAndExistingNodes(t *testing.T) {
	m := nodeModel()
	m.nodes = []api.NodeRelationship{rel("me", "bob", api.NodeStatusActive)}
	for _, name := range []string{"me", "bob"} {
		if msg, cmd := m.requestNode(name); cmd != nil {
			t.Errorf("requestNode(%q) sent a request: %q", name, msg)
		}
	}
	if _, cmd := m.requestNode("carol"); cmd == nil {
		t.Error("requestNode refused a fresh username")
	}
	if msg, cmd := m.requestNode(""); cmd != nil || !strings.Contains(msg, "Usage") {
		t.Errorf("empty username: %q", msg)
	}
}

func TestAcceptNodeNeedsAnIncomingRequest(t *testing.T) {
	m := nodeModel()
	m.nodes = []api.NodeRelationship{
		rel("bob", "me", api.NodeStatusPending),
		rel("me", "cat", api.NodeStatusPending),
	}
	if _, cmd := m.acceptNode("bob"); cmd == nil {
		t.Error("couldn't accept an incoming request")
	}
	// Accepting your own outgoing request, or a stranger, is nothing.
	for _, name := range []string{"cat", "stranger"} {
		if msg, cmd := m.acceptNode(name); cmd != nil || !strings.Contains(msg, "Nothing to accept") {
			t.Errorf("acceptNode(%q) = %q", name, msg)
		}
	}
}

func TestAcceptCommandPrefersAKeyChangeThenFallsBackToNodeRequests(t *testing.T) {
	m := nodeModel()
	m.nodes = []api.NodeRelationship{rel("bob", "me", api.NodeStatusPending)}
	_, _, cmd := m.runCommand("accept bob")
	if cmd == nil {
		t.Error(":accept bob did not accept bob's node request")
	}
	if res, _, _ := m.runCommand("accept nobody"); !strings.Contains(res, "Nothing to accept") {
		t.Errorf(":accept nobody = %q", res)
	}
}

func TestBlockNeedsTheConfirmWord(t *testing.T) {
	m := nodeModel()
	if msg, cmd := m.blockNode([]string{"bob"}); cmd != nil || !strings.Contains(msg, "confirm") {
		t.Errorf("block without confirm: %q", msg)
	}
	if _, cmd := m.blockNode([]string{"bob", "confirm"}); cmd == nil {
		t.Error("block with confirm did nothing")
	}
	if msg, cmd := m.blockNode([]string{"me", "confirm"}); cmd != nil {
		t.Errorf("blocked yourself: %q", msg)
	}
}

func TestNodesLoadedStoresTheListAndIgnoresLateResults(t *testing.T) {
	m := *nodeModel()
	next, _ := m.handleNodesLoaded(nodesLoadedMsg{nodes: []api.NodeRelationship{rel("me", "bob", api.NodeStatusActive)}})
	got := next.(Model)
	if !got.nodesLoaded || len(got.nodes) != 1 {
		t.Errorf("list not stored: %+v", got)
	}

	// Logged out while the request was out: don't resurrect the list.
	m.accessToken = ""
	next, _ = m.handleNodesLoaded(nodesLoadedMsg{nodes: []api.NodeRelationship{rel("me", "bob", api.NodeStatusActive)}})
	if got := next.(Model); got.nodesLoaded {
		t.Error("stored a list after logout")
	}
}

func TestNodesLoadFailureKeepsTheOldListAndSaysSo(t *testing.T) {
	m := *nodeModel()
	m.nodes = []api.NodeRelationship{rel("me", "bob", api.NodeStatusActive)}
	m.nodesLoaded = true
	next, _ := m.handleNodesLoaded(nodesLoadedMsg{err: errors.New("boom")})
	got := next.(Model)
	if len(got.nodes) != 1 || got.nodesErr != "boom" {
		t.Errorf("after failure: nodes=%d err=%q", len(got.nodes), got.nodesErr)
	}
}

func TestNodeActionReportsAndReloads(t *testing.T) {
	m := *nodeModel()
	next, cmd := m.handleNodeAction(nodeActionMsg{verb: "request", username: "bob"})
	if !strings.Contains(next.(Model).commandMsg, "Request sent to bob") || cmd == nil {
		t.Errorf("success: %q, cmd %v", next.(Model).commandMsg, cmd)
	}
	next, cmd = m.handleNodeAction(nodeActionMsg{verb: "request", username: "ghost", err: &api.APIError{StatusCode: 404, Message: "x"}})
	if !strings.Contains(next.(Model).commandMsg, "No such user: ghost") || cmd != nil {
		t.Errorf("404: %q", next.(Model).commandMsg)
	}
}

func TestRenderNodesShowsEachSectionAndNeverBlocked(t *testing.T) {
	m := *nodeModel()
	m.identity = testIdentityForRender(t)
	m.nodesLoaded = true
	m.nodes = []api.NodeRelationship{
		rel("bob", "me", api.NodeStatusPending),
		rel("me", "amy", api.NodeStatusActive),
		rel("me", "cat", api.NodeStatusPending),
		rel("me", "dan", api.NodeStatusBlocked),
	}
	out := m.renderNodes()
	for _, want := range []string{"Requests for you (1)", "bob", "Nodes (1)", "amy", "Waiting on them (1)", "cat"} {
		if !strings.Contains(out, want) {
			t.Errorf("render is missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "dan") {
		t.Error("a blocked user is listed")
	}
}

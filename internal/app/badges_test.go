package app

import (
	"strings"
	"testing"

	"github.com/HomieB-tt/synq/internal/api"
	"github.com/HomieB-tt/synq/internal/crypto"
)

func nodesModel(t *testing.T) Model {
	t.Helper()
	id, err := crypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	m := chatViewModel(t, 100, 30)
	m.identity, m.username, m.nodesLoaded = id, "me", true
	m.nodes = []api.NodeRelationship{
		{RequesterUsername: "amy", TargetUsername: "me", Status: api.NodeStatusPending},
		{RequesterUsername: "bob", TargetUsername: "me", Status: api.NodeStatusPending},
		{RequesterUsername: "me", TargetUsername: "cat", Status: api.NodeStatusPending}, // waiting on them
		{RequesterUsername: "dan", TargetUsername: "me", Status: api.NodeStatusActive},
	}
	return m
}

func TestNodesTabBadgeCountsRequestsWaitingOnYou(t *testing.T) {
	m := nodesModel(t)
	if n := m.tabBadge(tabNodes); n != 2 {
		t.Fatalf("badge = %d, want 2 (requests to you only)", n)
	}
	if bar := plain(m.renderTabBar()); !strings.Contains(bar, "Nodes (2 pending)") {
		t.Errorf("tab bar = %q, want Nodes (2 pending)", bar)
	}
	m.nodesLoaded = false
	if n := m.tabBadge(tabNodes); n != 0 {
		t.Errorf("before the list loads there is nothing to count, got %d", n)
	}
	m.nodesLoaded, m.identity = true, nil
	if n := m.tabBadge(tabNodes); n != 0 {
		t.Errorf("a guest has no requests, got %d", n)
	}
}

func TestBadgesNeverShowZero(t *testing.T) {
	m := chatViewModel(t, 100, 30)
	bar := plain(m.renderTabBar())
	if strings.Contains(bar, "(") {
		t.Errorf("no badges expected on a quiet model: %q", bar)
	}
}

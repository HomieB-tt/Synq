package app

import (
	"errors"
	"strings"
	"testing"

	"github.com/HomieB-tt/synq/internal/api"
	"github.com/HomieB-tt/synq/internal/crypto"
	"github.com/HomieB-tt/synq/internal/db"
)

func deleteModel(t *testing.T) *Model {
	m := nodeModel() // logged in as "me" against a dummy server
	id, err := crypto.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	m.identity = id
	return m
}

func TestDeleteAccountNeedsEverythingAndAnExactConfirmation(t *testing.T) {
	m := deleteModel(t)
	if msg, cmd := m.deleteAccountCommand(nil); cmd != nil || !strings.Contains(msg, "for good") || !strings.Contains(msg, ":delete-account me confirm") {
		t.Errorf("bare: %q", msg)
	}
	if msg, cmd := m.deleteAccountCommand([]string{"bob", "confirm"}); cmd != nil || !strings.Contains(msg, "your own username") {
		t.Errorf("wrong name: %q", msg)
	}
	if msg, cmd := m.deleteAccountCommand([]string{"me"}); cmd != nil || !strings.Contains(msg, "confirm") {
		t.Errorf("no confirm: %q", msg)
	}
	if msg, cmd := m.deleteAccountCommand([]string{"me", "confirm"}); cmd == nil || !strings.Contains(msg, "Deleting") {
		t.Errorf("confirmed: %q", msg)
	}

	loggedOut := *m
	loggedOut.accessToken = ""
	if msg, cmd := loggedOut.deleteAccountCommand([]string{"me", "confirm"}); cmd != nil || !strings.Contains(msg, "Log in") {
		t.Errorf("logged out: %q", msg)
	}
	unregistered := *m
	unregistered.username = ""
	if msg, cmd := unregistered.deleteAccountCommand([]string{"me", "confirm"}); cmd != nil || !strings.Contains(msg, "haven't registered") {
		t.Errorf("unregistered: %q", msg)
	}
	noServer := *m
	noServer.apiClient = nil
	if msg, cmd := noServer.deleteAccountCommand(nil); cmd != nil || !strings.Contains(msg, "no synq-server") {
		t.Errorf("no server: %q", msg)
	}
}

func TestDeletedAccountSignsOutAndForgetsTheName(t *testing.T) {
	m := *deleteModel(t)
	m.store = newTestKeyStore(t)
	if err := m.store.SavePreference(db.PrefUsername, "me"); err != nil {
		t.Fatal(err)
	}
	m.nodes = []api.NodeRelationship{rel("me", "bob", api.NodeStatusActive)}
	m.nodesLoaded = true

	next, _ := m.handleAccountDeleted(accountDeletedMsg{username: "me"})
	got := next.(Model)
	if got.username != "" || got.accessToken != "" || got.nodes != nil || got.nodesLoaded {
		t.Errorf("still signed in: user=%q token=%q nodes=%d", got.username, got.accessToken, len(got.nodes))
	}
	if name, _ := got.store.LoadPreference(db.PrefUsername); name != "" {
		t.Errorf("device still remembers %q", name)
	}
	if !strings.Contains(got.commandMsg, "deleted") || !strings.Contains(got.commandMsg, ":register") {
		t.Errorf("message = %q", got.commandMsg)
	}
}

func TestFailedDeleteChangesNothing(t *testing.T) {
	m := *deleteModel(t)
	next, _ := m.handleAccountDeleted(accountDeletedMsg{username: "me", err: errors.New("boom")})
	got := next.(Model)
	if got.username != "me" || got.accessToken != "tok" || !strings.Contains(got.commandMsg, "boom") {
		t.Errorf("after failure: user=%q token=%q msg=%q", got.username, got.accessToken, got.commandMsg)
	}
}

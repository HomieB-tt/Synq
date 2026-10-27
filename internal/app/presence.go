package app

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/HomieB-tt/synq/internal/api"
	"github.com/HomieB-tt/synq/internal/ws"
)

// This file is the online/offline indicator on the Nodes tab. The
// server tells you which of your active nodes are online, and pushes a
// node_presence frame when one comes or goes. Both sides have to share:
// :presence off hides you from your nodes and hides them from you, so
// the feature cannot be used to watch someone while staying invisible.

// presenceVisible reports whether the Nodes tab should draw dots: the
// server has answered, and you have not hidden yourself.
func (m Model) presenceVisible() bool {
	return m.presenceKnown && m.presenceShare
}

// applyPresence replaces the online set with the server's answer.
func (m *Model) applyPresence(p api.Presence) {
	m.presenceKnown = true
	m.presenceShare = p.Share
	m.online = make(map[string]bool, len(p.Online))
	if !p.Share {
		return
	}
	for _, id := range p.Online {
		m.online[id] = true
	}
}

// handlePresenceEvent folds one node_presence frame into the set, and
// says so in the command bar when an active node comes online.
func (m *Model) handlePresenceEvent(e ws.Event) {
	if !m.presenceVisible() {
		return // we are hidden, or have no answer yet: show nothing
	}
	lists := groupNodes(m.nodes, m.username)
	var who string
	for _, r := range lists.Active {
		if r.UserID == e.User {
			who = r.Username
		}
	}
	if who == "" {
		return // not an active node of ours (or the list is stale)
	}
	next := make(map[string]bool, len(m.online)+1)
	for id := range m.online {
		next[id] = true
	}
	was := next[e.User]
	if e.Online {
		next[e.User] = true
	} else {
		delete(next, e.User)
	}
	m.online = next
	if e.Online && !was {
		m.commandMsg = fmt.Sprintf("%s is online.", sanitizeForTerminal(who))
	}
}

// presenceSetMsg is the result of :presence on|off.
type presenceSetMsg struct {
	share        bool
	err          error
	unauthorized bool
}

// presenceCommand implements :presence [on|off].
func (m *Model) presenceCommand(args []string) (string, tea.Cmd) {
	if msg := m.nodePrecheck(); msg != "" {
		return msg, nil
	}
	if len(args) == 0 {
		if m.presenceKnown && !m.presenceShare {
			return "Your online status is hidden (and so are your nodes'). :presence on shows it.", nil
		}
		return "Your online status is visible to your nodes, and theirs to you. :presence off hides it.", nil
	}
	var share bool
	switch strings.ToLower(args[0]) {
	case "on":
		share = true
	case "off":
		share = false
	default:
		return "Usage: :presence [on|off]", nil
	}
	client, token := m.apiClient, m.accessToken
	return "Updating your online status...", func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), apiHTTPTimeout)
		defer cancel()
		err := client.SetPresence(ctx, token, share)
		return presenceSetMsg{share: share, err: err, unauthorized: api.IsUnauthorized(err)}
	}
}

func (m Model) handlePresenceSet(msg presenceSetMsg) (tea.Model, tea.Cmd) {
	if m.accessToken == "" {
		return m, nil // logged out while the request was out
	}
	if msg.err != nil {
		if msg.unauthorized {
			return m, m.maybeReauthCmd()
		}
		m.commandMsg = "Couldn't change your online status: " + sanitizeForTerminal(msg.err.Error())
		return m, nil
	}
	if !msg.share {
		m.presenceKnown, m.presenceShare, m.online = true, false, nil
		m.commandMsg = "Your online status is now hidden, and so are your nodes'."
		return m, nil
	}
	m.presenceKnown, m.presenceShare = true, true
	m.commandMsg = "Your online status is now visible to your nodes."
	return m, m.fetchNodesCmd() // brings back who is online
}

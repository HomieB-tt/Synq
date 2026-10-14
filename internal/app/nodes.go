package app

import (
	"context"
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/HomieB-tt/synq/internal/api"
)

// This file is the Nodes tab (synq-DESIGN.md section 5): the list of
// who you are connected to, and the commands that change it. A "node" is
// an accepted, mutual relationship; only active nodes can chat or
// exchange files, and there is deliberately no user search - you request
// someone by their exact username, which they have to tell you.

// nodeRow is one other user in one of the lists.
type nodeRow struct {
	Username string
	UserID   string
}

// nodeLists is the caller's relationships, sorted into what the tab
// shows. Blocked relationships appear in none of them: showing them
// would reveal to a blocked user, or to anyone looking over a shoulder,
// who is blocked - and a block is something you do and forget.
type nodeLists struct {
	Active   []nodeRow // accepted both ways
	Incoming []nodeRow // they asked, you haven't answered
	Outgoing []nodeRow // you asked, they haven't answered
}

// groupNodes sorts rels into nodeLists from the point of view of the user
// named me. Usernames are compared exactly: they are the server's
// identifier, and case-folding here could pair the wrong account.
func groupNodes(rels []api.NodeRelationship, me string) nodeLists {
	var out nodeLists
	for _, r := range rels {
		var other nodeRow
		iAmRequester := false
		switch {
		case me != "" && r.RequesterUsername == me:
			other = nodeRow{Username: r.TargetUsername, UserID: r.TargetID}
			iAmRequester = true
		case me != "" && r.TargetUsername == me:
			other = nodeRow{Username: r.RequesterUsername, UserID: r.RequesterID}
		default:
			continue // not ours, or we don't know who we are yet
		}
		switch r.Status {
		case api.NodeStatusActive:
			out.Active = append(out.Active, other)
		case api.NodeStatusPending:
			if iAmRequester {
				out.Outgoing = append(out.Outgoing, other)
			} else {
				out.Incoming = append(out.Incoming, other)
			}
		}
	}
	for _, l := range [][]nodeRow{out.Active, out.Incoming, out.Outgoing} {
		sort.Slice(l, func(i, j int) bool { return l[i].Username < l[j].Username })
	}
	return out
}

func (l nodeLists) find(list []nodeRow, username string) (nodeRow, bool) {
	for _, r := range list {
		if r.Username == username {
			return r, true
		}
	}
	return nodeRow{}, false
}

// --- loading ---

type nodesLoadedMsg struct {
	nodes        []api.NodeRelationship
	err          error
	unauthorized bool
}

// fetchNodesCmd reloads the relationship list. It is nil (a no-op for
// tea.Batch) when there is nothing to ask: no server, or not logged in.
func (m Model) fetchNodesCmd() tea.Cmd {
	if m.apiClient == nil || m.accessToken == "" {
		return nil
	}
	client, token := m.apiClient, m.accessToken
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), apiHTTPTimeout)
		defer cancel()
		nodes, err := client.ListNodes(ctx, token)
		return nodesLoadedMsg{nodes: nodes, err: err, unauthorized: api.IsUnauthorized(err)}
	}
}

func (m Model) handleNodesLoaded(msg nodesLoadedMsg) (tea.Model, tea.Cmd) {
	if m.accessToken == "" {
		return m, nil // logged out while the request was out
	}
	if msg.err != nil {
		if msg.unauthorized {
			return m, m.maybeReauthCmd()
		}
		m.nodesErr = msg.err.Error()
		return m, nil
	}
	m.nodes = msg.nodes
	m.nodesLoaded = true
	m.nodesErr = ""
	return m, nil
}

// --- actions ---

type nodeActionMsg struct {
	verb         string // "request", "accept" or "block"
	username     string
	err          error
	unauthorized bool
}

func (m *Model) nodePrecheck() string {
	switch {
	case m.apiClient == nil:
		return "Nodes need synq-server configured. Set SYNQ_SERVER_URL and restart Synq (see README.md)."
	case m.accessToken == "":
		return "Log in first - :register <username> if you haven't registered, or :login if you have."
	}
	return ""
}

// requestNode implements `:node <username>`.
func (m *Model) requestNode(username string) (string, tea.Cmd) {
	if msg := m.nodePrecheck(); msg != "" {
		return msg, nil
	}
	if username == "" {
		return "Usage: :node <username>", nil
	}
	if username == m.username {
		return "That's you.", nil
	}
	lists := groupNodes(m.nodes, m.username)
	if _, ok := lists.find(lists.Active, username); ok {
		return fmt.Sprintf("%s is already your node - :chat %s.", username, username), nil
	}
	client, token := m.apiClient, m.accessToken
	return fmt.Sprintf("Sending a node request to %s...", username), func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), apiHTTPTimeout)
		defer cancel()
		err := client.SendNodeRequest(ctx, token, username)
		return nodeActionMsg{verb: "request", username: username, err: err, unauthorized: api.IsUnauthorized(err)}
	}
}

// acceptNode implements `:accept <username>` for a node request (the same
// command also accepts a changed key - see runCommand, which tries that
// first).
func (m *Model) acceptNode(username string) (string, tea.Cmd) {
	lists := groupNodes(m.nodes, m.username)
	row, ok := lists.find(lists.Incoming, username)
	if !ok {
		return fmt.Sprintf("Nothing to accept from %s - No unaccepted key change or node request from them on file. (:nodes refreshes the list.)", username), nil
	}
	if msg := m.nodePrecheck(); msg != "" {
		return msg, nil
	}
	client, token := m.apiClient, m.accessToken
	return fmt.Sprintf("Accepting %s...", username), func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), apiHTTPTimeout)
		defer cancel()
		err := client.AcceptNodeRequest(ctx, token, row.UserID)
		return nodeActionMsg{verb: "accept", username: username, err: err, unauthorized: api.IsUnauthorized(err)}
	}
}

// blockNode implements `:block <username> confirm`. Blocking has no undo
// in the API and is silent to the other person, so it needs the explicit
// confirm word rather than a single keystroke away from a typo.
func (m *Model) blockNode(args []string) (string, tea.Cmd) {
	if msg := m.nodePrecheck(); msg != "" {
		return msg, nil
	}
	if len(args) == 0 || args[0] == "" {
		return "Usage: :block <username> confirm", nil
	}
	username := args[0]
	if username == m.username {
		return "That's you.", nil
	}
	if len(args) < 2 || args[1] != "confirm" {
		return fmt.Sprintf("Blocking %s ends any chat with them and can't be undone from here. They are not told. Run :block %s confirm to proceed.", username, username), nil
	}
	client, token := m.apiClient, m.accessToken
	lists := groupNodes(m.nodes, m.username)
	known := ""
	for _, l := range [][]nodeRow{lists.Active, lists.Incoming, lists.Outgoing} {
		if r, ok := lists.find(l, username); ok {
			known = r.UserID
		}
	}
	return fmt.Sprintf("Blocking %s...", username), func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), apiHTTPTimeout)
		defer cancel()
		id := known
		if id == "" {
			// Not in the list (no relationship yet): look the id up.
			keys, err := client.GetPublicKeys(ctx, token, username)
			if err != nil {
				return nodeActionMsg{verb: "block", username: username, err: err, unauthorized: api.IsUnauthorized(err)}
			}
			id = keys.UserID
		}
		err := client.BlockNode(ctx, token, id)
		return nodeActionMsg{verb: "block", username: username, err: err, unauthorized: api.IsUnauthorized(err)}
	}
}

func (m Model) handleNodeAction(msg nodeActionMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		switch {
		case msg.unauthorized:
			m.commandMsg = "Your session token expired - renewing it. Run that again in a moment."
			return m, m.maybeReauthCmd()
		case api.IsNotFound(msg.err):
			m.commandMsg = fmt.Sprintf("No such user: %s", msg.username)
		case api.IsRateLimited(msg.err):
			m.commandMsg = "synq-server rate-limited that - wait a moment and try again."
		default:
			m.commandMsg = fmt.Sprintf("Couldn't %s %s: %v", msg.verb, msg.username, msg.err)
		}
		return m, nil
	}
	switch msg.verb {
	case "request":
		m.commandMsg = fmt.Sprintf("Request sent to %s. You can chat once they accept.", msg.username)
	case "accept":
		m.commandMsg = fmt.Sprintf("%s is now your node - :chat %s.", msg.username, msg.username)
	case "block":
		m.commandMsg = fmt.Sprintf("Blocked %s.", msg.username)
	}
	// What the server now holds is the truth (a request may have been
	// auto-accepted, for one), so reload rather than patch the list.
	return m, m.fetchNodesCmd()
}

// --- rendering ---

func (m Model) renderNodes() string {
	switch {
	case m.identity == nil:
		return "Create an identity to build your network. Restart Synq and choose \"Create your identity.\""
	case m.apiClient == nil:
		return "Nodes need synq-server configured. Set SYNQ_SERVER_URL and restart Synq (see README.md)."
	case m.accessToken == "":
		return "Log in to see your nodes - :register <username> or :login."
	case !m.nodesLoaded && m.nodesErr != "":
		return m.theme.Warning.Render("Couldn't load your nodes: "+sanitizeForTerminal(m.nodesErr)) + "\n\nPress r to retry."
	case !m.nodesLoaded:
		return "Loading your nodes..."
	}

	lists := groupNodes(m.nodes, m.username)
	var b strings.Builder
	section := func(title string, rows []nodeRow, hint string) {
		if len(rows) == 0 {
			return
		}
		fmt.Fprintf(&b, "%s (%d)\n", title, len(rows))
		for _, r := range rows {
			b.WriteString("  ")
			b.WriteString(sanitizeForTerminal(r.Username))
			if hint != "" {
				b.WriteString(m.theme.Muted.Render("   " + strings.ReplaceAll(hint, "%s", sanitizeForTerminal(r.Username))))
			}
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
	}
	section("Requests for you", lists.Incoming, ":accept %s")
	section("Nodes", lists.Active, ":chat %s")
	section("Waiting on them", lists.Outgoing, "")

	if len(lists.Active)+len(lists.Incoming)+len(lists.Outgoing) == 0 {
		b.WriteString("No nodes yet.\n\nThere's no user search: ask someone for their exact username, then\nsend them a request with :node <username>.\n\n")
	}
	if m.nodesErr != "" {
		b.WriteString(m.theme.Warning.Render("Last refresh failed: " + sanitizeForTerminal(m.nodesErr)))
		b.WriteString("\n\n")
	}
	b.WriteString(m.theme.Muted.Render(":node <user> request · :accept <user> · :block <user> confirm · r refresh"))
	return b.String()
}

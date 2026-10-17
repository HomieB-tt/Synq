package app

import (
	"context"
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

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
	Verified bool   // linked a GitHub account
	Handle   string // ...and this is its handle
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
			other = nodeRow{Username: r.TargetUsername, UserID: r.TargetID,
				Verified: r.TargetGitHubVerified, Handle: r.TargetGitHubHandle}
			iAmRequester = true
		case me != "" && r.TargetUsername == me:
			other = nodeRow{Username: r.RequesterUsername, UserID: r.RequesterID,
				Verified: r.RequesterGitHubVerified, Handle: r.RequesterGitHubHandle}
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

	return m.renderNodeList()
}

// --- selection and keys ---

// nodeKind says which section an entry came from, and so what the row's
// shortcut keys do.
type nodeKind int

const (
	nodeIncoming nodeKind = iota // they asked: a accepts
	nodeActive                   // mutual: enter starts a chat
	nodeOutgoing                 // you asked: nothing to do but wait
)

type nodeEntry struct {
	nodeRow
	kind nodeKind
}

// entries is every row in display order - requests for you first,
// because they are the only ones waiting on you - flattened so one index
// can select across the sections.
func (l nodeLists) entries() []nodeEntry {
	out := make([]nodeEntry, 0, len(l.Incoming)+len(l.Active)+len(l.Outgoing))
	for _, r := range l.Incoming {
		out = append(out, nodeEntry{r, nodeIncoming})
	}
	for _, r := range l.Active {
		out = append(out, nodeEntry{r, nodeActive})
	}
	for _, r := range l.Outgoing {
		out = append(out, nodeEntry{r, nodeOutgoing})
	}
	return out
}

// listPage is how many rows PgUp / PgDn move on the Nodes tab.
func (m Model) listPage() int {
	_, h := m.innerSize()
	return max(h-5, 1)
}

// updateNodesKey handles a key on the Nodes tab, reporting whether it
// did. Everything it can do is also a command; these are the shortcuts
// for the common cases. Blocking deliberately has no one-key form - it
// only explains how to do it (see blockNode) - because it can't be
// undone and the other person isn't told.
func (m *Model) updateNodesKey(key string) (handled bool, cmd tea.Cmd) {
	if m.identity == nil || m.apiClient == nil || m.accessToken == "" || !m.nodesLoaded {
		return false, nil
	}
	entries := groupNodes(m.nodes, m.username).entries()
	if len(entries) == 0 {
		return false, nil
	}
	m.nodesSel = min(max(m.nodesSel, 0), len(entries)-1)
	cur := entries[m.nodesSel]

	switch key {
	case "up", "k":
		m.nodesSel = max(m.nodesSel-1, 0)
	case "down", "j":
		m.nodesSel = min(m.nodesSel+1, len(entries)-1)
	case "pgup":
		m.nodesSel = max(m.nodesSel-m.listPage(), 0)
	case "pgdown":
		m.nodesSel = min(m.nodesSel+m.listPage(), len(entries)-1)
	case "g", "home":
		m.nodesSel = 0
	case "G", "end":
		m.nodesSel = len(entries) - 1

	case "a":
		if cur.kind != nodeIncoming {
			m.commandMsg = fmt.Sprintf("Nothing to accept from %s.", sanitizeForTerminal(cur.Username))
			return true, nil
		}
		msg, c := m.acceptNode(cur.Username)
		m.commandMsg = msg
		return true, c

	case "enter":
		switch cur.kind {
		case nodeActive:
			msg, _, c := m.runCommand("chat " + cur.Username)
			m.commandMsg = msg
			return true, c
		case nodeIncoming:
			msg, c := m.acceptNode(cur.Username)
			m.commandMsg = msg
			return true, c
		}
		m.commandMsg = fmt.Sprintf("Waiting for %s to accept your request.", sanitizeForTerminal(cur.Username))

	case "b":
		m.commandMsg, _ = m.blockNode([]string{cur.Username})

	default:
		return false, nil
	}
	return true, nil
}

// --- rendering the list ---

// renderNodeList draws the three sections with a selection. Usernames
// come from other people, so they are sanitized; a section title is one
// unbroken span (plain text people - and tests - can search for).
func (m Model) renderNodeList() string {
	t := m.theme
	w, h := m.innerSize()
	lists := groupNodes(m.nodes, m.username)
	entries := lists.entries()

	if len(entries) == 0 {
		lines := []string{t.AccentText.Render("Nodes"), ""}
		for _, l := range wrapToWidth("No nodes yet.", w) {
			lines = append(lines, t.Text.Render(l))
		}
		lines = append(lines, "")
		for _, l := range wrapToWidth("There's no user search: ask someone for their exact username, then send them a request with :node <username>.", w) {
			lines = append(lines, t.Muted.Render(l))
		}
		return strings.Join(m.withNodesFooter(lines, w, h, nodeEntry{}, false), "\n")
	}

	sel := min(max(m.nodesSel, 0), len(entries)-1)

	// All rows, section by section, remembering where the selected one
	// landed so the window can follow it.
	var body []string
	selLine, idx := 0, 0
	section := func(title string, rows []nodeRow, kind nodeKind) {
		if len(rows) == 0 {
			return
		}
		if len(body) > 0 {
			body = append(body, "")
		}
		body = append(body, t.AccentText.Render(fmt.Sprintf("%s (%d)", title, len(rows))))
		for _, r := range rows {
			if idx == sel {
				selLine = len(body)
			}
			body = append(body, m.nodeRowLine(nodeEntry{r, kind}, idx == sel, w))
			idx++
		}
	}
	section("Requests for you", lists.Incoming, nodeIncoming)
	section("Nodes", lists.Active, nodeActive)
	section("Waiting on them", lists.Outgoing, nodeOutgoing)

	footerRows := 2
	if m.nodesErr != "" {
		footerRows = 3
	}
	room := max(h-footerRows-1, 1)
	start := 0
	if len(body) > room {
		start = min(max(selLine-room/2, 0), len(body)-room)
	}
	body = body[start:min(start+room, len(body))]

	return strings.Join(m.withNodesFooter(body, w, h, entries[sel], true), "\n")
}

// withNodesFooter pads lines down to the pane's last rows and appends
// the footer: any failed-refresh warning, the keys for the selected row,
// and how to send a request.
func (m Model) withNodesFooter(lines []string, w, h int, cur nodeEntry, haveSel bool) []string {
	t := m.theme
	var footer []string
	if m.nodesErr != "" {
		footer = append(footer, t.Warning.Render(truncateToWidth("Last refresh failed: "+sanitizeForTerminal(m.nodesErr), w)))
	}
	keys := "r refresh"
	if haveSel {
		switch cur.kind {
		case nodeIncoming:
			keys = "a accept · b block · ↑/↓ pgup/pgdn · r refresh"
		case nodeActive:
			keys = "enter chat · b block · ↑/↓ pgup/pgdn · r refresh"
		case nodeOutgoing:
			keys = "b block · ↑/↓ pgup/pgdn · r refresh"
		}
	}
	footer = append(footer,
		t.Muted.Render(truncateToWidth(keys, w)),
		t.Muted.Render(truncateToWidth(":node <username> sends a request · :block <username> confirm", w)),
	)
	for len(lines) < h-len(footer) {
		lines = append(lines, "")
	}
	return append(lines[:max(h-len(footer), 0)], footer...)
}

// nodeRowLine draws one entry: marker, name, GitHub badge, and on the
// right what state the relationship is in. It is exactly w columns wide.
func (m Model) nodeRowLine(e nodeEntry, selected bool, w int) string {
	base, muted, accent, bg := m.rowStyles(selected)
	marker, markerStyle := "  ", base
	if selected {
		marker, markerStyle = "▌ ", accent
	}

	status := map[nodeKind]string{
		nodeIncoming: "wants to connect",
		nodeActive:   "connected",
		nodeOutgoing: "waiting for reply",
	}[e.kind]

	// On a narrow pane something has to give. The name matters most, then
	// the badge, then the status words - so drop them in the reverse
	// order, only as far as needed to leave the name a readable width.
	const minName = 8
	verified, handle := e.Verified, e.Handle
	room := func() int {
		return w - lipgloss.Width(marker) - badgeWidth(verified, handle) - lipgloss.Width(status) - 2
	}
	if status != "" && room() < minName {
		status = ""
	}
	if verified && w-lipgloss.Width(marker)-badgeWidth(verified, handle) < minName {
		verified, handle = false, ""
	}
	badge := badgeWidth(verified, handle)
	name := truncateToWidth(sanitizeForTerminal(e.Username), max(room(), 1))
	used := lipgloss.Width(marker) + lipgloss.Width(name) + badge

	return markerStyle.Render(marker) +
		base.Bold(true).Render(name) +
		m.badgeSpans(verified, handle, base, muted, bg) +
		base.Render(strings.Repeat(" ", max(w-used-lipgloss.Width(status), 1))) +
		muted.Render(status)
}

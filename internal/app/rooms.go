package app

import (
	"context"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/HomieB-tt/synq/internal/api"
	"github.com/HomieB-tt/synq/internal/chat"
	"github.com/HomieB-tt/synq/internal/room"
	"github.com/HomieB-tt/synq/internal/ws"
)

// Rooms are small, invite-only, ephemeral group conversations (see
// ROOMS.md in synq-server). Each one shows up in the Chat tab as a thread,
// so the message list, composer, scrolling and unread counts are the ones
// 1:1 chats already use; what differs is who you are talking to and how
// messages are sealed. The sealing, signing and the "what do I do with this
// frame" decisions live in internal/room; this file is the glue to the
// server, the thread store and the screen.
//
// Everything here is memory-only. Quitting wipes it, and a room can't be
// rejoined after a restart: the key died with the process.

// roomNoun is what the feature is called on screen and in help. It is the
// one place to change if the name changes; the command word is roomCommandName.
const (
	roomNoun        = "room"
	roomCommandName = "room"
)

// roomThreadPrefix marks a chat thread key as a room's. A real contact key
// is hex, so it can never collide.
const roomThreadPrefix = "room:"

func roomThread(id string) chat.ContactKey { return chat.ContactKey(roomThreadPrefix + id) }

func isRoomThread(c chat.ContactKey) bool {
	_, ok := roomIDOf(c)
	return ok
}

// roomIDOf is the inverse: the room id behind a thread key, if it is one.
func roomIDOf(c chat.ContactKey) (string, bool) {
	s := string(c)
	if !strings.HasPrefix(s, roomThreadPrefix) {
		return "", false
	}
	return s[len(roomThreadPrefix):], true
}

// roomPeer is someone the owner invited: what the owner learned from the
// key lookup, kept until they join.
type roomPeer struct {
	userID   string
	username string
	signKey  string // hex Ed25519
	contact  chat.ContactKey
}

// roomEntry is one room as this client knows it.
type roomEntry struct {
	r      *room.Room
	owner  bool
	joined bool // false while only invited
	ready  bool // the "you can talk" line has been shown

	// peers is the owner's table of people it invited, by user id.
	peers map[string]roomPeer

	// replyBack selects the message a reply goes to: 0 none, 1 the newest
	// message, 2 the one before, and so on (Ctrl+R steps through them).
	replyBack int
}

// --- messages the room commands produce ---

type roomCreatedMsg struct {
	room *api.Room
	err  error
}

// roomOpMsg is the outcome of one room action. op is "invite", "accept",
// "leave", "end" or "drop" (leave-or-end, nothing shown).
type roomOpMsg struct {
	op       string
	id       string
	username string
	err      error
}

type roomDetailMsg struct {
	id   string
	room *api.Room
	err  error
}

type roomListMsg struct {
	rows []api.RoomSummary
	err  error
}

type roomSendFailedMsg struct {
	id  string
	mid string
	err error
}

// --- small helpers ---

func (m Model) roomEntryFor(id string) *roomEntry {
	if m.rooms == nil {
		return nil
	}
	return m.rooms[id]
}

func (m *Model) putRoom(e *roomEntry) {
	if m.rooms == nil {
		m.rooms = make(map[string]*roomEntry)
	}
	m.rooms[e.r.ID] = e
}

func (m Model) roomLabel(id string) string {
	if e := m.roomEntryFor(id); e != nil {
		return "# " + e.r.Name
	}
	return "# room"
}

// roomSay adds a line the app wrote itself to the room's thread.
func (m *Model) roomSay(id, text string) {
	m.chatStore.Append(roomThread(id), chat.Message{
		Body:   []byte(text),
		At:     time.Now(),
		System: true,
	})
}

// usernameForID resolves a user id to a name from what the client already
// knows: the node list, then contacts. "" if neither has it.
func (m Model) usernameForID(id string) string {
	for _, n := range m.nodes {
		switch id {
		case n.RequesterID:
			return n.RequesterUsername
		case n.TargetID:
			return n.TargetUsername
		}
	}
	if c, ok := m.chatContactIDs[id]; ok {
		if info, ok := m.chatContacts[c]; ok {
			return info.Username
		}
	}
	return ""
}

// roomSelf is this client's user id in the room: the owner knows it from
// creating the room, a member finds itself on the roster by username.
func (m Model) roomSelf(e *roomEntry) string {
	if e.owner {
		return e.r.OwnerID
	}
	for id, p := range e.r.Roster {
		if p.Username == m.username {
			return id
		}
	}
	return ""
}

// findRoom picks the room a command refers to: the one named (exact name,
// or a unique prefix, ignoring case), else the open thread's room, else the
// only room there is.
func (m Model) findRoom(name string) (*roomEntry, string) {
	if name != "" {
		var hits []*roomEntry
		for _, e := range m.rooms {
			if strings.EqualFold(e.r.Name, name) {
				return e, ""
			}
			if strings.HasPrefix(strings.ToLower(e.r.Name), strings.ToLower(name)) {
				hits = append(hits, e)
			}
		}
		switch len(hits) {
		case 1:
			return hits[0], ""
		case 0:
			return nil, fmt.Sprintf("No %s called %q. :%s list shows yours.", roomNoun, name, roomCommandName)
		default:
			return nil, fmt.Sprintf("More than one %s starts with %q - type more of the name.", roomNoun, name)
		}
	}
	if id, ok := roomIDOf(m.chatActive); ok {
		if e := m.roomEntryFor(id); e != nil {
			return e, ""
		}
	}
	if len(m.rooms) == 1 {
		for _, e := range m.rooms {
			return e, ""
		}
	}
	if len(m.rooms) == 0 {
		return nil, fmt.Sprintf("You aren't in any %s. :%s new <name> makes one.", roomNoun, roomCommandName)
	}
	return nil, fmt.Sprintf("Open a %s first (Chat tab), or give its name.", roomNoun)
}

// --- the :room command ---

const roomUsage = "Usage: :room new <name> | invite <username> | accept [name] | leave [name] | end [name] confirm | members | list"

func (m *Model) roomCommand(args []string) (string, tea.Cmd) {
	if m.identity == nil {
		return "Create an identity first. Restart Synq and choose \"Create your identity.\"", nil
	}
	if m.apiClient == nil {
		return "Rooms need synq-server configured. Set SYNQ_SERVER_URL and restart Synq (see README.md).", nil
	}
	if m.accessToken == "" {
		return "Log in first - :register <username> if you haven't registered, or :login if you have.", nil
	}
	if len(args) == 0 {
		args = []string{"list"}
	}
	client, token := m.apiClient, m.accessToken

	switch strings.ToLower(args[0]) {
	case "new", "create":
		name, ok := room.CleanName(strings.Join(args[1:], " "))
		if !ok {
			return fmt.Sprintf("A %s name is 1-%d characters, with no control characters. :%s new <name>", roomNoun, room.MaxNameRunes, roomCommandName), nil
		}
		if len(m.ownedRooms()) >= 3 {
			return fmt.Sprintf("You already own 3 %ss - end one first.", roomNoun), nil
		}
		return fmt.Sprintf("Creating %s...", name), func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), apiHTTPTimeout)
			defer cancel()
			r, err := client.CreateRoom(ctx, token, name)
			return roomCreatedMsg{room: r, err: err}
		}

	case "invite":
		if len(args) != 2 {
			return fmt.Sprintf("Usage: :%s invite <username>", roomCommandName), nil
		}
		e, why := m.findRoom("")
		if e == nil {
			return why, nil
		}
		if !e.owner {
			return fmt.Sprintf("Only the owner of a %s can invite people.", roomNoun), nil
		}
		if len(e.r.Roster)+m.pendingInvites(e) >= room.MaxMembers {
			return fmt.Sprintf("A %s holds at most %d people.", roomNoun, room.MaxMembers), nil
		}
		id, username := e.r.ID, args[1]
		return fmt.Sprintf("Looking up %s...", username), func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), apiHTTPTimeout)
			defer cancel()
			keys, err := client.GetPublicKeys(ctx, token, username)
			return chatResolveResultMsg{username: username, keys: keys, err: err, roomInvite: id}
		}

	case "accept", "join":
		e, why := m.findRoom(strings.Join(args[1:], " "))
		if e == nil {
			return why, nil
		}
		if e.joined {
			return fmt.Sprintf("You're already in %s.", e.r.Name), nil
		}
		return fmt.Sprintf("Joining %s...", e.r.Name), m.roomOpCmd("accept", e.r.ID, "")

	case "leave", "decline":
		e, why := m.findRoom(strings.Join(args[1:], " "))
		if e == nil {
			return why, nil
		}
		if e.owner {
			return fmt.Sprintf("You own this %s, so you can't just leave it: :%s end confirm ends it for everyone.", roomNoun, roomCommandName), nil
		}
		return fmt.Sprintf("Leaving %s...", e.r.Name), m.roomOpCmd("leave", e.r.ID, "")

	case "end":
		rest := args[1:]
		confirmed := len(rest) > 0 && strings.EqualFold(rest[len(rest)-1], "confirm")
		if confirmed {
			rest = rest[:len(rest)-1]
		}
		e, why := m.findRoom(strings.Join(rest, " "))
		if e == nil {
			return why, nil
		}
		if !e.owner {
			return fmt.Sprintf("Only the owner can end a %s. :%s leave takes you out of it.", roomNoun, roomCommandName), nil
		}
		if !confirmed {
			return fmt.Sprintf("Ending %s removes it and everything said in it for everyone. Type :%s end confirm to do it.", e.r.Name, roomCommandName), nil
		}
		return fmt.Sprintf("Ending %s...", e.r.Name), m.roomOpCmd("end", e.r.ID, "")

	case "members", "who":
		e, why := m.findRoom(strings.Join(args[1:], " "))
		if e == nil {
			return why, nil
		}
		id := e.r.ID
		return "Fetching the member list...", func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), apiHTTPTimeout)
			defer cancel()
			r, err := client.GetRoom(ctx, token, id)
			return roomDetailMsg{id: id, room: r, err: err}
		}

	case "list", "ls":
		m.activeTab = tabChat
		return m.roomSummary(), m.fetchRoomsCmd()
	}
	return roomUsage, nil
}

// ownedRooms are the open rooms this client owns, name-sorted.
func (m Model) ownedRooms() []*roomEntry {
	var out []*roomEntry
	for _, e := range m.rooms {
		if e.owner && e.joined {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].r.Name < out[j].r.Name })
	return out
}

// pendingInvites counts people the owner invited who haven't joined.
func (m Model) pendingInvites(e *roomEntry) int {
	n := 0
	for id := range e.peers {
		if _, in := e.r.Roster[id]; !in {
			n++
		}
	}
	return n
}

func (m Model) roomSummary() string {
	if len(m.rooms) == 0 {
		return fmt.Sprintf("No %ss yet. :%s new <name> makes one; an invitation shows up in the Chat tab.", roomNoun, roomCommandName)
	}
	var parts []string
	for _, e := range m.sortedRooms() {
		switch {
		case !e.joined:
			parts = append(parts, fmt.Sprintf("%s (invited)", e.r.Name))
		case e.owner:
			parts = append(parts, fmt.Sprintf("%s (yours, %d)", e.r.Name, len(e.r.Roster)))
		default:
			parts = append(parts, fmt.Sprintf("%s (%d)", e.r.Name, len(e.r.Roster)))
		}
	}
	return "Your " + roomNoun + "s: " + strings.Join(parts, ", ")
}

func (m Model) sortedRooms() []*roomEntry {
	out := make([]*roomEntry, 0, len(m.rooms))
	for _, e := range m.rooms {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].r.Name < out[j].r.Name })
	return out
}

func (m Model) roomOpCmd(op, id, username string) tea.Cmd {
	client, token := m.apiClient, m.accessToken
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), apiHTTPTimeout)
		defer cancel()
		var err error
		switch op {
		case "invite":
			err = client.InviteToRoom(ctx, token, id, username)
		case "accept":
			err = client.AcceptRoomInvite(ctx, token, id)
		case "leave":
			err = client.LeaveRoom(ctx, token, id)
		case "end":
			err = client.EndRoom(ctx, token, id)
		case "drop":
			// Get out of a room without knowing whether it is ours: a
			// member can leave; the owner is told to end it instead.
			err = client.LeaveRoom(ctx, token, id)
			if api.IsConflict(err) {
				err = client.EndRoom(ctx, token, id)
			}
		}
		return roomOpMsg{op: op, id: id, username: username, err: err}
	}
}

// fetchRoomsCmd reloads the server's list of rooms. nil when there is
// nothing to ask.
func (m Model) fetchRoomsCmd() tea.Cmd {
	if m.apiClient == nil || m.accessToken == "" {
		return nil
	}
	client, token := m.apiClient, m.accessToken
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), apiHTTPTimeout)
		defer cancel()
		rows, err := client.ListRooms(ctx, token)
		return roomListMsg{rows: rows, err: err}
	}
}

// --- results ---

func (m Model) handleRoomCreated(msg roomCreatedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil || msg.room == nil {
		m.commandMsg = fmt.Sprintf("Couldn't create the %s: %v", roomNoun, msg.err)
		return m, nil
	}
	if m.identity == nil {
		return m, nil
	}
	key, err := room.NewKey()
	if err != nil {
		m.commandMsg = fmt.Sprintf("Couldn't create a key for the %s: %v", roomNoun, err)
		// The server has the room; don't leave it behind half-made.
		return m, m.roomOpCmd("drop", msg.room.ID, "")
	}
	r := room.New(msg.room.ID, msg.room.Name, msg.room.OwnerID)
	r.SetOwnerState(key, room.Peer{
		ID:       msg.room.OwnerID,
		Username: m.username,
		SignKey:  hex.EncodeToString(m.identity.SigningPublic),
	})
	e := &roomEntry{r: r, owner: true, joined: true, ready: true, peers: map[string]roomPeer{}}
	m.putRoom(e)
	m.roomSay(r.ID, fmt.Sprintf("You created %s. Invite people you're connected to with :%s invite <username> - up to %d in all. "+
		"Quitting Synq, or :%s end confirm, ends it for everyone and wipes it.", r.Name, roomCommandName, room.MaxMembers, roomCommandName))
	m.activeTab = tabChat
	m.chatActive = roomThread(r.ID)
	m.chatInput, m.chatBack = "", 0
	m.commandMsg = fmt.Sprintf("Created %s.", r.Name)
	return m, nil
}

func (m Model) handleRoomInviteResolved(msg chatResolveResultMsg) (tea.Model, tea.Cmd) {
	e := m.roomEntryFor(msg.roomInvite)
	if e == nil || !e.owner {
		return m, nil
	}
	if msg.err != nil {
		if api.IsNotFound(msg.err) {
			m.commandMsg = fmt.Sprintf("No such user: %s", msg.username)
		} else {
			m.commandMsg = fmt.Sprintf("Couldn't look up %s: %v", msg.username, msg.err)
		}
		return m, nil
	}
	// The same checks :chat makes: usable signed keys, and the pin. The
	// room key will travel to this person over that pairwise channel, so
	// it has to be one this device trusts.
	contact, outcome, fail := m.acceptResolved(msg)
	if fail != "" {
		m.commandMsg = fail
		return m, nil
	}
	if outcome == pinChanged {
		m.commandMsg = fmt.Sprintf("%s's key CHANGED since you pinned it - not inviting them. Compare fingerprints with :verify %s, or :accept %s to trust the new key.",
			msg.username, msg.username, msg.username)
		return m, nil
	}
	if k, err := hex.DecodeString(msg.keys.PubKey); err != nil || len(k) != 32 {
		m.commandMsg = fmt.Sprintf("%s has an unusable signing key on file.", msg.username)
		return m, nil
	}
	if e.peers == nil {
		e.peers = map[string]roomPeer{}
	}
	e.peers[msg.keys.UserID] = roomPeer{
		userID:   msg.keys.UserID,
		username: msg.username,
		signKey:  msg.keys.PubKey,
		contact:  contact,
	}
	m.commandMsg = fmt.Sprintf("Inviting %s...", msg.username)
	return m, m.roomOpCmd("invite", e.r.ID, msg.username)
}

func (m Model) handleRoomOp(msg roomOpMsg) (tea.Model, tea.Cmd) {
	e := m.roomEntryFor(msg.id)
	switch msg.op {
	case "invite":
		if msg.err != nil {
			if e != nil {
				for id, p := range e.peers {
					if p.username == msg.username {
						if _, in := e.r.Roster[id]; !in {
							delete(e.peers, id)
						}
					}
				}
			}
			if api.IsNotFound(msg.err) {
				m.commandMsg = fmt.Sprintf("You can only invite people you have an active node with. %s isn't one yet - see :node %s.", msg.username, msg.username)
			} else {
				m.commandMsg = fmt.Sprintf("Couldn't invite %s: %v", msg.username, msg.err)
			}
			return m, nil
		}
		if e != nil {
			m.roomSay(msg.id, fmt.Sprintf("You invited %s. They're in once they accept.", msg.username))
		}
		m.commandMsg = fmt.Sprintf("Invited %s.", msg.username)

	case "accept":
		if msg.err != nil {
			m.commandMsg = fmt.Sprintf("Couldn't join: %v", msg.err)
			return m, nil
		}
		if e == nil {
			return m, nil
		}
		e.joined = true
		m.roomSay(msg.id, fmt.Sprintf("You joined %s. Waiting for the owner's key - it arrives in a moment.", e.r.Name))
		m.activeTab = tabChat
		m.chatActive = roomThread(msg.id)
		m.chatInput, m.chatBack = "", 0
		m.commandMsg = ""

	case "leave":
		if msg.err != nil && !api.IsNotFound(msg.err) {
			m.commandMsg = fmt.Sprintf("Couldn't leave: %v", msg.err)
			return m, nil
		}
		name := m.roomLabel(msg.id)
		m.dropRoom(msg.id)
		m.commandMsg = fmt.Sprintf("Left %s.", name)

	case "end":
		if msg.err != nil && !api.IsNotFound(msg.err) {
			m.commandMsg = fmt.Sprintf("Couldn't end it: %v", msg.err)
			return m, nil
		}
		name := m.roomLabel(msg.id)
		m.dropRoom(msg.id)
		m.commandMsg = fmt.Sprintf("Ended %s - it and everything said in it are gone.", name)
	}
	return m, nil
}

func (m Model) handleRoomDetail(msg roomDetailMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil || msg.room == nil {
		m.commandMsg = fmt.Sprintf("Couldn't fetch the member list: %v", msg.err)
		return m, nil
	}
	var names []string
	for _, mem := range msg.room.Members {
		label := mem.Username
		if mem.UserID == msg.room.OwnerID {
			label += " (owner)"
		}
		if mem.Status == "INVITED" {
			label += " (invited)"
		}
		names = append(names, label)
	}
	m.commandMsg = fmt.Sprintf("%s: %s", msg.room.Name, strings.Join(names, ", "))
	return m, nil
}

// handleRoomList reconciles what the server says with what this client
// holds: an invitation we missed becomes a thread; a room the server no
// longer has is dropped; and a room the server has us in that we hold no
// key for (left over from a crashed launch) is left or ended, since a key
// that died with the process can't be recovered.
func (m Model) handleRoomList(msg roomListMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.commandMsg = fmt.Sprintf("Couldn't load your %ss: %v", roomNoun, msg.err)
		return m, nil
	}
	onServer := map[string]bool{}
	var cmds []tea.Cmd
	for _, row := range msg.rows {
		onServer[row.ID] = true
		if m.roomEntryFor(row.ID) != nil {
			continue
		}
		name, ok := room.CleanName(row.Name)
		if !ok {
			name = "room"
		}
		switch row.Status {
		case "INVITED":
			e := &roomEntry{r: room.New(row.ID, name, row.OwnerID)}
			m.putRoom(e)
			m.roomSay(row.ID, fmt.Sprintf("%s invited you to this %s. :%s accept %s joins, :%s leave %s declines.",
				m.ownerLabel(row.OwnerID), roomNoun, roomCommandName, name, roomCommandName, name))
			m.noteUnread(roomThread(row.ID), false)
		default:
			// Joined on the server, unknown here: a leftover.
			cmds = append(cmds, m.roomOpCmd("drop", row.ID, ""))
		}
	}
	for id, e := range m.rooms {
		if !onServer[id] {
			name := e.r.Name
			m.dropRoom(id)
			m.commandMsg = fmt.Sprintf("%s ended while you were away.", name)
		}
	}
	return m, tea.Batch(cmds...)
}

func (m Model) ownerLabel(id string) string {
	if n := m.usernameForID(id); n != "" {
		return sanitizeForTerminal(n)
	}
	return "Someone"
}

// dropRoom forgets a room: wipes its key and deletes its thread.
func (m *Model) dropRoom(id string) {
	e := m.rooms[id]
	if e == nil {
		return
	}
	e.r.Wipe()
	delete(m.rooms, id)
	th := roomThread(id)
	m.chatStore.Delete(th)
	delete(m.unread, th)
	if m.chatActive == th {
		m.chatActive = ""
		m.chatInput, m.chatBack = "", 0
	}
}

// wipeRooms drops every room (quit, logout).
func (m *Model) wipeRooms() {
	for id := range m.rooms {
		m.dropRoom(id)
	}
}

// --- server events ---

func isRoomEvent(t string) bool { return strings.HasPrefix(t, "room_") }

func (m *Model) handleRoomEvent(ev ws.Event) tea.Cmd {
	switch ev.Type {
	case ws.EventTypeRoomInvite:
		if m.roomEntryFor(ev.Room) != nil {
			return nil
		}
		name, ok := room.CleanName(ev.Name)
		if !ok {
			name = "room"
		}
		m.putRoom(&roomEntry{r: room.New(ev.Room, name, ev.From)})
		owner := m.ownerLabel(ev.From)
		m.roomSay(ev.Room, fmt.Sprintf("%s invited you to this %s. :%s accept %s joins, :%s leave %s declines.",
			owner, roomNoun, roomCommandName, name, roomCommandName, name))
		m.noteUnread(roomThread(ev.Room), false)
		m.commandMsg = fmt.Sprintf("%s invited you to %s - see the Chat tab (3), then :%s accept %s", owner, name, roomCommandName, name)

	case ws.EventTypeRoomJoined:
		e := m.roomEntryFor(ev.Room)
		if e == nil || !e.owner {
			return nil
		}
		p, ok := e.peers[ev.User]
		if !ok {
			// Someone this client never looked up: can't hand them a key
			// it can't address, and can't vouch for their signing key.
			return nil
		}
		e.r.AddPeer(room.Peer{ID: p.userID, Username: p.username, SignKey: p.signKey})
		m.roomSay(ev.Room, fmt.Sprintf("%s joined.", p.username))
		return m.distributeRoomState(e)

	case ws.EventTypeRoomLeft:
		e := m.roomEntryFor(ev.Room)
		if e == nil {
			return nil
		}
		name := ""
		if p, ok := e.r.Roster[ev.User]; ok {
			name = p.Username
		} else if p, ok := e.peers[ev.User]; ok {
			name = p.username
		}
		if name == "" {
			name = "Someone"
		}
		wasMember := false
		if _, in := e.r.Roster[ev.User]; in {
			wasMember = true
		}
		if e.owner {
			e.r.RemovePeer(ev.User)
			delete(e.peers, ev.User)
		}
		if wasMember || e.owner {
			m.roomSay(ev.Room, fmt.Sprintf("%s left.", name))
		}
		if e.owner && wasMember {
			return m.distributeRoomState(e)
		}

	case ws.EventTypeRoomEnded:
		e := m.roomEntryFor(ev.Room)
		if e == nil {
			return nil
		}
		name := e.r.Name
		m.dropRoom(ev.Room)
		why := "the owner ended it"
		if ev.Reason == "owner_gone" {
			why = "the owner went offline"
		}
		m.commandMsg = fmt.Sprintf("%s ended: %s. Everything in it is gone.", name, why)

	case ws.EventTypeRoomMessage:
		m.handleRoomMessage(ev)

	case ws.EventTypeRoomAck:
		m.chatStore.SetSeq(roomThread(ev.Room), ev.Cid, ev.Seq)
	}
	return nil
}

func (m *Model) handleRoomMessage(ev ws.Event) {
	e := m.roomEntryFor(ev.Room)
	if e == nil || !e.joined {
		return
	}
	got, res := e.r.Receive(ev.From, ev.Seq, ev.Payload)
	switch res {
	case room.Accepted:
		m.showRoomMessage(e, got)
	case room.Rejected:
		m.commandMsg = fmt.Sprintf("Dropped a message in %s that didn't verify.", e.r.Name)
	}
}

// showRoomMessage puts an accepted message in the thread.
func (m *Model) showRoomMessage(e *roomEntry, got room.Received) {
	th := roomThread(e.r.ID)
	author := "someone"
	if p, ok := e.r.Roster[got.From]; ok {
		author = p.Username
	}
	m.chatStore.AppendOrdered(th, chat.Message{
		Body:   []byte(got.Msg.Body),
		At:     time.Now(),
		ID:     got.Msg.ID,
		Author: author,
		Quote:  m.quoteFor(th, got.Msg.Re),
		Seq:    got.Seq,
	})
	m.noteIncoming(th)
	if !m.viewingThread(th) {
		m.noteUnread(th, false)
		m.commandMsg = fmt.Sprintf("New message in %s - press 3 for Chat", m.roomLabel(e.r.ID))
	}
}

// quoteFor builds the one-line excerpt shown above a reply: who wrote the
// message being answered and the start of what they said. "" if the
// message isn't known (it was before this client joined, say).
func (m Model) quoteFor(th chat.ContactKey, id string) string {
	target, ok := m.chatStore.FindByID(th, id)
	if !ok {
		return ""
	}
	who := target.Author
	if target.Outgoing {
		who = "you"
	}
	text := strings.Join(strings.Fields(sanitizeForTerminal(string(target.Body))), " ")
	return truncateToWidth(who+": "+text, 60)
}

// --- key and roster distribution (owner) ---

// distributeRoomState sends the current key and roster to every member
// other than the owner, each over their own pairwise encrypted channel.
func (m *Model) distributeRoomState(e *roomEntry) tea.Cmd {
	st, err := e.r.State()
	if err != nil {
		m.commandMsg = fmt.Sprintf("Couldn't prepare the %s's key: %v", roomNoun, err)
		return nil
	}
	wire, err := room.EncodeState(st)
	if err != nil {
		m.commandMsg = fmt.Sprintf("Couldn't prepare the %s's key: %v", roomNoun, err)
		return nil
	}
	var cmds []tea.Cmd
	for id := range e.r.Roster {
		if id == e.r.OwnerID {
			continue
		}
		p, ok := e.peers[id]
		if !ok {
			continue
		}
		if c := m.sendMessage(p.contact, wire); c != nil {
			cmds = append(cmds, c)
		}
	}
	return tea.Batch(cmds...)
}

// receiveRoomControl handles a room control message that arrived over a
// pairwise channel from contact. Only the room's owner may change a
// room's state; room.ApplyState enforces that against the sender's user id.
func (m *Model) receiveRoomControl(contact chat.ContactKey, plaintext []byte) tea.Cmd {
	st, err := room.DecodeState(plaintext)
	if err != nil {
		m.commandMsg = fmt.Sprintf("Ignored a malformed %s message from %s.", roomNoun, m.chatContactLabel(contact))
		return nil
	}
	e := m.roomEntryFor(st.Room)
	if e == nil || e.owner {
		// A room we don't know about (or one we own): not ours to join
		// or change because someone sent us bytes.
		return nil
	}
	from, ok := m.recipientUserID(contact)
	if !ok {
		return nil
	}
	released, err := e.r.ApplyState(from, st)
	if err != nil {
		m.commandMsg = fmt.Sprintf("Ignored a %s update from %s: %v", roomNoun, m.chatContactLabel(contact), err)
		return nil
	}
	if !e.ready {
		e.ready = true
		m.roomSay(e.r.ID, fmt.Sprintf("Connected: the owner's key arrived. %d in the %s; messages are end-to-end encrypted.", len(e.r.Roster), roomNoun))
	}
	for _, got := range released {
		m.showRoomMessage(e, got)
	}
	return nil
}

// --- sending ---

// sendRoomText sends the draft in the open room thread.
func (m Model) sendRoomText(id, body string) (tea.Model, tea.Cmd) {
	e := m.roomEntryFor(id)
	if e == nil || !e.joined || !e.r.Ready() {
		m.commandMsg = "Waiting for the owner's key - you can write as soon as it arrives."
		return m, nil
	}
	if len(body) > room.MaxBodyBytes {
		m.commandMsg = fmt.Sprintf("That message is %d bytes; a %s message can be at most %d.", len(body), roomNoun, room.MaxBodyBytes)
		return m, nil
	}
	if m.wsClient == nil || m.identity == nil {
		m.commandMsg = "Not connected to synq-server - that wasn't sent."
		return m, nil
	}
	th := roomThread(id)
	re, quote := "", ""
	if target, ok := m.replyTarget(e); ok {
		re = target.ID
		quote = m.quoteFor(th, target.ID)
	}
	mid := room.NewMessageID()
	sealed, err := room.Seal(e.r.Key, id, e.r.Gen, m.identity.SigningPrivate, room.Message{
		ID: mid, Re: re, Body: body, TS: time.Now().Unix(),
	})
	if err != nil {
		m.commandMsg = fmt.Sprintf("Couldn't encrypt that message: %v", err)
		return m, nil
	}
	frame, err := ws.EncodeRoomSend(id, mid, sealed)
	if err != nil {
		m.commandMsg = fmt.Sprintf("Couldn't build a frame: %v", err)
		return m, nil
	}
	e.r.MarkSent(mid)
	e.replyBack = 0
	m.chatInput, m.chatBack = "", 0
	m.chatStore.AppendOrdered(th, chat.Message{
		Body:     []byte(body),
		At:       time.Now(),
		Outgoing: true,
		ID:       mid,
		Author:   m.username,
		Quote:    quote,
	})
	m.chatScroll, m.chatScrollFor = 0, th
	client := m.wsClient
	return m, func() tea.Msg {
		if err := client.Send(frame); err != nil {
			return roomSendFailedMsg{id: id, mid: mid, err: err}
		}
		return nil
	}
}

func (m Model) handleRoomSendFailed(msg roomSendFailedMsg) (tea.Model, tea.Cmd) {
	if m.roomEntryFor(msg.id) != nil {
		m.roomSay(msg.id, "⚠ A message you just sent didn't reach synq-server.")
	}
	m.commandMsg = fmt.Sprintf("That didn't reach synq-server: %v", msg.err)
	return m, nil
}

// --- replies ---

// replyCandidates are the messages a reply can point at, newest first.
func (m Model) replyCandidates(th chat.ContactKey) []chat.Message {
	n := m.chatStore.Len(th)
	recent := m.chatStore.Slice(th, max(n-200, 0), n)
	var out []chat.Message
	for i := len(recent) - 1; i >= 0; i-- {
		if !recent[i].System && recent[i].ID != "" {
			out = append(out, recent[i])
		}
	}
	return out
}

// replyTarget is the message the draft is currently a reply to.
func (m Model) replyTarget(e *roomEntry) (chat.Message, bool) {
	if e.replyBack <= 0 {
		return chat.Message{}, false
	}
	c := m.replyCandidates(roomThread(e.r.ID))
	if e.replyBack > len(c) {
		return chat.Message{}, false
	}
	return c[e.replyBack-1], true
}

// cycleReply steps Ctrl+R: newest message, the one before it, ... then off.
func (m *Model) cycleReply(id string) {
	e := m.roomEntryFor(id)
	if e == nil {
		return
	}
	n := len(m.replyCandidates(roomThread(id)))
	e.replyBack++
	if e.replyBack > n {
		e.replyBack = 0
	}
}

// cancelReply drops a reply selection; it reports whether there was one.
func (m *Model) cancelReply(id string) bool {
	e := m.roomEntryFor(id)
	if e == nil || e.replyBack == 0 {
		return false
	}
	e.replyBack = 0
	return true
}

// --- screen ---

func (m Model) roomStatusLines(id string, w int) []string {
	t := m.theme
	e := m.roomEntryFor(id)
	switch {
	case e == nil:
		return []string{t.Muted.Render(truncateToWidth("○ this "+roomNoun+" has ended", w))}
	case !e.joined:
		return []string{t.Muted.Render(truncateToWidth(fmt.Sprintf("◌ you're invited - :%s accept %s to join, :%s leave %s to decline", roomCommandName, e.r.Name, roomCommandName, e.r.Name), w))}
	case m.wsClient == nil:
		return []string{t.Muted.Render(truncateToWidth("○ offline - nothing you write is sent until the connection is back", w))}
	case !e.r.Ready():
		return []string{t.Muted.Render(truncateToWidth("◌ waiting for the owner's key - you can write as soon as it arrives", w))}
	}
	text := fmt.Sprintf("● end-to-end encrypted · %d here", len(e.r.Roster))
	if e.owner {
		if p := m.pendingInvites(e); p > 0 {
			text += fmt.Sprintf(" · %d invited", p)
		}
		text += " · you own this " + roomNoun
	}
	return []string{lipgloss.NewStyle().
		Foreground(t.Success).
		Background(t.Palette.Background).
		Render(truncateToWidth(text, w))}
}

func (m Model) roomHint(id string) string {
	e := m.roomEntryFor(id)
	if e != nil {
		if target, ok := m.replyTarget(e); ok {
			who := target.Author
			if target.Outgoing {
				who = "yourself"
			}
			snippet := truncateToWidth(strings.Join(strings.Fields(sanitizeForTerminal(string(target.Body))), " "), 40)
			return fmt.Sprintf("↪ replying to %s: %s · ctrl+r older · esc cancel", who, snippet)
		}
	}
	return "enter send · ctrl+r reply · ctrl+p commands · esc back to list"
}

// --- quitting ---

// requestQuit is every quit path's first step. A client that owns an open
// room asks first, because quitting ends the room for everyone.
func (m Model) requestQuit() (tea.Model, tea.Cmd) {
	if len(m.ownedRooms()) > 0 && !m.quitPrompt {
		m.quitPrompt = true
		m.commandMsg = m.quitPromptText()
		return m, nil
	}
	return m.doQuit()
}

func (m Model) quitPromptText() string {
	var names []string
	for _, e := range m.ownedRooms() {
		names = append(names, e.r.Name)
	}
	return fmt.Sprintf("You own %s: %s. Quitting ends it for everyone and wipes everything said in it. Quit anyway? y = quit and end · n or esc = stay",
		pluralRooms(len(names)), strings.Join(names, ", "))
}

func pluralRooms(n int) string {
	if n == 1 {
		return "an open " + roomNoun
	}
	return fmt.Sprintf("%d open %ss", n, roomNoun)
}

func (m Model) updateQuitPrompt(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		m.quitPrompt = false
		return m.doQuit()
	case "n", "N", "esc":
		m.quitPrompt = false
		m.commandMsg = "Staying."
		return m, nil
	}
	m.commandMsg = m.quitPromptText()
	return m, nil
}

// doQuit leaves or ends every room (best effort, bounded), closes the
// connection and exits, purging all state.
func (m Model) doQuit() (tea.Model, tea.Cmd) {
	m.quitting = true
	leave := m.leaveRoomsCmd()
	if m.wsClient != nil {
		m.wsClient.Close()
	}
	m.chatStore.Purge()
	m.wipeRooms()
	quit := m.quitCmd()
	if leave == nil {
		return m, quit
	}
	return m, tea.Sequence(leave, quit)
}

type roomExit struct {
	id    string
	owner bool
}

// leaveRoomsCmd ends the rooms this client owns and leaves the rest, with
// one short deadline for all of it so a dead network can't hold the
// program open. If it doesn't get through, the server's owner lease ends
// an owned room within ROOM_OWNER_GRACE_SECS anyway.
func (m Model) leaveRoomsCmd() tea.Cmd {
	if len(m.rooms) == 0 || m.apiClient == nil || m.accessToken == "" {
		return nil
	}
	var exits []roomExit
	for id, e := range m.rooms {
		exits = append(exits, roomExit{id: id, owner: e.owner && e.joined})
	}
	client, token := m.apiClient, m.accessToken
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		for _, x := range exits {
			if x.owner {
				_ = client.EndRoom(ctx, token, x.id)
			} else {
				_ = client.LeaveRoom(ctx, token, x.id)
			}
		}
		return nil
	}
}

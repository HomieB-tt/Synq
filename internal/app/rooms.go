package app

import (
	"context"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/HomieB-tt/synq/internal/api"
	"github.com/HomieB-tt/synq/internal/chat"
	"github.com/HomieB-tt/synq/internal/room"
	"github.com/HomieB-tt/synq/internal/transfer"
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
	roomNoun        = "synapse"
	roomCommandName = "synapse"
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
	box      string // hex X25519, for the direct channel
}

// roomEntry is one room as this client knows it.
type roomEntry struct {
	r      *room.Room
	owner  bool
	joined bool // false while only invited
	ready  bool // the "you can talk" line has been shown

	// peers is the owner's table of people it invited, by user id.
	peers map[string]roomPeer

	// ownerBox and ownerSign are the owner's keys as this client looked
	// them up (and verified) when accepting the invitation. The first state
	// has to agree with them; after that the roster carries the keys.
	ownerBox, ownerSign string

	// offeredTo is the member the owner has offered the room to (owner
	// side); offeredToMe is set while the owner has offered it to this
	// client (member side).
	offeredTo   string
	offeredToMe bool

	// removed remembers who the owner just removed, so their departure
	// reads as a removal rather than someone leaving.
	removed map[string]bool

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
// "leave", "end", "kick", "offer", "cancel" (withdraw an offer), "decline"
// (refuse one), "take" (accept one) or "drop" (leave-or-end, nothing
// shown). userID is who a kick or offer was about.
type roomOpMsg struct {
	op       string
	id       string
	username string
	userID   string
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
	return "# " + roomNoun
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

const roomUsage = "Usage: :" + roomCommandName + " new <name> | invite <username> | accept [name] | decline | leave [name] | kick <username> | transfer <username|cancel> | end [name] confirm | members | connect <username> | list"

func (m *Model) roomCommand(args []string) (string, tea.Cmd) {
	if m.identity == nil {
		return "Create an identity first. Restart Synq and choose \"Create your identity.\"", nil
	}
	if m.apiClient == nil {
		return "Synapses need synq-server configured. Set SYNQ_SERVER_URL and restart Synq (see README.md).", nil
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
		if e.joined && e.offeredToMe {
			return fmt.Sprintf("Taking over %s...", e.r.Name), m.roomOpCmd("take", e.r.ID, "")
		}
		if e.joined {
			return fmt.Sprintf("You're already in %s.", e.r.Name), nil
		}
		// The room key will come from the owner, sealed to the owner's
		// X25519 key, so that key is looked up and checked first - the
		// server's word for it isn't enough.
		owner := m.usernameForID(e.r.OwnerID)
		if owner == "" {
			return fmt.Sprintf("Can't tell who owns %s yet - refreshing your nodes; try :%s accept again in a moment.", e.r.Name, roomCommandName), m.fetchNodesCmd()
		}
		id := e.r.ID
		return fmt.Sprintf("Checking %s's keys...", owner), func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), apiHTTPTimeout)
			defer cancel()
			keys, err := client.GetPublicKeys(ctx, token, owner)
			return chatResolveResultMsg{username: owner, keys: keys, err: err, roomAccept: id}
		}

	case "decline":
		e, why := m.findRoom(strings.Join(args[1:], " "))
		if e == nil {
			return why, nil
		}
		if e.offeredToMe {
			return fmt.Sprintf("Declining the offer of %s...", e.r.Name), m.roomOpCmd("decline", e.r.ID, "")
		}
		if e.owner {
			return fmt.Sprintf("You own this %s, so you can't just leave it: :%s end confirm ends it for everyone.", roomNoun, roomCommandName), nil
		}
		return fmt.Sprintf("Leaving %s...", e.r.Name), m.roomOpCmd("leave", e.r.ID, "")

	case "kick", "remove":
		if len(args) != 2 {
			return fmt.Sprintf("Usage: :%s kick <username>", roomCommandName), nil
		}
		e, why := m.findRoom("")
		if e == nil {
			return why, nil
		}
		if !e.owner {
			return fmt.Sprintf("Only the owner of a %s can remove people.", roomNoun), nil
		}
		id, username, ok := m.roomMemberNamed(e, args[1], true)
		if !ok {
			return fmt.Sprintf("%s isn't in this %s.", args[1], roomNoun), nil
		}
		if id == e.r.OwnerID {
			return fmt.Sprintf("That's you. :%s end confirm ends the %s.", roomCommandName, roomNoun), nil
		}
		return fmt.Sprintf("Removing %s...", username), m.roomUserOpCmd("kick", e.r.ID, username, id)

	case "transfer", "handover", "give":
		if len(args) != 2 {
			return fmt.Sprintf("Usage: :%s transfer <username> | :%s transfer cancel", roomCommandName, roomCommandName), nil
		}
		e, why := m.findRoom("")
		if e == nil {
			return why, nil
		}
		if !e.owner {
			return fmt.Sprintf("Only the owner of a %s can hand it over.", roomNoun), nil
		}
		if strings.EqualFold(args[1], "cancel") {
			if e.offeredTo == "" {
				return "No ownership offer is open.", nil
			}
			return "Withdrawing the offer...", m.roomOpCmd("cancel", e.r.ID, "")
		}
		id, username, ok := m.roomMemberNamed(e, args[1], false)
		if !ok {
			return fmt.Sprintf("%s isn't in this %s yet - ownership can only go to someone who has joined.", args[1], roomNoun), nil
		}
		if id == e.r.OwnerID {
			return fmt.Sprintf("You already own this %s.", roomNoun), nil
		}
		return fmt.Sprintf("Offering %s to %s...", e.r.Name, username), m.roomUserOpCmd("offer", e.r.ID, username, id)

	case "leave":
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

	case "connect":
		// The one way two members who are not nodes get to each other:
		// a node request, sent only to someone you can see in the
		// roster. Nobody is ever added to a synapse by a stranger, and
		// this does not skip the other side's say - they still accept.
		if len(args) != 2 {
			return fmt.Sprintf("Usage: :%s connect <username>", roomCommandName), nil
		}
		e, why := m.findRoom("")
		if e == nil {
			return why, nil
		}
		_, username, ok := m.roomMemberNamed(e, args[1], false)
		if !ok {
			return fmt.Sprintf("%s isn't in this %s.", args[1], roomNoun), nil
		}
		if username == m.username {
			return "That's you.", nil
		}
		return m.requestNode(username)

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

// roomMemberNamed finds someone in the room by username (any case). With
// includeInvited it also finds people the owner invited who haven't joined.
func (m Model) roomMemberNamed(e *roomEntry, name string, includeInvited bool) (id, username string, ok bool) {
	for pid, p := range e.r.Roster {
		if strings.EqualFold(p.Username, name) {
			return pid, p.Username, true
		}
	}
	if includeInvited {
		for pid, p := range e.peers {
			if strings.EqualFold(p.username, name) {
				return pid, p.username, true
			}
		}
	}
	return "", "", false
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
	return m.roomUserOpCmd(op, id, username, "")
}

func (m Model) roomUserOpCmd(op, id, username, userID string) tea.Cmd {
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
		case "kick":
			err = client.KickFromRoom(ctx, token, id, userID)
		case "offer":
			err = client.OfferRoomTransfer(ctx, token, id, userID)
		case "cancel", "decline":
			err = client.CancelRoomTransfer(ctx, token, id)
		case "take":
			err = client.AcceptRoomTransfer(ctx, token, id)
		case "drop":
			// Get out of a room without knowing whether it is ours: a
			// member can leave; the owner is told to end it instead.
			err = client.LeaveRoom(ctx, token, id)
			if api.IsConflict(err) {
				err = client.EndRoom(ctx, token, id)
			}
		}
		return roomOpMsg{op: op, id: id, username: username, userID: userID, err: err}
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
		Box:      hex.EncodeToString(m.identity.BoxPublic[:]),
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
	// room key will be sealed to this person's box key, so it has to be
	// one this device trusts.
	_, outcome, fail := m.acceptResolved(msg)
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
		box:      msg.keys.BoxPubKey,
	}
	m.commandMsg = fmt.Sprintf("Inviting %s...", msg.username)
	return m, m.roomOpCmd("invite", e.r.ID, msg.username)
}

// handleRoomAcceptResolved is the second half of :room accept: the owner's
// keys came back. They get the same checks as for :chat, and must belong to
// the user the server says owns the room; only then is the invitation
// accepted, with the owner's box key remembered for opening their key.
func (m Model) handleRoomAcceptResolved(msg chatResolveResultMsg) (tea.Model, tea.Cmd) {
	e := m.roomEntryFor(msg.roomAccept)
	if e == nil || e.joined {
		return m, nil
	}
	if msg.err != nil {
		m.commandMsg = fmt.Sprintf("Couldn't look up %s to join: %v", msg.username, msg.err)
		return m, nil
	}
	_, outcome, fail := m.acceptResolved(msg)
	if fail != "" {
		m.commandMsg = fail
		return m, nil
	}
	if outcome == pinChanged {
		m.commandMsg = fmt.Sprintf("%s's key CHANGED since you pinned it - not joining. Compare fingerprints with :verify %s, or :accept %s to trust the new key.",
			msg.username, msg.username, msg.username)
		return m, nil
	}
	if msg.keys.UserID != e.r.OwnerID {
		m.commandMsg = fmt.Sprintf("%s isn't who the server says owns this %s - not joining.", msg.username, roomNoun)
		return m, nil
	}
	e.ownerBox, e.ownerSign = msg.keys.BoxPubKey, msg.keys.PubKey
	m.commandMsg = fmt.Sprintf("Joining %s...", e.r.Name)
	return m, m.roomOpCmd("accept", e.r.ID, "")
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

	case "kick":
		if msg.err != nil {
			m.commandMsg = fmt.Sprintf("Couldn't remove %s: %v", msg.username, msg.err)
			return m, nil
		}
		if e != nil {
			if e.removed == nil {
				e.removed = map[string]bool{}
			}
			e.removed[msg.userID] = true
		}
		m.commandMsg = fmt.Sprintf("Removed %s.", msg.username)

	case "offer":
		if msg.err != nil {
			m.commandMsg = fmt.Sprintf("Couldn't offer the %s to %s: %v", roomNoun, msg.username, msg.err)
			return m, nil
		}
		if e != nil {
			e.offeredTo = msg.userID
			m.roomSay(msg.id, fmt.Sprintf("You offered %s to %s. You stay the owner until they :%s accept.", e.r.Name, msg.username, roomCommandName))
		}
		m.commandMsg = fmt.Sprintf("Offered to %s.", msg.username)

	case "cancel":
		if msg.err != nil && !api.IsNotFound(msg.err) {
			m.commandMsg = fmt.Sprintf("Couldn't withdraw the offer: %v", msg.err)
			return m, nil
		}
		if e != nil {
			e.offeredTo = ""
			m.roomSay(msg.id, "You withdrew the ownership offer.")
		}
		m.commandMsg = "Offer withdrawn."

	case "decline":
		if msg.err != nil && !api.IsNotFound(msg.err) {
			m.commandMsg = fmt.Sprintf("Couldn't decline: %v", msg.err)
			return m, nil
		}
		if e != nil {
			e.offeredToMe = false
			m.roomSay(msg.id, "You declined the ownership offer.")
		}
		m.commandMsg = "Declined."

	case "take":
		if msg.err != nil {
			m.commandMsg = fmt.Sprintf("Couldn't take over: %v", msg.err)
			return m, nil
		}
		// The server's room_owner_changed event does the rest.

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
			name = roomNoun
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
	m.purgeRoomFiles(id)
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
			name = roomNoun
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
		e.r.AddPeer(room.Peer{ID: p.userID, Username: p.username, SignKey: p.signKey, Box: p.box})
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
		if e.offeredTo == ev.User {
			e.offeredTo = ""
		}
		verb := "left"
		if e.removed[ev.User] {
			verb = "was removed"
			delete(e.removed, ev.User)
		}
		if e.owner && wasMember {
			// Whoever just went must not be able to read what comes next:
			// new key, to everyone still here.
			if err := e.r.Rekey(); err != nil {
				m.commandMsg = fmt.Sprintf("Couldn't change the %s's key: %v", roomNoun, err)
				return nil
			}
			m.roomSay(ev.Room, fmt.Sprintf("%s %s - the key was changed, so they can't read anything new.", name, verb))
			return m.distributeRoomState(e)
		}
		if wasMember || e.owner {
			m.roomSay(ev.Room, fmt.Sprintf("%s %s.", name, verb))
		}

	case ws.EventTypeRoomEnded:
		e := m.roomEntryFor(ev.Room)
		if e == nil {
			return nil
		}
		name := e.r.Name
		m.dropRoom(ev.Room)
		switch ev.Reason {
		case "kicked":
			m.commandMsg = fmt.Sprintf("You were removed from %s. Everything in it is gone from here.", name)
		case "invite_withdrawn":
			m.commandMsg = fmt.Sprintf("Your invitation to %s was withdrawn when the %s changed hands.", name, roomNoun)
		case "owner_gone":
			m.commandMsg = fmt.Sprintf("%s ended: the owner went offline. Everything in it is gone.", name)
		default:
			m.commandMsg = fmt.Sprintf("%s ended: the owner ended it. Everything in it is gone.", name)
		}

	case ws.EventTypeRoomDirect:
		m.handleRoomDirect(ev)

	case ws.EventTypeRoomTransferOffer:
		e := m.roomEntryFor(ev.Room)
		if e == nil || !e.joined || e.owner || ev.From != e.r.OwnerID {
			return nil
		}
		e.offeredToMe = true
		who := m.ownerLabel(ev.From)
		if p, ok := e.r.Roster[ev.From]; ok {
			who = sanitizeForTerminal(p.Username)
		}
		m.roomSay(ev.Room, fmt.Sprintf("%s wants to hand this %s to you. :%s accept makes you the owner; :%s decline refuses. Owning it means it ends if you quit.",
			who, roomNoun, roomCommandName, roomCommandName))
		m.noteUnread(roomThread(ev.Room), false)
		m.commandMsg = fmt.Sprintf("%s offered you %s - see the Chat tab (3), then :%s accept or :%s decline", who, e.r.Name, roomCommandName, roomCommandName)

	case ws.EventTypeRoomTransferCancelled:
		e := m.roomEntryFor(ev.Room)
		if e == nil {
			return nil
		}
		switch {
		case e.offeredToMe:
			e.offeredToMe = false
			m.roomSay(ev.Room, "The ownership offer was withdrawn.")
		case e.offeredTo != "":
			who := "They"
			if p, ok := e.r.Roster[e.offeredTo]; ok {
				who = p.Username
			}
			e.offeredTo = ""
			m.roomSay(ev.Room, fmt.Sprintf("%s declined the ownership offer. You're still the owner.", who))
		}

	case ws.EventTypeRoomOwnerChanged:
		return m.handleRoomOwnerChanged(ev)

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
	if got.Msg.File != nil {
		m.showRoomFile(e, got, author)
		return
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

// --- key and roster distribution ---

// sendDirect seals plain for one member of the room (static X25519 keys,
// see room.SealDirect) and sends it as a direct room frame. The server only
// delivers it between two joined members.
func (m Model) sendDirect(roomID, toID, toBox string, plain []byte) tea.Cmd {
	if m.wsClient == nil || m.identity == nil {
		return nil
	}
	theirs, err := decodeHexBoxPublicKey(toBox)
	if err != nil {
		return nil
	}
	sealed, err := room.SealDirect(m.identity.BoxPrivate, theirs, roomID, plain)
	if err != nil {
		return nil
	}
	frame, err := ws.EncodeRoomDirect(roomID, toID, sealed)
	if err != nil {
		return nil
	}
	client := m.wsClient
	return func() tea.Msg {
		if err := client.Send(frame); err != nil {
			return roomSendFailedMsg{id: roomID, err: err}
		}
		return nil
	}
}

// distributeRoomState sends the current key and roster to every member
// other than the owner, each sealed for that member alone.
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
	for id, p := range e.r.Roster {
		if id == e.r.OwnerID {
			continue
		}
		if c := m.sendDirect(e.r.ID, id, p.Box, wire); c != nil {
			cmds = append(cmds, c)
		}
	}
	return tea.Batch(cmds...)
}

// directBoxOf is the box key to use for a direct payload from `from`: the
// roster's, or - before the first state - the owner's as looked up when the
// invitation was accepted. Anyone else has none and can't be opened.
func (m Model) directBoxOf(e *roomEntry, from string) (*[32]byte, bool) {
	hexKey := ""
	if p, ok := e.r.Roster[from]; ok {
		hexKey = p.Box
	} else if from == e.r.OwnerID {
		hexKey = e.ownerBox
	}
	if hexKey == "" {
		return nil, false
	}
	k, err := decodeHexBoxPublicKey(hexKey)
	return k, err == nil
}

// handleRoomDirect handles a payload another member sealed for us. Only the
// room's current owner may change its state; room.ApplyState enforces that
// against the sender's user id, and the seal proves the sender holds that
// user's box key.
func (m *Model) handleRoomDirect(ev ws.Event) {
	e := m.roomEntryFor(ev.Room)
	if e == nil || !e.joined || e.owner || m.identity == nil {
		return
	}
	theirs, ok := m.directBoxOf(e, ev.From)
	if !ok {
		return
	}
	plain, err := room.OpenDirect(m.identity.BoxPrivate, theirs, ev.Room, ev.Payload)
	if err != nil || !room.IsControl(plain) {
		return
	}
	st, err := room.DecodeState(plain)
	if err != nil || st.Room != e.r.ID {
		m.commandMsg = fmt.Sprintf("Ignored a malformed %s message.", roomNoun)
		return
	}
	if !e.ready && e.ownerBox != "" {
		// The first state must describe the owner by the keys we checked.
		for _, p := range st.Roster {
			if p.ID == st.Owner && (!strings.EqualFold(p.Box, e.ownerBox) || !strings.EqualFold(p.SignKey, e.ownerSign)) {
				m.commandMsg = fmt.Sprintf("Ignored a %s key that named the owner with different keys than you verified.", roomNoun)
				return
			}
		}
	}
	released, err := e.r.ApplyState(ev.From, st)
	if err != nil {
		m.commandMsg = fmt.Sprintf("Ignored a %s update: %v", roomNoun, err)
		return
	}
	if !e.ready {
		e.ready = true
		e.ownerBox, e.ownerSign = "", ""
		m.roomSay(e.r.ID, fmt.Sprintf("Connected: the owner's key arrived. %d in the %s; messages are end-to-end encrypted.", len(e.r.Roster), roomNoun))
	}
	for _, got := range released {
		m.showRoomMessage(e, got)
	}
}

// handleRoomOwnerChanged adopts the server's announcement that the room has
// a new owner - but only if the new owner is someone already on this
// client's roster; a bare announcement can't install a stranger.
func (m *Model) handleRoomOwnerChanged(ev ws.Event) tea.Cmd {
	e := m.roomEntryFor(ev.Room)
	if e == nil || !e.joined {
		return nil
	}
	if !e.r.Ready() {
		// Our key never arrived and we have no verified way to learn the new
		// owner's keys. Step out; the new owner can invite us again.
		m.commandMsg = fmt.Sprintf("%s changed hands before your key arrived, so you can't be keyed in. Leaving - ask to be invited again.", e.r.Name)
		return m.roomOpCmd("leave", ev.Room, "")
	}
	// Who we are has to be read before the owner changes: for an owner it is
	// the room's owner id.
	self := m.roomSelf(e)
	previous := e.r.OwnerID
	if err := e.r.SetOwner(ev.Owner); err != nil {
		m.commandMsg = fmt.Sprintf("Ignored an ownership change in %s: the new owner isn't someone you know here.", e.r.Name)
		return nil
	}
	e.offeredTo, e.offeredToMe = "", false
	e.peers = map[string]roomPeer{}
	newName := "someone"
	if p, ok := e.r.Roster[ev.Owner]; ok {
		newName = sanitizeForTerminal(p.Username)
	}
	switch {
	case ev.Owner == self:
		e.owner = true
		m.roomSay(ev.Room, fmt.Sprintf("You now own this %s. It ends if you quit or go offline, unless you hand it on.", roomNoun))
		m.commandMsg = fmt.Sprintf("You own %s now.", e.r.Name)
	case previous == self || e.owner:
		e.owner = false
		m.roomSay(ev.Room, fmt.Sprintf("%s now owns this %s. It no longer ends when you quit.", newName, roomNoun))
	default:
		m.roomSay(ev.Room, fmt.Sprintf("%s now owns this %s.", newName, roomNoun))
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
	if msg.mid == "" {
		// A key handed to a member, not a message.
		m.commandMsg = fmt.Sprintf("Couldn't reach synq-server to deliver the %s's key: %v", roomNoun, msg.err)
		return m, nil
	}
	if m.roomEntryFor(msg.id) != nil {
		m.roomSay(msg.id, "⚠ A message you just sent didn't reach synq-server.")
	}
	m.commandMsg = fmt.Sprintf("That didn't reach synq-server: %v", msg.err)
	return m, nil
}

// --- files ---

// sendRoomFileCmd starts `:send <path>` in an open room: encrypt, upload
// the ciphertext into the room, then (handleRoomFileSent) tell the members
// what it is inside a signed, sealed message.
func (m *Model) sendRoomFileCmd(path string) (string, tea.Cmd) {
	id, _ := roomIDOf(m.chatActive)
	e := m.roomEntryFor(id)
	switch {
	case e == nil || !e.joined:
		return fmt.Sprintf("Join the %s first.", roomNoun), nil
	case !e.r.Ready():
		return "Waiting for the owner's key - you can share a file as soon as it arrives.", nil
	case m.apiClient == nil || m.accessToken == "":
		return "Sending files needs you to be logged in to synq-server.", nil
	case m.wsClient == nil || m.identity == nil:
		return "Not connected to synq-server - that wasn't sent.", nil
	}
	path = strings.Trim(strings.TrimSpace(path), `"'`)
	path, err := transfer.ExpandHome(path)
	if err != nil || path == "" {
		return "Usage: :send <path to file>", nil
	}
	cache, err := m.ensureCache()
	if err != nil {
		return fmt.Sprintf("Couldn't prepare a scratch directory: %v", err), nil
	}
	client, token, scratch := m.apiClient, m.accessToken, cache.Root()
	tr, tick := m.beginTransfer("", filepath.Base(path), true)
	return "Encrypting and uploading…", tea.Batch(tick, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), fileTransferTimeout)
		defer cancel()
		ctx = transfer.WithProgress(ctx, tr.prog)
		sent, err := transfer.SendRoomFile(ctx, client, token, id, path, scratch)
		return roomFileSentMsg{room: id, sent: sent, err: err, unauthorized: api.IsUnauthorized(err), tr: tr}
	})
}

type roomFileSentMsg struct {
	room         string
	sent         *transfer.Sent
	err          error
	unauthorized bool
	tr           *activeTransfer
}

func (m Model) handleRoomFileSent(msg roomFileSentMsg) (tea.Model, tea.Cmd) {
	m.endTransfer(msg.tr)
	if msg.err != nil {
		switch {
		case msg.unauthorized:
			m.commandMsg = "Your session token expired - renewing it. Run :send again in a moment."
			return m, m.maybeReauthCmd()
		case api.IsConflict(msg.err):
			m.commandMsg = fmt.Sprintf("This %s is full of files (at most 20, or 300 MB in all). They're cleared when it ends.", roomNoun)
		case api.IsNotFound(msg.err):
			m.commandMsg = fmt.Sprintf("That %s has ended, or you're no longer in it.", roomNoun)
		default:
			m.commandMsg = fmt.Sprintf("Couldn't share the file: %v", msg.err)
		}
		return m, nil
	}
	e := m.roomEntryFor(msg.room)
	if e == nil || !e.joined || !e.r.Ready() || m.wsClient == nil || m.identity == nil {
		m.commandMsg = fmt.Sprintf("The %s ended before the file could be announced.", roomNoun)
		return m, nil
	}
	sent := msg.sent
	body := fmt.Sprintf("📎 %s (%s)", sent.Name, humanSize(sent.Size))
	mid := room.NewMessageID()
	sealed, err := room.Seal(e.r.Key, e.r.ID, e.r.Gen, m.identity.SigningPrivate, room.Message{
		ID:   mid,
		Body: body,
		File: &room.FileRef{ID: sent.AttachmentID, Name: sent.Name, Size: sent.Size, SHA: sent.Hash, Key: sent.Key[:]},
		TS:   time.Now().Unix(),
	})
	if err != nil {
		m.commandMsg = fmt.Sprintf("Uploaded, but couldn't announce the file: %v", err)
		return m, nil
	}
	frame, err := ws.EncodeRoomSend(e.r.ID, mid, sealed)
	if err != nil {
		m.commandMsg = fmt.Sprintf("Uploaded, but couldn't build the announcement: %v", err)
		return m, nil
	}
	e.r.MarkSent(mid)
	th := roomThread(e.r.ID)
	m.chatStore.AppendOrdered(th, chat.Message{
		Body:     []byte(body),
		At:       time.Now(),
		Outgoing: true,
		ID:       mid,
		Author:   m.username,
	})
	m.chatScroll, m.chatScrollFor = 0, th
	m.commandMsg = fmt.Sprintf("Shared %s. Everyone in the %s can fetch it until the %s ends.", sent.Name, roomNoun, roomNoun)
	client, id := m.wsClient, e.r.ID
	return m, func() tea.Msg {
		if err := client.Send(frame); err != nil {
			return roomSendFailedMsg{id: id, mid: mid, err: err}
		}
		return nil
	}
}

// showRoomFile lists a file a member shared. Nothing is downloaded until
// the user picks it in :files.
func (m *Model) showRoomFile(e *roomEntry, got room.Received, author string) {
	f := got.Msg.File
	th := roomThread(e.r.ID)
	if m.entryByID(f.ID) != nil {
		return
	}
	if len(m.files) >= maxFileEntries {
		m.commandMsg = fmt.Sprintf("Too many pending files - ignored one shared in %s.", e.r.Name)
		return
	}
	name := transfer.SafeName(f.Name)
	var key [32]byte
	copy(key[:], f.Key)
	label := sanitizeForTerminal(author) + " in # " + sanitizeForTerminal(e.r.Name)
	m.files = append(m.files, &fileEntry{
		roomID:  e.r.ID,
		contact: th,
		from:    label,
		offer:   &transfer.Offer{AttachmentID: f.ID, Name: name, Size: f.Size, CiphertextSHA256: f.SHA},
		key:     &key,
	})
	m.chatStore.AppendOrdered(th, chat.Message{
		Body:   fmt.Appendf(nil, "📎 %s (%s) - :files to view or save it", name, humanSize(f.Size)),
		At:     time.Now(),
		ID:     got.Msg.ID,
		Author: author,
		Seq:    got.Seq,
	})
	m.noteIncoming(th)
	if !m.viewingThread(th) {
		m.noteUnread(th, false)
		m.commandMsg = fmt.Sprintf("%s shared %s (%s) in %s - :files", author, name, humanSize(f.Size), m.roomLabel(e.r.ID))
	}
}

// purgeRoomFiles forgets every file shared in a room and deletes its
// decrypted copies. Files the user saved with s are outside the cache and
// stay: saving is the one way to keep one.
func (m *Model) purgeRoomFiles(id string) {
	kept := m.files[:0]
	for _, f := range m.files {
		if f.roomID != id {
			kept = append(kept, f)
			continue
		}
		if m.fileCache != nil {
			_ = m.fileCache.Remove(f.offer.AttachmentID)
		}
		f.key, f.path = nil, ""
	}
	for i := len(kept); i < len(m.files); i++ {
		m.files[i] = nil
	}
	m.files = kept
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
		if p, ok := e.r.Roster[e.offeredTo]; ok && e.offeredTo != "" {
			text += " · offered to " + sanitizeForTerminal(p.Username)
		}
	}
	if e.offeredToMe {
		text += fmt.Sprintf(" · the owner offered it to you: :%s accept / :%s decline", roomCommandName, roomCommandName)
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
	return fmt.Sprintf("You own %s: %s. Quitting ends it for everyone and wipes everything said in it - :%s transfer <username> hands it on first. Quit anyway? y = quit and end · n or esc = stay",
		pluralRooms(len(names)), strings.Join(names, ", "), roomCommandName)
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

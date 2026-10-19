package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/HomieB-tt/synq/internal/api"
	"github.com/HomieB-tt/synq/internal/chat"
	"github.com/HomieB-tt/synq/internal/db"
	"github.com/HomieB-tt/synq/internal/transfer"
)

// This file is the app half of file transfer (DESIGN.md §10): the
// server session that attachments hang off, `:send`, the `:files`
// picker, and `:savedir`. The protocol work itself - encryption,
// upload, download, verification - lives in internal/transfer, where it
// can be tested without a terminal; everything here is dispatch and
// presentation.

const (
	// fileHeartbeatEvery is how often the server session's lease is
	// renewed. The server's default lease is 60s, so this tolerates two
	// missed beats.
	fileHeartbeatEvery = 20 * time.Second

	// fileTransferTimeout bounds one whole upload or download.
	fileTransferTimeout = 30 * time.Minute

	// maxFileEntries caps how many offers one launch will list, so a
	// contact can't grow this list without bound.
	maxFileEntries = 100
)

// fileState is where one received file is in its life.
type fileState int

const (
	fileOffered     fileState = iota // offer received, nothing downloaded
	fileDownloading                  // claim/download/decrypt in progress
	fileReady                        // decrypted into the cache, awaiting a decision
	fileIgnored                      // declined; plaintext deleted
	fileFailed                       // download or verification failed
)

func (s fileState) label() string {
	switch s {
	case fileOffered:
		return "offered"
	case fileDownloading:
		return "downloading…"
	case fileReady:
		return "ready"
	case fileIgnored:
		return "ignored"
	default:
		return "failed"
	}
}

// fileEntry is one offer received from a contact. The key never leaves
// this struct (and so this process): it is what opens the download.
type fileEntry struct {
	contact chat.ContactKey
	from    string
	offer   *transfer.Offer
	key     *[32]byte
	state   fileState

	path  string // decrypted file in the cache, once ready
	note  string // last result or error, shown under the list
	after rune   // 'v' or 's': what to do as soon as the download finishes
}

// --- server session ---

type fileSessionMsg struct {
	id           string
	err          error
	unauthorized bool
}

type fileHeartbeatTickMsg struct{ session string }

type fileHeartbeatResultMsg struct {
	session      string
	err          error
	unauthorized bool
	gone         bool // the server no longer knows this session
}

func createFileSessionCmd(apiClient *api.Client, token string) tea.Cmd {
	if apiClient == nil || token == "" {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), apiHTTPTimeout)
		defer cancel()
		s, err := apiClient.CreateSession(ctx, token)
		if err != nil {
			return fileSessionMsg{err: err, unauthorized: api.IsUnauthorized(err)}
		}
		return fileSessionMsg{id: s.ID}
	}
}

func fileHeartbeatTickCmd(session string) tea.Cmd {
	return tea.Tick(fileHeartbeatEvery, func(time.Time) tea.Msg {
		return fileHeartbeatTickMsg{session: session}
	})
}

func fileHeartbeatCmd(apiClient *api.Client, token, session string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), apiHTTPTimeout)
		defer cancel()
		err := apiClient.Heartbeat(ctx, token, session)
		return fileHeartbeatResultMsg{
			session:      session,
			err:          err,
			unauthorized: api.IsUnauthorized(err),
			gone:         api.IsNotFound(err),
		}
	}
}

func (m Model) handleFileSession(msg fileSessionMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		if msg.unauthorized {
			return m, m.maybeReauthCmd()
		}
		// Not fatal and not worth interrupting for: sending and
		// receiving files will say so when attempted.
		return m, nil
	}
	if m.accessToken == "" {
		return m, nil // logged out while the request was out
	}
	m.fileSession = msg.id
	return m, fileHeartbeatTickCmd(msg.id)
}

func (m Model) handleFileHeartbeatTick(msg fileHeartbeatTickMsg) (tea.Model, tea.Cmd) {
	if msg.session != m.fileSession || m.apiClient == nil || m.accessToken == "" {
		return m, nil // a superseded or finished chain: let it die
	}
	return m, fileHeartbeatCmd(m.apiClient, m.accessToken, msg.session)
}

func (m Model) handleFileHeartbeatResult(msg fileHeartbeatResultMsg) (tea.Model, tea.Cmd) {
	if msg.session != m.fileSession {
		return m, nil
	}
	switch {
	case msg.gone:
		// The lease lapsed (a long sleep, a dead network). Everything
		// this launch had uploaded is already gone server-side; start
		// afresh rather than keep beating a dead session.
		m.fileSession = ""
		return m, createFileSessionCmd(m.apiClient, m.accessToken)
	case msg.unauthorized:
		return m, tea.Batch(m.maybeReauthCmd(), fileHeartbeatTickCmd(msg.session))
	default:
		// Transient errors: just keep beating.
		return m, fileHeartbeatTickCmd(msg.session)
	}
}

// startFileSession is what login, registration and startup call once a
// token exists.
func (m Model) startFileSession() tea.Cmd {
	return createFileSessionCmd(m.apiClient, m.accessToken)
}

// quitCmd is every quit path's exit: it deletes the decrypted-file cache
// synchronously, then ends the server session (which deletes anything
// this launch uploaded) with a short deadline so a dead network can't
// hold the program open.
func (m *Model) quitCmd() tea.Cmd {
	if m.fileCache != nil {
		_ = m.fileCache.Close()
		m.fileCache = nil
	}
	m.files = nil
	if m.fileSession == "" || m.apiClient == nil || m.accessToken == "" {
		return tea.Quit
	}
	client, token, session := m.apiClient, m.accessToken, m.fileSession
	m.fileSession = ""
	terminate := func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = client.TerminateSession(ctx, token, session, "user_quit")
		return nil
	}
	return tea.Sequence(terminate, tea.Quit)
}

// ensureCache creates the private decrypted-file cache on first use.
func (m *Model) ensureCache() (*transfer.Cache, error) {
	if m.fileCache != nil {
		return m.fileCache, nil
	}
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	c, err := transfer.NewCache(base)
	if err != nil {
		return nil, err
	}
	m.fileCache = c
	return c, nil
}

// --- :send ---

type fileSendResultMsg struct {
	contact      chat.ContactKey
	sent         *transfer.Sent
	err          error
	unauthorized bool
	tr           *activeTransfer
}

// sendFileCmd starts `:send <path>` for the open thread's contact.
func (m *Model) sendFileCmd(path string) (string, tea.Cmd) {
	switch {
	case m.activeTab != tabChat || m.chatActive == "":
		return "Open a chat first (:chat <username>), then :send <path>.", nil
	case isRoomThread(m.chatActive):
		return "Files can't be shared in a room yet - send one in a 1:1 chat for now.", nil
	case m.apiClient == nil || m.accessToken == "":
		return "Sending files needs you to be logged in to synq-server.", nil
	case m.fileSession == "":
		return "Not ready to send files yet - the transfer session isn't up. Try again in a moment.", nil
	}
	if reason := m.keyBlockReason(m.chatActive); reason != "" {
		return reason, nil
	}
	to, ok := m.recipientUserID(m.chatActive)
	if !ok {
		return fmt.Sprintf("No user id on file for %s - run :chat %s again to look them up.", m.chatContactLabel(m.chatActive), m.chatContactLabel(m.chatActive)), nil
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

	client, token, session, contact := m.apiClient, m.accessToken, m.fileSession, m.chatActive
	scratch := cache.Root()
	tr, tick := m.beginTransfer("", filepath.Base(path), true)
	return "Encrypting and uploading…", tea.Batch(tick, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), fileTransferTimeout)
		defer cancel()
		ctx = transfer.WithProgress(ctx, tr.prog)
		sent, err := transfer.SendFile(ctx, client, token, session, to, path, scratch)
		return fileSendResultMsg{contact: contact, sent: sent, err: err, unauthorized: api.IsUnauthorized(err), tr: tr}
	})
}

func (m Model) handleFileSendResult(msg fileSendResultMsg) (tea.Model, tea.Cmd) {
	m.endTransfer(msg.tr)
	if msg.err != nil {
		switch {
		case msg.unauthorized:
			m.commandMsg = "Your session token expired - renewing it. Run :send again in a moment."
			return m, m.maybeReauthCmd()
		case api.IsNotFound(msg.err):
			m.commandMsg = "synq-server refused that: you can only send files to active nodes."
		default:
			m.commandMsg = fmt.Sprintf("Couldn't send the file: %v", msg.err)
		}
		return m, nil
	}

	// The upload is done; now tell the contact, over the encrypted chat
	// channel, what it is and how to open it. This is the only place the
	// file's key and name go.
	body, err := transfer.EncodeOffer(msg.sent.AttachmentID, msg.sent.Name, msg.sent.Size, msg.sent.Hash, msg.sent.Key)
	if err != nil {
		m.commandMsg = fmt.Sprintf("Uploaded, but couldn't build the offer: %v", err)
		return m, nil
	}
	m.chatStore.Append(msg.contact, chat.Message{
		Body:     fmt.Appendf(nil, "📎 sent %s (%s)", msg.sent.Name, humanSize(msg.sent.Size)),
		At:       time.Now(),
		Outgoing: true,
	})
	m.commandMsg = fmt.Sprintf("Sent %s. They can fetch it while you stay connected - quitting removes it from the server.", msg.sent.Name)
	return m, m.sendMessage(msg.contact, body)
}

// --- receiving offers ---

// receiveOffer is called from handleSealed for a body that is a file
// offer. It lists the offer and tells the user; it downloads nothing -
// that waits for the user to pick the file.
func (m *Model) receiveOffer(contact chat.ContactKey, body []byte) {
	label := m.chatContactLabel(contact)
	offer, key, err := transfer.DecodeOffer(body)
	if err != nil {
		m.commandMsg = fmt.Sprintf("Ignored a malformed file offer from %s.", label)
		return
	}
	for _, e := range m.files {
		if e.offer.AttachmentID == offer.AttachmentID {
			return // the same offer again
		}
	}
	if len(m.files) >= maxFileEntries {
		m.commandMsg = fmt.Sprintf("Too many pending files - ignored an offer from %s.", label)
		return
	}
	m.files = append(m.files, &fileEntry{contact: contact, from: label, offer: offer, key: key})
	m.chatStore.Append(contact, chat.Message{
		Body: fmt.Appendf(nil, "📎 offered %s (%s) - :files to view or save it", offer.Name, humanSize(offer.Size)),
		At:   time.Now(),
	})
	m.noteIncoming(contact)
	m.noteUnread(contact, false) // the notice below says it better
	m.commandMsg = fmt.Sprintf("%s offered you %s (%s) - :files", label, offer.Name, humanSize(offer.Size))
}

func (m *Model) entryByID(id string) *fileEntry {
	for _, e := range m.files {
		if e.offer.AttachmentID == id {
			return e
		}
	}
	return nil
}

// --- download ---

type fileReceiveResultMsg struct {
	id           string
	path         string
	err          error
	unauthorized bool
	tr           *activeTransfer
}

func (m *Model) downloadCmd(e *fileEntry) tea.Cmd {
	if m.apiClient == nil || m.accessToken == "" {
		e.note = "Log in to synq-server to download."
		return nil
	}
	if m.fileSession == "" {
		e.note = "The transfer session isn't up yet - try again in a moment."
		return nil
	}
	cache, err := m.ensureCache()
	if err != nil {
		e.note = fmt.Sprintf("Couldn't prepare a download directory: %v", err)
		return nil
	}
	e.state = fileDownloading
	e.note = "Downloading and verifying…"

	client, token, session := m.apiClient, m.accessToken, m.fileSession
	offer, key := e.offer, e.key
	tr, tick := m.beginTransfer(offer.AttachmentID, offer.Name, false)
	return tea.Batch(tick, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), fileTransferTimeout)
		defer cancel()
		ctx = transfer.WithProgress(ctx, tr.prog)
		path, err := transfer.ReceiveFile(ctx, client, token, session, offer, key, cache)
		return fileReceiveResultMsg{id: offer.AttachmentID, path: path, err: err, unauthorized: api.IsUnauthorized(err), tr: tr}
	})
}

func (m Model) handleFileReceiveResult(msg fileReceiveResultMsg) (tea.Model, tea.Cmd) {
	m.endTransfer(msg.tr)
	e := m.entryByID(msg.id)
	if e == nil || e.state == fileIgnored {
		// Gone or ignored mid-download: don't resurrect it.
		if e != nil && m.fileCache != nil {
			_ = m.fileCache.Remove(msg.id)
		}
		return m, nil
	}
	if msg.err != nil {
		e.state = fileFailed
		e.after = 0
		switch {
		case msg.unauthorized:
			e.state = fileOffered // nothing was claimed; safe to retry
			e.note = "Your session token expired - renewing it. Try again in a moment."
			return m, m.maybeReauthCmd()
		case api.IsConflict(msg.err):
			e.note = "Not available yet (the sender may still be uploading) or already fetched."
		case api.IsNotFound(msg.err):
			e.note = "The sender is gone or the file expired - ask them to send it again."
		default:
			e.note = fmt.Sprintf("Failed: %v", msg.err)
		}
		return m, nil
	}

	e.state = fileReady
	e.path = msg.path
	e.note = "Downloaded and verified. v view · s save · i ignore"
	switch after := e.after; after {
	case 'v':
		e.after = 0
		return m, m.viewCmd(e)
	case 's':
		e.after = 0
		return m, m.saveCmd(e)
	}
	return m, nil
}

// --- view / save / ignore ---

type fileViewResultMsg struct {
	id  string
	err error
}

type fileSaveResultMsg struct {
	id   string
	path string
	err  error
}

// viewCmd opens a ready file in the user's default application for its
// type. Plaintext leaves synq's control at this point - the viewer reads
// the cache file, and may copy it elsewhere - which is what "view" means,
// and the reason it refuses anything an OS would run rather than show.
func (m *Model) viewCmd(e *fileEntry) tea.Cmd {
	if e.state != fileReady {
		return nil
	}
	if !transfer.CanView(e.offer.Name) {
		e.note = "This kind of file could run code if opened, so it can't be viewed from here. Save it (s) and open it yourself if you trust it."
		return nil
	}
	open := m.fileOpen
	if open == nil {
		open = transfer.OpenDefault
	}
	path, id := e.path, e.offer.AttachmentID
	client, token, session := m.apiClient, m.accessToken, m.fileSession
	e.note = "Opening in your default application…"
	return func() tea.Msg {
		if err := open(path); err != nil {
			return fileViewResultMsg{id: id, err: err}
		}
		// Best effort: tell the server it was viewed. A failure here
		// changes nothing for the user.
		if client != nil && token != "" && session != "" {
			ctx, cancel := context.WithTimeout(context.Background(), apiHTTPTimeout)
			defer cancel()
			_ = client.MarkOpened(ctx, token, id, session)
		}
		return fileViewResultMsg{id: id}
	}
}

func (m Model) handleFileViewResult(msg fileViewResultMsg) (tea.Model, tea.Cmd) {
	if e := m.entryByID(msg.id); e != nil {
		if msg.err != nil {
			e.note = fmt.Sprintf("Couldn't open it: %v", msg.err)
		} else {
			e.note = "Opened. s to keep it · i to discard it (it is deleted when you quit either way)."
		}
	}
	return m, nil
}

// saveCmd copies a ready file to the user's save directory.
func (m *Model) saveCmd(e *fileEntry) tea.Cmd {
	if e.state != fileReady {
		return nil
	}
	if m.saveDir == "" {
		e.note = "No save folder set. Choose one with :savedir <path>, then press s again."
		return nil
	}
	dir, src, name, id := m.saveDir, e.path, e.offer.Name, e.offer.AttachmentID
	return func() tea.Msg {
		path, err := transfer.SaveCopy(dir, src, name)
		return fileSaveResultMsg{id: id, path: path, err: err}
	}
}

func (m Model) handleFileSaveResult(msg fileSaveResultMsg) (tea.Model, tea.Cmd) {
	if e := m.entryByID(msg.id); e != nil {
		if msg.err != nil {
			e.note = fmt.Sprintf("Couldn't save it: %v", msg.err)
		} else {
			e.note = "Saved to " + msg.path
		}
	}
	return m, nil
}

// ignoreCmd discards a file: its decrypted copy is deleted now, and the
// server is told if the file had been fully received.
func (m *Model) ignoreCmd(e *fileEntry) tea.Cmd {
	wasReady := e.state == fileReady
	e.state = fileIgnored
	e.after = 0
	e.path = ""
	e.note = "Ignored - nothing was kept."
	if m.fileCache != nil {
		_ = m.fileCache.Remove(e.offer.AttachmentID)
	}
	if !wasReady || m.apiClient == nil || m.accessToken == "" || m.fileSession == "" {
		return nil
	}
	client, token, session, id := m.apiClient, m.accessToken, m.fileSession, e.offer.AttachmentID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), apiHTTPTimeout)
		defer cancel()
		_ = client.IgnoreAttachment(ctx, token, id, session)
		return nil
	}
}

// --- :files picker ---

func (m *Model) openFilePicker() string {
	if len(m.files) == 0 {
		return "No files yet. When a contact sends one it shows up here."
	}
	m.filePickerOpen = true
	if m.filePickerIndex >= len(m.files) {
		m.filePickerIndex = len(m.files) - 1
	}
	return ""
}

func (m Model) updateFilePicker(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	n := len(m.files)
	if n == 0 {
		m.filePickerOpen = false
		return m, nil
	}
	if m.filePickerIndex >= n {
		m.filePickerIndex = n - 1
	}
	e := m.files[m.filePickerIndex]

	switch msg.String() {
	case "esc", "ctrl+c", "q":
		m.filePickerOpen = false
		return m, nil
	case "up", "k":
		m.filePickerIndex = (m.filePickerIndex + n - 1) % n
	case "down", "j":
		m.filePickerIndex = (m.filePickerIndex + 1) % n

	case "enter", "d":
		if e.state == fileOffered || e.state == fileFailed {
			e.state = fileOffered
			return m, m.downloadCmd(e)
		}
		if e.state == fileReady {
			return m, m.viewCmd(e)
		}

	case "v", "s":
		action := rune(msg.String()[0])
		switch e.state {
		case fileOffered, fileFailed:
			// Choosing view or save on a file that hasn't been fetched
			// fetches it first and then does what was asked.
			e.state = fileOffered
			e.after = action
			return m, m.downloadCmd(e)
		case fileDownloading:
			e.after = action
			e.note = "Will " + map[rune]string{'v': "open", 's': "save"}[action] + " it as soon as it has downloaded."
		case fileReady:
			if action == 'v' {
				return m, m.viewCmd(e)
			}
			return m, m.saveCmd(e)
		}

	case "i", "x":
		if e.state != fileIgnored {
			return m, m.ignoreCmd(e)
		}
	}
	return m, nil
}

func (m Model) renderFilePicker() string {
	const title = "Files"
	const footerText = "↑/↓ select · enter download · v view · s save · i ignore · esc close"

	type row struct {
		text      string
		highlight bool
	}
	rows := make([]row, 0, len(m.files))
	for i, e := range m.files {
		text := fmt.Sprintf("%s  %s  from %s  [%s]",
			truncateRunes(e.offer.Name, 36), humanSize(e.offer.Size), truncateRunes(e.from, 16), m.fileStateLabel(e))
		if i == m.filePickerIndex {
			rows = append(rows, row{text: "› " + text, highlight: true})
		} else {
			rows = append(rows, row{text: "  " + text})
		}
	}

	status := ""
	if m.filePickerIndex < len(m.files) {
		status = m.files[m.filePickerIndex].note
	}
	saveLine := "Save folder: " + m.saveDir
	if m.saveDir == "" {
		saveLine = "Save folder: not set - :savedir <path>"
	}

	width := lipgloss.Width(footerText)
	for _, r := range rows {
		width = max(width, lipgloss.Width(r.text))
	}
	width = max(width, lipgloss.Width(saveLine))
	// The status line wraps instead of widening the box.
	statusLines := wrapText(status, width)

	pad := func(s string) string {
		if gap := width - lipgloss.Width(s); gap > 0 {
			s += strings.Repeat(" ", gap)
		}
		return s
	}

	lines := []string{m.theme.Warning.Render(pad(title)), ""}
	for _, r := range rows {
		if r.highlight {
			lines = append(lines, m.theme.TabActive.Render(pad(r.text)))
		} else {
			lines = append(lines, m.theme.Muted.Render(pad(r.text)))
		}
	}
	lines = append(lines, "")
	for _, l := range statusLines {
		lines = append(lines, m.theme.Warning.Render(pad(l)))
	}
	if len(statusLines) > 0 {
		lines = append(lines, "")
	}
	lines = append(lines, m.theme.Muted.Render(pad(saveLine)), m.theme.Muted.Render(pad(footerText)))

	return m.theme.Border.
		BorderForeground(m.theme.Palette.Accent).
		Padding(1, 3).
		Render(strings.Join(lines, "\n"))
}

// --- :savedir ---

func (m *Model) savedirCommand(arg string) string {
	arg = strings.Trim(strings.TrimSpace(arg), `"'`)
	if arg == "" {
		if m.saveDir == "" {
			return "No save folder set. Usage: :savedir <path>"
		}
		return "Files are saved to " + m.saveDir + ". Change it with :savedir <path>."
	}
	dir, err := transfer.ValidateSaveDir(arg)
	if err != nil {
		return fmt.Sprintf("Can't use that folder: %v", err)
	}
	m.saveDir = dir
	if err := m.store.SavePreference(db.PrefSaveDir, dir); err != nil {
		return fmt.Sprintf("Save folder set to %s, but couldn't remember it: %v", dir, err)
	}
	return "Save folder set to " + dir + "."
}

// --- small helpers ---

// humanSize formats a byte count the way a person reads it.
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}

// wrapText breaks s into lines of at most width cells, on spaces.
func wrapText(s string, width int) []string {
	if s == "" || width <= 0 {
		return nil
	}
	var lines []string
	line := ""
	for _, word := range strings.Fields(s) {
		switch {
		case line == "":
			line = word
		case lipgloss.Width(line)+1+lipgloss.Width(word) <= width:
			line += " " + word
		default:
			lines = append(lines, line)
			line = word
		}
	}
	return append(lines, line)
}

// sanitizeForTerminal removes control characters - including the ESC
// that begins every terminal escape sequence - from text a contact
// wrote before it is drawn. Without this a contact could move the
// cursor, rewrite the screen or set the window title from inside a chat
// message. Newlines and tabs become spaces so a message stays one row.
func sanitizeForTerminal(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t' || r == '\r':
			return ' '
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
			return -1
		}
		return r
	}, s)
}

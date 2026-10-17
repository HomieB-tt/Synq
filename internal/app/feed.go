package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/HomieB-tt/synq/internal/api"
	"github.com/HomieB-tt/synq/internal/db"
	"github.com/HomieB-tt/synq/internal/lineedit"
)

// This file is the Feed tab: the public post stream, a post's comments,
// and the modal composer for writing posts and comments. Reading works
// for guests (authors show as "node" - API.md, "Feed"); everything that
// writes needs a login.

const (
	feedPageSize = 50

	// Limits from API.md: title 1-120 characters, content 1-10000.
	maxTitleRunes   = 120
	maxContentRunes = 10000
)

var feedCategories = []string{api.CategoryGeneral, api.CategoryProject, api.CategoryHiring}

type composeKind int

const (
	composePost composeKind = iota
	composeComment
)

// composeState is the modal composer. It exists only while composeOpen.
type composeState struct {
	kind     composeKind
	postID   string // for a comment: the post it goes on
	category string // for a post
	title    string
	body     string
	// titleBack and bodyBack are the cursors: runes from the end of the
	// text, so a freshly set or cleared string has its cursor at the end
	// (see lineedit).
	titleBack int
	bodyBack  int
	field     int // 0 = title, 1 = body (a comment has only a body)
	err       string
	sending   bool
}

// --- loading ---

type feedLoadedMsg struct {
	posts  []api.Post
	offset int
	err    error
}

// fetchFeedCmd loads one page. A guest can read the Feed, so it needs only
// an API client; if the token turns out to be expired it retries as a
// guest rather than show nothing (the re-auth that follows restores
// real author names on the next refresh).
func (m Model) fetchFeedCmd(offset int) tea.Cmd {
	if m.apiClient == nil {
		return nil
	}
	client, token := m.apiClient, m.accessToken
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), apiHTTPTimeout)
		defer cancel()
		posts, err := client.ListFeed(ctx, token, feedPageSize, offset)
		if err != nil && api.IsUnauthorized(err) && token != "" {
			posts, err = client.ListFeed(ctx, "", feedPageSize, offset)
		}
		return feedLoadedMsg{posts: posts, offset: offset, err: err}
	}
}

func (m Model) handleFeedLoaded(msg feedLoadedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.feedErr = msg.err.Error()
		return m, nil
	}
	m.feedErr = ""
	m.feedLoaded = true
	if msg.offset == 0 {
		m.feedPosts = msg.posts
	} else {
		m.feedPosts = append(m.feedPosts, msg.posts...)
	}
	m.feedMore = len(msg.posts) == feedPageSize
	if n := len(m.feedVisible()); m.feedIndex >= n {
		m.feedIndex = max(n-1, 0)
	}
	// The first time the Feed ever loads there is no "last looked" to
	// compare with, so everything on it counts as seen; only what
	// arrives after this can be new.
	if m.feedSeenAt.IsZero() {
		m.markFeedSeen()
	}
	return m, nil
}

type commentsLoadedMsg struct {
	postID   string
	comments []api.Comment
	err      error
}

func (m Model) fetchCommentsCmd(postID string) tea.Cmd {
	if m.apiClient == nil {
		return nil
	}
	client, token := m.apiClient, m.accessToken
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), apiHTTPTimeout)
		defer cancel()
		cs, err := client.ListComments(ctx, token, postID)
		if err != nil && api.IsUnauthorized(err) && token != "" {
			cs, err = client.ListComments(ctx, "", postID)
		}
		return commentsLoadedMsg{postID: postID, comments: cs, err: err}
	}
}

func (m Model) handleCommentsLoaded(msg commentsLoadedMsg) (tea.Model, tea.Cmd) {
	if !m.feedOpen || m.openPost() == nil || m.openPost().ID != msg.postID {
		return m, nil // closed or moved on while it loaded
	}
	if msg.err != nil {
		m.feedCommentsErr = msg.err.Error()
		return m, nil
	}
	m.feedCommentsErr = ""
	m.feedComments = msg.comments
	m.feedCommentsLoaded = true
	return m, nil
}

func (m *Model) openPost() *api.Post {
	vis := m.feedVisible()
	if m.feedIndex < 0 || m.feedIndex >= len(vis) {
		return nil
	}
	return &vis[m.feedIndex]
}

// --- filtering and "new" ---

// feedFilters are the category filters in the order [ and ] cycle
// through them; "" is "all".
var feedFilters = []string{"", api.CategoryProject, api.CategoryHiring, api.CategoryGeneral}

// feedFilterLabel is how a filter is named in the bar and messages.
func feedFilterLabel(f string) string {
	switch f {
	case api.CategoryProject:
		return "Project"
	case api.CategoryHiring:
		return "Hiring"
	case api.CategoryGeneral:
		return "General"
	}
	return "All"
}

// feedVisible is the posts the list shows: all of them, or just the
// selected category. The filter is applied to the posts already fetched,
// so there is nothing to gain from asking the server again.
func (m Model) feedVisible() []api.Post {
	if m.feedFilter == "" {
		return m.feedPosts
	}
	var out []api.Post
	for _, p := range m.feedPosts {
		if p.Category == m.feedFilter {
			out = append(out, p)
		}
	}
	return out
}

// cycleFeedFilter moves to the next (+1) or previous (-1) category,
// wrapping, and puts the selection back at the top of the new list.
func (m *Model) cycleFeedFilter(dir int) {
	cur := 0
	for i, f := range feedFilters {
		if f == m.feedFilter {
			cur = i
		}
	}
	m.feedFilter = feedFilters[((cur+dir)%len(feedFilters)+len(feedFilters))%len(feedFilters)]
	m.feedIndex = 0
}

// feedIsNew reports whether a post was created after the user last
// looked at the Feed. With no baseline yet, or a timestamp that doesn't
// parse, nothing is new - a missing marker is better than a wrong one.
func (m Model) feedIsNew(p api.Post) bool {
	if m.feedSeenAt.IsZero() {
		return false
	}
	t, err := time.Parse(time.RFC3339Nano, p.CreatedAt)
	return err == nil && t.After(m.feedSeenAt)
}

// feedNewIn is how many of the given posts are new.
func (m Model) feedNewIn(posts []api.Post) int {
	n := 0
	for _, p := range posts {
		if m.feedIsNew(p) {
			n++
		}
	}
	return n
}

// markFeedSeen records that the user has now seen everything fetched so
// far, clearing the new markers, and remembers it for next launch. It
// runs when you leave the Feed tab and when you quit (see Update), and
// once at the first-ever load to set the baseline. Saving is best-effort.
func (m *Model) markFeedSeen() {
	var newest time.Time
	for _, p := range m.feedPosts {
		if t, err := time.Parse(time.RFC3339Nano, p.CreatedAt); err == nil && t.After(newest) {
			newest = t
		}
	}
	if !newest.After(m.feedSeenAt) {
		return
	}
	m.feedSeenAt = newest
	if m.store != nil {
		_ = m.store.SavePreference(db.PrefFeedSeen, newest.UTC().Format(time.RFC3339Nano))
	}
}

// --- key handling on the Feed tab ---

// updateFeedKeys handles keys on the Feed tab. handled is false for keys
// it has no use for, which then fall through to the normal tab keys.
func (m Model) updateFeedKeys(msg tea.KeyPressMsg) (next tea.Model, cmd tea.Cmd, handled bool) {
	key := msg.String()

	if m.feedOpen {
		switch key {
		case "esc", "backspace", "left", "h":
			m.feedOpen = false
			m.feedComments, m.feedCommentsLoaded, m.feedCommentsErr = nil, false, ""
			m.deleteArmed = ""
			return m, nil, true
		case "c":
			if reason := m.writePrecheck(); reason != "" {
				m.commandMsg = reason
				return m, nil, true
			}
			m.openCompose(composeState{kind: composeComment, postID: m.openPost().ID})
			return m, nil, true
		case "r":
			return m, m.fetchCommentsCmd(m.openPost().ID), true
		case "x":
			return m.deleteOwnPost()
		}
		m.deleteArmed = "" // any other key disarms a pending delete
		return m, nil, false
	}

	n := len(m.feedVisible())
	switch key {
	case "]":
		m.cycleFeedFilter(1)
		return m, nil, true
	case "[":
		m.cycleFeedFilter(-1)
		return m, nil, true
	case "up", "k":
		if n > 0 {
			m.feedIndex = (m.feedIndex + n - 1) % n
		}
		return m, nil, true
	case "down", "j":
		if n > 0 {
			m.feedIndex = (m.feedIndex + 1) % n
		}
		return m, nil, true
	case "enter", "right", "l":
		if p := m.openPost(); p != nil {
			m.feedOpen = true
			m.feedComments, m.feedCommentsLoaded, m.feedCommentsErr = nil, false, ""
			return m, m.fetchCommentsCmd(p.ID), true
		}
		return m, nil, true
	case "r":
		return m, m.fetchFeedCmd(0), true
	case "m":
		if m.feedMore {
			return m, m.fetchFeedCmd(len(m.feedPosts)), true
		}
		return m, nil, true
	case "p":
		if msg := m.startPost(""); msg != "" {
			m.commandMsg = msg
		}
		return m, nil, true
	}
	return m, nil, false
}

func (m *Model) writePrecheck() string {
	switch {
	case m.apiClient == nil:
		return "Posting needs synq-server configured. Set SYNQ_SERVER_URL and restart Synq (see README.md)."
	case m.accessToken == "":
		return "Log in to post or comment - :register <username> or :login."
	}
	return ""
}

// startPost opens the composer for a new post and returns "" - or, if it
// can't, why not. cat may be empty (General) or any of project, hiring,
// general.
func (m *Model) startPost(cat string) string {
	if reason := m.writePrecheck(); reason != "" {
		return reason
	}
	category := api.CategoryGeneral
	if cat != "" {
		category = strings.ToUpper(cat)
		ok := false
		for _, c := range feedCategories {
			ok = ok || c == category
		}
		if !ok {
			return "Usage: :post [project|hiring|general]"
		}
	}
	m.activeTab = tabFeed
	m.openCompose(composeState{kind: composePost, category: category})
	return ""
}

func (m *Model) openCompose(c composeState) {
	m.composeOpen = true
	m.compose = c
}

// deleteOwnPost removes the open post if it is the user's; the first
// press arms it and the second, immediately after, deletes - hard delete,
// no recovery (API.md).
func (m Model) deleteOwnPost() (tea.Model, tea.Cmd, bool) {
	p := m.openPost()
	if p == nil {
		return m, nil, true
	}
	if m.username == "" || p.Author != m.username {
		m.commandMsg = "You can only delete your own posts."
		return m, nil, true
	}
	if m.deleteArmed != p.ID {
		m.deleteArmed = p.ID
		m.commandMsg = "Press x again to delete this post for good (its comments go with it)."
		return m, nil, true
	}
	m.deleteArmed = ""
	if reason := m.writePrecheck(); reason != "" {
		m.commandMsg = reason
		return m, nil, true
	}
	client, token, id := m.apiClient, m.accessToken, p.ID
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), apiHTTPTimeout)
		defer cancel()
		err := client.DeletePost(ctx, token, id)
		return feedActionMsg{verb: "delete", err: err, unauthorized: api.IsUnauthorized(err)}
	}, true
}

// --- reporting ---

func (m *Model) reportOpenPost(reason string) (string, tea.Cmd) {
	if m.activeTab != tabFeed || !m.feedOpen || m.openPost() == nil {
		return "Open a post first (Feed tab, Enter), then :report [reason].", nil
	}
	if p := m.writePrecheck(); p != "" {
		return p, nil
	}
	if len([]rune(reason)) > 500 {
		return "A report reason can be at most 500 characters.", nil
	}
	client, token, id := m.apiClient, m.accessToken, m.openPost().ID
	return "Reporting...", func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), apiHTTPTimeout)
		defer cancel()
		err := client.ReportPost(ctx, token, id, reason)
		return feedActionMsg{verb: "report", err: err, unauthorized: api.IsUnauthorized(err)}
	}
}

// --- results of writes ---

type feedActionMsg struct {
	verb         string // "post", "comment", "delete", "report"
	postID       string // for comment: the post commented on
	err          error
	unauthorized bool
}

func (m Model) handleFeedAction(msg feedActionMsg) (tea.Model, tea.Cmd) {
	if msg.verb == "post" || msg.verb == "comment" {
		m.compose.sending = false
	}
	if msg.err != nil {
		text := ""
		switch {
		case msg.unauthorized:
			text = "Your session token expired - renewing it. Send again in a moment."
		case api.IsRateLimited(msg.err):
			text = "synq-server rate-limited that - wait a moment and send again."
		case api.IsNotFound(msg.err):
			text = "That post no longer exists, or isn't yours."
		default:
			text = fmt.Sprintf("Couldn't %s: %v", msg.verb, msg.err)
		}
		if m.composeOpen && (msg.verb == "post" || msg.verb == "comment") {
			m.compose.err = text // the draft stays put
		} else {
			m.commandMsg = text
		}
		if msg.unauthorized {
			return m, m.maybeReauthCmd()
		}
		return m, nil
	}
	switch msg.verb {
	case "post":
		m.composeOpen = false
		m.compose = composeState{}
		m.commandMsg = "Posted."
		return m, m.fetchFeedCmd(0)
	case "comment":
		m.composeOpen = false
		m.compose = composeState{}
		m.commandMsg = "Comment added."
		if p := m.openPost(); p != nil && m.feedOpen {
			return m, m.fetchCommentsCmd(p.ID)
		}
	case "delete":
		m.feedOpen = false
		m.feedComments, m.feedCommentsLoaded = nil, false
		m.commandMsg = "Post deleted."
		return m, m.fetchFeedCmd(0)
	case "report":
		m.commandMsg = "Reported. A moderator will take a look."
	}
	return m, nil
}

// --- the composer ---

// validateCompose checks a draft against the server's limits, returning
// the cleaned title and content, or why it can't be sent.
func validateCompose(c composeState) (title, body string, problem string) {
	title = strings.TrimSpace(sanitizeSingleLine(c.title))
	body = strings.TrimSpace(sanitizeMultiline(c.body))
	if c.kind == composePost {
		if title == "" {
			return "", "", "A post needs a title."
		}
		if n := len([]rune(title)); n > maxTitleRunes {
			return "", "", fmt.Sprintf("The title is %d characters; the limit is %d.", n, maxTitleRunes)
		}
	}
	if body == "" {
		if c.kind == composePost {
			return "", "", "A post needs some content."
		}
		return "", "", "A comment can't be empty."
	}
	if n := len([]rune(body)); n > maxContentRunes {
		return "", "", fmt.Sprintf("That's %d characters; the limit is %d.", n, maxContentRunes)
	}
	return title, body, ""
}

type editorDoneMsg struct {
	path string
	err  error
}

func (m Model) updateCompose(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	c := &m.compose
	if c.sending {
		return m, nil // one send at a time; the result unlocks it
	}
	switch msg.String() {
	case "esc", "ctrl+c":
		m.composeOpen = false
		m.compose = composeState{}
		m.commandMsg = "Discarded."
		return m, nil

	case "ctrl+s":
		title, body, problem := validateCompose(*c)
		if problem != "" {
			c.err = problem
			return m, nil
		}
		if reason := m.writePrecheck(); reason != "" {
			c.err = reason
			return m, nil
		}
		c.err = ""
		c.sending = true
		client, token := m.apiClient, m.accessToken
		kind, category, postID := c.kind, c.category, c.postID
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), apiHTTPTimeout)
			defer cancel()
			if kind == composeComment {
				_, err := client.CreateComment(ctx, token, postID, body)
				return feedActionMsg{verb: "comment", postID: postID, err: err, unauthorized: api.IsUnauthorized(err)}
			}
			_, err := client.CreatePost(ctx, token, category, title, body)
			return feedActionMsg{verb: "post", err: err, unauthorized: api.IsUnauthorized(err)}
		}

	case "tab":
		if c.kind == composePost {
			c.field = 1 - c.field
		}
		return m, nil

	case "ctrl+g":
		if c.kind == composePost {
			for i, cat := range feedCategories {
				if cat == c.category {
					c.category = feedCategories[(i+1)%len(feedCategories)]
					break
				}
			}
		}
		return m, nil

	case "ctrl+o":
		return m.openEditor()

	case "enter":
		if c.kind == composePost && c.field == 0 {
			c.field = 1
			return m, nil
		}
		c.body, c.bodyBack = lineedit.Insert(c.body, c.bodyBack, "\n")
		return m, nil
	}

	// Everything else edits the field that has focus, at its cursor. The
	// title is one line; the body is several, so Up/Down move between
	// its lines.
	inTitle := c.kind == composePost && c.field == 0
	text, back, multiline := &c.body, &c.bodyBack, true
	if inTitle {
		text, back, multiline = &c.title, &c.titleBack, false
	}
	if t, b, ok := lineedit.Apply(*text, *back, msg.String(), multiline); ok {
		*text, *back = t, b
		return m, nil
	}
	if msg.Text != "" {
		typed := sanitizeMultiline(msg.Text)
		if inTitle {
			typed = sanitizeSingleLine(msg.Text)
		}
		*text, *back = lineedit.Insert(*text, *back, typed)
	}
	return m, nil
}

// openEditor hands the draft body to $EDITOR (DESIGN.md section 8) and
// takes the result back. Anything long is easier to write there than in
// the box, and a terminal editor is where a developer's paste and
// editing habits live. With no $EDITOR set the box stays the only
// option, and the user is told so.
func (m Model) openEditor() (tea.Model, tea.Cmd) {
	editor := strings.Fields(os.Getenv("EDITOR"))
	if len(editor) == 0 {
		m.compose.err = "$EDITOR isn't set - type in the box instead."
		return m, nil
	}
	f, err := os.CreateTemp("", "synq-draft-*.txt")
	if err != nil {
		m.compose.err = fmt.Sprintf("Couldn't create a draft file: %v", err)
		return m, nil
	}
	_, werr := f.WriteString(m.compose.body)
	cerr := f.Close()
	if werr != nil || cerr != nil {
		os.Remove(f.Name())
		m.compose.err = "Couldn't write the draft file."
		return m, nil
	}
	path := f.Name()
	cmd := exec.Command(editor[0], append(editor[1:], path)...)
	return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
		return editorDoneMsg{path: path, err: err}
	})
}

func (m Model) handleEditorDone(msg editorDoneMsg) (tea.Model, tea.Cmd) {
	defer os.Remove(msg.path)
	if msg.err != nil {
		// DESIGN.md section 8: a failing or abandoned editor discards
		// the edit; the draft as it was stays.
		m.compose.err = fmt.Sprintf("The editor exited with an error, so nothing was changed (%v).", msg.err)
		return m, nil
	}
	b, err := os.ReadFile(msg.path)
	if err != nil {
		m.compose.err = fmt.Sprintf("Couldn't read the draft back: %v", err)
		return m, nil
	}
	m.compose.body = strings.TrimRight(string(b), "\n")
	m.compose.bodyBack = 0 // the draft was replaced wholesale; cursor to the end
	m.compose.field = 1
	m.compose.err = ""
	return m, nil
}

// --- rendering ---

// contentWidth is how wide post text is wrapped: the pane's inner width,
// capped so lines stay readable on a very wide terminal.
func (m Model) contentWidth() int {
	w, _ := m.innerSize()
	return min(max(w, 30), 100)
}

func (m Model) renderFeed() string {
	switch {
	case m.apiClient == nil:
		b := "No synq-server configured, so there's no Feed to show. Set SYNQ_SERVER_URL and restart Synq (see README.md)."
		if m.identity == nil {
			b += "\n\nYou're browsing as a guest."
		}
		return b
	case m.feedOpen && m.openPost() != nil:
		return m.renderPost()
	case !m.feedLoaded && m.feedErr != "":
		return m.theme.Warning.Render("Couldn't load the Feed: "+sanitizeSingleLine(m.feedErr)) + "\n\nPress r to retry."
	case !m.feedLoaded:
		return "Loading the Feed..."
	}

	var b strings.Builder
	vis := m.feedVisible()
	if len(m.feedPosts) == 0 {
		b.WriteString("The Feed is empty. Be the first: press p (or :post) to write one.\n")
	}
	if m.accessToken == "" {
		b.WriteString(m.theme.Muted.Render("Browsing as a guest - authors show as \"node\". Log in to see who wrote what and to post.") + "\n\n")
	}
	if len(m.feedPosts) > 0 {
		b.WriteString(m.feedFilterBar() + "\n\n")
		if len(vis) == 0 {
			b.WriteString(m.theme.Muted.Render("No "+feedFilterLabel(m.feedFilter)+" posts loaded. [ and ] change the category.") + "\n")
		}
	}
	// Keep the selection on screen: show a window around it.
	_, ih := m.innerSize()
	rows := max(ih-8, 3)
	start := max(m.feedIndex-rows/2, 0)
	end := min(start+rows, len(vis))
	start = max(end-rows, 0)
	now := time.Now()
	for i := start; i < end; i++ {
		p := vis[i]
		verified := ""
		if p.GitHubVerified {
			verified = " ✓"
		}
		isNew := m.feedIsNew(p)
		title := truncateRunes(sanitizeSingleLine(p.Title), 60)
		meta := fmt.Sprintf("  %s%s · %s", truncateRunes(sanitizeSingleLine(p.Author), 20), verified, ago(p.CreatedAt, now))
		if isNew {
			meta += " · new"
		}
		switch {
		case i == m.feedIndex:
			b.WriteString(m.theme.TabActive.Render("› ") + m.categoryChip(p.Category) + m.theme.TabActive.Render(" "+title) + m.theme.Muted.Render(meta) + "\n")
		case isNew:
			b.WriteString(m.theme.AccentText.Render("● ") + m.categoryChip(p.Category) + " " + title + m.theme.Muted.Render(meta) + "\n")
		default:
			b.WriteString("  " + m.categoryChip(p.Category) + " " + title + m.theme.Muted.Render(meta) + "\n")
		}
	}
	if start > 0 || end < len(vis) {
		fmt.Fprintf(&b, "\n%s", m.theme.Muted.Render(fmt.Sprintf("showing %d-%d of %d loaded", start+1, end, len(vis))))
	}
	hints := "↑/↓ select · enter open · [ ] category · p post · r refresh"
	if m.feedMore {
		hints += " · m more"
	}
	b.WriteString("\n\n" + m.theme.Muted.Render(hints))
	if m.feedErr != "" {
		b.WriteString("\n" + m.theme.Warning.Render("Last refresh failed: "+sanitizeSingleLine(m.feedErr)))
	}
	return b.String()
}

// feedFilterBar is the category row above the list: the current one in
// the accent colour, and how many posts in the current view are new.
func (m Model) feedFilterBar() string {
	var parts []string
	for _, f := range feedFilters {
		label := feedFilterLabel(f)
		if f == m.feedFilter {
			parts = append(parts, m.theme.AccentText.Render("["+label+"]"))
		} else {
			parts = append(parts, m.theme.Muted.Render(label))
		}
	}
	bar := strings.Join(parts, m.theme.Muted.Render(" · "))
	if n := m.feedNewIn(m.feedVisible()); n > 0 {
		bar += m.theme.Muted.Render(fmt.Sprintf("   ● %d new", n))
	}
	return bar
}

func (m Model) renderPost() string {
	p := m.feedVisible()[m.feedIndex]
	w := m.contentWidth()
	now := time.Now()

	verified := ""
	if p.GitHubVerified && p.GitHubHandle != "" {
		verified = " [✓ GitHub: @" + sanitizeSingleLine(p.GitHubHandle) + "]"
	}
	var b strings.Builder
	b.WriteString(m.categoryChip(p.Category) + " " + m.theme.Warning.Render(sanitizeSingleLine(p.Title)) + "\n")
	b.WriteString(m.theme.Muted.Render(fmt.Sprintf("%s%s · %s", sanitizeSingleLine(p.Author), verified, ago(p.CreatedAt, now))) + "\n\n")
	b.WriteString(strings.Join(wrapParagraphs(sanitizeMultiline(p.Content), w), "\n") + "\n\n")

	b.WriteString(m.theme.StatusBar.Render(fmt.Sprintf("Comments (%d)", len(m.feedComments))) + "\n")
	switch {
	case m.feedCommentsErr != "":
		b.WriteString(m.theme.Warning.Render("Couldn't load comments: "+sanitizeSingleLine(m.feedCommentsErr)) + "\n")
	case !m.feedCommentsLoaded:
		b.WriteString(m.theme.Muted.Render("Loading...") + "\n")
	case len(m.feedComments) == 0:
		b.WriteString(m.theme.Muted.Render("No comments yet.") + "\n")
	}
	for _, c := range m.feedComments {
		b.WriteString("\n" + m.theme.Muted.Render(fmt.Sprintf("%s · %s", sanitizeSingleLine(c.Author), ago(c.CreatedAt, now))) + "\n")
		for _, l := range wrapParagraphs(sanitizeMultiline(c.Content), w-2) {
			b.WriteString("  " + l + "\n")
		}
	}
	hints := "esc back · c comment · r refresh · :report [reason]"
	if m.username != "" && p.Author == m.username {
		hints += " · x delete"
	}
	b.WriteString("\n" + m.theme.Muted.Render(hints))
	return b.String()
}

func (m Model) renderCompose() string {
	c := m.compose
	w := m.contentWidth()
	var b strings.Builder

	if c.kind == composeComment {
		b.WriteString(m.theme.Warning.Render("New comment") + "\n\n")
	} else {
		b.WriteString(m.theme.Warning.Render("New post") + m.theme.Muted.Render("  ["+categoryTag(c.category)+"]") + "\n\n")
		label := "Title"
		if c.field == 0 {
			label = "› Title"
		}
		b.WriteString(m.theme.Muted.Render(label) + "\n")
		tb, ta := lineedit.Split(c.title, c.titleBack)
		title := sanitizeSingleLine(tb) + sanitizeSingleLine(ta)
		if c.field == 0 {
			title = sanitizeSingleLine(tb) + "▏" + sanitizeSingleLine(ta)
		}
		b.WriteString(title + m.theme.Muted.Render(fmt.Sprintf("  %d/%d", len([]rune(c.title)), maxTitleRunes)) + "\n\n")
	}
	label := "Content"
	if c.kind == composeComment || c.field == 1 {
		label = "› Content"
	}
	b.WriteString(m.theme.Muted.Render(label+fmt.Sprintf("  %d/%d", len([]rune(c.body)), maxContentRunes)) + "\n")
	bb, ba := lineedit.Split(c.body, c.bodyBack)
	focused := c.kind == composeComment || c.field == 1
	// Mark the cursor with a zero-width placeholder before wrapping, so it
	// ends up on whichever wrapped line it lands in, then swap in the
	// visible caret. The placeholder can't clash with typed text:
	// sanitizeMultiline strips every control character. Wrapping leaves a
	// column for the caret so a full line doesn't overflow the pane.
	const mark = "\x00"
	text := sanitizeMultiline(bb) + sanitizeMultiline(ba)
	if focused {
		text = sanitizeMultiline(bb) + mark + sanitizeMultiline(ba)
	}
	lines := wrapParagraphs(text, max(w-1, 1))
	at := len(lines) - 1 // where the cursor is; the last line if it's lost
	for i, l := range lines {
		if strings.Contains(l, mark) {
			at = i
			lines[i] = strings.Replace(l, mark, "▏", 1)
			break
		}
	}
	// Scroll so the cursor stays in view: the tail while you type at the
	// end, and the cursor's line at the top once you've moved above it.
	_, ih := m.innerSize()
	room := max(ih-10, 4)
	if len(lines) > room {
		start := min(len(lines)-room, at)
		lines = lines[start : start+room]
	}
	b.WriteString(strings.Join(lines, "\n") + "\n\n")

	if c.err != "" {
		b.WriteString(m.theme.Warning.Render(sanitizeSingleLine(c.err)) + "\n")
	}
	if c.sending {
		b.WriteString(m.theme.Muted.Render("Sending...") + "\n")
	}
	hints := "ctrl+s send · ctrl+o $EDITOR · esc discard"
	if c.kind == composePost {
		hints = "tab title/content · ctrl+g category · " + hints
	}
	b.WriteString(m.theme.Muted.Render(hints))
	return b.String()
}

// --- helpers ---

func categoryTag(c string) string {
	switch c {
	case api.CategoryProject, api.CategoryHiring, api.CategoryGeneral:
		return c
	}
	return "?" // a category this client doesn't know; never echo unchecked text
}

// ago renders a server timestamp (RFC 3339) relative to now.
func ago(ts string, now time.Time) string {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return sanitizeSingleLine(truncateRunes(ts, 20))
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
	return t.Format("2006-01-02")
}

// wrapParagraphs wraps text to width, keeping blank lines between
// paragraphs, the line breaks the author wrote, and a line's leading
// indentation - code and log excerpts are exactly what gets posted. A
// line that already fits is left exactly as written.
func wrapParagraphs(s string, width int) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		switch {
		case strings.TrimSpace(line) == "":
			out = append(out, "")
		case lipgloss.Width(line) <= width:
			out = append(out, line)
		default:
			indent := line[:len(line)-len(strings.TrimLeft(line, " "))]
			for _, l := range wrapToWidth(strings.TrimLeft(line, " "), max(width-len(indent), 10)) {
				out = append(out, indent+l)
			}
		}
	}
	return out
}

// sanitizeSingleLine strips control characters and flattens line breaks.
func sanitizeSingleLine(s string) string { return sanitizeForTerminal(s) }

// sanitizeMultiline strips control characters but keeps newlines (and
// turns tabs into spaces), for text that is allowed several lines.
func sanitizeMultiline(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n':
			return r
		case r == '\t':
			return ' '
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
			return -1
		}
		return r
	}, s)
}

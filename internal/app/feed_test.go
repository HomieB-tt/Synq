package app

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/HomieB-tt/synq/internal/api"
)

func feedModel() Model {
	return Model{
		apiClient:   api.NewClient("https://example.invalid"),
		accessToken: "tok",
		username:    "me",
		activeTab:   tabFeed,
		feedLoaded:  true,
		feedPosts: []api.Post{
			{ID: "p1", Category: "GENERAL", Title: "first", Content: "hello", Author: "amy"},
			{ID: "p2", Category: "PROJECT", Title: "mine", Content: "world", Author: "me"},
		},
	}
}

func TestValidateCompose(t *testing.T) {
	cases := []struct {
		name    string
		c       composeState
		problem string // substring; empty means valid
	}{
		{"ok post", composeState{kind: composePost, title: " T ", body: " B "}, ""},
		{"no title", composeState{kind: composePost, body: "b"}, "title"},
		{"blank title", composeState{kind: composePost, title: "   ", body: "b"}, "title"},
		{"long title", composeState{kind: composePost, title: strings.Repeat("x", 121), body: "b"}, "title is 121"},
		{"max title", composeState{kind: composePost, title: strings.Repeat("x", 120), body: "b"}, ""},
		{"no body", composeState{kind: composePost, title: "t"}, "content"},
		{"long body", composeState{kind: composePost, title: "t", body: strings.Repeat("x", 10001)}, "10001"},
		{"max body", composeState{kind: composePost, title: "t", body: strings.Repeat("x", 10000)}, ""},
		{"ok comment", composeState{kind: composeComment, body: "hi"}, ""},
		{"empty comment", composeState{kind: composeComment, body: "  \n "}, "empty"},
		{"multibyte counts characters", composeState{kind: composePost, title: strings.Repeat("日", 120), body: "b"}, ""},
	}
	for _, c := range cases {
		_, _, problem := validateCompose(c.c)
		if (c.problem == "") != (problem == "") || !strings.Contains(problem, c.problem) {
			t.Errorf("%s: problem = %q, want containing %q", c.name, problem, c.problem)
		}
	}
	title, body, _ := validateCompose(composeState{kind: composePost, title: " T\x1b[2J ", body: " B\x00\n\nC "})
	if title != "T[2J" || body != "B\n\nC" {
		t.Errorf("cleaned = %q, %q", title, body)
	}
}

func TestSanitizeMultilineKeepsNewlinesOnly(t *testing.T) {
	got := sanitizeMultiline("a\r\nb\rc\td\x1b[31me\x07")
	if got != "a\nb\nc d[31me" {
		t.Errorf("got %q", got)
	}
}

func TestWrapParagraphsKeepsIndentAndBlankLines(t *testing.T) {
	got := wrapParagraphs("    indented code line\n\npara", 40)
	if len(got) != 3 || got[0] != "    indented code line" || got[1] != "" || got[2] != "para" {
		t.Errorf("got %q", got)
	}
	long := wrapParagraphs("    "+strings.Repeat("word ", 30), 30)
	for _, l := range long {
		if !strings.HasPrefix(l, "    ") {
			t.Errorf("wrapped line lost its indent: %q", l)
		}
	}
}

func TestAgo(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }
	cases := map[string]string{
		at(10 * time.Second):      "just now",
		at(5 * time.Minute):       "5m ago",
		at(3 * time.Hour):         "3h ago",
		at(2 * 24 * time.Hour):    "2d ago",
		at(90 * 24 * time.Hour):   "2026-07-16",
		"garbage\x1b[2Jtimestamp": "garbage[2Jtimestamp",
	}
	for in, want := range cases {
		if got := ago(in, now); got != want {
			t.Errorf("ago(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCategoryTagNeverEchoesUnknownText(t *testing.T) {
	if categoryTag("PROJECT") != "PROJECT" || categoryTag("\x1b[2J") != "?" {
		t.Error("categoryTag let unknown text through")
	}
}

func TestFeedNavigationAndOpen(t *testing.T) {
	m := feedModel()
	next, _, handled := m.updateFeedKeys(keyPressWith("j"))
	if !handled || next.(Model).feedIndex != 1 {
		t.Fatalf("j did not move down: %+v", next.(Model).feedIndex)
	}
	m = next.(Model)
	next, _, _ = m.updateFeedKeys(keyPressWith("j"))
	if next.(Model).feedIndex != 0 {
		t.Error("selection did not wrap")
	}
	next, cmd, _ := m.updateFeedKeys(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := next.(Model)
	if !got.feedOpen || cmd == nil {
		t.Errorf("enter did not open the post and load its comments: open=%v cmd=%v", got.feedOpen, cmd)
	}
}

func TestWritesNeedALogin(t *testing.T) {
	m := feedModel()
	m.accessToken = ""
	if msg := m.startPost(""); !strings.Contains(msg, "Log in") || m.composeOpen {
		t.Errorf("guest could open the composer: %q", msg)
	}
	m.feedOpen = true
	next, _, _ := m.updateFeedKeys(keyPressWith("c"))
	if next.(Model).composeOpen {
		t.Error("guest could open the comment composer")
	}
}

func TestStartPostCategories(t *testing.T) {
	m := feedModel()
	if msg := m.startPost("hiring"); msg != "" || m.compose.category != "HIRING" {
		t.Errorf("hiring: %q, %+v", msg, m.compose)
	}
	m = feedModel()
	if msg := m.startPost("bogus"); !strings.Contains(msg, "Usage") || m.composeOpen {
		t.Errorf("bogus category: %q", msg)
	}
	m = feedModel()
	m.startPost("")
	if m.compose.category != "GENERAL" {
		t.Errorf("default category = %q", m.compose.category)
	}
}

func typeInto(t *testing.T, m Model, keys ...tea.KeyPressMsg) Model {
	t.Helper()
	for _, k := range keys {
		next, _ := m.updateCompose(k)
		m = next.(Model)
	}
	return m
}

func TestComposerTypingFieldsAndCategory(t *testing.T) {
	m := feedModel()
	m.startPost("")
	m = typeInto(t, m,
		keyPressWith("H"), keyPressWith("i"),
		tea.KeyPressMsg{Code: tea.KeyEnter},                                       // title -> content
		keyPressWith("b"), tea.KeyPressMsg{Code: tea.KeyEnter}, keyPressWith("2"), // newline inside content
		tea.KeyPressMsg{Code: tea.KeyBackspace},
	)
	if m.compose.title != "Hi" || m.compose.body != "b\n" || m.compose.field != 1 {
		t.Errorf("compose = %+v", m.compose)
	}
	m = typeInto(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	if m.compose.field != 0 {
		t.Error("tab did not return to the title")
	}
	m = typeInto(t, m, tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
	if m.compose.category != "PROJECT" {
		t.Errorf("ctrl+g category = %q", m.compose.category)
	}
}

func TestComposerSendValidatesFirstAndKeepsTheDraft(t *testing.T) {
	m := feedModel()
	m.startPost("")
	m.compose.title = "t" // no body
	next, cmd := m.updateCompose(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	got := next.(Model)
	if cmd != nil || got.compose.err == "" || got.compose.sending {
		t.Errorf("invalid draft was sent: cmd=%v err=%q", cmd, got.compose.err)
	}
	if got.compose.title != "t" {
		t.Error("draft lost")
	}

	m.compose.body = "content"
	next, cmd = m.updateCompose(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if cmd == nil || !next.(Model).compose.sending {
		t.Error("valid draft was not sent")
	}
	// While sending, further keys are ignored.
	again, cmd2 := next.(Model).updateCompose(keyPressWith("x"))
	if cmd2 != nil || again.(Model).compose.body != "content" {
		t.Error("typing was accepted mid-send")
	}
}

func TestComposerEscDiscards(t *testing.T) {
	m := feedModel()
	m.startPost("")
	m.compose.title = "draft"
	next, _ := m.updateCompose(tea.KeyPressMsg{Code: tea.KeyEsc})
	got := next.(Model)
	if got.composeOpen || got.compose.title != "" {
		t.Errorf("esc kept the draft: %+v", got.compose)
	}
}

func TestFeedActionSuccessClosesComposerAndReloads(t *testing.T) {
	m := feedModel()
	m.startPost("")
	m.compose.sending = true
	next, cmd := m.handleFeedAction(feedActionMsg{verb: "post"})
	got := next.(Model)
	if got.composeOpen || cmd == nil || got.commandMsg != "Posted." {
		t.Errorf("after post: open=%v cmd=%v msg=%q", got.composeOpen, cmd, got.commandMsg)
	}
}

func TestFeedActionFailureKeepsTheDraftAndSaysWhy(t *testing.T) {
	m := feedModel()
	m.startPost("")
	m.compose.title, m.compose.body = "t", "b"
	m.compose.sending = true
	next, _ := m.handleFeedAction(feedActionMsg{verb: "post", err: errors.New("boom")})
	got := next.(Model)
	if !got.composeOpen || got.compose.title != "t" || got.compose.body != "b" {
		t.Error("draft was lost on failure")
	}
	if got.compose.sending || !strings.Contains(got.compose.err, "boom") {
		t.Errorf("state after failure: sending=%v err=%q", got.compose.sending, got.compose.err)
	}

	m.compose.sending = true
	next, _ = m.handleFeedAction(feedActionMsg{verb: "post", err: &api.APIError{StatusCode: 429, Message: "slow"}})
	if !strings.Contains(next.(Model).compose.err, "rate-limited") {
		t.Errorf("429: %q", next.(Model).compose.err)
	}
}

func TestDeleteNeedsTwoPressesAndOwnership(t *testing.T) {
	m := feedModel()
	m.feedOpen = true

	// Someone else's post: refused outright.
	m.feedIndex = 0
	next, cmd, _ := m.updateFeedKeys(keyPressWith("x"))
	if cmd != nil || !strings.Contains(next.(Model).commandMsg, "your own") {
		t.Errorf("deleted someone else's post: %q", next.(Model).commandMsg)
	}

	// Own post: first press arms, second deletes.
	m.feedIndex = 1
	next, cmd, _ = m.updateFeedKeys(keyPressWith("x"))
	if cmd != nil || next.(Model).deleteArmed != "p2" {
		t.Fatalf("first press: armed=%q cmd=%v", next.(Model).deleteArmed, cmd)
	}
	m = next.(Model)
	// Another key disarms.
	disarmed, _, _ := m.updateFeedKeys(keyPressWith("z"))
	if disarmed.(Model).deleteArmed != "" {
		t.Error("an unrelated key did not disarm the delete")
	}
	next, cmd, _ = m.updateFeedKeys(keyPressWith("x"))
	if cmd == nil {
		t.Error("second press did not delete")
	}
	_ = next
}

func TestReportNeedsAnOpenPost(t *testing.T) {
	m := feedModel()
	if msg, cmd := m.reportOpenPost("spam"); cmd != nil || !strings.Contains(msg, "Open a post") {
		t.Errorf("report with nothing open: %q", msg)
	}
	m.feedOpen = true
	if _, cmd := m.reportOpenPost("spam"); cmd == nil {
		t.Error("report of the open post did nothing")
	}
	if msg, cmd := m.reportOpenPost(strings.Repeat("x", 501)); cmd != nil || !strings.Contains(msg, "500") {
		t.Errorf("oversized reason: %q", msg)
	}
}

func TestFeedLoadedPagingAndSelectionClamp(t *testing.T) {
	m := Model{}
	page := make([]api.Post, feedPageSize)
	next, _ := m.handleFeedLoaded(feedLoadedMsg{posts: page})
	got := next.(Model)
	if !got.feedMore || len(got.feedPosts) != feedPageSize {
		t.Errorf("first page: more=%v n=%d", got.feedMore, len(got.feedPosts))
	}
	next, _ = got.handleFeedLoaded(feedLoadedMsg{posts: page[:3], offset: feedPageSize})
	got = next.(Model)
	if got.feedMore || len(got.feedPosts) != feedPageSize+3 {
		t.Errorf("second page: more=%v n=%d", got.feedMore, len(got.feedPosts))
	}
	got.feedIndex = 40
	next, _ = got.handleFeedLoaded(feedLoadedMsg{posts: page[:2], offset: 0})
	if next.(Model).feedIndex != 1 {
		t.Errorf("selection not clamped after a shorter reload: %d", next.(Model).feedIndex)
	}
}

func TestLateCommentsForAClosedPostAreIgnored(t *testing.T) {
	m := feedModel()
	next, _ := m.handleCommentsLoaded(commentsLoadedMsg{postID: "p1", comments: []api.Comment{{ID: "c"}}})
	if next.(Model).feedCommentsLoaded {
		t.Error("stored comments for a post that isn't open")
	}
	m.feedOpen = true
	m.feedIndex = 0
	next, _ = m.handleCommentsLoaded(commentsLoadedMsg{postID: "p2", comments: []api.Comment{{ID: "c"}}})
	if next.(Model).feedCommentsLoaded {
		t.Error("stored comments for a different post than the open one")
	}
	next, _ = m.handleCommentsLoaded(commentsLoadedMsg{postID: "p1", comments: []api.Comment{{ID: "c"}}})
	if !next.(Model).feedCommentsLoaded {
		t.Error("dropped comments for the open post")
	}
}

func TestRenderFeedNeverPrintsControlCharacters(t *testing.T) {
	m := feedModel()
	m.feedPosts[0].Title = "evil\x1b[2Jtitle"
	m.feedPosts[0].Author = "bad\x1b]0;x\x07guy"
	out := m.renderFeed()
	if strings.ContainsRune(out, 0x1b) && strings.Contains(out, "evil\x1b") {
		t.Errorf("raw escape from a post title reached the screen: %q", out)
	}
	m.feedOpen = true
	m.feedPosts[0].Content = "line\x1b[31mred"
	if out := m.renderFeed(); strings.Contains(out, "line\x1b[31m") {
		t.Errorf("raw escape from post content reached the screen: %q", out)
	}
}

func TestRenderFeedGuestAndEmptyStates(t *testing.T) {
	m := Model{apiClient: api.NewClient("https://example.invalid"), feedLoaded: true}
	if out := m.renderFeed(); !strings.Contains(out, "empty") || !strings.Contains(out, "guest") {
		t.Errorf("empty guest feed: %q", out)
	}
	m = Model{}
	if out := m.renderFeed(); !strings.Contains(out, "SYNQ_SERVER_URL") {
		t.Errorf("no server: %q", out)
	}
}

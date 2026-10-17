package app

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/HomieB-tt/synq/internal/api"
	"github.com/HomieB-tt/synq/internal/db"
)

func filterModel() Model {
	m := feedModel()
	m.feedPosts = []api.Post{
		{ID: "a", Category: api.CategoryGeneral, Title: "g1", CreatedAt: "2026-10-14T10:00:00.250Z"},
		{ID: "b", Category: api.CategoryProject, Title: "p1", CreatedAt: "2026-10-14T09:00:00Z"},
		{ID: "c", Category: api.CategoryHiring, Title: "h1", CreatedAt: "2026-10-14T08:00:00Z"},
		{ID: "d", Category: api.CategoryProject, Title: "p2", CreatedAt: "2026-10-14T07:00:00Z"},
	}
	return m
}

func feedKey(m Model, s string) Model {
	next, _, _ := m.updateFeedKeys(tea.KeyPressMsg{Code: rune(s[0]), Text: s})
	return next.(Model)
}

func TestBracketKeysCycleTheCategoryFilter(t *testing.T) {
	m := filterModel()
	m.feedIndex = 3
	m = feedKey(m, "]")
	if m.feedFilter != api.CategoryProject || m.feedIndex != 0 {
		t.Fatalf("after ]: filter %q index %d, want PROJECT and the top", m.feedFilter, m.feedIndex)
	}
	if got := m.feedVisible(); len(got) != 2 || got[0].ID != "b" || got[1].ID != "d" {
		t.Fatalf("visible = %+v, want the two project posts in order", got)
	}
	m = feedKey(m, "]")
	m = feedKey(m, "]")
	if m.feedFilter != api.CategoryGeneral {
		t.Fatalf("third ] = %q, want GENERAL", m.feedFilter)
	}
	m = feedKey(m, "]")
	if m.feedFilter != "" {
		t.Fatalf("] past the last should wrap to all, got %q", m.feedFilter)
	}
	m = feedKey(m, "[")
	if m.feedFilter != api.CategoryGeneral {
		t.Fatalf("[ from all should wrap to GENERAL, got %q", m.feedFilter)
	}
}

func TestSelectionAndOpenFollowTheFilteredList(t *testing.T) {
	m := filterModel()
	m = feedKey(m, "]") // PROJECT: b, d
	m = feedKey(m, "j")
	if p := m.openPost(); p == nil || p.ID != "d" {
		t.Fatalf("second row of the filtered list should be d, got %+v", p)
	}
	m = feedKey(m, "j") // wraps within the two visible posts
	if p := m.openPost(); p == nil || p.ID != "b" {
		t.Fatalf("selection should wrap inside the filtered list, got %+v", p)
	}
	m.feedFilter = api.CategoryHiring
	m.feedIndex = 5
	if m.openPost() != nil {
		t.Error("an out-of-range selection should open nothing")
	}
}

func TestMoreStillPagesByLoadedCountWhileFiltered(t *testing.T) {
	m := filterModel()
	m.feedMore = true
	m = feedKey(m, "]")
	_, cmd, handled := m.updateFeedKeys(tea.KeyPressMsg{Code: 'm', Text: "m"})
	if !handled || cmd == nil {
		t.Fatal("m should fetch the next page even while a category is selected")
	}
}

func TestFilterBarAndEmptyCategory(t *testing.T) {
	m := filterModel()
	m.width, m.height = 100, 30
	m.theme = chatViewModel(t, 1, 1).theme
	out := plain(m.renderFeed())
	for _, want := range []string{"[All]", "Project", "Hiring", "General", "[ ] category"} {
		if !strings.Contains(out, want) {
			t.Errorf("feed missing %q:\n%s", want, out)
		}
	}
	m.feedPosts = m.feedPosts[:1] // only a GENERAL post
	m.feedFilter = api.CategoryHiring
	out = plain(m.renderFeed())
	if !strings.Contains(out, "No Hiring posts loaded") {
		t.Errorf("an empty category should say so:\n%s", out)
	}
}

func TestPostsNewerThanTheLastLookAreMarked(t *testing.T) {
	m := filterModel()
	m.theme = chatViewModel(t, 1, 1).theme
	m.width, m.height = 100, 30
	if m.feedIsNew(m.feedPosts[0]) {
		t.Error("with no baseline nothing is new")
	}
	m.feedSeenAt = time.Date(2026, 10, 14, 8, 30, 0, 0, time.UTC)
	got := map[string]bool{}
	for _, p := range m.feedPosts {
		got[p.ID] = m.feedIsNew(p)
	}
	if !got["a"] || !got["b"] || got["c"] || got["d"] {
		t.Errorf("new flags = %v, want a and b only", got)
	}
	out := plain(m.renderFeed())
	if !strings.Contains(out, "2 new") {
		t.Errorf("header should count the new posts:\n%s", out)
	}
	m.feedPosts[0].CreatedAt = "garbage"
	if m.feedIsNew(m.feedPosts[0]) {
		t.Error("an unparseable timestamp must not count as new")
	}
}

func TestSeenTimeKeepsSubSecondPrecision(t *testing.T) {
	ks := newTestKeyStore(t)
	m := filterModel()
	m.store = ks
	m.markFeedSeen()
	// Reload as the next launch would: the newest post, created a
	// fraction of a second past a whole second, must not look new.
	v, err := ks.LoadPreference(db.PrefFeedSeen)
	if err != nil {
		t.Fatalf("LoadPreference: %v", err)
	}
	seen, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		t.Fatalf("saved value %q: %v", v, err)
	}
	m2 := filterModel()
	m2.feedSeenAt = seen
	if n := m2.feedNewIn(m2.feedPosts); n != 0 {
		t.Errorf("after marking seen, %d posts are still new", n)
	}
}

func TestFirstLoadSetsTheBaselineAndLaterOnesAreNew(t *testing.T) {
	m := feedModel()
	m.store = newTestKeyStore(t)
	m.feedPosts, m.feedLoaded = nil, false
	first := []api.Post{{ID: "x", Category: "GENERAL", CreatedAt: "2026-10-14T08:00:00Z"}}
	next, _ := m.handleFeedLoaded(feedLoadedMsg{posts: first})
	m = next.(Model)
	if m.feedSeenAt.IsZero() || m.feedNewIn(m.feedPosts) != 0 {
		t.Fatalf("the first load should count as seen: seenAt %v, new %d", m.feedSeenAt, m.feedNewIn(m.feedPosts))
	}
	more := append([]api.Post{{ID: "y", Category: "GENERAL", CreatedAt: "2026-10-14T09:00:00Z"}}, first...)
	next, _ = m.handleFeedLoaded(feedLoadedMsg{posts: more})
	m = next.(Model)
	if m.feedNewIn(m.feedPosts) != 1 || !m.feedIsNew(m.feedPosts[0]) {
		t.Errorf("only the post that arrived afterwards should be new")
	}
}

func TestLeavingTheFeedOrQuittingClearsTheMarkers(t *testing.T) {
	m := filterModel()
	m.store = newTestKeyStore(t)
	m.theme = chatViewModel(t, 1, 1).theme
	m.feedSeenAt = time.Date(2026, 10, 14, 8, 30, 0, 0, time.UTC)

	next, _ := m.Update(tea.KeyPressMsg{Code: '3', Text: "3"}) // to Chat
	left := next.(Model)
	if left.activeTab == tabFeed {
		t.Fatal("setup: 3 should leave the Feed")
	}
	if n := left.feedNewIn(left.feedPosts); n != 0 {
		t.Errorf("leaving the Feed should mark it seen, %d still new", n)
	}
	if v, err := m.store.LoadPreference(db.PrefFeedSeen); err != nil || v == "" {
		t.Errorf("seen time not saved: %q, %v", v, err)
	}

	m2 := filterModel()
	m2.store = newTestKeyStore(t)
	m2.feedSeenAt = time.Date(2026, 10, 14, 8, 30, 0, 0, time.UTC)
	next, _ = m2.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if n := next.(Model).feedNewIn(m2.feedPosts); n != 0 {
		t.Errorf("quitting should mark the Feed seen, %d still new", n)
	}
}

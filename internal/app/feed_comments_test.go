package app

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/HomieB-tt/synq/internal/api"
	"github.com/HomieB-tt/synq/internal/ui/styles"
)

func commentsN(from, n int) []api.Comment {
	out := make([]api.Comment, n)
	for i := range out {
		out[i] = api.Comment{
			ID:        fmt.Sprintf("c%d", from+i),
			Author:    "amy",
			Content:   fmt.Sprintf("comment number %d", from+i),
			CreatedAt: "2026-10-18T08:00:00Z",
		}
	}
	return out
}

// openPostModel is the Feed with the first post open and n comments
// loaded, in a 100x30 terminal.
func openPostModel(n int) Model {
	m := feedModel()
	m.theme = styles.Default()
	m.width, m.height = 100, 30
	m.feedOpen = true
	m.feedComments = commentsN(1, n)
	m.feedCommentsLoaded = true
	return m
}

func press(t *testing.T, m Model, k tea.KeyPressMsg) Model {
	t.Helper()
	next, _, _ := m.updateFeedKeys(k)
	return next.(Model)
}

// However many comments there are, the post view is exactly as tall as
// the pane. Before this it simply grew, pushing the tab bar off the top.
func TestPostViewIsExactlyAsTallAsThePane(t *testing.T) {
	for _, n := range []int{0, 1, 5, 50, 300} {
		m := openPostModel(n)
		_, ih := m.innerSize()
		out := plain(m.renderPost())
		if got := len(strings.Split(out, "\n")); got != ih {
			t.Errorf("%d comments: post view is %d rows, pane has %d", n, got, ih)
		}
		w, _ := m.innerSize()
		for _, l := range strings.Split(out, "\n") {
			if lipgloss.Width(l) > w {
				t.Errorf("%d comments: a line is %d wide, pane is %d: %q", n, lipgloss.Width(l), w, l)
				break
			}
		}
	}
}

func TestPostViewScrollsThroughALongThread(t *testing.T) {
	m := openPostModel(300)

	top := plain(m.renderPost())
	if !strings.Contains(top, "first") || strings.Contains(top, "comment number 300") {
		t.Fatalf("the top of the post should show the title and not the last comment:\n%s", top)
	}
	if !strings.Contains(top, "more below") || strings.Contains(top, "more above") {
		t.Errorf("at the top, only 'more below' should show:\n%s", top)
	}

	m = press(t, m, keyPressWith("j"))
	if m.feedScroll != 1 {
		t.Errorf("j scrolled to %d, want 1", m.feedScroll)
	}
	m = press(t, m, keyPressWith("k"))
	m = press(t, m, keyPressWith("k"))
	if m.feedScroll != 0 {
		t.Errorf("scrolling above the top: %d", m.feedScroll)
	}

	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyPgDown})
	if want := m.postViewRows() - 1; m.feedScroll != want {
		t.Errorf("pgdown scrolled to %d, want %d", m.feedScroll, want)
	}

	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnd})
	if m.feedScroll != m.maxPostScroll() || m.feedScroll == 0 {
		t.Errorf("end scrolled to %d, max is %d", m.feedScroll, m.maxPostScroll())
	}
	bottom := plain(m.renderPost())
	if !strings.Contains(bottom, "comment number 300") {
		t.Errorf("the end of the post should show the last comment:\n%s", bottom)
	}
	if strings.Contains(bottom, "more below") || !strings.Contains(bottom, "more above") {
		t.Errorf("at the end, only 'more above' should show:\n%s", bottom)
	}
	m = press(t, m, keyPressWith("j"))
	if m.feedScroll != m.maxPostScroll() {
		t.Error("scrolled past the end")
	}

	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyHome})
	if m.feedScroll != 0 {
		t.Errorf("home scrolled to %d", m.feedScroll)
	}
}

func TestAShortPostDoesNotScroll(t *testing.T) {
	m := openPostModel(2)
	m = press(t, m, keyPressWith("j"))
	if m.feedScroll != 0 || strings.Contains(plain(m.renderPost()), "more below") {
		t.Errorf("a post that fits scrolled or claimed more: scroll=%d", m.feedScroll)
	}
}

func TestFullPageOfCommentsOffersMoreAndMAppendsTheNextPage(t *testing.T) {
	m := openPostModel(0)
	m.feedComments, m.feedCommentsLoaded = nil, false

	next, _ := m.handleCommentsLoaded(commentsLoadedMsg{
		postID: "p1", comments: commentsN(1, commentPageSize), offset: 0, limit: commentPageSize,
	})
	m = next.(Model)
	if !m.feedCommentsMore || len(m.feedComments) != commentPageSize {
		t.Fatalf("after a full page: more=%v, have %d", m.feedCommentsMore, len(m.feedComments))
	}
	view := plain(m.renderPost())
	if !strings.Contains(view, "Comments (50+)") || !strings.Contains(view, "m more comments") {
		t.Errorf("a full page should say there may be more:\n%s", view)
	}

	// m asks for the next page, starting where this one ends.
	_, cmd, handled := m.updateFeedKeys(keyPressWith("m"))
	if !handled || cmd == nil {
		t.Fatalf("m did not load more: handled=%v cmd=%v", handled, cmd)
	}

	next, _ = m.handleCommentsLoaded(commentsLoadedMsg{
		postID: "p1", comments: commentsN(commentPageSize+1, 7), offset: commentPageSize, limit: commentPageSize,
	})
	m = next.(Model)
	if len(m.feedComments) != commentPageSize+7 || m.feedCommentsMore {
		t.Errorf("after the last page: have %d, more=%v", len(m.feedComments), m.feedCommentsMore)
	}
	if got := m.feedComments[commentPageSize].ID; got != fmt.Sprintf("c%d", commentPageSize+1) {
		t.Errorf("pages were not appended in order: %s", got)
	}
	if strings.Contains(plain(m.renderPost()), "m more comments") {
		t.Error("still offering more when the thread has ended")
	}
	if _, cmd, _ := m.updateFeedKeys(keyPressWith("m")); cmd != nil {
		t.Error("m fetched again with nothing left")
	}
}

func TestAPageThatNoLongerFitsIsDropped(t *testing.T) {
	m := openPostModel(10)
	// A refresh replaced what was on screen; a late page for offset 50 would
	// leave a gap after comment 10.
	next, _ := m.handleCommentsLoaded(commentsLoadedMsg{
		postID: "p1", comments: commentsN(51, 5), offset: 50, limit: commentPageSize,
	})
	if got := len(next.(Model).feedComments); got != 10 {
		t.Errorf("a page that does not follow on was kept: %d comments", got)
	}
}

func TestRefreshKeepsWhatWasLoadedAndLooksOnePastIt(t *testing.T) {
	for _, c := range []struct{ have, extra, want int }{
		{0, 0, commentPageSize},                 // nothing yet: one page
		{10, 0, commentPageSize},                // a short thread: one page
		{120, 0, 121},                           // keep all 120 and look one past
		{120, 1, 122},                           // a comment was just added
		{maxCommentsFetch, 1, maxCommentsFetch}, // never past the server's cap
	} {
		if got := commentReloadLimit(c.have, c.extra); got != c.want {
			t.Errorf("commentReloadLimit(%d, %d) = %d, want %d", c.have, c.extra, got, c.want)
		}
	}
	m := openPostModel(120)
	if cmd := m.reloadCommentsCmd("p1", 0); cmd == nil {
		t.Error("reload built no command")
	}
}

func TestOpeningAndClosingAPostResetsScrollAndPaging(t *testing.T) {
	m := openPostModel(300)
	m.feedCommentsMore = true
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnd})
	if m.feedScroll == 0 {
		t.Fatal("setup: did not scroll")
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.feedOpen || m.feedScroll != 0 || m.feedCommentsMore || m.feedComments != nil {
		t.Errorf("closing left state behind: open=%v scroll=%d more=%v n=%d",
			m.feedOpen, m.feedScroll, m.feedCommentsMore, len(m.feedComments))
	}
}

func TestCommentAddedTellsYouWhenTheThreadHasMoreToLoad(t *testing.T) {
	m := openPostModel(commentPageSize)
	m.feedCommentsMore = true
	m.composeOpen = true
	next, cmd := m.handleFeedAction(feedActionMsg{verb: "comment", postID: "p1"})
	got := next.(Model)
	if cmd == nil {
		t.Error("the thread was not reloaded after commenting")
	}
	if !strings.Contains(got.commandMsg, "press m") {
		t.Errorf("no hint that the new comment is past the loaded pages: %q", got.commandMsg)
	}

	m = openPostModel(3)
	m.composeOpen = true
	next, _ = m.handleFeedAction(feedActionMsg{verb: "comment", postID: "p1"})
	if got := next.(Model).commandMsg; got != "Comment added." {
		t.Errorf("a short thread should just say %q, got %q", "Comment added.", got)
	}
}

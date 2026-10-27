package app

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/HomieB-tt/synq/internal/chat"
	"github.com/HomieB-tt/synq/internal/lineedit"
)

// This file draws the Chat tab. The key handling it pairs with lives in
// model.go (updateChatCompose for an open thread, updateNormalMode for
// the thread list); the state it reads is documented on Model.
//
// Three rules shape everything here, each learned from a bug the rest of
// the TUI already had to fix:
//
//   - Every span carries the theme background (see styles.Theme.Text).
//     A bare string between two styled spans loses it.
//   - Nothing is wider or taller than the pane it's drawn in. lipgloss
//     pads a block up to a size but never cuts one down, so the view
//     measures the pane (innerSize) and builds to fit rather than hoping
//     a long thread stays short.
//   - Only what's on screen is built. A thread can hold thousands of
//     messages; drawing one frame touches roughly one screenful of them
//     (see chatMessageArea) and never deep-copies the whole history.

const (
	// chatBar is the left-edge marker that sets a message block apart
	// from its neighbours - the cheap alternative to boxing every
	// message in a bubble.
	chatBar = "▎"

	// chatGroupGap is how long a sender can go quiet before their next
	// message gets a fresh name-and-time header instead of continuing
	// the previous block.
	chatGroupGap = 5 * time.Minute

	// chatBoxedComposerMinHeight is the pane height below which the
	// composer drops its border (3 rows) for a bare prompt (1 row), so a
	// short terminal still shows some of the conversation.
	chatBoxedComposerMinHeight = 12

	// chatMaxStatusLines caps the trust banner. The longest message it
	// can carry (a pinned-key mismatch) wraps to a few lines on a narrow
	// terminal; past this it is cut, since the composer hint and the
	// bottom bar repeat the actionable part.
	chatMaxStatusLines = 3

	// chatPageStep is how many messages PgUp / PgDn move.
	chatPageStep = 5

	// chatFixedRows is roughly how many rows of an open thread go to
	// things other than messages (header, banner, spacer, composer,
	// hint) - used to work out how much conversation fits on screen.
	chatFixedRows = 7
)

// maxPaneWidth caps how wide the pane (and the bottom bar under it) gets.
// Past this, text lines are too long to read comfortably and everything
// hugs the left edge of a wide terminal, so the pane stays this wide and
// is centred instead. The tab bar above it still spans the window.
const maxPaneWidth = 110

// screenWidth is the terminal width, with the fallback used before the
// first WindowSizeMsg arrives.
func (m Model) screenWidth() int {
	if m.width <= 0 {
		return 80
	}
	return m.width
}

// paneWidth is the outer width of the pane: the terminal, up to
// maxPaneWidth.
func (m Model) paneWidth() int {
	return min(m.screenWidth(), maxPaneWidth)
}

// paneMargin is the blank space to the left of a centred pane.
func (m Model) paneMargin() int {
	return (m.screenWidth() - m.paneWidth()) / 2
}

// indentPane shifts every line right by the pane margin, filling the gap
// with the theme background so no cell is left unstyled.
func (m Model) indentPane(block string) string {
	n := m.paneMargin()
	if n <= 0 {
		return block
	}
	gap := lipgloss.NewStyle().Background(m.theme.Palette.Background).Render(strings.Repeat(" ", n))
	lines := strings.Split(block, "\n")
	for i, l := range lines {
		lines[i] = gap + l
	}
	return strings.Join(lines, "\n")
}

// panelStyle is the bordered pane every tab's body is drawn in. Both
// renderContent and innerSize derive their numbers from this one style,
// so the pane and the maths that fits things inside it can't drift apart.
func (m Model) panelStyle() lipgloss.Style {
	return m.theme.Border.Padding(1, 2)
}

// bottomLines is the bottom bar as styled rows: one for the key hints or
// the command prompt, or the last command result wrapped to the terminal
// width - several of those results are a full sentence or two. Capped at
// four rows so a runaway message can't eat the screen.
func (m Model) bottomLines() []string {
	width := m.paneWidth()
	if m.commandMode {
		return []string{m.commandBarLine()}
	}
	if s := m.transferStatus(width); s != "" {
		return []string{m.theme.AccentText.Render(s)}
	}
	if m.commandMsg != "" {
		wrapped := wrapToWidth(m.commandMsg, width)
		if len(wrapped) > 4 {
			wrapped = wrapped[:4]
			wrapped[3] = truncateToWidth(wrapped[3]+"…", width)
		}
		out := make([]string, len(wrapped))
		for i, l := range wrapped {
			out[i] = m.theme.Warning.Render(l)
		}
		return out
	}
	return []string{m.theme.StatusBar.Render(truncateToWidth(m.hintText(), width))}
}

// contentSize is the outer size of the bordered pane: the whole terminal
// minus the header row, the spacer row under it, and however many rows
// the bottom bar needs this frame. (lipgloss v2's Width and Height
// include the border and padding, so this is the pane's true footprint.)
func (m Model) contentSize() (w, h int) {
	w = m.paneWidth()
	h = m.height
	if h <= 0 {
		h = 24
	}
	h -= 2 + len(m.bottomLines())
	if h < 3 {
		h = 3
	}
	return w, h
}

// innerSize is the room left for a tab's body once the pane's border and
// padding are taken out.
func (m Model) innerSize() (w, h int) {
	ow, oh := m.contentSize()
	fx, fy := m.panelStyle().GetFrameSize()
	return max(ow-fx, 1), max(oh-fy, 1)
}

// ---------------------------------------------------------------------
// Thread list
// ---------------------------------------------------------------------

// renderChat draws the Chat tab: the thread list when no thread is open
// (m.chatActive == ""), or one open conversation otherwise. See the
// Model.chatStore doc comment for what "session-only" means here.
func (m Model) renderChat() string {
	if m.chatActive == "" {
		return m.renderChatList()
	}
	return m.renderChatThread()
}

func (m Model) renderChatList() string {
	t := m.theme
	w, h := m.innerSize()
	threads := m.orderedThreads()

	title := t.AccentText.Render("Chats")
	if len(threads) == 0 {
		lines := []string{title, "", t.Text.Render("No open chats yet.")}
		for _, l := range wrapToWidth("Start one with :chat <username>. Chat history is session-only - quitting wipes it.", w) {
			lines = append(lines, t.Muted.Render(l))
		}
		return strings.Join(lines, "\n")
	}

	sel := min(max(m.chatListIndex, 0), len(threads)-1)

	// Rows available: everything but the title, its spacer and the
	// footer. The window follows the selection so a long list scrolls
	// instead of overflowing.
	rows := max(h-3, 1)
	start := 0
	if len(threads) > rows {
		start = min(max(sel-rows/2, 0), len(threads)-rows)
	}
	end := min(start+rows, len(threads))

	lines := []string{title + t.Muted.Render(fmt.Sprintf("  %d open", len(threads))), ""}
	for i := start; i < end; i++ {
		lines = append(lines, m.chatListRow(threads[i], i == sel, w))
	}
	for len(lines) < h-1 {
		lines = append(lines, "")
	}
	lines = append(lines, t.Muted.Render(truncateToWidth("↑/↓ select · enter open · :chat <username> starts a new thread", w)))
	return strings.Join(lines, "\n")
}

// chatListRow draws one thread: a focus marker, the contact, how many
// messages it holds, a preview of the latest, and its time - the whole
// row filled with the Selected background when it's the highlighted one.
func (m Model) chatListRow(contact chat.ContactKey, selected bool, w int) string {
	base, muted, accent, _ := m.rowStyles(selected)

	marker, markerStyle := "  ", base
	if selected {
		marker, markerStyle = "▌ ", accent
	}

	n := m.chatStore.Len(contact)
	var last chat.Message
	if s := m.chatStore.Slice(contact, n-1, n); len(s) == 1 {
		last = s[0]
	}

	const stampW = 5 // "15:04"
	name := truncateToWidth(m.chatContactLabel(contact), max(w/3, 8))
	count := fmt.Sprintf(" (%d)", n)
	unread := ""
	if u := m.unreadIn(contact); u > 0 {
		unread = fmt.Sprintf("  ● %d new", u)
	}

	// Whatever is left after the fixed parts goes to the preview; on a
	// narrow pane there may be none, and the row is just name and time.
	avail := w - lipgloss.Width(marker) - lipgloss.Width(name) - lipgloss.Width(count) - lipgloss.Width(unread) - 2 - 1 - stampW
	preview := ""
	if avail >= 6 {
		text := sanitizeForTerminal(string(last.Body))
		if last.Outgoing {
			text = "you: " + text
		} else if last.Author != "" {
			text = last.Author + ": " + text
		}
		preview = truncateToWidth(text, avail)
	}

	used := lipgloss.Width(marker) + lipgloss.Width(name) + lipgloss.Width(count) + lipgloss.Width(unread)
	var b strings.Builder
	b.WriteString(markerStyle.Render(marker))
	b.WriteString(base.Bold(true).Render(name))
	b.WriteString(muted.Render(count))
	if unread != "" {
		b.WriteString(accent.Bold(true).Render(unread))
	}
	if preview != "" {
		b.WriteString(base.Render("  "))
		b.WriteString(muted.Render(preview))
		used += 2 + lipgloss.Width(preview)
	}
	b.WriteString(base.Render(strings.Repeat(" ", max(w-used-stampW, 1))))
	b.WriteString(muted.Render(last.At.Format("15:04")))
	return b.String()
}

// ---------------------------------------------------------------------
// Open thread
// ---------------------------------------------------------------------

// renderChatThread lays out one conversation top to bottom: a header
// row, the trust banner, the messages (anchored to the bottom, newest
// last), the composer, and a hint line. Each fixed part is measured
// first and the messages get whatever rows remain.
func (m Model) renderChatThread() string {
	t := m.theme
	w, h := m.innerSize()
	contact := m.chatActive

	// Asked once and reused: it reads the pin store, and the banner,
	// composer and hint all depend on the answer.
	reason := m.keyBlockReason(contact)

	status := m.chatStatusLines(w, reason)
	boxed := h >= chatBoxedComposerMinHeight
	composer := m.chatComposer(w, boxed, reason != "")
	hint := t.Muted.Render(truncateToWidth(m.composeHintFor(reason), w))

	// The conversation and the composer are what the screen is for, so
	// on a short pane everything else gives way, least important first:
	// the spacer row, then the hint line, then the header. The trust
	// banner is trimmed rather than dropped (a key warning must never
	// vanish) but never to the point of crowding out the last message.
	status = status[:max(min(len(status), h-len(composer)-1), 1)]
	showSpacer, showHint, showHeader := true, true, true
	fixed := func() int {
		n := len(status) + len(composer)
		for _, shown := range []bool{showSpacer, showHint, showHeader} {
			if shown {
				n++
			}
		}
		return n
	}
	for _, drop := range []*bool{&showSpacer, &showHint, &showHeader} {
		if fixed()+1 <= h {
			break
		}
		*drop = false
	}

	rows := max(h-fixed(), 1)
	area, hasAbove := m.chatMessageArea(w, rows)

	lines := make([]string, 0, h)
	if showHeader {
		lines = append(lines, m.chatHeader(w, hasAbove))
	}
	lines = append(lines, status...)
	if showSpacer {
		lines = append(lines, "")
	}
	lines = append(lines, area...)
	lines = append(lines, composer...)
	if showHint {
		lines = append(lines, hint)
	}
	return strings.Join(lines, "\n")
}

// chatHeader is the contact's name on the left and, on the right, where
// the view is relative to the live end of the conversation.
func (m Model) chatHeader(w int, hasAbove bool) string {
	t := m.theme
	left := t.AccentText.Render(truncateToWidth(m.chatContactLabel(m.chatActive), max(w-20, 8)))

	var marks []string
	if hasAbove {
		marks = append(marks, "↑ earlier")
	}
	if off := m.chatOffset(); off > 0 {
		marks = append(marks, fmt.Sprintf("↓ %d newer · ctrl+end", off))
	}
	right := strings.Join(marks, "  ")
	gap := w - lipgloss.Width(left) - lipgloss.Width(right)
	if right == "" || gap < 2 {
		return left
	}
	return left + t.Text.Render(strings.Repeat(" ", gap)) + t.Muted.Render(right)
}

// chatStatusLines is the trust banner under the header: how far this
// thread is from being able to carry a message, in order of how much
// the user needs to know. A key mismatch outranks everything - it is
// the one state the user must act on before anything will send
// (DESIGN.md section 2) - and a thread that can already carry messages
// says so quietly rather than saying nothing, because "encrypted" is
// the reassurance this screen exists to give.
func (m Model) chatStatusLines(w int, blockReason string) []string {
	t := m.theme
	if id, ok := roomIDOf(m.chatActive); ok {
		return m.roomStatusLines(id, w)
	}
	if blockReason != "" {
		lines := wrapToWidth("⚠ "+blockReason, w)
		if len(lines) > chatMaxStatusLines {
			lines = lines[:chatMaxStatusLines]
			lines[chatMaxStatusLines-1] = truncateToWidth(lines[chatMaxStatusLines-1]+"…", w)
		}
		out := make([]string, len(lines))
		for i, l := range lines {
			out[i] = t.Warning.Render(l)
		}
		return out
	}

	switch {
	case m.wsClient == nil:
		return []string{t.Muted.Render(truncateToWidth("○ offline - anything you send stays local until the connection is back", w))}
	case !m.hasSessionKey(m.chatActive):
		return []string{t.Muted.Render(truncateToWidth("◌ securing - messages go out as soon as this contact's key arrives", w))}
	}
	return []string{lipgloss.NewStyle().
		Foreground(t.Success).
		Background(t.Palette.Background).
		Render("● end-to-end encrypted")}
}

// chatMessageArea returns exactly rows lines of conversation, newest at
// the bottom (padded with blank rows above when the thread is short), and
// whether older messages exist above what's shown.
//
// Scroll position is counted in messages back from the newest, not in
// wrapped lines. That makes the upper bound exact and free (the thread
// length) where a line count would need every message wrapped - and
// wrapping a long thread on each keypress is exactly the cost this
// avoids. It also means a frame only ever builds blocks for the last few
// messages: each message is at least one row, so `rows` of them are
// always enough to fill the area.
func (m Model) chatMessageArea(w, rows int) (lines []string, hasAbove bool) {
	t := m.theme
	contact := m.chatActive
	total := m.chatStore.Len(contact)

	if total == 0 {
		for _, l := range wrapToWidth("No messages yet - type below and press enter.", w) {
			lines = append(lines, t.Muted.Render(l))
		}
		if len(lines) > rows {
			lines = lines[:rows]
		}
		return padTop(lines, rows), false
	}

	off := min(max(m.chatOffset(), 0), total-1)
	end := total - off
	start := max(end-rows-1, 0) // one extra message of context for grouping
	msgs := m.chatStore.Slice(contact, start, end)

	var blocks [][]string
	used := 0
	first := len(msgs) // index in msgs of the oldest message drawn
	for i := len(msgs) - 1; i >= 0 && used < rows; i-- {
		var prev *chat.Message
		if i > 0 {
			prev = &msgs[i-1]
		}
		b := m.chatMessageLines(prev, msgs[i], w)
		if m.chatMarkFor == contact && m.chatMarkCount > 0 && start+i == m.chatMarkIdx {
			// The unread boundary sits directly above the first message
			// that arrived while you were away; the spacer row a new
			// sender's block starts with would only separate the two.
			if len(b) > 0 && b[0] == "" {
				b = b[1:]
			}
			b = append([]string{m.chatDivider(w, m.chatMarkCount)}, b...)
		}
		blocks = append(blocks, b)
		used += len(b)
		first = i
	}
	hasAbove = start+first > 0

	// blocks run newest-first; the screen reads oldest-first.
	for i := len(blocks) - 1; i >= 0; i-- {
		lines = append(lines, blocks[i]...)
	}
	// A message taller than the room (or a last block that overshoots)
	// is cut from the top, so the newest lines are the ones that stay.
	if len(lines) > rows {
		lines = lines[len(lines)-rows:]
		hasAbove = true
	}
	return padTop(lines, rows), hasAbove
}

// chatDivider is the "N new messages" rule drawn above the first unread
// message: a labelled horizontal line the width of the pane.
func (m Model) chatDivider(w, n int) string {
	label := fmt.Sprintf(" %d new messages ", n)
	if n == 1 {
		label = " 1 new message "
	}
	label = truncateToWidth(label, w)
	left := max((w-lipgloss.Width(label))/2, 0)
	right := max(w-left-lipgloss.Width(label), 0)
	return m.theme.AccentText.Render(strings.Repeat("─", left) + label + strings.Repeat("─", right))
}

// syncChatMarker keeps the unread divider and the unread counts in step
// with what is on screen. It runs after every Update.
//
//   - When a thread comes on screen, the divider is snapshotted: it goes
//     above the first message that arrived while you were away, and stays
//     on that message for as long as the thread stays on screen, however
//     much more arrives - so it keeps meaning "this is where you left
//     off". A thread opened with unread messages also scrolls to it.
//   - When the thread goes off screen (back to the list, or another tab)
//     the snapshot is dropped, so coming back to new messages shows a
//     fresh divider and coming back to none shows no divider.
//   - A thread on screen has nothing unread.
func (m *Model) syncChatMarker() {
	if m.chatStore == nil || !m.viewingThread(m.chatActive) {
		m.chatMarkFor, m.chatMarkCount, m.chatMarkIdx = "", 0, 0
		return
	}
	if m.chatMarkFor != m.chatActive {
		total := m.chatStore.Len(m.chatActive)
		count := min(m.unread[m.chatActive], total)
		m.chatMarkFor, m.chatMarkCount, m.chatMarkIdx = m.chatActive, count, total-count
		if count > 0 {
			// Land with the divider in view, as near the top of the
			// conversation area as it will go.
			m.chatScroll = m.offsetToShowFrom(m.chatMarkIdx)
			m.chatScrollFor = m.chatActive
		}
	}
	delete(m.unread, m.chatActive)
}

// offsetToShowFrom is the scroll offset (messages back from the newest,
// see chatOffset) that puts message first - with the unread divider
// above it - at the top of the conversation area, followed by as many of
// the messages after it as fit. If everything from first to the newest
// already fits, that is offset 0: the live view.
//
// It lays the messages out rather than guessing a rows-per-message: a
// message is anywhere from one row to three (a header and a spacer when
// the sender changes or has been quiet), so a guess scrolls too far in
// one thread and not far enough in another.
func (m Model) offsetToShowFrom(first int) int {
	contact := m.chatActive
	total := m.chatStore.Len(contact)
	if first < 0 || first >= total {
		return 0
	}
	w, h := m.innerSize()
	rows := max(h-chatFixedRows, 1)

	from := max(first-1, 0) // one earlier message, for grouping context
	msgs := m.chatStore.Slice(contact, from, min(first+rows, total))

	used := 1 // the divider itself
	last := first
	for i := first - from; i < len(msgs); i++ {
		var prev *chat.Message
		if i > 0 {
			prev = &msgs[i-1]
		}
		b := m.chatMessageLines(prev, msgs[i], w)
		if i == first-from && len(b) > 0 && b[0] == "" {
			b = b[1:] // the divider replaces the block's leading spacer
		}
		if i > first-from && used+len(b) > rows {
			break // this one would push the divider off the top
		}
		used += len(b)
		last = from + i
	}
	return max(total-1-last, 0)
}

// padTop pads lines with blank rows above until there are n, so a short
// conversation sits against the composer instead of floating at the top.
func padTop(lines []string, n int) []string {
	if len(lines) >= n {
		return lines
	}
	out := make([]string, 0, n)
	for i := len(lines); i < n; i++ {
		out = append(out, "")
	}
	return append(out, lines...)
}

// chatMessageLines draws one message. The first message of a run from
// the same sender (or the first after a quiet spell) gets a header with
// the sender's name and the time and a blank row above it; the rest of
// the run is just body lines under the same bar. Your own messages carry
// an accent bar and name, theirs a muted bar - alignment-free, so it
// reads the same at any width.
func (m Model) chatMessageLines(prev *chat.Message, msg chat.Message, w int) []string {
	t := m.theme
	barColor := t.Palette.Muted
	if msg.Outgoing {
		barColor = t.Palette.Accent
	}
	bar := t.Text.Foreground(barColor).Render(chatBar)
	sp := t.Text.Render(" ")

	if msg.System {
		// A line the app wrote ("alice joined"): muted, no sender.
		var sys []string
		if prev != nil {
			sys = append(sys, "")
		}
		for _, l := range wrapToWidth(sanitizeForTerminal(string(msg.Body)), max(w-2, 1)) {
			sys = append(sys, t.Muted.Render("· "+l))
		}
		return sys
	}

	var lines []string
	groupStart := prev == nil || prev.System || prev.Outgoing != msg.Outgoing || prev.Author != msg.Author || msg.At.Sub(prev.At) > chatGroupGap
	if msg.Quote != "" {
		groupStart = true // a reply always gets its own header and quote
	}
	if groupStart {
		if prev != nil && !prev.System {
			lines = append(lines, "")
		}
		who := m.chatContactLabel(m.chatActive)
		if msg.Author != "" {
			who = sanitizeForTerminal(msg.Author)
		}
		name := t.Text.Bold(true).Render(truncateToWidth(who, max(w-10, 8)))
		if msg.Outgoing {
			name = t.AccentText.Render("you")
		}
		lines = append(lines, bar+sp+name+t.Text.Render("  ")+t.Muted.Render(msg.At.Format("15:04")))
	}
	if msg.Quote != "" {
		lines = append(lines, bar+sp+t.Muted.Render(truncateToWidth("↪ "+msg.Quote, max(w-2, 1))))
	}

	text := sanitizeForTerminal(string(msg.Body))
	for _, l := range wrapToWidth(text, max(w-2, 1)) {
		lines = append(lines, bar+sp+t.Text.Render(l))
	}
	return lines
}

// chatComposer draws the input: a bordered box (or, on a short pane, a
// bare prompt) holding the part of the draft around the cursor. Showing a
// window of the draft rather than wrapping it keeps the composer a fixed
// height however long the draft grows, and keeps the cursor in view: with
// the cursor at the end that is the tail of the draft, and after moving
// back through it the window follows (lineedit.Window).
func (m Model) chatComposer(w int, boxed, blocked bool) []string {
	t := m.theme
	prompt := t.AccentText.Render("› ")
	cursor := t.AccentText.Render("█") // empty draft: the block, then the hint

	inner := w
	if boxed {
		inner = w - 4 // border and padding
	}
	textW := max(inner-lipgloss.Width("› ")-1, 1)

	var body string
	if m.chatInput == "" {
		body = prompt + cursor + t.Muted.Render(truncateToWidth(" Type a message…", max(textW, 1)))
	} else {
		before, after := lineedit.Split(m.chatInput, m.chatBack)
		b, a := lineedit.Window(sanitizeForTerminal(before), sanitizeForTerminal(after), textW, runeCells)
		body = prompt + t.Text.Render(b) + m.cursorSpans(a)
	}

	if !boxed {
		return []string{body}
	}
	box := t.BorderFocus
	if blocked {
		box = t.Border.BorderForeground(t.Palette.Warning)
	}
	return strings.Split(box.Padding(0, 1).Width(w).Render(body), "\n")
}

// chatOffset is how many messages back from the newest the open thread
// is scrolled. The offset is remembered for one thread at a time
// (chatScrollFor), so switching threads - by any of the several paths
// that set chatActive - lands at the live end without each of them
// having to remember to reset it.
func (m Model) chatOffset() int {
	if m.chatScrollFor != m.chatActive {
		return 0
	}
	return m.chatScroll
}

// scrollChat moves the open thread delta messages back (positive) or
// forward (negative), clamped to the thread: the bound is the thread
// length, so a held key can't pile up an offset that later takes as many
// presses to undo.
func (m *Model) scrollChat(delta int) {
	last := max(m.chatStore.Len(m.chatActive)-1, 0)
	m.chatScroll = min(max(m.chatOffset()+delta, 0), last)
	m.chatScrollFor = m.chatActive
}

// noteIncoming keeps a scrolled-back view where it is when a new message
// lands in that thread. The offset counts from the newest message, so
// without this each arrival would slide the view one message forward
// under the reader.
func (m *Model) noteIncoming(contact chat.ContactKey) {
	if m.chatScrollFor == contact && m.chatScroll > 0 {
		m.chatScroll++
	}
}

// wrapChatList moves the thread-list selection like moveChatList but
// wraps around at either end - the behaviour of the up/down keys.
func (m *Model) wrapChatList(delta int) {
	n := len(m.orderedThreads())
	if n == 0 {
		m.chatListIndex = 0
		return
	}
	m.chatListIndex = ((m.chatListIndex+delta)%n + n) % n
}

// orderedThreads is the thread list in display order: threads with
// unread messages first, then by most recent message, newest at the top.
// This is the order the list is drawn in and the order the selection
// index refers to - everything that indexes into the list goes through
// here, so what you see highlighted is what Enter opens. (chat.Store
// itself keeps first-message order; that is storage, not display.) Ties
// keep that stored order, so the list never shuffles for no reason.
func (m Model) orderedThreads() []chat.ContactKey {
	if m.chatStore == nil {
		return nil
	}
	threads := m.chatStore.Threads()
	type ranked struct {
		key    chat.ContactKey
		unread bool
		at     time.Time
	}
	rs := make([]ranked, len(threads))
	for i, k := range threads {
		rs[i] = ranked{key: k, unread: m.unread[k] > 0}
		if n := m.chatStore.Len(k); n > 0 {
			if last := m.chatStore.Slice(k, n-1, n); len(last) == 1 {
				rs[i].at = last[0].At
			}
		}
	}
	sort.SliceStable(rs, func(i, j int) bool {
		if rs[i].unread != rs[j].unread {
			return rs[i].unread
		}
		return rs[i].at.After(rs[j].at)
	})
	out := make([]chat.ContactKey, len(rs))
	for i, r := range rs {
		out[i] = r.key
	}
	return out
}

// selectedThread is the contact the list selection is on ("" if none).
func (m Model) selectedThread() chat.ContactKey {
	threads := m.orderedThreads()
	if len(threads) == 0 {
		return ""
	}
	return threads[min(max(m.chatListIndex, 0), len(threads)-1)]
}

// withListAnchor keeps the list selection on prev after the order may
// have changed. The selection is an index, and a message arriving
// re-ranks the list - without this the highlight would slide onto a
// different contact between one frame and the next, and Enter would open
// the wrong thread.
func (m Model) withListAnchor(prev chat.ContactKey) Model {
	if prev == "" {
		return m
	}
	for i, k := range m.orderedThreads() {
		if k == prev {
			m.chatListIndex = i
			break
		}
	}
	return m
}

// moveChatList moves the thread-list selection, clamped to the list.
func (m *Model) moveChatList(delta int) {
	n := len(m.orderedThreads())
	if n == 0 {
		m.chatListIndex = 0
		return
	}
	m.chatListIndex = min(max(m.chatListIndex+delta, 0), n-1)
}

// openSelectedChat opens the highlighted thread. Every thread in the
// list was created by a lookup or an inbound message that already
// recorded the contact (see rememberContact), so opening it directly is
// the same state `:chat <username>` would reach - minus the server
// round trip.
func (m *Model) openSelectedChat() {
	threads := m.orderedThreads()
	if len(threads) == 0 {
		return
	}
	m.chatActive = threads[min(max(m.chatListIndex, 0), len(threads)-1)]
	m.chatInput, m.chatBack = "", 0
}

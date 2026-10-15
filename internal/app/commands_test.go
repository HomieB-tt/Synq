package app

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// runCommandNames reads the command names runCommand dispatches on, from
// the source. A command added to the switch but not to commandSpecs would
// be invisible in the help panel, the command list and the prompts; this
// fails instead.
func runCommandNames(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile("model.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	start := strings.Index(src, "func (m *Model) runCommand(")
	if start < 0 {
		t.Fatal("runCommand not found")
	}
	body := src[start:]
	if end := strings.Index(body, "\n}\n"); end > 0 {
		body = body[:end]
	}
	var names []string
	for _, m := range regexp.MustCompile(`(?m)^\tcase ((?:"[a-z]+"(?:, )?)+):`).FindAllStringSubmatch(body, -1) {
		for _, q := range regexp.MustCompile(`"([a-z]+)"`).FindAllStringSubmatch(m[1], -1) {
			names = append(names, q[1])
		}
	}
	return names
}

func TestEveryCommandIsDocumented(t *testing.T) {
	dispatched := map[string]bool{}
	for _, n := range runCommandNames(t) {
		if n != "q" { // :q is the documented alias of :quit
			dispatched[n] = true
		}
	}
	if len(dispatched) < 15 {
		t.Fatalf("found only %d commands in runCommand - the source scan is broken", len(dispatched))
	}
	documented := map[string]bool{}
	for _, s := range commandSpecs {
		if documented[s.name] {
			t.Errorf("command %q is listed twice", s.name)
		}
		documented[s.name] = true
	}
	var missing, extra []string
	for n := range dispatched {
		if !documented[n] {
			missing = append(missing, n)
		}
	}
	for n := range documented {
		if !dispatched[n] {
			extra = append(extra, n)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 {
		t.Errorf("runCommand handles %v but commandSpecs doesn't list them", missing)
	}
	if len(extra) > 0 {
		t.Errorf("commandSpecs lists %v but runCommand doesn't handle them", extra)
	}
}

func TestCommandSpecsAreComplete(t *testing.T) {
	groups := map[string]bool{}
	for _, g := range commandGroups {
		groups[g] = true
	}
	for _, s := range commandSpecs {
		if !groups[s.group] {
			t.Errorf(":%s is in unknown group %q", s.name, s.group)
		}
		if s.brief == "" || s.detail == "" {
			t.Errorf(":%s needs a brief and a detail", s.name)
		}
		if s.prompt != "" && s.hint == "" {
			t.Errorf(":%s opens a prompt but has no hint under it", s.name)
		}
		if s.prompt != "" && s.fill {
			t.Errorf(":%s can't both open a prompt and pre-fill the palette", s.name)
		}
	}
	if len(helpCommands) != len(commandSpecs) {
		t.Errorf("help panel lists %d commands, registry has %d", len(helpCommands), len(commandSpecs))
	}
}

func commandIndex(t *testing.T, name string) int {
	t.Helper()
	for i, s := range commandSpecs {
		if s.name == name {
			return i
		}
	}
	t.Fatalf("no command %q", name)
	return -1
}

// --- prompts ---

func promptKeys(m Model, keys ...tea.KeyPressMsg) Model {
	for _, k := range keys {
		next, _ := m.updatePrompt(k)
		m = next.(Model)
	}
	return m
}

func typePrompt(m Model, s string) Model {
	for _, r := range s {
		m = promptKeys(m, keyPressWith(string(r)))
	}
	return m
}

// A bare :send opens a prompt, and what is typed reaches the command.
func TestBareSendOpensPromptAndSubmitRunsIt(t *testing.T) {
	m := fill(chatViewModel(t, 100, 30), 0, nil)
	m.activeTab = tabChat

	if res, quit, cmd := m.runCommand("send"); res != "" || quit || cmd != nil || !m.promptOpen {
		t.Fatalf("bare :send: res=%q quit=%v cmd=%v open=%v", res, quit, cmd != nil, m.promptOpen)
	}
	m = typePrompt(m, "/tmp/report.pdf")
	if m.prompt.input != "/tmp/report.pdf" {
		t.Fatalf("typed %q", m.prompt.input)
	}
	next, _ := m.updatePrompt(key(tea.KeyEnter))
	m = next.(Model)
	if m.promptOpen {
		t.Fatal("Enter should close the prompt")
	}
	// No server in this test model, so :send reaches its own "log in"
	// refusal - which proves the typed value was handed to the command.
	if !strings.Contains(m.commandMsg, "logged in") {
		t.Fatalf("command message = %q", m.commandMsg)
	}
}

// No point asking for a path when there is no chat to send it in.
func TestPromptRefusesUpFrontWhenTheCommandCouldNotRun(t *testing.T) {
	m := chatViewModel(t, 100, 30)
	res, _, _ := m.runCommand("send")
	if m.promptOpen || !strings.Contains(res, "Open a chat first") {
		t.Fatalf("open=%v res=%q", m.promptOpen, res)
	}
}

func TestPromptEscAndEmptyEnterCancel(t *testing.T) {
	m := fill(chatViewModel(t, 100, 30), 0, nil)
	m.activeTab = tabChat
	m.runCommand("send")
	m = typePrompt(m, "abc")
	m = promptKeys(m, key(tea.KeyEsc))
	if m.promptOpen || m.commandMsg != "Cancelled." || m.prompt.input != "" {
		t.Fatalf("esc: open=%v msg=%q input=%q", m.promptOpen, m.commandMsg, m.prompt.input)
	}

	m.runCommand("send")
	m = promptKeys(m, key(tea.KeyEnter)) // nothing typed
	if m.promptOpen || m.commandMsg != "Cancelled." {
		t.Fatalf("empty enter: open=%v msg=%q", m.promptOpen, m.commandMsg)
	}
}

func TestPromptStartsFromCurrentValueAndHasACursor(t *testing.T) {
	m := chatViewModel(t, 100, 30)
	m.saveDir = "/data/files"
	m.runCommand("savedir")
	if !m.promptOpen || m.prompt.input != "/data/files" {
		t.Fatalf("savedir prompt: open=%v input=%q", m.promptOpen, m.prompt.input)
	}
	m = promptKeys(m, key(tea.KeyLeft), key(tea.KeyLeft), keyPressWith("X"))
	if m.prompt.input != "/data/filXes" {
		t.Fatalf("input = %q", m.prompt.input)
	}
}

func TestBareCommandsThatNeedAnArgumentAllPrompt(t *testing.T) {
	for _, s := range commandSpecs {
		if s.prompt == "" {
			continue
		}
		m := fill(chatViewModel(t, 100, 30), 0, nil)
		m.activeTab = tabChat
		m.identity = nil
		m.apiClient = nil
		m.runCommand(s.name)
		// Either a prompt opened, or a reason it couldn't - never silence.
		if !m.promptOpen {
			res, _, _ := m.runCommand(s.name)
			if res == "" {
				t.Errorf("bare :%s did nothing at all", s.name)
			}
		}
	}
}

func TestPromptRenderFitsAndShowsHint(t *testing.T) {
	m := fill(chatViewModel(t, 80, 24), 0, nil)
	m.activeTab = tabChat
	m.runCommand("send")
	m = typePrompt(m, strings.Repeat("long/path/", 20))
	out := plain(m.renderPrompt())
	if !strings.Contains(out, "File to send") || !strings.Contains(out, "full path") {
		t.Errorf("prompt missing label or hint:\n%s", out)
	}
	w, _ := m.overlayArea()
	for _, l := range strings.Split(out, "\n") {
		if len([]rune(l)) > w {
			t.Errorf("prompt line wider than the pane (%d > %d): %q", len([]rune(l)), w, l)
		}
	}
}

// --- command list ---

func TestCommandListOpensFromProfileAndPalette(t *testing.T) {
	m := chatViewModel(t, 100, 30)
	m.activeTab = tabProfile
	next, _ := m.Update(keyPressWith("c"))
	m = next.(Model)
	if !m.cmdListOpen {
		t.Fatal("c on the Profile tab should open the command list")
	}
	next, _ = m.Update(key(tea.KeyEsc))
	m = next.(Model)
	if m.cmdListOpen {
		t.Fatal("esc should close it")
	}

	// Elsewhere "c" is not a key.
	m.activeTab = tabNodes
	next, _ = m.Update(keyPressWith("c"))
	if next.(Model).cmdListOpen {
		t.Fatal("c opened the list outside the Profile tab")
	}

	m.runCommand("commands")
	if !m.cmdListOpen {
		t.Fatal(":commands should open the list")
	}
}

func TestCommandListSelectionIsClamped(t *testing.T) {
	m := chatViewModel(t, 100, 30)
	m.openCommandList()
	next, _ := m.updateCommandList(key(tea.KeyUp))
	m = next.(Model)
	if m.cmdListIndex != 0 {
		t.Fatalf("index = %d", m.cmdListIndex)
	}
	next, _ = m.updateCommandList(key(tea.KeyEnd))
	m = next.(Model)
	next, _ = m.updateCommandList(key(tea.KeyDown))
	m = next.(Model)
	if m.cmdListIndex != len(commandSpecs)-1 {
		t.Fatalf("index = %d, want %d", m.cmdListIndex, len(commandSpecs)-1)
	}
}

func TestCommandListEnterRunsPromptsOrFills(t *testing.T) {
	enterOn := func(m Model, name string) Model {
		m.openCommandList()
		m.cmdListIndex = commandIndex(t, name)
		next, _ := m.updateCommandList(key(tea.KeyEnter))
		return next.(Model)
	}

	// A command with a prompt opens it (here refused, as there's no chat,
	// and the reason is shown).
	m := enterOn(chatViewModel(t, 100, 30), "send")
	if m.cmdListOpen || !strings.Contains(m.commandMsg, "Open a chat first") {
		t.Fatalf("send: open=%v msg=%q", m.cmdListOpen, m.commandMsg)
	}
	m = fill(chatViewModel(t, 100, 30), 0, nil)
	m.activeTab = tabChat
	m = enterOn(m, "send")
	if !m.promptOpen {
		t.Fatal("choosing :send in a chat should open its prompt")
	}

	// A destructive command pre-fills the palette rather than running.
	m = enterOn(chatViewModel(t, 100, 30), "block")
	if !m.commandMode || m.commandInput != "block " {
		t.Fatalf("block: mode=%v input=%q", m.commandMode, m.commandInput)
	}

	// A plain command just runs.
	m = enterOn(chatViewModel(t, 100, 30), "theme")
	if !m.themePickerOpen {
		t.Fatal("choosing :theme should open the theme picker")
	}
}

func TestCommandListRenderFitsAndExplains(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {100, 40}, {120, 50}} {
		m := chatViewModel(t, size[0], size[1])
		m.openCommandList()
		for _, name := range []string{"node", "send", "quit"} {
			m.cmdListIndex = commandIndex(t, name)
			out := plain(m.renderCommandList())
			lines := strings.Split(out, "\n")
			_, h := m.overlayArea()
			if len(lines) > h {
				t.Errorf("%dx%d (%s): %d lines > %d available", size[0], size[1], name, len(lines), h)
			}
			spec, _ := specByName(name)
			firstWord := strings.Fields(spec.detail)[0]
			if !strings.Contains(out, firstWord) {
				t.Errorf("%dx%d: description of :%s not shown:\n%s", size[0], size[1], name, out)
			}
			if !strings.Contains(out, "›") {
				t.Errorf("no selection marker:\n%s", out)
			}
		}
	}
}

// --- welcome and profile ---

func TestWelcomePagesThroughAndTheKeyIsConsumed(t *testing.T) {
	m := chatViewModel(t, 100, 30)
	m.welcomeOpen = true

	// First key: next page, and it must not also act on the tab underneath.
	next, _ := m.Update(keyPressWith("4"))
	m = next.(Model)
	if !m.welcomeOpen || m.welcomePage != 1 || m.activeTab == tabProfile {
		t.Fatalf("after first key: open=%v page=%d tab=%v", m.welcomeOpen, m.welcomePage, m.activeTab)
	}
	// Last page: any key closes (no store in this model - must not panic).
	next, _ = m.Update(keyPressWith("x"))
	m = next.(Model)
	if m.welcomeOpen || m.welcomePage != 0 {
		t.Fatalf("after last key: open=%v page=%d", m.welcomeOpen, m.welcomePage)
	}
}

func TestWelcomeEscSkipsFromAnyPage(t *testing.T) {
	m := chatViewModel(t, 100, 30)
	m.welcomeOpen = true
	next, _ := m.Update(key(tea.KeyEsc))
	if next.(Model).welcomeOpen {
		t.Fatal("esc should close the guide from the first page")
	}
}

func TestWelcomeCommandReopensFromTheStart(t *testing.T) {
	m := chatViewModel(t, 100, 30)
	m.welcomePage = 1
	m.runCommand("welcome")
	if !m.welcomeOpen || m.welcomePage != 0 {
		t.Fatalf("open=%v page=%d", m.welcomeOpen, m.welcomePage)
	}
}

func TestWelcomePagesFitAndCoverTheBasics(t *testing.T) {
	var all strings.Builder
	for page := range welcomePages {
		for _, size := range [][2]int{{80, 24}, {100, 40}} {
			m := chatViewModel(t, size[0], size[1])
			m.welcomePage = page
			out := plain(m.renderWelcome())
			all.WriteString(out)
			if _, h := m.overlayArea(); len(strings.Split(out, "\n")) > h {
				t.Errorf("page %d at %dx%d is taller than the pane (%d > %d)",
					page+1, size[0], size[1], len(strings.Split(out, "\n")), h)
			}
		}
	}
	for _, want := range []string{"Welcome to Synq", "press c", ":register", ":node", ":chat", ":send", ":welcome"} {
		if !strings.Contains(all.String(), want) {
			t.Errorf("welcome guide never mentions %q", want)
		}
	}
}

func TestProfilePointsAtTheCommandList(t *testing.T) {
	m := chatViewModel(t, 100, 30)
	if out := m.renderProfile(); !strings.Contains(out, "Press c") {
		t.Errorf("guest profile doesn't mention the command list:\n%s", out)
	}
}

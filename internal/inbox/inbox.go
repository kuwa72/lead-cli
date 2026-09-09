package inbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/kuwa72/lead-cli/internal/dispatch"
	"github.com/kuwa72/lead-cli/internal/ports"
	"github.com/kuwa72/lead-cli/internal/state"
)

// Options wires the inbox to its collaborators. Only Gh is required.
type Options struct {
	Gh    ports.GhClient
	Store *state.Store // nil = no local sections
	Seen  *SeenStore   // nil = no read-state tracking
	Shell Shell        // nil = TeaShell
	// Herdr opens agent logs/panes in a new tab. Nil falls back to $PAGER.
	Herdr ports.HerdrRunner
	// Editor is $EDITOR (may carry flags: "code --wait"). Empty disables e/r.
	Editor string
	// AgentsPath is the repository's AGENTS.md for the r key.
	AgentsPath string
	// Repo is "owner/name" for the header (cosmetic).
	Repo string
	// Refresh > 0 reloads the sections periodically (interactive mode only).
	Refresh time.Duration
	Now     func() time.Time
	// Say turns a one-liner into needs-review issues via the spec AI
	// (`lead say`, issue #94) and returns a human summary. followUp > 0
	// files a 実機NG follow-up referencing that issue (inbox n key).
	// Nil disables s/n.
	Say func(ctx context.Context, oneLiner string, followUp int) (string, error)
	// Parallel is the dispatcher's parallel limit for the header.
	// 0 means dispatch.DefaultParallel.
	Parallel int
	// RunningCount returns the dispatcher's current running count.
	// Nil falls back to counting the running section.
	RunningCount func() (int, error)
}

type mode int

const (
	modeList mode = iota
	modeDetail
	modeInput
	modeHelp
)

type inputState struct {
	prompt string
	text   []rune
	submit func(text string) tea.Cmd
}

// Model is the bubbletea model. Construct with New.
type Model struct {
	opts         Options
	sections     []Section
	cursor       int
	expanded     bool
	mode         mode
	detail       ports.Issue
	detailOffset int
	detailPrBody string
	detailPrNum  int
	input        inputState
	status       string
	loadErr      error
	loading      bool
	refreshedAt  time.Time
	sessionSince time.Time // loaded last_seen_at at session start; refresh uses this
	width        int
	height       int
}

// Messages.
type (
	loadedMsg struct {
		sections     []Section
		err          error
		sessionSince time.Time
	}
	doneMsg struct {
		status  string
		err     error
		refresh bool
	}
	detailMsg struct {
		issue    ports.Issue
		prNumber int
		prBody   string
		err      error
	}
	editBodyMsg struct {
		issue ports.Issue
		err   error
	}
	editedMsg struct {
		number int
		body   string
		err    error
	}
	tickMsg time.Time
)

// New builds the model; call Init (or Run/RunHeadless) to load data.
func New(opts Options) Model {
	if opts.Shell == nil {
		opts.Shell = TeaShell{}
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Parallel <= 0 {
		opts.Parallel = dispatch.DefaultParallel
	}
	m := Model{opts: opts, sections: Build(nil, nil, nil, nil, nil), loading: true}
	if opts.Seen != nil {
		shown, err := opts.Seen.HelpShown()
		if err == nil && !shown {
			m.mode = modeHelp
		}
	}
	return m
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.loadCmd()}
	if m.opts.Refresh > 0 {
		cmds = append(cmds, m.tickCmd())
	}
	return tea.Batch(cmds...)
}

func (m Model) tickCmd() tea.Cmd {
	return tea.Tick(m.opts.Refresh, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m Model) loadCmd() tea.Cmd {
	opts := m.opts
	return func() tea.Msg {
		ctx := context.Background()

		var loadErr error
		var seen *SeenState
		if opts.Seen != nil {
			var err error
			seen, err = opts.Seen.Load()
			if err != nil {
				loadErr = err
				seen = &SeenState{}
			}
		}
		if seen == nil {
			seen = &SeenState{}
		}

		// sessionSince is the loaded last_seen_at for this session; refresh
		// reuses it so merged items stay visible until the next session.
		sessionSince := m.sessionSince
		if sessionSince.IsZero() {
			sessionSince = seen.LastSeenAt
			// Mark the session open so the next session starts from now.
			if opts.Seen != nil {
				seen.LastSeenAt = opts.Now()
				if err := opts.Seen.Save(seen); err != nil {
					loadErr = err
				}
			}
		}

		review, err := opts.Gh.ListByLabel(ctx, LabelNeedsReview)
		if err != nil {
			return loadedMsg{err: err}
		}
		blocked, err := opts.Gh.ListByLabel(ctx, LabelBlocked)
		if err != nil {
			return loadedMsg{err: err}
		}
		var wfs []state.Workflow
		if opts.Store != nil {
			if wfs, err = opts.Store.List(); err != nil {
				return loadedMsg{err: err}
			}
		}

		// Build uses the sessionSince (not the updated last_seen_at) so the
		// current session keeps seeing merges that happened at or after the
		// time the inbox opened.
		buildSeen := &SeenState{LastSeenAt: sessionSince, Confirmed: seen.Confirmed}
		var merged []ports.MergedIssue
		if merged, err = opts.Gh.ListMergedSince(ctx, sessionSince); err != nil {
			return loadedMsg{err: err}
		}

		sections := Build(review, blocked, merged, buildSeen, wfs)
		return loadedMsg{sections: sections, sessionSince: sessionSince, err: loadErr}
	}
}

// Row is one selectable line in the inbox: either a section header or an issue.
type Row struct {
	Section *Section
	Item    *Item
}

func (r Row) IsHeader() bool { return r.Item == nil }

// visible flattens the rows the cursor can reach.
func (m Model) visible() []Row {
	var out []Row
	for i := range m.sections {
		s := &m.sections[i]
		out = append(out, Row{Section: s})
		if s.Collapsed && !m.expanded {
			continue
		}
		for j := range s.Items {
			out = append(out, Row{Section: s, Item: &s.Items[j]})
		}
	}
	return out
}

func (m Model) current() (Row, bool) {
	rows := m.visible()
	if len(rows) == 0 {
		return Row{}, false
	}
	if m.cursor >= len(rows) {
		m.cursor = len(rows) - 1
	}
	return rows[m.cursor], true
}

func (m *Model) clampCursor() {
	n := len(m.visible())
	if m.cursor >= n {
		m.cursor = n - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

func (m *Model) focusFirstItem() {
	for i, r := range m.visible() {
		if !r.IsHeader() {
			m.cursor = i
			return
		}
	}
	m.cursor = 0
}

func (m *Model) toggleSection(s *Section) {
	if m.expanded {
		m.expanded = false
		for i := range m.sections {
			m.sections[i].Collapsed = false
		}
		s.Collapsed = true
		return
	}
	s.Collapsed = !s.Collapsed
}

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tickMsg:
		return m, tea.Batch(m.loadCmd(), m.tickCmd())
	case loadedMsg:
		m.loading = false
		m.loadErr = msg.err
		if msg.sections != nil {
			oldSections := m.sections
			m.sections = msg.sections
			for i := range m.sections {
				for _, old := range oldSections {
					if old.Kind == m.sections[i].Kind {
						m.sections[i].Collapsed = old.Collapsed
						break
					}
				}
			}
			m.refreshedAt = m.opts.Now()
			m.clampCursor()
			m.focusFirstItem()
		}
		if !msg.sessionSince.IsZero() && m.sessionSince.IsZero() {
			m.sessionSince = msg.sessionSince
		}
		return m, nil
	case doneMsg:
		if msg.err != nil {
			m.status = "エラー: " + msg.err.Error()
		} else {
			m.status = msg.status
		}
		if msg.refresh && msg.err == nil {
			m.loading = true
			return m, m.loadCmd()
		}
		return m, nil
	case detailMsg:
		if msg.err != nil {
			m.status = "エラー: " + msg.err.Error()
			return m, nil
		}
		m.detail, m.detailOffset, m.detailPrNum, m.detailPrBody, m.mode = msg.issue, 0, msg.prNumber, msg.prBody, modeDetail
		return m, nil
	case editBodyMsg:
		if msg.err != nil {
			m.status = "エラー: " + msg.err.Error()
			return m, nil
		}
		return m, m.openEditorForBody(msg.issue)
	case editedMsg:
		if msg.err != nil {
			m.status = "エラー: " + msg.err.Error()
			return m, nil
		}
		return m, m.approveWithBodyCmd(msg.number, msg.body)
	case tea.KeyMsg:
		switch m.mode {
		case modeInput:
			return m.updateInput(msg)
		case modeDetail:
			return m.updateDetail(msg)
		case modeHelp:
			m.mode = modeList
			if m.opts.Seen != nil {
				if err := m.opts.Seen.MarkHelpShown(); err != nil {
					m.status = "読込エラー: " + err.Error()
				}
			}
			return m, nil
		}
		return m.updateList(msg)
	}
	return m, nil
}

func keyString(msg tea.KeyMsg) string {
	if msg.Type == tea.KeyRunes {
		if len(msg.Runes) != 1 {
			return ""
		}
		return string(msg.Runes)
	}
	return msg.String()
}

func (m Model) updateList(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := keyString(msg)
	switch key {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "j", "down":
		m.cursor++
		m.clampCursor()
		return m, nil
	case "k", "up":
		m.cursor--
		m.clampCursor()
		return m, nil
	case "z", "tab":
		m.expanded = !m.expanded
		m.clampCursor()
		return m, nil
	case "R":
		m.loading = true
		return m, m.loadCmd()
	case "?":
		m.mode = modeHelp
		return m, nil
	case "r":
		return m.openAgentsMd()
	case "s":
		if m.opts.Say == nil {
			m.status = "s 新規起票は未設定です（spec AI なし）。`lead say \"一言\"` を直接実行できます"
			return m, nil
		}
		say := m.opts.Say
		m.mode = modeInput
		m.input = inputState{prompt: "新規起票 — 一言（Enter でレビュー待ちを起票, Esc 取消）> ", submit: func(text string) tea.Cmd {
			if text == "" {
				return func() tea.Msg { return doneMsg{status: "空の一言は起票しません"} }
			}
			return func() tea.Msg {
				summary, err := say(context.Background(), text, 0)
				if err != nil {
					return doneMsg{err: err}
				}
				return doneMsg{status: summary, refresh: true}
			}
		}}
		return m, nil
	}

	row, ok := m.current()
	if !ok {
		if key != "" {
			m.status = "対象がありません"
		}
		return m, nil
	}
	if row.IsHeader() {
		switch key {
		case "enter", " ", "space", "right", "l":
			m.toggleSection(row.Section)
			m.clampCursor()
			return m, nil
		}
		if key != "" {
			m.status = "区画ヘッダーが選択中です"
		}
		return m, nil
	}
	it := *row.Item
	gh := m.opts.Gh
	switch key {
	case "enter":
		return m, func() tea.Msg {
			ctx := context.Background()
			iss, err := gh.View(ctx, it.Number)
			if err != nil {
				return detailMsg{err: err}
			}
			prNumber, prBody := it.PRNumber, ""
			if prNumber > 0 {
				if body, err := gh.PrBody(ctx, prNumber); err == nil {
					prBody = body
				}
			}
			return detailMsg{issue: iss, prNumber: prNumber, prBody: prBody}
		}
	case "a":
		if it.Kind != KindNeedsReview {
			m.status = fmt.Sprintf("#%d は承認対象外（レビュー待ちの Issue のみ a で ready にできます）", it.Number)
			return m, nil
		}
		return m, m.approveCmd(it.Number)
	case "e":
		if it.Kind != KindNeedsReview {
			m.status = fmt.Sprintf("#%d は編集して承認の対象外（レビュー待ちのみ）", it.Number)
			return m, nil
		}
		if strings.TrimSpace(m.opts.Editor) == "" {
			m.status = "$EDITOR が未設定のため e は使えません"
			return m, nil
		}
		return m, func() tea.Msg {
			iss, err := gh.View(context.Background(), it.Number)
			return editBodyMsg{issue: iss, err: err}
		}
	case "x":
		if !it.Kind.IsIssueQueue() {
			m.status = fmt.Sprintf("#%d は却下対象外（ラベル区画の Issue のみ）", it.Number)
			return m, nil
		}
		number := it.Number
		m.mode = modeInput
		m.input = inputState{prompt: fmt.Sprintf("却下理由 #%d（空で理由なし close, Esc 取消）> ", number), submit: func(text string) tea.Cmd {
			return func() tea.Msg {
				ctx := context.Background()
				if text != "" {
					if err := gh.IssueComment(ctx, number, text); err != nil {
						return doneMsg{err: err}
					}
				}
				if err := gh.IssueClose(ctx, number); err != nil {
					return doneMsg{err: err}
				}
				return doneMsg{status: fmt.Sprintf("#%d を却下（close）しました", number), refresh: true}
			}
		}}
		return m, nil
	case "t":
		number := it.Number
		m.mode = modeInput
		m.input = inputState{prompt: fmt.Sprintf("一言 #%d（Enter 送信, Esc 取消）> ", number), submit: func(text string) tea.Cmd {
			if text == "" {
				return func() tea.Msg { return doneMsg{status: "空のコメントは送りません"} }
			}
			return func() tea.Msg {
				if err := gh.IssueComment(context.Background(), number, text); err != nil {
					return doneMsg{err: err}
				}
				return doneMsg{status: fmt.Sprintf("#%d にコメントしました", number)}
			}
		}}
		return m, nil
	case "o":
		return m, func() tea.Msg {
			if err := gh.BrowseIssue(context.Background(), it.Number); err != nil {
				return doneMsg{err: err}
			}
			return doneMsg{status: fmt.Sprintf("#%d をブラウザで開きました", it.Number)}
		}
	case "c":
		if it.Kind != KindMerged {
			m.status = fmt.Sprintf("#%d は確認済み対象外（最近マージの Issue のみ c で確認できます）", it.Number)
			return m, nil
		}
		if m.opts.Seen == nil {
			m.status = "読み込み状態がありません"
			return m, nil
		}
		number := it.Number
		return m, func() tea.Msg {
			if err := m.opts.Seen.Confirm(number); err != nil {
				return doneMsg{err: err}
			}
			return doneMsg{status: fmt.Sprintf("#%d を確認しました", number), refresh: true}
		}
	case "p":
		return m.peekItem(it)
	case "n":
		if m.opts.Say == nil {
			m.status = "n 不具合報告は未設定です（spec AI なし）。`lead say --follow-up N \"一言\"` を直接実行できます"
			return m, nil
		}
		say := m.opts.Say
		number := it.Number
		kind := it.Kind
		m.mode = modeInput
		m.input = inputState{prompt: fmt.Sprintf("不具合報告 #%d — 一言（Enter で追い Issue 起票, Esc 取消）> ", number), submit: func(text string) tea.Cmd {
			if text == "" {
				return func() tea.Msg { return doneMsg{status: "空の一言は起票しません"} }
			}
			return func() tea.Msg {
				ctx := context.Background()
				if kind == KindMerged && m.opts.Seen != nil {
					if err := m.opts.Seen.Confirm(number); err != nil {
						return doneMsg{err: err}
					}
				}
				summary, err := say(ctx, text, number)
				if err != nil {
					return doneMsg{err: err}
				}
				return doneMsg{status: summary, refresh: true}
			}
		}}
		return m, nil
	}
	return m, nil
}

func (m Model) approveCmd(number int) tea.Cmd {
	gh := m.opts.Gh
	return func() tea.Msg {
		ctx := context.Background()
		if err := gh.IssueRemoveLabel(ctx, number, LabelNeedsReview); err != nil {
			return doneMsg{err: err}
		}
		if err := gh.IssueAddLabel(ctx, number, LabelReady); err != nil {
			return doneMsg{err: err}
		}
		return doneMsg{status: fmt.Sprintf("#%d を承認: needs-review → ready", number), refresh: true}
	}
}

func (m Model) approveWithBodyCmd(number int, body string) tea.Cmd {
	gh := m.opts.Gh
	approve := m.approveCmd(number)
	return func() tea.Msg {
		if err := gh.IssueEditBody(context.Background(), number, body); err != nil {
			return doneMsg{err: err}
		}
		msg := approve()
		if d, ok := msg.(doneMsg); ok && d.err == nil {
			d.status = fmt.Sprintf("#%d の本文を更新して承認: needs-review → ready", number)
			return d
		}
		return msg
	}
}

func editorArgv(editor string, path string) []string {
	return append(strings.Fields(editor), path)
}

// openEditorForBody writes the body to a temp file, hands the terminal to
// $EDITOR, then reads the result back.
func (m Model) openEditorForBody(iss ports.Issue) tea.Cmd {
	f, err := os.CreateTemp("", fmt.Sprintf("lead-issue-%d-*.md", iss.Number))
	if err != nil {
		return func() tea.Msg { return doneMsg{err: err} }
	}
	path := f.Name()
	if _, err := f.WriteString(iss.Body); err != nil {
		f.Close()
		os.Remove(path)
		return func() tea.Msg { return doneMsg{err: err} }
	}
	f.Close()
	argv := editorArgv(m.opts.Editor, path)
	c := exec.Command(argv[0], argv[1:]...)
	number := iss.Number
	return m.opts.Shell.Exec(c, func(err error) tea.Msg {
		defer os.Remove(path)
		if err != nil {
			return editedMsg{number: number, err: fmt.Errorf("editor: %w", err)}
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return editedMsg{number: number, err: rerr}
		}
		return editedMsg{number: number, body: string(raw)}
	})
}

func (m Model) openAgentsMd() (tea.Model, tea.Cmd) {
	if strings.TrimSpace(m.opts.Editor) == "" {
		m.status = "$EDITOR が未設定のため r は使えません"
		return m, nil
	}
	if m.opts.AgentsPath == "" {
		m.status = "AGENTS.md の場所が不明です（リポジトリ内で起動してください）"
		return m, nil
	}
	argv := editorArgv(m.opts.Editor, m.opts.AgentsPath)
	c := exec.Command(argv[0], argv[1:]...)
	path := m.opts.AgentsPath
	return m, m.opts.Shell.Exec(c, func(err error) tea.Msg {
		if err != nil {
			return doneMsg{err: fmt.Errorf("editor: %w", err)}
		}
		return doneMsg{status: "規約を編集しました: " + path}
	})
}

func (m Model) peekItem(it Item) (tea.Model, tea.Cmd) {
	if it.Kind != KindRunning && it.Kind != KindBlocked {
		m.status = fmt.Sprintf("#%d は実行中/止まってるエージェントではありません（p は実行中/止まってるエージェントのみ）", it.Number)
		return m, nil
	}
	if it.LogPath == "" && it.Pane == "" {
		m.status = fmt.Sprintf("#%d のエージェントログもペインもありません", it.Number)
		return m, nil
	}
	if it.LogPath != "" {
		return m, m.peekWithHerdrOrPager(it.Number, it.LogPath, it.Pane)
	}
	if m.opts.Herdr != nil {
		return m, m.peekHerdrCmd(it.Number, "", it.Pane)
	}
	m.status = fmt.Sprintf("#%d のペインを開くには herdr が必要です", it.Number)
	return m, nil
}

func (m Model) peekWithHerdrOrPager(number int, logPath, pane string) tea.Cmd {
	return func() tea.Msg {
		if m.opts.Herdr == nil {
			return m.peekPagerCmd(number, logPath)()
		}
		err := m.opts.Herdr.Peek(context.Background(), logPath, pane)
		if err != nil && ports.IsBinaryNotFound(err) {
			return m.peekPagerCmd(number, logPath)()
		}
		if err != nil {
			return doneMsg{err: err}
		}
		return doneMsg{status: fmt.Sprintf("#%d のエージェントログを herdr タブで開きました", number)}
	}
}

func (m Model) peekHerdrCmd(number int, logPath, pane string) tea.Cmd {
	return func() tea.Msg {
		err := m.opts.Herdr.Peek(context.Background(), logPath, pane)
		if err != nil {
			return doneMsg{err: err}
		}
		if logPath != "" {
			return doneMsg{status: fmt.Sprintf("#%d のエージェントログを herdr タブで開きました", number)}
		}
		return doneMsg{status: fmt.Sprintf("#%d のエージェント画面を herdr タブで開きました", number)}
	}
}

func (m Model) peekPagerCmd(number int, logPath string) tea.Cmd {
	pager := os.Getenv("PAGER")
	if strings.TrimSpace(pager) == "" {
		pager = "less"
	}
	argv := append(strings.Fields(pager), logPath)
	c := exec.Command(argv[0], argv[1:]...)
	return m.opts.Shell.Exec(c, func(err error) tea.Msg {
		if err != nil {
			return doneMsg{err: fmt.Errorf("pager: %w", err)}
		}
		return doneMsg{status: fmt.Sprintf("#%d のエージェントログを開きました", number)}
	})
}

func (m Model) updateInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc, tea.KeyCtrlC:
		m.mode = modeList
		m.status = "取り消しました"
		return m, nil
	case tea.KeyEnter:
		m.mode = modeList
		text := strings.TrimSpace(string(m.input.text))
		submit := m.input.submit
		m.input = inputState{}
		return m, submit(text)
	case tea.KeyBackspace:
		if n := len(m.input.text); n > 0 {
			m.input.text = m.input.text[:n-1]
		}
		return m, nil
	case tea.KeyRunes, tea.KeySpace:
		if msg.Type == tea.KeySpace {
			m.input.text = append(m.input.text, ' ')
		} else {
			m.input.text = append(m.input.text, msg.Runes...)
		}
		return m, nil
	}
	return m, nil
}

func (m Model) updateDetail(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch keyString(msg) {
	case "esc", "q", "enter":
		m.mode = modeList
		return m, nil
	case "ctrl+c":
		return m, tea.Quit
	case "j", "down":
		m.detailOffset++
	case "k", "up":
		if m.detailOffset > 0 {
			m.detailOffset--
		}
	}
	return m, nil
}

// --- view ---------------------------------------------------------------------

// View implements tea.Model.
func (m Model) View() string {
	switch m.mode {
	case modeDetail:
		return m.viewDetail()
	case modeHelp:
		return m.viewHelp()
	}
	return m.viewList()
}

func (m Model) rule() string {
	w := m.width
	if w <= 0 {
		w = 78
	}
	return strings.Repeat("─", w)
}

func (m Model) viewList() string {
	var b strings.Builder
	running := 0
	counted := false
	if m.opts.RunningCount != nil {
		if n, err := m.opts.RunningCount(); err == nil {
			running = n
			counted = true
		}
	}
	if !counted {
		for _, s := range m.sections {
			if s.Kind == KindRunning {
				running = len(s.Items)
				break
			}
		}
	}
	head := "lead"
	if m.opts.Repo != "" {
		head += " — " + m.opts.Repo
	}
	right := fmt.Sprintf("実行中 %d / 並列上限 %d", running, m.opts.Parallel)
	if m.loading {
		right += "   ⟳ 読込中"
	} else if age := relAge(m.refreshedAt, m.opts.Now()); age != "" {
		right += "   ⟳ " + age
	}
	fmt.Fprintf(&b, "%s%s%s\n", head, strings.Repeat(" ", max(2, 78-len([]rune(head))-len([]rune(right)))), right)
	b.WriteString(m.rule() + "\n")
	if m.loadErr != nil {
		fmt.Fprintf(&b, "読込エラー: %v\n", m.loadErr)
	}

	rows := m.visible()
	now := m.opts.Now()
	for i, r := range rows {
		isCur := i == m.cursor
		if r.IsHeader() {
			s := r.Section
			folded := s.Collapsed && !m.expanded
			mark := "▾"
			if folded {
				mark = "▸"
			}
			cur := "  "
			if isCur {
				cur = "▶ "
			}
			fmt.Fprintf(&b, "%s%s %s (%d)\n", cur, mark, s.Kind.Title(), len(s.Items))
			continue
		}
		it := *r.Item
		cur := "  "
		if isCur {
			cur = "> "
		}
		fmt.Fprintf(&b, "%s#%-4d %s%s\n", cur, it.Number, it.Title, itemMeta(it, now))
	}
	if len(rows) == 0 && m.loadErr == nil && !m.loading {
		b.WriteString("  人間の仕事はありません\n")
	}
	b.WriteString(m.rule() + "\n")
	if m.mode == modeInput {
		fmt.Fprintf(&b, "%s%s▏\n", m.input.prompt, string(m.input.text))
	} else {
		b.WriteString(m.footer() + "\n")
		if m.status != "" {
			b.WriteString(m.status + "\n")
		}
	}
	return b.String()
}

func (m Model) footer() string {
	tokens := []string{"s 新規起票", "r AGENTS.md", "R 更新", "? 操作一覧", "q 終了"}
	if row, ok := m.current(); ok {
		if row.IsHeader() {
			if len(row.Section.Items) > 0 {
				tokens = []string{"Enter 開閉", "→ 開閉", "z 全開閉", "? 操作一覧", "q 終了"}
			}
		} else {
			switch row.Item.Kind {
			case KindNeedsReview:
				tokens = []string{"a 承認", "e 編集", "t コメント", "x 却下", "Enter 詳細", "? 操作一覧", "q 終了"}
			case KindBlocked:
				tokens = []string{"t 再指示", "p エージェント画面", "x 却下", "Enter 詳細", "? 操作一覧", "q 終了"}
			case KindMerged:
				tokens = []string{"c 確認済み", "n 不具合報告", "p エージェント画面", "o ブラウザ", "Enter 詳細", "? 操作一覧", "q 終了"}
			case KindRunning:
				tokens = []string{"p エージェント画面", "o ブラウザ", "Enter 詳細", "? 操作一覧", "q 終了"}
			}
		}
	}
	width := m.width
	if width <= 0 {
		width = 78
	}
	var lines []string
	line := ""
	for _, token := range tokens {
		candidate := token
		if line != "" {
			candidate = line + "   " + token
		}
		if line != "" && len([]rune(candidate)) > width {
			lines = append(lines, line)
			line = token
		} else {
			line = candidate
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func itemMeta(it Item, now time.Time) string {
	var parts []string
	if it.Kind == KindMerged {
		if age := relAge(it.MergedAt, now); age != "" {
			parts = append(parts, age)
		}
	}
	if it.Agent != "" {
		parts = append(parts, it.Agent)
	}
	if it.Attempts > 0 {
		parts = append(parts, fmt.Sprintf("×%d", it.Attempts))
	}
	if it.PID > 0 {
		parts = append(parts, fmt.Sprintf("pid %d", it.PID))
	}
	if len(parts) == 0 {
		return ""
	}
	return "    " + strings.Join(parts, " ")
}

func (m Model) viewDetail() string {
	var b strings.Builder
	fmt.Fprintf(&b, "#%d %s  [%s]\n", m.detail.Number, m.detail.Title, m.detail.State)
	b.WriteString(m.rule() + "\n")
	content := m.detail.Body
	if m.detailPrNum > 0 && strings.TrimSpace(m.detailPrBody) != "" {
		content += "\n\n---\n## PR 本文\n\n" + m.detailPrBody
	}
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	off := m.detailOffset
	if off > len(lines)-1 {
		off = max(0, len(lines)-1)
	}
	limit := len(lines)
	if m.height > 0 {
		limit = min(len(lines), off+max(1, m.height-4))
	}
	for _, l := range lines[off:limit] {
		b.WriteString(l + "\n")
	}
	b.WriteString(m.rule() + "\n")
	if m.detailPrNum == 0 && m.opts.Store != nil {
		b.WriteString("（PR 未記録）\n")
	}
	b.WriteString("j/k スクロール  Esc/q 戻る\n")
	return b.String()
}

func (m Model) viewHelp() string {
	return strings.Join([]string{
		"受信箱の操作一覧",
		m.rule(),
		"Issueを進める",
		"  a  承認 — レビュー待ちを完了にする",
		"  e  編集 — 本文を直してから承認する",
		"  t  コメント — Issue に指示や補足を送る",
		"  x  却下 — 理由を残して Issue を閉じる",
		"  Enter  詳細 — Issue 本文と関連情報を読む",
		"",
		"エージェントを確認する",
		"  p エージェント画面 — 実行中/停止中の画面を開く",
		"",
		"マージ後に確認する",
		"  c 確認済み — マージ済みの Issue を一覧から外す",
		"  n 不具合報告 — 元 Issue を参照する追い Issue を起票する",
		"  o  ブラウザ — Issue のページを開く",
		"",
		"全体操作",
		"  s 新規起票 — 一言からレビュー待ちの Issue を起票する",
		"  r  AGENTS.md — プロジェクトのルールを開く",
		"  R  更新 — 最新の一覧を読み直す",
		"  ?  操作一覧 — この画面を開く",
		"  q  終了 — 受信箱を閉じる",
		"",
		"移動",
		"  j/k  上下に移動する（区画ヘッダーと Issue 行を連続）",
		"  Enter / space / → / l  区画を開閉する",
		"  z / Tab  すべての区画を開閉する",
		"",
		m.rule(),
		"q / Esc で一覧に戻る（その他のキーでも戻ります）",
		"",
	}, "\n")
}

// relAge renders "N秒前"-style ages; zero time yields "".
func relAge(t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d秒前", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d分前", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d時間前", int(d.Hours()))
	}
	return fmt.Sprintf("%d日前", int(d.Hours()/24))
}

// RepoSlug extracts "owner/name" from a git remote URL ("" when unknown).
func RepoSlug(url string) string {
	u := strings.TrimSuffix(strings.TrimSpace(url), ".git")
	u = strings.TrimSuffix(u, "/")
	if u == "" {
		return ""
	}
	u = strings.ReplaceAll(u, ":", "/")
	parts := strings.Split(u, "/")
	if len(parts) < 2 {
		return ""
	}
	return parts[len(parts)-2] + "/" + parts[len(parts)-1]
}

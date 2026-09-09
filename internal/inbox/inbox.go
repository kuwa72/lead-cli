package inbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/kuwa72/lead-cli/internal/ports"
	"github.com/kuwa72/lead-cli/internal/state"
)

// Options wires the inbox to its collaborators. Only Gh is required.
type Options struct {
	Gh    ports.GhClient
	Store *state.Store // nil = no local sections
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
	input        inputState
	status       string
	loadErr      error
	loading      bool
	refreshedAt  time.Time
	width        int
	height       int
}

// Messages.
type (
	loadedMsg struct {
		sections []Section
		err      error
	}
	doneMsg struct {
		status  string
		err     error
		refresh bool
	}
	detailMsg struct {
		issue ports.Issue
		err   error
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
	return Model{opts: opts, sections: Build(nil, nil, nil), loading: true}
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
		return loadedMsg{sections: Build(review, blocked, wfs)}
	}
}

// visible flattens the rows the cursor can reach.
func (m Model) visible() []Item {
	var out []Item
	for _, s := range m.sections {
		if s.Collapsed && !m.expanded {
			continue
		}
		out = append(out, s.Items...)
	}
	return out
}

func (m Model) current() (Item, bool) {
	rows := m.visible()
	if len(rows) == 0 {
		return Item{}, false
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
		if msg.err == nil {
			m.sections = msg.sections
			m.refreshedAt = m.opts.Now()
			m.clampCursor()
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
		m.detail, m.detailOffset, m.mode = msg.issue, 0, modeDetail
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
			m.status = "say は未設定です（spec AI なし）。`lead say \"一言\"` を直接実行できます"
			return m, nil
		}
		say := m.opts.Say
		m.mode = modeInput
		m.input = inputState{prompt: "say 一言（Enter で needs-review 起票, Esc 取消）> ", submit: func(text string) tea.Cmd {
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

	it, ok := m.current()
	if !ok {
		if key != "" {
			m.status = "対象がありません"
		}
		return m, nil
	}
	gh := m.opts.Gh
	switch key {
	case "enter":
		return m, func() tea.Msg {
			iss, err := gh.View(context.Background(), it.Number)
			return detailMsg{issue: iss, err: err}
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
	case "p":
		return m.peekItem(it)
	case "n":
		if m.opts.Say == nil {
			m.status = "n 実機NG は未設定です（spec AI なし）。`lead say --follow-up N \"一言\"` を直接実行できます"
			return m, nil
		}
		say := m.opts.Say
		number := it.Number
		m.mode = modeInput
		m.input = inputState{prompt: fmt.Sprintf("実機NG #%d — 一言（Enter で追い Issue 起票, Esc 取消）> ", number), submit: func(text string) tea.Cmd {
			if text == "" {
				return func() tea.Msg { return doneMsg{status: "空の一言は起票しません"} }
			}
			return func() tea.Msg {
				summary, err := say(context.Background(), text, number)
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

const keyBar = "a 承認  e 編集して承認  x 却下  t 一言返す  p 覗く  n 実機NG  r 規約  s say  o ブラウザ  Enter 本文  z 折畳  R 更新  ? ヘルプ  q 終了"

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
	for _, s := range m.sections {
		if s.Kind == KindRunning {
			running = len(s.Items)
		}
	}
	head := "lead"
	if m.opts.Repo != "" {
		head += " — " + m.opts.Repo
	}
	right := fmt.Sprintf("実行中 %d", running)
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
	idx := 0
	for _, s := range m.sections {
		folded := s.Collapsed && !m.expanded
		mark := ""
		if folded {
			mark = "  ▸"
		}
		fmt.Fprintf(&b, "%s (%d)%s\n", s.Kind.Title(), len(s.Items), mark)
		if folded {
			continue
		}
		for _, it := range s.Items {
			cur := "  "
			if idx < len(rows) && idx == m.cursor {
				cur = "> "
			}
			idx++
			fmt.Fprintf(&b, "  %s#%-4d %s%s\n", cur, it.Number, it.Title, itemMeta(it))
		}
	}
	if len(rows) == 0 && m.loadErr == nil && !m.loading {
		b.WriteString("  人間の仕事はありません\n")
	}
	b.WriteString(m.rule() + "\n")
	if m.mode == modeInput {
		fmt.Fprintf(&b, "%s%s▏\n", m.input.prompt, string(m.input.text))
	} else {
		b.WriteString(keyBar + "\n")
		if m.status != "" {
			b.WriteString(m.status + "\n")
		}
	}
	return b.String()
}

func itemMeta(it Item) string {
	var parts []string
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
	lines := strings.Split(strings.ReplaceAll(m.detail.Body, "\r\n", "\n"), "\n")
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
	b.WriteString("j/k スクロール  Esc/q 戻る\n")
	return b.String()
}

func (m Model) viewHelp() string {
	return strings.Join([]string{
		"受信箱のキー（docs/rfc-inbox-ux.md §5.2）",
		m.rule(),
		"Enter  本文をフルスクリーン表示",
		"a      承認: needs-review を外し ready を付ける",
		"e      $EDITOR で本文を編集して承認",
		"x      却下: 任意の一言をコメントして close",
		"t      一言返す: Issue コメント",
		"p      覗く: エージェントのログ/画面を herdr タブまたは $PAGER で開く",
		"n      実機 NG: 一言から追い Issue を起票",
		"r      規約: AGENTS.md を $EDITOR で開く",
		"s      say: 一言から needs-review Issue を起票",
		"o      ブラウザで開く",
		"j/k    移動   z 折畳の切替   R 再読込   q 終了",
		m.rule(),
		"何かキーを押すと戻ります",
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

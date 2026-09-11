package inbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/muesli/termenv"

	"github.com/kuwa72/lead-cli/internal/dispatch"
	"github.com/kuwa72/lead-cli/internal/notify"
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
	// files a real-device-regression follow-up referencing that issue (inbox n key).
	// Nil disables s/n.
	Say func(ctx context.Context, oneLiner string, followUp int) (string, error)
	// Parallel is the dispatcher's parallel limit for the header.
	// 0 means dispatch.DefaultParallel.
	Parallel int
	// RunningCount returns the dispatcher's current running count.
	// Nil falls back to counting the running section.
	RunningCount func() (int, error)
	// PreviewDelay is the debounce before fetching a preview after the
	// cursor moves. Interactive runs set this to 150ms; headless tests
	// leave it at zero so previews load synchronously.
	PreviewDelay time.Duration
	// Headless is set by RunHeadless to avoid blocking timers in tests.
	Headless bool
	// Theme is the LEAD_THEME environment value (empty = terminal default).
	Theme string
	// Cache is the persistent issue cache store (issue #140).
	Cache *CacheStore
	// Agent is the active coding agent (agy/claude/codex/devin/opencode/gemini).
	Agent string
	// AgentMode is the active agent dispatch mode (batch/dangerous/interactive).
	AgentMode string
	// OnConfigChange is invoked when the user toggles Agent or AgentMode via keyboard.
	OnConfigChange func(agent, mode string)
	// IssueCreation controls the default behavior of 's' (direct vs ai).
	IssueCreation string
	// OnSettingsChange is invoked when user modifies any setting (issueCreation, agent, mode).
	OnSettingsChange func(cfg Config)
	// Notifier sends desktop/terminal notifications on milestone events (issue #145).
	Notifier notify.Notifier
}

type mode int

const (
	modeList mode = iota
	modeDetail
	modeInput
	modeHelp
	modeSettings
	modeLog
)

type inputState struct {
	prompt       string
	text         []rune
	cursor       int
	submit       func(text string) tea.Cmd
	targetNumber int    // issue number being acted on, 0 for actions without a target (s)
	action       string // "send" or "reject"; drives the input footer
}

// Model is the bubbletea model. Construct with New.
type Model struct {
	opts              Options
	sections          []Section
	cursor            int
	expanded          bool
	mode              mode
	detail            ports.Issue
	detailOffset      int
	detailPrBody      string
	detailPrNum       int
	detailPrErr       bool
	detailComments    []ports.Comment
	detailCommentsErr bool
	detailAgentLog    string
	detailPrDiff      string
	detailPrDiffErr   bool
	previewOffset     int
	previewSnapshot   previewSnapshot
	input             inputState
	status            string
	loadErr           error
	loading           bool
	refreshedAt       time.Time
	sessionSince      time.Time // loaded last_seen_at at session start; refresh uses this
	width             int
	height            int
	listTop           int // first visible row in the scrolled list body
	listOffset        int // scroll offset for the item list
	selectedIssue     int // issue number under the cursor (0 when a header is selected)
	previewReq        int // monotonic id to discard stale preview responses
	previewLoading    bool
	previewErr        error
	previewCache      map[int]previewSnapshot
	pendingOps        map[int]bool // issue numbers with in-flight state-changing operations
	theme             *Theme       // nil-safe; set by Run/RunHeadless
	helpReturnMode    mode         // mode to restore when help is dismissed
	statusGen         int          // generation counter for status auto-clear
	agent             string       // active agent
	agentMode         string       // active agent mode
	issueCreation     string       // issue creation default ("direct" or "ai")
	settingsCursor    int          // selected row in settings view
	knownNeedsReview  map[int]bool // tracked needs-review issues for completion notify
	knownBlocked      map[int]bool // tracked blocked issues for completion notify
	hasInitialLoad    bool         // suppresses notifications on initial load
	repoOpenCount     int          // total open issues in repository on GitHub
	logs              []string     // event log / status history entries
	logOffset         int          // scroll offset for log view
}

// Messages.
type (
	loadedMsg struct {
		sections      []Section
		err           error
		sessionSince  time.Time
		repoOpenCount int
	}
	doneMsg struct {
		status  string
		err     error
		refresh bool
		number  int // issue number the operation targeted, for pendingOps tracking
	}
	detailMsg struct {
		issue       ports.Issue
		prNumber    int
		prBody      string
		prErr       bool
		prDiff      string
		prDiffErr   bool
		comments    []ports.Comment
		commentsErr bool
		err         error
	}
	previewMsg struct {
		req      int
		item     Item
		snapshot previewSnapshot
		err      error
	}
	previewDelayMsg struct {
		req  int
		item Item
	}
	approvalCheckMsg struct {
		number     int
		issue      ports.Issue
		editedBody string // non-empty for the edit-then-approve flow
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
	tickMsg        time.Time
	statusClearMsg struct {
		gen int
	}
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
	var initialSections []Section
	var initialStatus string
	if opts.Cache != nil {
		if cache, err := opts.Cache.Load(); err == nil && cache != nil {
			var wfs []state.Workflow
			if opts.Store != nil {
				wfs, _ = opts.Store.List()
			}
			openNums := make(map[int]bool, len(cache.OpenNumbers))
			for _, n := range cache.OpenNumbers {
				openNums[n] = true
			}
			initialSections = Build(cache.Review, cache.Blocked, cache.Merged, nil, wfs, BuildOptions{
				Repo:        opts.Repo,
				OpenNumbers: openNums,
				OpenIssues:  cache.OpenIssues,
			})
			initialStatus = "cached"
		}
	}
	if initialSections == nil {
		initialSections = Build(nil, nil, nil, nil, nil)
	}
	if opts.Notifier == nil {
		opts.Notifier = notify.New(notify.Options{})
	}
	m := Model{
		opts:             opts,
		sections:         initialSections,
		status:           initialStatus,
		loading:          true,
		previewCache:     make(map[int]previewSnapshot),
		pendingOps:       make(map[int]bool),
		knownNeedsReview: make(map[int]bool),
		knownBlocked:     make(map[int]bool),
		theme:            NewTheme(ModeTerminal, termenv.Ascii, nil),
		agent:            opts.Agent,
		agentMode:        opts.AgentMode,
		issueCreation:    opts.IssueCreation,
	}
	if m.agent == "" {
		m.agent = "agy"
	}
	if m.agentMode == "" {
		m.agentMode = "batch"
	}
	if m.issueCreation == "" {
		m.issueCreation = IssueCreationDirect
	}
	if opts.Seen != nil {
		shown, err := opts.Seen.HelpShown()
		if err == nil && !shown {
			m.mode = modeHelp
		}
	}
	if opts.Repo != "" {
		m.addLog(fmt.Sprintf("Started lead inbox for %s", opts.Repo))
	} else {
		m.addLog("Started lead inbox")
	}
	m.focusFirstItem()
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

// Agent returns the currently selected coding agent.
func (m Model) Agent() string {
	if m.agent == "" {
		return "agy"
	}
	return m.agent
}

// AgentMode returns the currently selected agent dispatch mode.
func (m Model) AgentMode() string {
	if m.agentMode == "" {
		return "batch"
	}
	return m.agentMode
}

var availableAgentModes = []string{"batch", "dangerous", "interactive"}
var availableAgents = []string{"agy", "claude", "codex", "devin", "opencode", "gemini"}

func nextAgentMode(cur string) string {
	for i, mode := range availableAgentModes {
		if mode == cur {
			return availableAgentModes[(i+1)%len(availableAgentModes)]
		}
	}
	return availableAgentModes[0]
}

func nextAgent(cur string) string {
	for i, ag := range availableAgents {
		if ag == cur {
			return availableAgents[(i+1)%len(availableAgents)]
		}
	}
	return availableAgents[0]
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

		var openCount int
		var openNumbers map[int]bool
		var openIssuesList []ports.IssueSummary
		if openIssues, err := opts.Gh.ListOpen(ctx); err == nil {
			openCount = len(openIssues)
			openIssuesList = openIssues
			if len(openIssues) > 0 {
				openNumbers = make(map[int]bool, len(openIssues)+len(review)+len(blocked))
				for _, iss := range openIssues {
					openNumbers[iss.Number] = true
				}
				for _, iss := range review {
					openNumbers[iss.Number] = true
				}
				for _, iss := range blocked {
					openNumbers[iss.Number] = true
				}

				// Zombie workflow sync: if an in_progress workflow in this repo is no longer
				// open on GitHub, mark it completed in the store.
				if opts.Store != nil {
					for i, w := range wfs {
						if w.Status == state.StatusInProgress && !openNumbers[w.Issue] {
							if opts.Repo == "" || w.Repository == "" || w.Repository == "local" || RepoSlug(w.Repository) == opts.Repo {
								w.Status = state.StatusCompleted
								wfs[i] = w
								_ = opts.Store.Upsert(w)
							}
						}
					}
				}
			}
		}

		sections := Build(review, blocked, merged, buildSeen, wfs, BuildOptions{
			Repo:        opts.Repo,
			OpenNumbers: openNumbers,
			OpenIssues:  openIssuesList,
		})
		if loadErr == nil && opts.Cache != nil {
			var openList []int
			for n := range openNumbers {
				openList = append(openList, n)
			}
			_ = opts.Cache.Save(&IssueCache{
				Repo:        opts.Repo,
				CachedAt:    opts.Now(),
				Review:      review,
				Blocked:     blocked,
				Merged:      merged,
				OpenNumbers: openList,
				OpenIssues:  openIssuesList,
			})
		}
		return loadedMsg{sections: sections, sessionSince: sessionSince, err: loadErr, repoOpenCount: openCount}
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
		m.clampCursor()
		m.scrollToCursor()
		return m, nil
	case tickMsg:
		return m, tea.Batch(m.loadCmd(), m.tickCmd())
	case statusClearMsg:
		if msg.gen == m.statusGen {
			m.status = ""
		}
		return m, nil
	case loadedMsg:
		m.loading = false
		m.loadErr = msg.err
		m.repoOpenCount = msg.repoOpenCount
		if msg.err != nil && len(m.sections) > 0 {
			m.addLog("Error: " + msg.err.Error())
			cmd := m.setStatus("offline: " + msg.err.Error())
			return m, cmd
		}
		oldRow, _ := m.current()
		m.detail = ports.Issue{}
		m.detailPrNum, m.detailPrBody, m.detailOffset = 0, "", 0
		m.detailPrErr, m.detailComments, m.detailCommentsErr = false, nil, false
		m.detailAgentLog = ""
		wasRefreshed := !m.refreshedAt.IsZero()
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
			m.checkNotifications(msg.sections)
			m.restoreSelection(oldRow)
			m.scrollToCursor()
		}
		if !msg.sessionSince.IsZero() && m.sessionSince.IsZero() {
			m.sessionSince = msg.sessionSince
		}
		var reviewCount, blockedCount int
		for _, s := range msg.sections {
			switch s.Kind {
			case KindNeedsReview:
				reviewCount = len(s.Items)
			case KindBlocked:
				blockedCount = len(s.Items)
			}
		}
		totalActive := reviewCount + blockedCount
		var statusText string
		if totalActive == 0 {
			if msg.repoOpenCount > 0 {
				statusText = fmt.Sprintf("Loaded: 0 inbox items (%d open issues in repo without lead labels)", msg.repoOpenCount)
			} else {
				statusText = "Loaded: 0 open issues in repo"
			}
		} else {
			statusText = fmt.Sprintf("Loaded: %d needs-review, %d blocked (%d open in repo)", reviewCount, blockedCount, msg.repoOpenCount)
		}
		m.addLog(statusText)
		var statusCmd tea.Cmd
		if m.status == "" || m.status == "cached" {
			statusCmd = m.setStatus(statusText)
		}
		// Load the preview only on reloads, not on the very first load, so the
		// inbox is ready immediately and tests that start with a key sequence do
		// not observe a preview fetch.
		if wasRefreshed {
			if statusCmd != nil {
				return m, tea.Batch(statusCmd, m.loadPreviewForCurrent())
			}
			return m, m.loadPreviewForCurrent()
		}
		return m, statusCmd
	case doneMsg:
		if msg.number > 0 {
			delete(m.pendingOps, msg.number)
		}
		if msg.err != nil {
			return m, m.setStatus("Error: " + msg.err.Error())
		}
		cmd := m.setStatus(msg.status)
		if msg.refresh {
			m.loading = true
			return m, tea.Batch(m.loadCmd(), cmd)
		}
		return m, cmd
	case detailMsg:
		if msg.err != nil {
			return m, m.setStatus("Error: Could not load details. " + msg.err.Error())
		}
		m.detail = msg.issue
		m.detailOffset = 0
		m.detailPrNum = msg.prNumber
		m.detailPrBody = msg.prBody
		m.detailPrErr = msg.prErr
		m.detailComments = msg.comments
		m.detailCommentsErr = msg.commentsErr
		m.detailPrDiff = msg.prDiff
		m.detailPrDiffErr = msg.prDiffErr
		m.mode = modeDetail
		return m, nil
	case previewMsg:
		if msg.req != m.previewReq {
			return m, nil
		}
		m.previewLoading = false
		if msg.err != nil {
			m.previewErr = msg.err
			return m, nil
		}
		m.previewErr = nil
		m.applySnapshot(msg.snapshot)
		m.cacheSnapshot(msg.item.Number, msg.snapshot)
		return m, nil
	case previewDelayMsg:
		if msg.req != m.previewReq {
			return m, nil
		}
		return m, m.previewLoadCmd(msg.item, msg.req)
	case approvalCheckMsg:
		if m.issueChanged(msg.issue) {
			if msg.number > 0 {
				delete(m.pendingOps, msg.number)
			}
			m.detail = mergeIssue(m.detail, msg.issue)
			m.cacheSnapshot(msg.issue.Number, previewSnapshot{issue: m.detail})
			m.status = "Issue changed. Review before approving."
			return m, nil
		}
		if msg.editedBody != "" {
			return m, m.approveWithBodyCmd(msg.number, msg.editedBody)
		}
		return m, m.doApproveCmd(msg.number)
	case editBodyMsg:
		if msg.err != nil {
			return m, m.setStatus("Error: Could not load issue. " + msg.err.Error())
		}
		// Remember the body we are about to edit so approveWithBodyCmd can detect
		// concurrent changes.
		m.detail = msg.issue
		m.cacheSnapshot(msg.issue.Number, previewSnapshot{issue: msg.issue})
		return m, m.openEditorForBody(msg.issue)
	case editedMsg:
		if msg.err != nil {
			return m, m.setStatus("Error: Editor failed. " + msg.err.Error())
		}
		m.pendingOps[msg.number] = true
		return m, m.approveWithBodyCmd(msg.number, msg.body)
	case tea.KeyMsg:
		if m.tooSmall() {
			switch keyString(msg) {
			case "q", "ctrl+c":
				return m, tea.Quit
			}
			return m, nil
		}
		switch m.mode {
		case modeInput:
			return m.updateInput(msg)
		case modeDetail:
			return m.updateDetail(msg)
		case modeSettings:
			return m.updateSettings(msg)
		case modeLog:
			return m.updateLog(msg)
		case modeHelp:
			if m.helpReturnMode == modeDetail {
				m.mode = modeDetail
			} else {
				m.mode = modeList
			}
			m.helpReturnMode = modeList
			if m.opts.Seen != nil {
				if err := m.opts.Seen.MarkHelpShown(); err != nil {
					m.setStatus("Error: Could not save help state. " + err.Error())
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
		m.previewOffset = 0
		m.cursor++
		m.clampCursor()
		m.scrollToCursor()
		return m, m.loadPreviewForCurrent()
	case "k", "up":
		m.previewOffset = 0
		m.cursor--
		m.clampCursor()
		m.scrollToCursor()
		return m, m.loadPreviewForCurrent()
	case "J":
		m.previewOffset++
		return m, nil
	case "K":
		if m.previewOffset > 0 {
			m.previewOffset--
		}
		return m, nil
	case "ctrl+d":
		m.previewOffset += 5
		return m, nil
	case "ctrl+u":
		if m.previewOffset > 5 {
			m.previewOffset -= 5
		} else {
			m.previewOffset = 0
		}
		return m, nil
	case "z", "tab":
		m.previewOffset = 0
		m.expanded = !m.expanded
		m.clampCursor()
		m.scrollToCursor()
		return m, m.loadPreviewForCurrent()
	case "R":
		m.previewOffset = 0
		m.loading = true
		m.status = ""
		return m, m.loadCmd()
	case "?":
		m.helpReturnMode = modeList
		m.mode = modeHelp
		return m, nil
	case "r":
		return m.openAgentsMd()
	case ",", "S":
		m.mode = modeSettings
		return m, nil
	case "L":
		m.mode = modeLog
		m.logOffset = 0
		return m, nil
	case "m":
		m.agentMode = nextAgentMode(m.AgentMode())
		m.notifyConfigChange()
		return m, m.setStatus(fmt.Sprintf("Agent mode: %s", m.agentMode))
	case "g":
		m.agent = nextAgent(m.Agent())
		m.notifyConfigChange()
		return m, m.setStatus(fmt.Sprintf("Agent: %s", m.agent))
	case "s":
		say := m.opts.Say
		gh := m.opts.Gh
		m.mode = modeInput
		isAI := m.IssueCreation() == IssueCreationAI
		prompt := "New issue — title (Enter to create, !prefix for AI spec, Esc cancel) > "
		if isAI {
			prompt = "New issue — AI spec (Enter to generate spec, !title for direct create, Esc cancel) > "
		}
		m.input = inputState{
			action: "send",
			prompt: prompt,
			cursor: 0,
			submit: func(text string) tea.Cmd {
				if text == "" {
					return func() tea.Msg { return doneMsg{status: "Empty title; nothing filed."} }
				}
				isAISubmit := (!isAI && strings.HasPrefix(text, "!")) || (isAI && !strings.HasPrefix(text, "!"))
				if isAISubmit {
					if say == nil {
						return func() tea.Msg { return doneMsg{status: "Spec AI is not configured."} }
					}
					oneLiner := strings.TrimSpace(strings.TrimPrefix(text, "!"))
					if oneLiner == "" {
						return func() tea.Msg { return doneMsg{status: "Empty one-liner; nothing filed."} }
					}
					return func() tea.Msg {
						summary, err := say(context.Background(), oneLiner, 0)
						if err != nil {
							return doneMsg{err: err}
						}
						return doneMsg{status: summary, refresh: true}
					}
				}
				directTitle := strings.TrimSpace(strings.TrimPrefix(text, "!"))
				return func() tea.Msg {
					ctx := context.Background()
					ref, err := gh.IssueCreate(ctx, directTitle, "Created from lead inbox.", []string{"needs-review"})
					if err != nil {
						return doneMsg{err: err}
					}
					return doneMsg{status: fmt.Sprintf("Created #%d %s", ref.Number, directTitle), refresh: true}
				}
			},
		}
		return m, nil
	}

	row, ok := m.current()
	if !ok {
		if key != "" {
			return m, m.setStatus("No item selected")
		}
		return m, nil
	}
	if row.IsHeader() {
		switch key {
		case "enter", " ", "space", "right", "l":
			m.toggleSection(row.Section)
			m.clampCursor()
			m.scrollToCursor()
			return m, nil
		}
		if key != "" {
			return m, m.setStatus("Section header selected")
		}
		return m, nil
	}
	it := *row.Item
	gh := m.opts.Gh
	switch key {
	case "enter":
		return m, m.openDetailCmd(it)
	case "a":
		if it.Kind != KindNeedsReview && it.Kind != KindBacklog {
			return m, m.setStatus(fmt.Sprintf("#%d is not actionable with approve (a only approves needs-review or backlog issues)", it.Number))
		}
		if m.pendingOps[it.Number] {
			return m, m.setStatus(fmt.Sprintf("Already processing #%d", it.Number))
		}
		m.pendingOps[it.Number] = true
		return m, m.startApproveCmd(it.Number)
	case "e":
		if it.Kind != KindNeedsReview {
			return m, m.setStatus(fmt.Sprintf("#%d cannot be edited (e is only for needs-review issues)", it.Number))
		}
		if strings.TrimSpace(m.opts.Editor) == "" {
			return m, m.setStatus("e requires $EDITOR")
		}
		return m, func() tea.Msg {
			iss, err := gh.View(context.Background(), it.Number)
			return editBodyMsg{issue: iss, err: err}
		}
	case "x":
		if !it.Kind.IsIssueQueue() {
			return m, m.setStatus(fmt.Sprintf("#%d cannot be rejected (x is only for label-driven issues)", it.Number))
		}
		number := it.Number
		m.mode = modeInput
		m.input = inputState{
			action:       "reject",
			targetNumber: number,
			prompt:       fmt.Sprintf("Reject #%d — reason (empty closes without comment, Esc cancel) > ", number),
			cursor:       0,
			submit: func(text string) tea.Cmd {
				return func() tea.Msg {
					ctx := context.Background()
					if text != "" {
						if err := gh.IssueComment(ctx, number, text); err != nil {
							return doneMsg{err: err, number: number}
						}
					}
					if err := gh.IssueClose(ctx, number); err != nil {
						return doneMsg{err: err, number: number}
					}
					return doneMsg{status: fmt.Sprintf("Rejected #%d", number), refresh: true, number: number}
				}
			},
		}
		return m, nil
	case "t":
		number := it.Number
		m.mode = modeInput
		m.input = inputState{
			action:       "send",
			targetNumber: number,
			prompt:       fmt.Sprintf("Reply #%d (Enter to send, Esc cancel) > ", number),
			cursor:       0,
			submit: func(text string) tea.Cmd {
				if text == "" {
					return func() tea.Msg { return doneMsg{status: "Empty comment; nothing sent."} }
				}
				return func() tea.Msg {
					if err := gh.IssueComment(context.Background(), number, text); err != nil {
						return doneMsg{err: err, number: number}
					}
					return doneMsg{status: fmt.Sprintf("Commented on #%d", number), number: number}
				}
			},
		}
		return m, nil
	case "o":
		return m, m.openBrowserCmd(it)
	case "c":
		if it.Kind != KindMerged {
			return m, m.setStatus(fmt.Sprintf("#%d is not merged (c only marks merged issues as seen)", it.Number))
		}
		if m.opts.Seen == nil {
			return m, m.setStatus("Read-state tracking is not available")
		}
		number := it.Number
		if m.pendingOps[number] {
			return m, m.setStatus(fmt.Sprintf("Already processing #%d", number))
		}
		m.pendingOps[number] = true
		return m, func() tea.Msg {
			if err := m.opts.Seen.Confirm(number); err != nil {
				return doneMsg{err: err, number: number}
			}
			return doneMsg{status: fmt.Sprintf("Marked #%d as seen", number), refresh: true, number: number}
		}
	case "p":
		return m.peekItem(it)
	case "n":
		if m.opts.Say == nil {
			return m, m.setStatus("n is not configured (no spec AI). Run `lead say --follow-up N \"one-liner\"` directly.")
		}
		say := m.opts.Say
		number := it.Number
		kind := it.Kind
		m.mode = modeInput
		m.input = inputState{
			action:       "send",
			targetNumber: number,
			prompt:       fmt.Sprintf("Bug report #%d — one-liner (Enter to file follow-up, Esc cancel) > ", number),
			cursor:       0,
			submit: func(text string) tea.Cmd {
				if text == "" {
					return func() tea.Msg { return doneMsg{status: "Empty one-liner; nothing filed."} }
				}
				return func() tea.Msg {
					ctx := context.Background()
					if kind == KindMerged && m.opts.Seen != nil {
						if err := m.opts.Seen.Confirm(number); err != nil {
							return doneMsg{err: err, number: number}
						}
					}
					summary, err := say(ctx, text, number)
					if err != nil {
						return doneMsg{err: err, number: number}
					}
					return doneMsg{status: summary, refresh: true, number: number}
				}
			},
		}
		return m, nil
	}
	return m, nil
}

func (m Model) openDetailCmd(it Item) tea.Cmd {
	gh := m.opts.Gh
	return func() tea.Msg {
		ctx := context.Background()
		iss, err := gh.View(ctx, it.Number)
		if err != nil {
			return detailMsg{err: err}
		}
		if iss.BlockedReason == "" {
			iss.BlockedReason = extractBlockedReason(iss.Body)
		}
		msg := detailMsg{issue: iss, prNumber: it.PRNumber}
		if it.PRNumber > 0 {
			body, err := gh.PrBody(ctx, it.PRNumber)
			if err != nil {
				msg.prErr = true
			} else {
				msg.prBody = body
			}
			diff, err := gh.PrDiff(ctx, it.PRNumber)
			if err != nil {
				msg.prDiffErr = true
			} else {
				msg.prDiff = diff
			}
		}
		comments, err := gh.IssueComments(ctx, it.Number)
		if err != nil {
			msg.commentsErr = true
		} else {
			msg.comments = comments
		}
		return msg
	}
}

func (m Model) openBrowserCmd(it Item) tea.Cmd {
	gh := m.opts.Gh
	return func() tea.Msg {
		if err := gh.BrowseIssue(context.Background(), it.Number); err != nil {
			return doneMsg{err: err}
		}
		return doneMsg{status: fmt.Sprintf("Opened #%d in browser", it.Number)}
	}
}

func (m Model) startApproveCmd(number int) tea.Cmd {
	gh := m.opts.Gh
	return func() tea.Msg {
		ctx := context.Background()
		iss, err := gh.View(ctx, number)
		if err != nil {
			return approvalCheckMsg{number: number, issue: ports.Issue{Number: number}}
		}
		return approvalCheckMsg{number: number, issue: iss}
	}
}

func (m Model) doApproveCmd(number int) tea.Cmd {
	return func() tea.Msg {
		status, err := m.doApprove(number)
		if err != nil {
			return doneMsg{err: err, number: number}
		}
		return doneMsg{status: status, refresh: true, number: number}
	}
}

// doApprove performs the label changes and returns a human status.
func (m Model) doApprove(number int) (string, error) {
	gh := m.opts.Gh
	ctx := context.Background()
	_ = gh.IssueRemoveLabel(ctx, number, LabelNeedsReview)
	if err := gh.IssueAddLabel(ctx, number, LabelReady); err != nil {
		return "", err
	}
	return fmt.Sprintf("Approved #%d (ready)", number), nil
}

func (m Model) approveWithBodyCmd(number int, body string) tea.Cmd {
	gh := m.opts.Gh
	return func() tea.Msg {
		ctx := context.Background()
		iss, err := gh.View(ctx, number)
		if err != nil {
			return doneMsg{err: err, number: number}
		}
		if m.issueChanged(iss) {
			return approvalCheckMsg{number: number, issue: iss, editedBody: body}
		}
		if err := gh.IssueEditBody(ctx, number, body); err != nil {
			return doneMsg{err: err, number: number}
		}
		status, err := m.doApprove(number)
		if err != nil {
			return doneMsg{err: err, number: number}
		}
		return doneMsg{status: status, refresh: true, number: number}
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
		return m, m.setStatus("r requires $EDITOR")
	}
	if m.opts.AgentsPath == "" {
		return m, m.setStatus("AGENTS.md path unknown (run inside a repository)")
	}
	argv := editorArgv(m.opts.Editor, m.opts.AgentsPath)
	c := exec.Command(argv[0], argv[1:]...)
	path := m.opts.AgentsPath
	return m, m.opts.Shell.Exec(c, func(err error) tea.Msg {
		if err != nil {
			return doneMsg{err: fmt.Errorf("editor: %w", err)}
		}
		return doneMsg{status: "Updated AGENTS.md: " + path}
	})
}

func (m Model) peekItem(it Item) (tea.Model, tea.Cmd) {
	if it.Kind != KindRunning && it.Kind != KindBlocked {
		return m, m.setStatus(fmt.Sprintf("#%d is not a running or blocked agent (p only peeks running or blocked agents)", it.Number))
	}
	if it.LogPath == "" && it.Pane == "" {
		return m, m.setStatus(fmt.Sprintf("#%d has no agent log or pane", it.Number))
	}
	if it.LogPath != "" {
		return m, m.peekWithHerdrOrPager(it.Number, it.LogPath, it.Pane)
	}
	if m.opts.Herdr != nil {
		return m, m.peekHerdrCmd(it.Number, "", it.Pane)
	}
	return m, m.setStatus(fmt.Sprintf("#%d needs herdr to open a pane", it.Number))
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
		return doneMsg{status: fmt.Sprintf("Opened #%d agent log in herdr tab", number)}
	}
}

func (m Model) peekHerdrCmd(number int, logPath, pane string) tea.Cmd {
	return func() tea.Msg {
		err := m.opts.Herdr.Peek(context.Background(), logPath, pane)
		if ports.IsPaneNotFound(err) {
			return doneMsg{status: fmt.Sprintf("#%d agent pane not found (session may have changed)", number)}
		}
		if err != nil {
			return doneMsg{err: err}
		}
		if logPath != "" {
			return doneMsg{status: fmt.Sprintf("Opened #%d agent log in herdr tab", number)}
		}
		return doneMsg{status: fmt.Sprintf("Opened #%d agent pane in herdr tab", number)}
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
		return doneMsg{status: fmt.Sprintf("Opened #%d agent log", number)}
	})
}

func (m Model) updateInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc, tea.KeyCtrlC:
		m.mode = modeList
		return m, m.setStatus("Canceled")
	case tea.KeyEnter:
		m.mode = modeList
		text := strings.TrimSpace(string(m.input.text))
		submit := m.input.submit
		targetNumber := m.input.targetNumber
		action := m.input.action
		m.input = inputState{}
		if targetNumber > 0 {
			m.pendingOps[targetNumber] = true
			if action == "reject" {
				m.status = fmt.Sprintf("Rejecting #%d...", targetNumber)
			} else {
				m.status = fmt.Sprintf("Replying to #%d...", targetNumber)
			}
			m.loading = true
		} else if action == "send" && text != "" {
			isAI := m.IssueCreation() == IssueCreationAI
			isAISubmit := (!isAI && strings.HasPrefix(text, "!")) || (isAI && !strings.HasPrefix(text, "!"))
			if isAISubmit {
				m.status = "Generating issue with spec AI (this may take a minute)..."
			} else {
				displayText := strings.TrimSpace(strings.TrimPrefix(text, "!"))
				m.status = fmt.Sprintf("Creating issue %q...", displayText)
			}
			m.loading = true
		}
		return m, submit(text)
	case tea.KeyLeft:
		if m.input.cursor > 0 {
			m.input.cursor--
		}
		return m, nil
	case tea.KeyRight:
		if m.input.cursor < len(m.input.text) {
			m.input.cursor++
		}
		return m, nil
	case tea.KeyHome, tea.KeyCtrlA:
		m.input.cursor = 0
		return m, nil
	case tea.KeyEnd, tea.KeyCtrlE:
		m.input.cursor = len(m.input.text)
		return m, nil
	case tea.KeyBackspace, tea.KeyCtrlH:
		if m.input.cursor > len(m.input.text) {
			m.input.cursor = len(m.input.text)
		}
		if m.input.cursor > 0 {
			m.input.text = append(m.input.text[:m.input.cursor-1], m.input.text[m.input.cursor:]...)
			m.input.cursor--
		}
		return m, nil
	case tea.KeyDelete, tea.KeyCtrlD:
		if m.input.cursor < 0 {
			m.input.cursor = 0
		}
		if m.input.cursor < len(m.input.text) {
			m.input.text = append(m.input.text[:m.input.cursor], m.input.text[m.input.cursor+1:]...)
		}
		return m, nil
	case tea.KeyCtrlU:
		if m.input.cursor > len(m.input.text) {
			m.input.cursor = len(m.input.text)
		}
		if m.input.cursor > 0 {
			m.input.text = append([]rune{}, m.input.text[m.input.cursor:]...)
			m.input.cursor = 0
		}
		return m, nil
	case tea.KeyCtrlK:
		if m.input.cursor < 0 {
			m.input.cursor = 0
		}
		if m.input.cursor < len(m.input.text) {
			m.input.text = append([]rune{}, m.input.text[:m.input.cursor]...)
		}
		return m, nil
	case tea.KeyCtrlW:
		if m.input.cursor > len(m.input.text) {
			m.input.cursor = len(m.input.text)
		}
		if m.input.cursor > 0 {
			idx := m.input.cursor
			for idx > 0 && unicode.IsSpace(m.input.text[idx-1]) {
				idx--
			}
			for idx > 0 && !unicode.IsSpace(m.input.text[idx-1]) {
				idx--
			}
			m.input.text = append(m.input.text[:idx], m.input.text[m.input.cursor:]...)
			m.input.cursor = idx
		}
		return m, nil
	case tea.KeyRunes, tea.KeySpace:
		var runes []rune
		if msg.Type == tea.KeySpace {
			runes = []rune{' '}
		} else {
			runes = msg.Runes
		}
		if len(runes) > 0 {
			if m.input.cursor < 0 {
				m.input.cursor = 0
			}
			if m.input.cursor > len(m.input.text) {
				m.input.cursor = len(m.input.text)
			}
			newText := make([]rune, 0, len(m.input.text)+len(runes))
			newText = append(newText, m.input.text[:m.input.cursor]...)
			newText = append(newText, runes...)
			newText = append(newText, m.input.text[m.input.cursor:]...)
			m.input.text = newText
			m.input.cursor += len(runes)
		}
		return m, nil
	}
	return m, nil
}

func (m Model) renderInputLine() string {
	text := m.input.text
	cursor := m.input.cursor
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(text) {
		cursor = len(text)
	}

	before := string(text[:cursor])
	var cur, after string

	if cursor < len(text) {
		ch := string(text[cursor])
		if m.theme != nil {
			cur = m.theme.Cursor(ch)
		} else {
			cur = "\x1b[7m" + ch + "\x1b[0m"
		}
		after = string(text[cursor+1:])
	} else {
		if m.theme != nil {
			cur = m.theme.Cursor(" ")
		} else {
			cur = "\x1b[7m \x1b[0m"
		}
	}
	return m.input.prompt + before + cur + after
}

func (m Model) updateDetail(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch keyString(msg) {
	case "esc", "q", "enter":
		m.mode = modeList
		m.detailOffset = 0
		return m, nil
	case "ctrl+c":
		return m, tea.Quit
	case "?":
		m.helpReturnMode = modeDetail
		m.mode = modeHelp
		return m, nil
	case "o":
		if row, ok := m.current(); ok && !row.IsHeader() {
			return m, m.openBrowserCmd(*row.Item)
		}
	case "j", "down":
		m.detailOffset++
	case "k", "up":
		if m.detailOffset > 0 {
			m.detailOffset--
		}
	case "pgdown":
		m.detailOffset += m.detailPageSize()
	case "pgup":
		if m.detailOffset > m.detailPageSize() {
			m.detailOffset -= m.detailPageSize()
		} else {
			m.detailOffset = 0
		}
	}
	return m, nil
}

func (m Model) detailPageSize() int {
	if m.height > 0 {
		return max(1, m.height-4)
	}
	return 10
}

// --- view ---------------------------------------------------------------------

// View implements tea.Model.
func (m Model) View() string {
	switch m.mode {
	case modeDetail:
		return m.viewDetail()
	case modeSettings:
		return m.viewSettings()
	case modeLog:
		return m.viewLog()
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
	char := "─"
	if m.theme != nil && m.theme.Mode() == ModePlain {
		char = "-"
	}
	return strings.Repeat(char, w)
}

func (m Model) now() time.Time {
	if m.opts.Now != nil {
		return m.opts.Now()
	}
	return time.Now()
}

func (m Model) headerLine(w int) string {
	left := "lead"
	if m.opts.Repo != "" {
		left += " — " + Sanitize(m.opts.Repo)
	}
	right := fmt.Sprintf("Running %d / parallel %d", m.runningCount(), m.opts.Parallel)
	if m.loading {
		right += "   ⟳ Loading…"
	} else if age := relAge(m.refreshedAt, m.now()); age != "" {
		right += "   ⟳ " + age
	}
	leftSpace := w - cellWidth(right)
	left = truncTail(left, leftSpace-2)
	plain := padRight(left, leftSpace) + right
	if m.theme == nil {
		return plain
	}
	return m.theme.Render(left, TokenFgEmphasis, TokenBgCanvas, true, false, false) + m.theme.Render(right, TokenFgSecondary, TokenBgCanvas, false, false, false)
}

func (m Model) borderChar(strong bool) string {
	if m.theme == nil {
		return "│"
	}
	return m.theme.Border(strong)
}

func (m Model) runningCount() int {
	if m.opts.RunningCount != nil {
		if n, err := m.opts.RunningCount(); err == nil {
			return n
		}
	}
	for _, s := range m.sections {
		if s.Kind == KindRunning {
			return len(s.Items)
		}
	}
	return 0
}

func (m Model) summaryLine(w int) string {
	parts := make([]string, 0, len(m.sections))
	for _, s := range m.sections {
		suffix := fmt.Sprintf("%d", len(s.Items))
		if s.Truncated {
			suffix += "+"
		}
		parts = append(parts, s.Kind.Title()+" "+suffix)
	}
	plain := padRight(truncTail(strings.Join(parts, "   "), w), w)
	if m.theme == nil {
		return plain
	}
	return m.theme.Render(plain, TokenFgSecondary, TokenBgCanvas, false, false, false)
}

func (m *Model) setStatus(s string) tea.Cmd {
	m.status = s
	m.statusGen++
	if m.opts.Headless || strings.HasPrefix(s, "Error:") || strings.HasPrefix(s, "Issue changed") {
		return nil
	}
	return m.statusClearCmd(m.statusGen)
}

func (m Model) statusClearCmd(gen int) tea.Cmd {
	return tea.Tick(5*time.Second, func(t time.Time) tea.Msg {
		return statusClearMsg{gen: gen}
	})
}

func (m Model) statusLine(w int) string {
	var s string
	switch {
	case m.loading && m.status == "cached":
		s = "Showing cached data (fetching latest updates from GitHub...)"
	case m.status != "":
		s = m.status
	case m.loadErr != nil:
		s = "Error: Could not load inbox. " + Sanitize(m.loadErr.Error())
	case m.loading:
		if m.opts.Repo != "" {
			s = fmt.Sprintf("Fetching issues for %s from GitHub...", m.opts.Repo)
		} else {
			s = "Fetching issues from GitHub..."
		}
	default:
		s = "j/k move  Enter open  ? help  q quit"
	}
	plain := padRight(truncTail(s, w), w)
	if m.theme == nil {
		return plain
	}
	return m.theme.Status(plain)
}

func (m Model) viewList() string {
	if m.tooSmall() {
		return m.viewTooSmall()
	}
	var b strings.Builder
	w := m.screenWidth()
	footerStr := m.footer()
	footerLines := strings.Count(footerStr, "\n") + 1
	bodyH := m.bodyHeightFor(footerLines)
	rows := m.visible()

	b.WriteString(m.headerLine(w) + "\n")
	b.WriteString(m.summaryLine(w) + "\n")
	b.WriteString(m.ruleW(w) + "\n")

	if m.isSplit() {
		leftW := (w - 1) * 60 / 100
		rightW := w - 1 - leftW
		cols := m.listColumns(leftW)
		rightHeader := padRight(m.previewHeader(), rightW-1)
		if m.theme != nil {
			rightHeader = m.theme.Preview(rightHeader, true)
		}
		b.WriteString(m.columnHeaderLine(cols, leftW) + m.borderChar(true) + " " + rightHeader + "\n")
		bodyLines := m.bodyLines(rows, m.listTop, bodyH, leftW, cols)
		preview := m.previewLines(rightW-1, bodyH)
		for i := 0; i < bodyH; i++ {
			left := ""
			if i < len(bodyLines) {
				left = bodyLines[i]
			}
			right := ""
			if i < len(preview) {
				right = preview[i]
			}
			right = padRight(right, rightW-1)
			if m.theme != nil {
				right = m.theme.Preview(right, i == 0)
			}
			b.WriteString(padRight(left, leftW) + m.borderChar(true) + " " + right + "\n")
		}
	} else {
		cols := m.listColumns(w)
		b.WriteString(m.columnHeaderLine(cols, w) + "\n")
		bodyLines := m.bodyLines(rows, m.listTop, bodyH, w, cols)
		for i := 0; i < bodyH; i++ {
			line := ""
			if i < len(bodyLines) {
				line = bodyLines[i]
			}
			b.WriteString(padRight(line, w) + "\n")
		}
	}

	b.WriteString(m.ruleW(w) + "\n")
	if m.mode == modeInput {
		b.WriteString(m.renderInputLine() + "\n")
	} else {
		b.WriteString(m.statusLine(w) + "\n")
	}
	b.WriteString(footerStr + "\n")
	return b.String()
}

// bodyHeightFor is the number of body rows given the actual footer line count.
func (m Model) bodyHeightFor(footerLines int) int {
	if m.height <= 0 {
		if n := len(m.visible()); n > 0 {
			return n
		}
		return 1
	}
	fixed := 6 + footerLines
	if h := m.height - fixed; h > 0 {
		return h
	}
	return 1
}

func (m Model) footer() string {
	tokens := m.footerTokens()
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
	plain := strings.Join(lines, "\n")
	if m.theme == nil {
		return plain
	}
	return m.theme.Render(plain, TokenFgSecondary, TokenBgCanvas, false, false, false)
}

func (m Model) footerTokens() []string {
	switch m.mode {
	case modeInput:
		if m.input.action == "reject" {
			return []string{"[Enter] Reject", "[Esc] Cancel"}
		}
		return []string{"[Enter] Send", "[Esc] Cancel"}
	case modeDetail:
		return []string{"[↑/↓] Scroll", "[PgUp/PgDn] Page", "[o] Browser", "[?] Help", "[Esc] Back"}
	case modeLog:
		return []string{"[↑/↓] Scroll", "[?] Help", "[Esc/q/L] Back"}
	case modeList:
		row, ok := m.current()
		if !ok {
			return []string{"[s] New", "[m] Mode: " + m.AgentMode(), "[g] Agent: " + m.Agent(), "[,] Settings", "[?] Help", "[q] Quit"}
		}
		var tokens []string
		if row.IsHeader() {
			if len(row.Section.Items) > 0 {
				tokens = []string{"[Enter] Toggle", "[z] Toggle all", "[s] New"}
			} else {
				tokens = []string{"[s] New"}
			}
		} else {
			switch row.Item.Kind {
			case KindNeedsReview, KindBacklog:
				tokens = []string{"[Enter] Open", "[a] Approve", "[t] Reply"}
			case KindBlocked:
				tokens = []string{"[Enter] Open", "[t] Reply", "[p] Peek"}
			case KindMerged:
				tokens = []string{"[Enter] Open", "[n] Report bug", "[c] Mark seen"}
			case KindRunning:
				tokens = []string{"[Enter] Open", "[p] Peek", "[o] Browser"}
			}
		}
		tokens = append(tokens, "[m] Mode: "+m.AgentMode(), "[g] Agent: "+m.Agent(), "[,] Settings", "[?] Help", "[q] Quit")
		return tokens
	}
	return []string{"[,] Settings", "[?] Help", "[q] Quit"}
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
	b.WriteString(m.rule() + "\n")
	content := m.detailContent()
	if content == "" {
		content = "No issue selected."
	}
	lines := strings.Split(content, "\n")
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
	b.WriteString(m.footer() + "\n")
	out := b.String()
	if m.theme != nil {
		out = m.theme.Render(out, TokenFgPrimary, TokenBgSurface, false, false, false)
	}
	return out
}

func (m Model) viewHelp() string {
	out := strings.Join([]string{
		"Inbox actions",
		m.rule(),
		"Move",
		"  [j/k] or [↑/↓]    Move up/down (headers and issues)",
		"  [J/K]             Scroll preview pane down/up",
		"  [Enter/space/→/l] Toggle section or open selected issue",
		"  [z/Tab]           Toggle all sections",
		"",
		"Issue actions",
		"  [a] Approve           needs-review issues only",
		"  [e] Edit then approve needs-review issues only",
		"  [t] Reply / comment   any issue",
		"  [x] Reject / close    any issue with optional reason",
		"  [p] Peek agent log    running or blocked agents only",
		"  [o] Open in browser   any issue",
		"  [c] Mark as seen      merged issues only",
		"  [n] Report bug         merged issues only",
		"",
		"Global",
		"  [s] New issue (direct create, or !one-liner for AI spec)",
		"  [L] Event log history",
		"  [,] Settings (configure agent, mode, issue creation)",
		"  [m] Toggle agent mode (batch/dangerous/interactive)",
		"  [g] Toggle agent (agy/claude/codex/devin/opencode/gemini)",
		"  [r] Open AGENTS.md",
		"  [R] Reload inbox",
		"  [?] Toggle this help",
		"  [q] Quit inbox",
		"",
		"Detail (full-screen)",
		"  [j/k] or [↑/↓]  Scroll",
		"  [PgUp/PgDn]     Page",
		"  [o] Open in browser",
		"  [?] This help",
		"  [Esc/q] Back to list",
		"",
		m.rule(),
		"Press Esc, q, or any other key to go back",
		"",
	}, "\n")
	if m.theme != nil {
		out = m.theme.Render(out, TokenFgPrimary, TokenBgCanvas, false, false, false)
	}
	return out
}

// relAge renders compact ages ("1m"-style); zero time yields "".
func relAge(t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
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

func (m *Model) checkNotifications(sections []Section) {
	currentReview := make(map[int]bool)
	currentBlocked := make(map[int]bool)

	for _, sec := range sections {
		switch sec.Kind {
		case KindNeedsReview:
			for _, it := range sec.Items {
				currentReview[it.Number] = true
				if m.hasInitialLoad && !m.knownNeedsReview[it.Number] {
					m.notifyItem("Needs Review", it)
				}
			}
		case KindBlocked:
			for _, it := range sec.Items {
				currentBlocked[it.Number] = true
				if m.hasInitialLoad && !m.knownBlocked[it.Number] {
					m.notifyItem("Agent Blocked", it)
				}
			}
		}
	}
	m.knownNeedsReview = currentReview
	m.knownBlocked = currentBlocked
	m.hasInitialLoad = true
}

func (m Model) notifyItem(kind string, it Item) {
	if m.opts.Notifier == nil {
		return
	}
	title := fmt.Sprintf("%s: #%d %s", kind, it.Number, it.Title)
	msg := ""
	if it.Agent != "" {
		msg = fmt.Sprintf("Agent: %s", it.Agent)
	}
	if it.PRNumber > 0 {
		if msg != "" {
			msg += fmt.Sprintf(" (PR #%d)", it.PRNumber)
		} else {
			msg = fmt.Sprintf("PR #%d", it.PRNumber)
		}
	}
	_ = m.opts.Notifier.Notify(title, msg)
}


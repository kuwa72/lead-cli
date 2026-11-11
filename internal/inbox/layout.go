package inbox

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
	"github.com/muesli/termenv"

	"github.com/kuwa72/lead-cli/internal/state"
)

// minWidth/minHeight gate the dashboard layout (docs/rfc-inbox-ui-density.md §2.1).
// minListW/minPreviewW are the smallest usable widths for the list pane
// and the preview content column in split mode.
const (
	minWidth          = 80
	minHeight         = 24
	minListW          = 48
	minPreviewW       = 36
	splitResizeStep   = 4
	minResizeListW    = 30
	minResizePreviewW = 28
)

// splitMinW is the smallest terminal width that fits both panes:
// list minimum + pane border + preview padding + preview minimum.
const splitMinW = minListW + 1 + 1 + minPreviewW

// screenWidth is the effective terminal width; 0 means "not yet known"
// (headless / unit tests) and falls back to a plain list width.
func (m Model) screenWidth() int {
	if m.width <= 0 {
		return 78
	}
	return m.width
}

// tooSmall reports whether a known terminal size is below the minimum.
// Unknown dimensions (0) never trigger the notice so headless output works.
func (m Model) tooSmall() bool {
	return (m.width > 0 && m.width < minWidth) || (m.height > 0 && m.height < minHeight)
}

// isSplit reports whether the terminal is wide enough for the split layout.
func (m Model) isSplit() bool {
	return m.width >= splitMinW && (m.height <= 0 || m.height >= minHeight)
}

// splitPaneWidths apportions a terminal of width w between the list pane
// (leftW) and the preview pane (rightW, including its 1-cell left
// padding); the pane border takes the remaining cell. The default split is
// balanced 50:50, clamped so neither pane shrinks below its minimum.
func splitPaneWidths(w int) (leftW, rightW int) {
	avail := w - 1 // pane border
	leftW = avail / 2
	if leftW < minListW {
		leftW = minListW
	}
	if maxLeft := avail - (minPreviewW + 1); leftW > maxLeft {
		leftW = maxLeft
	}
	return leftW, avail - leftW
}

// splitWidths returns the active (leftW, rightW) taking user resize offset into account.
func (m Model) splitWidths(w int) (leftW, rightW int) {
	leftW, rightW = splitPaneWidths(w)
	if m.splitOffset == 0 {
		return leftW, rightW
	}
	avail := w - 1
	leftW += m.splitOffset
	minL := minResizeListW
	maxL := avail - (minResizePreviewW + 1)
	if maxL < minL {
		return splitPaneWidths(w)
	}
	if leftW < minL {
		leftW = minL
	}
	if leftW > maxL {
		leftW = maxL
	}
	return leftW, avail - leftW
}

// bodyHeight is the number of rows reserved for the list/preview body,
// assuming a one-line footer (the common case).
func (m Model) bodyHeight() int { return m.bodyHeightFor(1) }

func (m Model) ruleW(w int) string {
	if w <= 0 {
		w = 78
	}
	char := "─"
	if m.theme != nil && m.theme.Mode() == ModePlain {
		char = "-"
	}
	line := strings.Repeat(char, w)
	if m.theme != nil && m.theme.Mode() != ModePlain && m.theme.Profile() != termenv.Ascii {
		return m.theme.Render(line, TokenBorderMuted, TokenBgCanvas, false, false, false)
	}
	return line
}

func cellWidth(s string) int { return lipgloss.Width(s) }

// truncTail truncates s to at most w display cells, ending with "…" when cut.
func truncTail(s string, w int) string {
	if cellWidth(s) <= w {
		return s
	}
	if w <= 0 {
		return ""
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		rw := runewidth.RuneWidth(r)
		if used+rw > w-1 {
			break
		}
		b.WriteRune(r)
		used += rw
	}
	return b.String() + "…"
}

// wrapCells breaks s into lines of at most w display cells without dropping
// characters. Full-width runes count as two cells (go-runewidth), so CJK text
// wraps at the pane width instead of being cut off.
func wrapCells(s string, w int) []string {
	if w <= 0 || cellWidth(s) <= w {
		return []string{s}
	}
	var lines []string
	var b strings.Builder
	used := 0
	for _, r := range s {
		rw := runewidth.RuneWidth(r)
		if used > 0 && used+rw > w {
			lines = append(lines, b.String())
			b.Reset()
			used = 0
		}
		b.WriteRune(r)
		used += rw
	}
	if b.Len() > 0 {
		lines = append(lines, b.String())
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

func padRight(s string, w int) string {
	if d := w - cellWidth(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

func padLeft(s string, w int) string {
	if d := w - cellWidth(s); d > 0 {
		return strings.Repeat(" ", d) + s
	}
	return s
}

// fitLeft truncates (with "…") and left-aligns s into w cells.
func fitLeft(s string, w int) string { return padRight(truncTail(s, w), w) }

// fitRight truncates (leading "…" is not used) and right-aligns s into w cells.
func fitRight(s string, w int) string { return padLeft(truncTail(s, w), w) }

func digits(n int) int {
	if n <= 0 {
		return 1
	}
	d := 0
	for n > 0 {
		n /= 10
		d++
	}
	return d
}

// shortAge renders the compact Updated column value ("12s"/"3m"/"2h"/"1d").
func shortAge(t, now time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := now.Sub(t)
	if d < 0 {
		d = 0
	}
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

// rowAge is the Updated column value. Running agents show last activity
// instead of the issue update time (issue #187): liveness at a glance in
// narrow layouts without a preview pane. Last-activity is the watchdog's
// definition (issue #216): log mtime, else started_at.
func rowAge(r Row, now time.Time) string {
	if r.Item != nil && r.Section.Kind == KindRunning {
		if last := state.LastActivity(r.Item.LogPath, r.Item.StartedAt); !last.IsZero() {
			return shortAge(last, now)
		}
	}
	if r.Item == nil {
		return "-"
	}
	return shortAge(r.Item.UpdatedAt, now)
}

// listColumns holds the resolved column widths for one render pass.
type listColumns struct {
	issueW, titleW, updW, agentW, triesW, prW int
	showUpd, showAgent, showTries, showPR     bool
}

// listColumns resolves widths against leftW. Numeric columns expand to the
// widest value in the current data; when the title cannot keep 20 cells the
// Updated → Agent → Tries → PR columns are hidden in that order.
func (m Model) listColumns(leftW int) listColumns {
	c := listColumns{issueW: 6, updW: 7, agentW: 7, triesW: 5, prW: 6,
		showUpd: true, showAgent: true, showTries: true, showPR: true}
	maxNum, maxTries, maxPR := 0, 0, 0
	for _, s := range m.sections {
		for _, it := range s.Items {
			if it.Number > maxNum {
				maxNum = it.Number
			}
			if it.Attempts > maxTries {
				maxTries = it.Attempts
			}
			if it.PRNumber > maxPR {
				maxPR = it.PRNumber
			}
		}
	}
	if d := digits(maxNum); d > c.issueW {
		c.issueW = d
	}
	if d := digits(maxTries); d > c.triesW {
		c.triesW = d
	}
	if d := digits(maxPR) + 1; d > c.prW {
		c.prW = d // '#' + digits
	}
	for {
		meta := 0
		if c.showUpd {
			meta += c.updW + 1
		}
		if c.showAgent {
			meta += c.agentW + 1
		}
		if c.showTries {
			meta += c.triesW + 1
		}
		if c.showPR {
			meta += c.prW + 1
		}
		c.titleW = leftW - (2 + c.issueW + 1 + meta)
		if c.titleW >= 20 {
			break
		}
		switch {
		case c.showUpd:
			c.showUpd = false
		case c.showAgent:
			c.showAgent = false
		case c.showTries:
			c.showTries = false
		case c.showPR:
			c.showPR = false
		default:
			return c // even title must shrink below 20
		}
	}
	return c
}

func (m Model) columnHeaderLine(c listColumns, leftW int) string {
	var b strings.Builder
	b.WriteString("  ") // selection column
	b.WriteString(fitRight("Issue", c.issueW))
	b.WriteString(" ")
	b.WriteString(fitLeft("Title", c.titleW))
	if c.showUpd {
		b.WriteString(" " + fitRight("Updated", c.updW))
	}
	if c.showAgent {
		b.WriteString(" " + fitLeft("Agent", c.agentW))
	}
	if c.showTries {
		b.WriteString(" " + fitRight("Tries", c.triesW))
	}
	if c.showPR {
		b.WriteString(" " + fitRight("PR", c.prW))
	}
	plain := padRight(b.String(), leftW)
	if m.theme == nil {
		return plain
	}
	return m.theme.Render(plain, TokenFgSecondary, TokenBgCanvas, true, false, false)
}

// renderRow renders one visible row (header or item) into leftW cells.
func (m Model) renderRow(r Row, c listColumns, leftW int, selected bool, now time.Time) string {
	var b strings.Builder
	if selected {
		b.WriteString("> ")
	} else {
		b.WriteString("  ")
	}
	if r.IsHeader() {
		s := r.Section
		mark := "▾"
		if s.Collapsed && !m.expanded {
			mark = "▸"
		}
		count := fmt.Sprintf("(%d)", len(s.Items))
		if s.Truncated {
			count = fmt.Sprintf("(%d+)", len(s.Items))
		}
		b.WriteString(mark + " " + s.Kind.Title() + " " + count)
		plain := padRight(b.String(), leftW)
		if m.theme == nil {
			return plain
		}
		if selected {
			return m.theme.Accent(plain[:2], true) + m.theme.Selected(plain[2:])
		}
		return m.theme.SectionHeader(r.Section.Kind, plain, len(r.Section.Items) > 0)
	}
	it := *r.Item
	rawTitle := Sanitize(it.Title)
	title := rawTitle
	var badge string
	var badgeToken Token
	if it.Kind == KindRunning && it.Stage != "" {
		badge = "[" + Sanitize(it.Stage) + "]"
		badgeToken = TokenAccentPrimary
		rawTitle = truncTail(rawTitle, c.titleW-cellWidth(badge)-2)
		title = rawTitle
		if title != "" {
			title += "  "
		}
		title += badge
	} else if it.DeferredBy > 0 {
		// Deferred rows carry the reason in the title column; the note is
		// kept whole while the title yields space for it (issue #212).
		badge = fmt.Sprintf("deferred: blocked by #%d", it.DeferredBy)
		badgeToken = TokenStatusWarning
		rawTitle = truncTail(rawTitle, c.titleW-cellWidth(badge)-2)
		title = rawTitle
		if title != "" {
			title += "  "
		}
		title += badge
	} else {
		rawTitle = truncTail(rawTitle, c.titleW)
		title = rawTitle
	}
	issueCell := fitRight(fmt.Sprintf("#%d", it.Number), c.issueW)
	titleCell := fitLeft(title, c.titleW)
	b.WriteString(issueCell)
	b.WriteString(" ")
	b.WriteString(titleCell)
	var updCell, agentCell, triesCell, prCell string
	if c.showUpd {
		updCell = fitRight(rowAge(r, now), c.updW)
		b.WriteString(" " + updCell)
	}
	if c.showAgent {
		agent := Sanitize(it.Agent)
		if agent == "" {
			agent = "-"
		}
		agentCell = fitLeft(agent, c.agentW)
		b.WriteString(" " + agentCell)
	}
	if c.showTries {
		tries := "-"
		if it.Attempts > 0 {
			tries = fmt.Sprintf("%d", it.Attempts)
		}
		triesCell = fitRight(tries, c.triesW)
		b.WriteString(" " + triesCell)
	}
	if c.showPR {
		pr := "-"
		if it.PRNumber > 0 {
			pr = fmt.Sprintf("#%d", it.PRNumber)
		}
		prCell = fitRight(pr, c.prW)
		b.WriteString(" " + prCell)
	}
	plain := padRight(b.String(), leftW)
	if selected && m.theme != nil {
		return m.theme.Accent(plain[:2], true) + m.theme.Selected(plain[2:])
	}
	if m.theme != nil && m.theme.Mode() != ModePlain && m.theme.Profile() != termenv.Ascii {
		var sb strings.Builder
		if badge != "" && cellWidth(rawTitle)+cellWidth(badge) <= c.titleW {
			sep := ""
			if rawTitle != "" {
				sep = "  "
			}
			used := cellWidth(rawTitle) + cellWidth(sep) + cellWidth(badge)
			pad := ""
			if c.titleW > used {
				pad = strings.Repeat(" ", c.titleW-used)
			}
			sb.WriteString(m.theme.Render("  "+issueCell+" "+rawTitle+sep, TokenFgPrimary, TokenBgCanvas, false, false, false))
			sb.WriteString(m.theme.Render(badge, badgeToken, TokenBgCanvas, true, false, false))
			if pad != "" {
				sb.WriteString(m.theme.Render(pad, TokenFgPrimary, TokenBgCanvas, false, false, false))
			}
		} else {
			sb.WriteString(m.theme.Render("  "+issueCell+" "+titleCell, TokenFgPrimary, TokenBgCanvas, false, false, false))
		}
		if c.showUpd {
			sb.WriteString(m.theme.Render(" "+updCell, TokenFgSecondary, TokenBgCanvas, false, false, false))
		}
		if c.showAgent {
			sb.WriteString(m.theme.Render(" "+agentCell, TokenFgSecondary, TokenBgCanvas, false, false, false))
		}
		if c.showTries {
			sb.WriteString(m.theme.Render(" "+triesCell, TokenFgSecondary, TokenBgCanvas, false, false, false))
		}
		if c.showPR {
			prTok := TokenFgSecondary
			if it.PRNumber > 0 {
				prTok = TokenStatusInfo
			}
			sb.WriteString(m.theme.Render(" "+prCell, prTok, TokenBgCanvas, false, false, false))
		}
		return padRight(sb.String(), leftW)
	}
	if m.theme != nil {
		return m.theme.ListRow(plain, false)
	}
	return plain
}

// headerRowIndex returns the index of the section-header row for the row at
// idx, or -1 when idx itself is a header.
func (m Model) headerRowIndex(rows []Row, idx int) int {
	for i := idx; i >= 0; i-- {
		if rows[i].IsHeader() {
			if rows[i].Section == rows[idx].Section {
				return i
			}
			return -1
		}
	}
	return -1
}

// bodyLines renders the scrolled window of list rows. When the section header
// of the first visible row scrolled off, it is pinned as a sticky first line.
func (m Model) bodyLines(rows []Row, top, bodyH, leftW int, c listColumns) []string {
	if top < 0 {
		top = 0
	}
	if top > len(rows) {
		top = len(rows)
	}
	end := top + bodyH
	if end > len(rows) {
		end = len(rows)
	}
	var lines []string
	// Sticky section header: top row is an item whose header scrolled off.
	if top < len(rows) && !rows[top].IsHeader() {
		if hi := m.headerRowIndex(rows, top); hi >= 0 && hi < top {
			lines = append(lines, m.renderRow(rows[hi], c, leftW, false, m.now()))
		}
	}
	for i := top; i < end && len(lines) < bodyH; i++ {
		lines = append(lines, m.renderRow(rows[i], c, leftW, i == m.cursor, m.now()))
	}
	return lines
}

func (m Model) hasAnyItems() bool {
	for _, s := range m.sections {
		if len(s.Items) > 0 {
			return true
		}
	}
	return false
}

const maxPreviewLogicalLines = 2000

// previewPaneHeight is the body height the preview pane gets in split mode.
func (m Model) previewPaneHeight() int {
	return m.bodyHeightFor(strings.Count(m.footer(), "\n") + 1 + logPaneHeight)
}

// previewTitleLines wraps the selected issue's title to w cells without ellipsis,
// bounded to at most half the pane height on very short terminals.
func (m Model) previewTitleLines(w, h int) []string {
	if w <= 0 || h <= 0 || m.detail.Number == 0 {
		return nil
	}
	lines := wrapCells(Sanitize(m.detail.Title), w)
	maxTitleH := max(1, h/2)
	if len(lines) > maxTitleH {
		lines = lines[:maxTitleH]
	}
	return lines
}

// previewWrappedBody returns the wrapped visual lines of the active inspector tab
// for width w and body height bodyH, and whether content overflows bodyH.
func (m Model) previewWrappedBody(w, bodyH int) ([]string, bool) {
	if w <= 0 || bodyH <= 0 {
		return nil, false
	}
	row, ok := m.current()
	if !ok || row.IsHeader() || m.detail.Number == 0 {
		return nil, false
	}

	isLiveLog := (m.inspectorTab == inspectorTabLog && (m.detailAgentLog != "" || row.Item.LogPath != "" || row.Item.Kind == KindRunning)) ||
		(m.inspectorTab == inspectorTabOverview && row.Item.Kind == KindRunning)

	if isLiveLog && m.previewOffset == 0 && m.detailAgentLog != "" {
		return m.liveLogWrappedBody(row.Item, w, bodyH)
	}

	content, more := m.previewContent(maxPreviewLogicalLines)
	var allVisual []string
	for _, l := range content {
		allVisual = append(allVisual, wrapCells(Sanitize(l), w)...)
	}
	overflows := more || len(allVisual) > bodyH
	return allVisual, overflows
}

// liveLogWrappedBody wraps the running agent header and log lines, ensuring that
// at previewOffset == 0 the most recent wrapped log lines remain visible inside
// bodyH even when earlier log lines wrap across multiple rows.
func (m Model) liveLogWrappedBody(it *Item, w, bodyH int) ([]string, bool) {
	var headerVisual []string
	headerVisual = append(headerVisual, wrapCells("Agent Live Output", w)...)
	if meta := runningMetaLine(it, m.detailPrNum, m.detailPrErr, m.now()); meta != "" {
		headerVisual = append(headerVisual, wrapCells(meta, w)...)
	}
	if it.LogPath != "" {
		headerVisual = append(headerVisual, wrapCells(fmt.Sprintf("Log: %s", Sanitize(it.LogPath)), w)...)
	}

	var logVisual []string
	rawLogs := strings.Split(Sanitize(strings.ReplaceAll(m.detailAgentLog, "\r\n", "\n")), "\n")
	for _, l := range rawLogs {
		logVisual = append(logVisual, wrapCells(l, w)...)
	}

	var issueVisual []string
	if m.detail.Body != "" {
		rawIssue, _ := appendSourceBody(nil, m.detail.Body, "Issue", maxPreviewLogicalLines)
		for _, l := range rawIssue {
			issueVisual = append(issueVisual, wrapCells(Sanitize(l), w)...)
		}
	}

	totalAll := len(headerVisual) + len(logVisual) + len(issueVisual)
	if totalAll <= bodyH {
		out := append(append(headerVisual, logVisual...), issueVisual...)
		return out, false
	}

	// Reserve 1 line for "Enter: Full details" when overflowing.
	visBudget := max(1, bodyH-1)
	availForLog := visBudget - len(headerVisual)
	if availForLog > 0 && len(logVisual) > availForLog {
		trimmedLog := logVisual[len(logVisual)-availForLog:]
		out := append(headerVisual, trimmedLog...)
		return out, true
	}
	out := append(append(headerVisual, logVisual...), issueVisual...)
	return out, true
}

// maxPreviewOffset is the maximum valid visual-line scroll offset for the right pane.
func (m Model) maxPreviewOffset() int {
	if !m.isSplit() {
		return 0
	}
	row, ok := m.current()
	if !ok || row.IsHeader() || m.detail.Number == 0 {
		return 0
	}
	_, rightW := m.splitWidths(m.screenWidth())
	w := rightW - 1
	h := m.previewPaneHeight()
	if w <= 0 || h <= 0 {
		return 0
	}
	titleLines := m.previewTitleLines(w, h)
	bodyH := h - len(titleLines)
	if bodyH <= 1 {
		return 0
	}
	content, _ := m.previewContent(maxPreviewLogicalLines)
	var allVisual []string
	for _, l := range content {
		allVisual = append(allVisual, wrapCells(Sanitize(l), w)...)
	}
	if len(allVisual) <= bodyH {
		return 0
	}
	visBudget := max(1, bodyH-1)
	return max(0, len(allVisual)-visBudget)
}

// previewOverflows reports whether the selected issue's preview is taller than
// the pane, so J/K can reveal more content. Pane height and wrapped visual lines
// are resolved here so the scroll hint and previewLines agree on what overflows.
func (m Model) previewOverflows() bool {
	if !m.isSplit() {
		return false
	}
	row, ok := m.current()
	if !ok || row.IsHeader() || m.detail.Number == 0 {
		return false
	}
	_, rightW := m.splitWidths(m.screenWidth())
	w := rightW - 1
	h := m.previewPaneHeight()
	if w <= 0 || h <= 0 {
		return false
	}
	titleLines := m.previewTitleLines(w, h)
	bodyH := h - len(titleLines)
	if bodyH <= 0 {
		return true
	}
	_, overflows := m.previewWrappedBody(w, bodyH)
	return overflows
}

// previewHeader is the column-header cell above the right preview pane.
func (m Model) previewHeader() string {
	row, ok := m.current()
	if !ok || row.IsHeader() || m.detail.Number == 0 {
		if !m.hasAnyItems() {
			return "Inbox Status"
		}
		return ""
	}
	prefix := ""
	if m.inspectorFocus {
		prefix = "● "
	}
	header := prefix + Sanitize(fmt.Sprintf("#%d  %s", m.detail.Number, row.Section.Kind.Title()))
	if m.previewOffset > 0 {
		header += fmt.Sprintf("  ↑ %d", m.previewOffset)
	}
	tabs := [3]string{"1:Overview", "2:Diff", "3:Log"}
	idx := m.inspectorTab
	if idx < 0 || idx >= len(tabs) {
		idx = 0
	}
	tabs[idx] = "[" + tabs[idx] + "]"
	header += "  " + strings.Join(tabs[:], " ")
	return header
}

// fitPreviewHeader formats the right-pane header to fit within w display cells
// without overflowing the terminal width when the right pane is narrow.
func (m Model) fitPreviewHeader(w int) string {
	if w <= 0 {
		return ""
	}
	row, ok := m.current()
	if !ok || row.IsHeader() || m.detail.Number == 0 {
		return fitLeft(m.previewHeader(), w)
	}
	full := m.previewHeader()
	if cellWidth(full) <= w {
		return padRight(full, w)
	}
	tabs := [3]string{"1:Overview", "2:Diff", "3:Log"}
	idx := m.inspectorTab
	if idx < 0 || idx >= len(tabs) {
		idx = 0
	}
	tabs[idx] = "[" + tabs[idx] + "]"
	tabsStr := strings.Join(tabs[:], " ")

	prefix := ""
	if m.inspectorFocus {
		prefix = "● "
	}
	offsetStr := ""
	if m.previewOffset > 0 {
		offsetStr = fmt.Sprintf(" ↑%d", m.previewOffset)
	}
	shortLeft := prefix + fmt.Sprintf("#%d", m.detail.Number) + offsetStr
	if cellWidth(shortLeft)+1+cellWidth(tabsStr) <= w {
		return fitLeft(shortLeft+" "+tabsStr, w)
	}
	return fitLeft(full, w)
}

// previewLines renders the selected issue's detail into at most h lines of
// width w, wrapping titles, bodies, diffs, and logs across visual lines.
func (m Model) previewLines(w, h int) []string {
	if h <= 0 || w <= 0 {
		return nil
	}
	if m.previewLoading {
		return []string{truncTail(fmt.Sprintf("Loading #%d…", m.detail.Number), w)}
	}
	if m.previewErr != nil {
		return []string{truncTail(fmt.Sprintf("Unable to load #%d. [R] Retry", m.detail.Number), w)}
	}
	row, ok := m.current()
	if !ok || row.IsHeader() || m.detail.Number == 0 {
		if !m.hasAnyItems() {
			var raw []string
			raw = append(raw, "No inbox items")
			raw = append(raw, "")
			if m.repoOpenCount > 0 {
				raw = append(raw, fmt.Sprintf("Repository has %d open issue(s) on GitHub,", m.repoOpenCount))
				raw = append(raw, "but none are labeled 'needs-review' or 'blocked'.")
			} else {
				raw = append(raw, "No open issues in repository.")
			}
			raw = append(raw, "")
			raw = append(raw, "Getting started:")
			raw = append(raw, "  • Press 's' to create a new issue (AI / manual)")
			raw = append(raw, "  • Run 'lead run' in terminal to pick any open issue")
			if m.repoOpenCount > 0 {
				raw = append(raw, "  • Label existing issue to show here:")
				raw = append(raw, "    gh issue edit <n> --add-label needs-review")
			}
			raw = append(raw, "  • Press 'L' to view event log history")
			var lines []string
			for _, r := range raw {
				for _, wrapped := range wrapCells(r, w) {
					if len(lines) >= h {
						break
					}
					lines = append(lines, wrapped)
				}
			}
			for len(lines) < h {
				lines = append(lines, "")
			}
			return lines[:h]
		}
		return nil
	}

	titleLines := m.previewTitleLines(w, h)
	lines := append([]string(nil), titleLines...)
	bodyH := h - len(titleLines)
	if bodyH <= 0 {
		return lines[:h]
	}

	allVisual, overflows := m.previewWrappedBody(w, bodyH)
	if !overflows && m.previewOffset == 0 {
		lines = append(lines, allVisual...)
		for len(lines) < h {
			lines = append(lines, "")
		}
		return lines[:h]
	}

	// When previewOffset > 0 on a live log, use the full wrapped visual lines so the user can scroll freely.
	if m.previewOffset > 0 {
		content, _ := m.previewContent(maxPreviewLogicalLines)
		allVisual = nil
		for _, l := range content {
			allVisual = append(allVisual, wrapCells(Sanitize(l), w)...)
		}
	}

	visBudget := max(1, bodyH-1)
	offset := m.previewOffset
	maxOff := max(0, len(allVisual)-visBudget)
	if offset > maxOff {
		offset = maxOff
	}
	if offset < len(allVisual) {
		end := min(len(allVisual), offset+visBudget)
		lines = append(lines, allVisual[offset:end]...)
	}
	if len(lines) < h {
		lines = append(lines, truncTail("Enter: Full details", w))
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	return lines[:h]
}

// scrollToCursor keeps the cursor inside the [listTop, listTop+bodyHeight)
// window. Called from Update after cursor-affecting keys and resize.
func (m *Model) scrollToCursor() {
	h := m.bodyHeight()
	rows := len(m.visible())
	maxTop := rows - h
	if maxTop < 0 {
		maxTop = 0
	}
	if m.cursor < m.listTop {
		m.listTop = m.cursor
	}
	if m.cursor >= m.listTop+h {
		m.listTop = m.cursor - h + 1
	}
	if m.listTop > maxTop {
		m.listTop = maxTop
	}
	if m.listTop < 0 {
		m.listTop = 0
	}
}

// viewTooSmall renders the size notice for sub-minimum terminals.
func (m Model) viewTooSmall() string {
	w := m.screenWidth()
	msg := "Terminal too small. Resize to 80x24. [q] Quit"
	var b strings.Builder
	for cellWidth(msg) > 0 {
		line := truncTail(msg, w)
		if line == msg && cellWidth(msg) <= w {
			b.WriteString(msg + "\n")
			break
		}
		// crude wrap: cut at width
		line = ""
		used := 0
		for i, r := range msg {
			rw := runewidth.RuneWidth(r)
			if used+rw > w {
				line = msg[:i]
				break
			}
			used += rw
		}
		if line == "" {
			line = msg
		}
		b.WriteString(line + "\n")
		msg = strings.TrimPrefix(msg[len(line):], " ")
	}
	return b.String()
}

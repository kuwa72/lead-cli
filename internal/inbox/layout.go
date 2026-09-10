package inbox

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

// minWidth/minHeight gate the dashboard layout (docs/rfc-inbox-ui-density.md §2.1).
const (
	minWidth   = 80
	minHeight  = 24
	splitWidth = 120
)

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
	return m.width >= splitWidth && (m.height <= 0 || m.height >= minHeight)
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
	return strings.Repeat(char, w)
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
		if r.Section.Kind == KindBlocked && len(r.Section.Items) > 0 {
			return m.theme.Render(plain, TokenStatusWarning, TokenBgCanvas, true, false, false)
		}
		return m.theme.ListRow(plain, true)
	}
	it := *r.Item
	title := Sanitize(it.Title)
	b.WriteString(fitRight(fmt.Sprintf("#%d", it.Number), c.issueW))
	b.WriteString(" ")
	b.WriteString(fitLeft(title, c.titleW))
	if c.showUpd {
		b.WriteString(" " + fitRight(shortAge(it.UpdatedAt, now), c.updW))
	}
	if c.showAgent {
		agent := Sanitize(it.Agent)
		if agent == "" {
			agent = "-"
		}
		b.WriteString(" " + fitLeft(agent, c.agentW))
	}
	if c.showTries {
		tries := "-"
		if it.Attempts > 0 {
			tries = fmt.Sprintf("%d", it.Attempts)
		}
		b.WriteString(" " + fitRight(tries, c.triesW))
	}
	if c.showPR {
		pr := "-"
		if it.PRNumber > 0 {
			pr = fmt.Sprintf("#%d", it.PRNumber)
		}
		b.WriteString(" " + fitRight(pr, c.prW))
	}
	plain := padRight(b.String(), leftW)
	if selected && m.theme != nil {
		return m.theme.Accent(plain[:2], true) + m.theme.Selected(plain[2:])
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

// previewHeader is the column-header cell above the right preview pane.
func (m Model) previewHeader() string {
	row, ok := m.current()
	if !ok || row.IsHeader() || m.detail.Number == 0 {
		return ""
	}
	return Sanitize(fmt.Sprintf("#%d  %s", m.detail.Number, row.Section.Kind.Title()))
}

// previewLines renders the selected issue's detail into at most h lines of
// width w, using section-specific extraction and source attribution.
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
		return nil
	}
	var lines []string
	lines = append(lines, truncTail(Sanitize(m.detail.Title), w))
	content, more := m.previewContent(max(0, h+m.previewOffset+20))
	if m.previewOffset > 0 {
		if m.previewOffset < len(content) {
			content = content[m.previewOffset:]
		} else if len(content) > 0 {
			content = content[len(content)-1:]
		}
	}
	for _, l := range content {
		if len(lines) >= h {
			break
		}
		lines = append(lines, truncTail(Sanitize(l), w))
	}
	if (more || m.previewOffset > 0) && len(lines) < h {
		lines = append(lines, "Enter: Full details")
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	return lines
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

package inbox

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/kuwa72/lead-cli/internal/ports"
)

// previewSnapshot is the complete data shown in a preview or detail pane.
type previewSnapshot struct {
	issue       ports.Issue
	prNumber    int
	prBody      string
	prErr       bool
	comments    []ports.Comment
	commentsErr bool
}

// applySnapshot updates the model's current detail fields from a snapshot.
func (m *Model) applySnapshot(s previewSnapshot) {
	m.detail = s.issue
	m.detailPrNum = s.prNumber
	m.detailPrBody = s.prBody
	m.detailPrErr = s.prErr
	m.detailComments = s.comments
	m.detailCommentsErr = s.commentsErr
}

// cacheSnapshot stores a snapshot so returning to the same issue is instant.
func (m *Model) cacheSnapshot(number int, s previewSnapshot) {
	if m.previewCache == nil {
		m.previewCache = make(map[int]previewSnapshot)
	}
	m.previewCache[number] = s
}

// cachedSnapshot returns the cached snapshot and true if it exists.
func (m *Model) cachedSnapshot(number int) (previewSnapshot, bool) {
	if m.previewCache == nil {
		return previewSnapshot{}, false
	}
	s, ok := m.previewCache[number]
	return s, ok
}

// issueChanged reports whether a freshly fetched issue differs from the loaded preview.
func (m Model) issueChanged(fresh ports.Issue) bool {
	if m.detail.Number == 0 {
		return false
	}
	if strings.TrimSpace(m.detail.State) != strings.TrimSpace(fresh.State) {
		return true
	}
	if strings.TrimSpace(m.detail.Body) != strings.TrimSpace(fresh.Body) {
		return true
	}
	return false
}

// mergeIssue updates the loaded detail with the freshly fetched issue fields
// while keeping cached comments/PR data intact.
func mergeIssue(old, fresh ports.Issue) ports.Issue {
	old.Number = fresh.Number
	old.Title = fresh.Title
	old.Body = fresh.Body
	old.State = fresh.State
	old.UpdatedAt = fresh.UpdatedAt
	if fresh.BlockedReason != "" {
		old.BlockedReason = fresh.BlockedReason
	}
	return old
}

// previewDelayCmd schedules a preview fetch after the configured debounce.
func (m Model) previewDelayCmd(it Item, req int) tea.Cmd {
	d := m.opts.PreviewDelay
	if d <= 0 {
		return func() tea.Msg { return previewDelayMsg{req: req, item: it} }
	}
	return tea.Tick(d, func(t time.Time) tea.Msg { return previewDelayMsg{req: req, item: it} })
}

// previewLoadCmd fetches the issue body, comments, and linked PR body.
func (m Model) previewLoadCmd(it Item, req int) tea.Cmd {
	gh := m.opts.Gh
	return func() tea.Msg {
		ctx := context.Background()
		iss, err := gh.View(ctx, it.Number)
		if err != nil {
			return previewMsg{req: req, item: it, err: err}
		}
		if iss.BlockedReason == "" {
			iss.BlockedReason = extractBlockedReason(iss.Body)
		}
		snap := previewSnapshot{issue: iss, prNumber: it.PRNumber}
		if it.PRNumber > 0 {
			body, err := gh.PrBody(ctx, it.PRNumber)
			if err != nil {
				snap.prErr = true
			} else {
				snap.prBody = body
			}
		}
		comments, err := gh.IssueComments(ctx, it.Number)
		if err != nil {
			snap.commentsErr = true
		} else {
			snap.comments = comments
		}
		return previewMsg{req: req, item: it, snapshot: snap}
	}
}

// loadPreviewForCurrent starts an async preview fetch for the selected item.
func (m *Model) loadPreviewForCurrent() tea.Cmd {
	row, ok := m.current()
	if !ok || row.IsHeader() {
		m.detail = ports.Issue{}
		m.detailPrNum, m.detailPrBody, m.detailOffset = 0, "", 0
		m.detailPrErr, m.detailComments, m.detailCommentsErr = false, nil, false
		m.previewLoading = false
		m.previewErr = nil
		m.selectedIssue = 0
		return nil
	}
	it := *row.Item
	m.selectedIssue = it.Number
	m.previewReq++
	req := m.previewReq
	if snap, ok := m.cachedSnapshot(it.Number); ok {
		m.applySnapshot(snap)
		m.previewLoading = false
		m.previewErr = nil
	} else {
		m.detail = ports.Issue{Number: it.Number, Title: it.Title, State: "OPEN"}
		m.detailPrNum, m.detailPrBody, m.detailOffset = 0, "", 0
		m.detailPrErr, m.detailComments, m.detailCommentsErr = false, nil, false
		m.previewLoading = true
		m.previewErr = nil
	}
	return m.previewDelayCmd(it, req)
}

// restoreSelection keeps the cursor on the previously selected issue number after reload.
func (m *Model) restoreSelection(old Row) {
	rows := m.visible()
	if old.IsHeader() {
		// If the old section was empty this was the initial load; focus the first item.
		if len(old.Section.Items) == 0 {
			m.focusFirstItem()
			return
		}
		for i, r := range rows {
			if r.IsHeader() && r.Section.Kind == old.Section.Kind {
				m.cursor = i
				m.selectedIssue = 0
				return
			}
		}
		m.focusFirstItem()
		return
	}
	if old.Item == nil || old.Section == nil {
		m.focusFirstItem()
		return
	}
	n := old.Item.Number
	oldKind := old.Section.Kind

	// Try to find the exact issue by number.
	for i, r := range rows {
		if !r.IsHeader() && r.Item.Number == n {
			m.expandForItem(r.Section.Kind)
			m.cursor = i
			m.selectedIssue = n
			return
		}
	}

	// Not found: fall back to the next item in the same section, then previous, then header.
	oldSectionIdx := -1
	for i, s := range m.sections {
		if s.Kind == oldKind {
			oldSectionIdx = i
			break
		}
	}
	if oldSectionIdx >= 0 && len(m.sections[oldSectionIdx].Items) > 0 {
		items := m.sections[oldSectionIdx].Items
		// Locate the old position; the next or previous surviving item is the fallback.
		oldPos := -1
		for i, it := range items {
			if it.Number == n {
				oldPos = i
				break
			}
		}
		if oldPos < 0 {
			oldPos = 0
		}
		if oldPos < len(items) {
			m.expandForItem(oldKind)
			m.cursor = m.rowIndex(oldKind, items[oldPos].Number)
			m.selectedIssue = items[oldPos].Number
			return
		}
		if oldPos > 0 {
			m.expandForItem(oldKind)
			m.cursor = m.rowIndex(oldKind, items[oldPos-1].Number)
			m.selectedIssue = items[oldPos-1].Number
			return
		}
	}

	// Fallback to the section header.
	for i, r := range rows {
		if r.IsHeader() && r.Section.Kind == oldKind {
			m.cursor = i
			m.selectedIssue = 0
			return
		}
	}
	m.focusFirstItem()
}

// expandForItem ensures a section is visible when an item inside it is selected.
func (m *Model) expandForItem(kind Kind) {
	for i := range m.sections {
		if m.sections[i].Kind == kind {
			m.sections[i].Collapsed = false
			return
		}
	}
}

// rowIndex returns the visible row index for an issue number in a given section.
func (m Model) rowIndex(kind Kind, number int) int {
	for i, r := range m.visible() {
		if !r.IsHeader() && r.Section.Kind == kind && r.Item.Number == number {
			return i
		}
	}
	return 0
}

var blockedReasonHeading = regexp.MustCompile(`(?mi)^#{1,6}\s*(?:Blocked reason|blocked)\s*[:：-]?\s*(.*)$`)

// extractBlockedReason pulls a structured stop reason from the issue body.
func extractBlockedReason(body string) string {
	m := blockedReasonHeading.FindStringSubmatchIndex(body)
	if m == nil {
		return ""
	}
	start := m[2]
	if start < 0 {
		return ""
	}
	nextHeading := regexp.MustCompile(`(?m)^#{1,6}\s`).FindStringIndex(body[start:])
	end := len(body)
	if nextHeading != nil {
		end = start + nextHeading[0]
	}
	return strings.TrimSpace(body[start:end])
}

// previewContent returns the section-specific preview lines and whether more content exists.
func (m Model) previewContent(h int) ([]string, bool) {
	row, ok := m.current()
	if !ok || row.IsHeader() || h <= 0 {
		return nil, false
	}
	kind := row.Item.Kind
	out := []string{}
	more := false
	switch kind {
	case KindNeedsReview:
		out, more = appendSourceBody(out, m.detail.Body, "Issue", h)
	case KindBlocked:
		out, more = m.blockedPreview(out, row.Item, h)
	case KindMerged:
		out, more = m.mergedPreview(out, row.Item, h)
	case KindRunning:
		out, more = m.runningPreview(out, row.Item, h)
	default:
		out, more = appendSourceBody(out, m.detail.Body, "Issue", h)
	}
	return out, more
}

func (m Model) blockedPreview(out []string, it *Item, h int) ([]string, bool) {
	if m.detail.BlockedReason != "" {
		out = append(out, "Blocked reason · Issue")
		lines, more := appendSourceBody(out[1:], m.detail.BlockedReason, "Issue", h-1)
		out = append(out[:1], lines...)
		if more {
			return out, true
		}
	} else if len(m.detailComments) > 0 {
		latest := m.detailComments[len(m.detailComments)-1]
		out = append(out, "Latest comment · Comment")
		body := latest.Body
		if latest.Author != "" {
			body = fmt.Sprintf("@%s: %s", latest.Author, body)
		}
		lines, more := appendSourceBody(out[1:], body, "Comment", h-1)
		out = append(out[:1], lines...)
		if more {
			return out, true
		}
	} else if m.detailCommentsErr {
		out = append(out, "Comments unavailable")
	} else {
		out = append(out, "No comments")
	}
	remaining := h - len(out)
	if remaining > 0 {
		meta := runningMetaLine(it, m.detailPrNum, m.detailPrErr)
		if meta != "" {
			out = append(out, meta)
		}
	}
	// Always show the issue body for blocked items so the original description is visible.
	if remaining := h - len(out); remaining > 0 {
		lines, more := appendSourceBody(out, m.detail.Body, "Issue", h)
		return lines, more
	}
	return out, false
}

func (m Model) mergedPreview(out []string, it *Item, h int) ([]string, bool) {
	remaining := h
	if m.detailPrNum > 0 {
		if m.detailPrErr {
			out = append(out, fmt.Sprintf("PR details unavailable (#%d)", m.detailPrNum))
			remaining--
		} else if m.detailPrBody != "" {
			source := fmt.Sprintf("PR #%d", m.detailPrNum)
			lines, more := appendSourceBody(out, m.detailPrBody, source, h)
			out = lines
			if more {
				return out, true
			}
			remaining = h - len(out)
		} else {
			out = append(out, fmt.Sprintf("PR #%d (no body)", m.detailPrNum))
			remaining--
		}
	} else {
		out = append(out, "No linked PR")
		remaining--
	}
	if remaining > 0 {
		lines, more := appendSourceBody(out, m.detail.Body, "Issue", h)
		out = lines
		if more {
			return out, true
		}
	}
	return out, false
}

func (m Model) runningPreview(out []string, it *Item, h int) ([]string, bool) {
	meta := runningMetaLine(it, m.detailPrNum, m.detailPrErr)
	if meta != "" {
		out = append(out, meta)
	}
	if len(out) >= h {
		return out, true
	}
	return appendSourceBody(out, m.detail.Body, "Issue", h)
}

func runningMetaLine(it *Item, prNum int, prErr bool) string {
	parts := []string{}
	if it.Agent != "" {
		parts = append(parts, fmt.Sprintf("Agent: %s", Sanitize(it.Agent)))
	}
	if it.Attempts > 0 {
		parts = append(parts, fmt.Sprintf("×%d", it.Attempts))
	}
	if prNum > 0 {
		if prErr {
			parts = append(parts, fmt.Sprintf("PR details unavailable (#%d)", prNum))
		} else {
			parts = append(parts, fmt.Sprintf("PR #%d", prNum))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "   ")
}

// appendSourceBody splits body into paragraphs, attributes headings to source,
// and returns up to budget lines. more is true when content was truncated.
func appendSourceBody(out []string, body, source string, budget int) ([]string, bool) {
	if budget <= 0 {
		return out, body != ""
	}
	body = Sanitize(strings.ReplaceAll(body, "\r\n", "\n"))
	var more bool
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimRight(line, " \t")
		if line == "" {
			continue
		}
		if budget <= 0 {
			more = true
			break
		}
		if h := headingText(line); h != "" {
			out = append(out, fmt.Sprintf("%s · %s", h, source))
		} else {
			out = append(out, line)
		}
		budget--
	}
	return out, more
}

var headingPattern = regexp.MustCompile(`^(#{1,6})\s*(.+)$`)

func headingText(line string) string {
	m := headingPattern.FindStringSubmatch(strings.TrimSpace(line))
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[2])
}

// detailContent builds the full-screen scrollable content for the selected issue.
// Full-screen content preserves raw headings for readability; source attribution
// is shown in the compact preview pane instead.
func (m Model) detailContent() string {
	if m.detail.Number == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "#%d %s  [%s]\n\n", m.detail.Number, m.detail.Title, m.detail.State)
	if m.detail.BlockedReason != "" {
		b.WriteString("Blocked reason · Issue\n")
		b.WriteString(m.detail.BlockedReason)
		b.WriteString("\n\n")
	}
	if m.detail.Body != "" {
		b.WriteString(m.detail.Body)
		b.WriteString("\n\n")
	}
	if m.detailPrNum > 0 {
		if m.detailPrErr {
			fmt.Fprintf(&b, "## PR body · PR details unavailable (#%d)\n\n", m.detailPrNum)
		} else if m.detailPrBody != "" {
			fmt.Fprintf(&b, "## PR body · PR #%d\n", m.detailPrNum)
			b.WriteString(m.detailPrBody)
			b.WriteString("\n\n")
		} else {
			fmt.Fprintf(&b, "## PR body · PR #%d (no body)\n\n", m.detailPrNum)
		}
	} else {
		b.WriteString("No linked PR\n\n")
	}
	if len(m.detailComments) > 0 {
		b.WriteString("Comments\n")
		for _, c := range m.detailComments {
			prefix := "Comment"
			if c.Author != "" {
				prefix = fmt.Sprintf("Comment from @%s", c.Author)
			}
			if c.CreatedAt != "" {
				prefix += " · " + c.CreatedAt
			}
			b.WriteString(prefix + "\n")
			b.WriteString(c.Body)
			b.WriteString("\n\n")
		}
	} else if m.detailCommentsErr {
		b.WriteString("Comments unavailable\n\n")
	}
	if it := m.currentItem(); it != nil && (it.Kind == KindRunning || it.Kind == KindBlocked) {
		meta := runningMetaLine(it, m.detailPrNum, m.detailPrErr)
		if meta != "" {
			b.WriteString("Running info\n")
			b.WriteString(meta)
			b.WriteString("\n\n")
		}
	}
	return Sanitize(strings.TrimRight(b.String(), "\n"))
}

func (m Model) currentItem() *Item {
	r, ok := m.current()
	if !ok || r.IsHeader() {
		return nil
	}
	return r.Item
}

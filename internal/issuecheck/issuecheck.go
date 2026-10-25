// Package issuecheck validates that an issue body carries the sections lead
// needs before an agent receives it (issue #254).
//
// `lead new` and `lead ready` both call Check so an issue cannot enter the
// queue with missing acceptance criteria, scope, or risk classification.
package issuecheck

import (
	"regexp"
	"strings"
)

// Required section headings. They are level-2 headings so the user can add
// level-3 subsections without ending the section.
const (
	AcceptanceHeading = "## 受入条件"
	OutOfScopeHeading = "## スコープ外"
)

// Accepted aliases. Bodies written in English (or by an AI draft) must not be
// rejected just because the heading language differs, and `**対象外**`-style
// bold labels count as the out-of-scope section (issue #264).
var (
	AcceptanceHeadings = []string{AcceptanceHeading, "## Acceptance criteria"}
	OutOfScopeHeadings = []string{OutOfScopeHeading, "## Scope", "## Out of scope", "## 対象外"}
	OutOfScopeMarkers  = []string{"**対象外**", "**スコープ外**", "**Out of scope**"}
)

// RiskLabels are the accepted risk classifications (docs/config.md).
var RiskLabels = []string{"risk:a", "risk:b"}

// Template is the body skeleton `lead new` opens in $EDITOR.
const Template = `# <title>

## 背景

<!-- なぜこの Issue が必要か -->

## 受入条件

- [ ] 実行可能な検証を含める（例: ` + "`./test/run-tests.sh`" + ` が pass する）

## スコープ外

- <!-- この Issue でやらないこと -->
`

var (
	heading = regexp.MustCompile(`^#{1,6}[ \t]+`)
	// checklist matches a Markdown task item: "- [ ] ...".
	checklist = regexp.MustCompile(`^[ \t]*-[ \t]+\[[ \t]*\][ \t]`)
	// codeSpan matches a backtick-quoted command or path.
	codeSpan = regexp.MustCompile("`[^`]+`")
	// testPath matches a repository test path: test/, *_test.go, *.spec.*.
	testPath = regexp.MustCompile(`(^|[^\w])(\./)?test/|[\w./-]+_test\.go\b|[\w./-]+\.spec\.[A-Za-z0-9]+`)
)

// RiskLabel returns the label for risk "a" or "b".
func RiskLabel(risk string) string { return "risk:" + risk }

// Title returns the text of the first Markdown heading, or "".
func Title(body string) string {
	for _, line := range strings.Split(body, "\n") {
		s := strings.TrimSpace(line)
		if !heading.MatchString(s) {
			continue
		}
		if t := strings.TrimSpace(strings.TrimLeft(s, "#")); t != "" {
			return t
		}
	}
	return ""
}

// Check returns the missing requirements for body and labels. An empty
// result means the issue is acceptable.
func Check(body string, labels []string) []string {
	var missing []string

	acceptance, ok := sectionAny(body, AcceptanceHeadings)
	if !ok {
		missing = append(missing, "`"+AcceptanceHeading+"` または `## Acceptance criteria` セクションがありません")
	} else {
		items := checkItems(acceptance)
		switch {
		case len(items) == 0:
			missing = append(missing, "`"+AcceptanceHeading+"` に `- [ ]` のチェック項目がありません")
		case !anyExecutable(items):
			missing = append(missing, "受入条件に実行可能な検証（バッククォートのコマンド、または test/ などのテストパス）がありません")
		}
	}

	if _, ok := sectionAny(body, OutOfScopeHeadings); !ok && !hasAnyMarker(body, OutOfScopeMarkers) {
		missing = append(missing, "`"+OutOfScopeHeading+"` / `## Scope` / `## Out of scope`（または `**対象外**`）がありません")
	}

	if riskLabel(labels) == "" {
		missing = append(missing, "リスク分類 (`risk:a` / `risk:b`) がありません")
	}

	return missing
}

// Message renders missing items as the human-facing bullet list.
func Message(missing []string) string {
	var b strings.Builder
	b.WriteString("Issue 本文が必須項目を満たしていません:\n")
	for _, m := range missing {
		b.WriteString("  - ")
		b.WriteString(m)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// RiskFromLabels returns "risk:a"/"risk:b" from labels, or "".
func RiskFromLabels(labels []string) string {
	return riskLabel(labels)
}

func riskLabel(labels []string) string {
	for _, want := range RiskLabels {
		for _, l := range labels {
			if l == want {
				return l
			}
		}
	}
	return ""
}

// sectionAny returns the lines under the first matching heading up to the
// next level-1/2 heading. Heading comparison is case-insensitive.
func sectionAny(body string, headings []string) (string, bool) {
	lines := strings.Split(body, "\n")
	start := -1
	for i, line := range lines {
		s := strings.TrimSpace(line)
		for _, h := range headings {
			if strings.EqualFold(s, h) {
				start = i + 1
				break
			}
		}
		if start >= 0 {
			break
		}
	}
	if start < 0 {
		return "", false
	}
	var b strings.Builder
	for _, line := range lines[start:] {
		s := strings.TrimSpace(line)
		if strings.HasPrefix(s, "# ") || strings.HasPrefix(s, "## ") {
			break
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String(), true
}

// hasAnyMarker reports whether any line is exactly one of markers
// (case-insensitive), e.g. a bold `**対象外**` inside a Scope section.
func hasAnyMarker(body string, markers []string) bool {
	for _, line := range strings.Split(body, "\n") {
		s := strings.TrimSpace(line)
		for _, m := range markers {
			if strings.EqualFold(s, m) {
				return true
			}
		}
	}
	return false
}

func checkItems(section string) []string {
	var items []string
	for _, line := range strings.Split(section, "\n") {
		if checklist.MatchString(line) {
			items = append(items, line)
		}
	}
	return items
}

func anyExecutable(items []string) bool {
	for _, item := range items {
		if codeSpan.MatchString(item) || testPath.MatchString(item) {
			return true
		}
	}
	return false
}

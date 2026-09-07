// Package prompter renders agent-facing prompts (issue #45).
// The decomposition template follows docs/rfc-25-workflow-flexibility.md §8;
// title rules follow the #15 guideline (256 chars, 要点/対象/意図).
package prompter

import (
	"fmt"
	"strings"
	"text/template"
	"unicode/utf8"
)

// DecomposeInput feeds the issue-split prompt template.
type DecomposeInput struct {
	Repository      string
	IssueNumber     int
	IssueTitle      string
	IssueBody       string
	RepositoryRules string
}

// decomposeTemplate is RFC §8.2 (plan-only: no code, child-issue drafts).
const decomposeTemplate = `あなたはGitHub Issueの分解担当です。

## 親Issue
- Repository: {{.Repository}}
- Number: #{{.IssueNumber}}
- Title: {{.IssueTitle}}
- Body:
{{.IssueBody}}

## リポジトリ規約
{{.RepositoryRules}}

## 目的
親Issueを、エージェントがTDDで1回の作業として完了できる子Issueへ分解してください。

## 制約
1. 各子Issueは、目的・対象範囲・受入条件・検証方法が1つのまとまりになっていること。
2. 受入条件は実行結果、生成物、終了状態など検証可能な形で書くこと。
3. 実装Issueでは、最初に追加する失敗テスト（Red）を明記すること。
4. 調査・文書作成など、PRが不要な作業は明示すること。
5. 子Issue間の依存関係を示し、並列実行できるものとできないものを分けること。
6. 親Issueの要件を勝手に追加・削除しないこと。不明点は「要確認」として分離すること。
7. 1つの子Issueに複数の独立した目的を詰め込まないこと。

## 出力形式
各候補を次の形式で出力してください。

### 子Issue案: <具体的なタイトル>
- Mode: implement | research | docs
- Purpose: <このIssueで完了させること>
- Scope: <変更対象と対象外>
- Depends on: <子Issue番号またはnone>
- Parallelizable: yes | no
- Acceptance criteria:
  - <実行可能または成果物で検証できる条件>
- TDD first step: <最初に追加してRedを確認するテスト。該当しない場合は理由>
- Expected output: PR | Issue comment | document | none
- Open questions:
  - <判断が必要な点。なければnone>

最後に、分解漏れ、重複、親Issueに残すべき受入条件を列挙してください。
`

// Render fills the decomposition template. Repository, IssueNumber, and
// IssueTitle are required; RepositoryRules defaults to a TDD pointer.
func Render(in DecomposeInput) (string, error) {
	if strings.TrimSpace(in.Repository) == "" {
		return "", fmt.Errorf("prompter: repository is required")
	}
	if in.IssueNumber <= 0 {
		return "", fmt.Errorf("prompter: issue number is required")
	}
	if strings.TrimSpace(in.IssueTitle) == "" {
		return "", fmt.Errorf("prompter: issue title is required")
	}
	if strings.TrimSpace(in.RepositoryRules) == "" {
		in.RepositoryRules = "TDD必須。テストなき実装・PRは受け付けない。"
	}
	tpl, err := template.New("decompose").Parse(decomposeTemplate)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	if err := tpl.Execute(&sb, in); err != nil {
		return "", err
	}
	return sb.String(), nil
}

// MaxTitleRunes is the mechanical title limit (256文字以内).
const MaxTitleRunes = 256

// LintTitle checks the mechanical title rules, returning human-readable
// problems (empty = fine). Content rules (要点/対象/意図) are documented
// in docs/prompts.md and checked by reviewers, not machines.
func LintTitle(title string) []string {
	var problems []string
	if strings.TrimSpace(title) == "" {
		return []string{"title is empty: state 要点 (what), 対象 (scope), 意図 (intent)"}
	}
	if n := utf8.RuneCountInString(title); n > MaxTitleRunes {
		problems = append(problems, fmt.Sprintf("title is %d chars: keep within 256 (要点/対象/意図を絞る)", n))
	}
	if strings.Contains(title, "\n") {
		problems = append(problems, "title spans lines: keep it a single line")
	}
	if strings.HasSuffix(strings.TrimSpace(title), ".") || strings.HasSuffix(strings.TrimSpace(title), "。") {
		problems = append(problems, "title ends with a period: drop trailing punctuation")
	}
	return problems
}

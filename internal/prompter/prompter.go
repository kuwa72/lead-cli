// Package prompter renders agent-facing prompts (issue #45).
// The decomposition template follows docs/rfc-25-workflow-flexibility.md §8;
// title rules follow the #15 guideline (256 chars, 要点/対象/意図).
package prompter

import (
	"fmt"
	"os"
	"path/filepath"
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

// IssuePromptInput holds the variables exposed to workflow prompt templates.
type IssuePromptInput struct {
	Number int
	Title  string
	Body   string
}

const defaultImplementPromptTemplate = `あなたはコーディングエージェントです。以下のGitHub IssueをTDD（テスト駆動開発）で実装してください。

## 対象Issue
- Number: #{{.Number}}
- Title: {{.Title}}
- Body:
{{.Body}}

## 実装手順
1. test/ に失敗するテストを追加し、Redを確認する。
2. 最小限の実装を行い、Greenを確認する。
3. リファクタリングを行い、テストがパスし続けることを確認する。
4. 全テストをパスさせ、PRを作成する。
`

const defaultReviewPromptTemplate = `あなたはIssueレビュー・品質改善エージェントです。以下のGitHub Issueの内容を精査し、改善・LGTM判定を行ってください。

## レビュー対象Issue
- Number: #{{.Number}}
- Title: {{.Title}}
- Body:
{{.Body}}

## レビュー観点
1. 目的・背景が明確か
2. 受入条件（Acceptance Criteria）が検証可能な形で書かれているか
3. テスト方針が示されているか
4. 適切なスコープに収まっているか

## 判定・操作
- 必要に応じて Issue 本文をより明確に改善してください。
- レビューがOKであれば、以下を実行して lgtm ラベルを付与し、LGTM コメントを残してください:
  lead lgtm {{.Number}}
- 追加確認が必要な場合は Issue にコメントを残し、needs-review ラベルを維持してください。
`

// RenderWorkflowPrompt renders the agent prompt for a workflow given its mode (implement|review),
// issue details, and an optional repository root directory for prompt template overrides.
func RenderWorkflowPrompt(mode string, number int, title, body, repoDir string) (string, error) {
	if mode == "" {
		mode = "implement"
	}
	tplText := ""
	// 1. Try <repoDir>/prompts/<mode>.md
	if repoDir != "" {
		p := filepath.Join(repoDir, "prompts", mode+".md")
		if data, err := os.ReadFile(p); err == nil {
			tplText = string(data)
		}
	}
	// 2. Try ~/.config/lead/prompts/<mode>.md
	if tplText == "" {
		if home, err := os.UserHomeDir(); err == nil {
			p := filepath.Join(home, ".config", "lead", "prompts", mode+".md")
			if data, err := os.ReadFile(p); err == nil {
				tplText = string(data)
			}
		}
	}
	// 3. Fallback to built-in templates
	if tplText == "" {
		switch mode {
		case "review":
			tplText = defaultReviewPromptTemplate
		default:
			tplText = defaultImplementPromptTemplate
		}
	}

	tpl, err := template.New(mode).Parse(tplText)
	if err != nil {
		return "", fmt.Errorf("prompter: parse template for %q: %w", mode, err)
	}

	input := IssuePromptInput{
		Number: number,
		Title:  title,
		Body:   body,
	}
	var sb strings.Builder
	if err := tpl.Execute(&sb, input); err != nil {
		return "", fmt.Errorf("prompter: execute template: %w", err)
	}
	return sb.String(), nil
}


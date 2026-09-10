# エージェント向けプロンプト指針 (issue #45)

RFC `docs/rfc-25-workflow-flexibility.md` §8 と旧 #15 タイトル指針の Go 版引継ぎ。
テンプレート本体は `internal/prompter`（単体・golden テスト付き）。

## 1. Issue 分解プロンプト (`prompter.Render`)

- 用途: 曖昧な親 Issue を、エージェントが TDD で1回で完了できる子 Issue 案へ分解する。
  コード生成ではなく計画生成に使う（`lead issue plan` 相当の将来機能の雛形）。
- 入力: Repository・Issue 番号・タイトル・本文・リポジトリ規約。
  規約省略時は TDD 既定文が入る。
- 出力形式（固定）: 子 Issue 案ごとに Mode / Purpose / Scope / Depends on /
  Parallelizable / Acceptance criteria / TDD first step / Expected output /
  Open questions。末尾に分解漏れ・重複・親に残す条件を列挙する。
- 制約の要点: 受入条件は実行結果・生成物・終了状態で検証可能にすること、
  Red テストの明示、親要件の勝手な加除の禁止（不明点は「要確認」に分離）。

## 2. タイトル指針（256文字以内・要点/対象/意図）

- **要点 (what)**: 何をするか1文で。動詞始まりを推奨
  （例: `impl(go):`, `fix(cli):`, `docs:`）。
- **対象 (scope)**: どこに効くか。モジュール・コマンド・ファイル範囲を書く。
- **意図 (intent)**: なぜ・何のためか。受入条件の方向が読めること。
- 機械規則（`prompter.LintTitle` が検査）:
  - 256文字（rune）以内、空不可、単一行、文末ピリオドなし。
- 人手規則（レビューで確認）: 上の3点が読み取れるか、汎用語
  （`対応`, `修正`, `改善` だけ）で終わっていないか。

## 2.1 仕様 AI プロンプト (`spec.BuildPrompt`, issue #94)

- 用途: `lead say "<一言>"` が仕様エージェントをヘッドレス起動するときのプロンプト
  （`docs/rfc-inbox-ux.md` §8）。テンプレート本体は `internal/spec`（golden テスト付き）。
- 入力: 一言、リポジトリ（origin URL）、`AGENTS.md` の内容（なければ TDD 既定文）、
  `--follow-up N` なら元 Issue、`--redraft N` なら対象 Issue とコメント一覧。
- 本文形式: §1 と同じ見出し（Mode / Purpose / Scope / Acceptance criteria /
  TDD first step / Open questions）。タイトルは §2 の規則を明記し、返却後に
  `prompter.LintTitle` で機械検査する（違反は起票せず終了）。
- 出力形式: JSON 配列 `[{"title","body"}]` のみ（`--redraft` はオブジェクト 1 つ）。
  コードフェンスや前置き文が混ざっても `spec.ParseDrafts` が JSON 部分だけを取り出す。
- 起票: `gh issue create --title --body --label needs-review`。複数件は本文末尾に
  相互参照を追記する。`--follow-up` は本文に `#N` がなければ `元 Issue: #N` を補う。
  `--redraft` は `gh issue edit --title --body-file` の後、変更点の要約をコメントする。
- エージェント選択: `--agent` → `$LEAD_SPEC_AGENT` → 既定（`agy`）。
  `agent.HeadlessArgv` の対応表にある CLI のみ。出力ログは状態ディレクトリの `logs/say-*.log`。

## 2.2 ワークフロープロンプト (`prompter.RenderWorkflowPromptWithOptions`, issue #84, #89)

- 用途: `lead work` / `lead run` でエージェントを対話起動する際のプロンプト。
- モード:
  - `implement`: TDD での実装（Red/Green/Refactor、テスト全パス、PR 作成）。リポジトリに `AGENTS.md` があれば自動でプロジェクト規約ブロックが注入される。
  - `review`: Issue 本文・受入条件の精緻化、レビュー、OK 時の `lgtm` ラベル付与と LGTM コメント。
- テンプレート選択と解決順序:
  - `--prompt-template <name>` フラグでテンプレート名を明示指定可能（省略時は `--mode` 名を使用）。
  - 解決順序:
    1. リポジトリ内 `<repoDir>/prompts/<name>.md`
    2. ユーザー設定 `~/.config/lead/prompts/<name>.md`
    3. 組み込みテンプレート（`implement`, `review`、未定義名は `implement` にフォールバック）
  - テンプレート変数は Go の `text/template` 構文:
    - `{{.Number}}`: Issue 番号
    - `{{.Title}}`: Issue タイトル
    - `{{.Body}}`: Issue 本文
    - `{{.Branch}}`: 作業ブランチ名
    - `{{.Mode}}`: 実行モード
    - `{{.AgentMode}}`: エージェント実行モード (`interactive`, `batch`, `dangerous`)
    - `{{.Rules}}`: リポジトリの `AGENTS.md` の内容（未設定時は空）

## 2.3 ラベル運用とピッカー分岐 (issue #89)

- ラベル状態:
  - `lgtm`: 着手可能な Issue。`lead work` の通常ピッカーに表示される。
  - `needs-review` / ラベルなし: レビュー待ち・未レビュー Issue。`lead work --mode review` のピッカーに表示される。
- 手動ラベル操作:
  - `lead lgtm <number>`: `lgtm` ラベルを付与し、`LGTM` コメントを投稿する。
  - `lead unlgtm <number>`: `lgtm` ラベルを削除する。

## 3. 振る舞い確認手順（静的 grep 禁止の代替）

テンプレート変更時は次の順で確認する。

1. `UPDATE_GOLDEN=1 go test ./internal/prompter/` で golden を再生成し、
   `git diff internal/prompter/testdata/` で差分を目視レビューする
   （意図しない文言変化がないか）。
2. `go test ./internal/prompter/ -v` が全件パスすること。
3. 実 Issue での目視確認（任意・推奨）:
   - `gh issue view <番号> --json number,title,body` を取得する。
   - 取得値を `DecomposeInput` に入れて描画する小片プログラム
     （例: `/tmp` 下の一時 `go run`）でプロンプト文面を生成する。
   - 生成文面をエージェントに投入し、出力が次を満たすか確認する:
     子 Issue ごとに Mode・Scope・検証可能な受入条件・TDD first step がある、
     親要件の捏造がない、要確認事項が分離されている。
   - タイトル案は `LintTitle` の機械規則＋§2 の人手規則で確認する。

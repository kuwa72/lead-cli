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

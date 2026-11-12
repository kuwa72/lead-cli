# RFC: LLM エージェント主導の 0→1 立ち上げ設計 (`lead bootstrap` / postmerge スモーク / `WaitChecks`)

- **Issue**: [#324](https://github.com/kuwa72/lead-cli/issues/324)
- **親トラッキングIssue**: [#323](https://github.com/kuwa72/lead-cli/issues/323)
- **インプット**: [#16 提供形態・UXモデル](rfc-16-product-ux.md) / [#25 ワークフロー柔軟性](rfc-25-workflow-flexibility.md) / `docs/testing.md`
- **ステータス**: Proposed
- **更新日**: 2026-11-13
- **実装 Issue**: #325（postmerge スモークの LLM エージェント化）/ #326（ワークフロー不在時の `WaitChecks`）/ #327（`lead bootstrap`）

---

## 1. 目的

`lead` は現状、Git リポジトリ・GitHub リモート・CI ワークフローが整った既存プロジェクトを前提に動作する。本 RFC は、空ディレクトリから LLM エージェント主導で 0→1 のアプリケーション開発を立ち上げるための設計を定める。対象は次の 3 点である。

1. **初期化導線**: `lead bootstrap` サブコマンドと、未初期化ディレクトリでの起動時導線。
2. **postmerge スモーク**: `internal/finish/postmerge.go` の `DefaultSmoke`（`go build ./...` + `bash test/test_cli_skeleton.sh` 固定実行）を LLM エージェント検証へ置き換える設計。
3. **CI 待機**: `.github/workflows` が存在しないリポジトリで `WaitChecks` が空チェック一覧をタイムアウトまで待ち続ける問題の解消。

---

## 2. 現状の問題

### 2.1 未初期化ディレクトリでは何も始まらない

`lead` の各コマンドは `git rev-parse` 相当のリポジトリ検出と `owner/repo` スラッグ解決を前提とする（`internal/cli/cli.go` の `repoSlug` / `repoRoot`）。Git リポジトリでない、または `origin` が GitHub リモートでないディレクトリでは、`lead run`・`lead dispatch` 等はリポジトリ情報なしで失敗するか何も起きず、ユーザーは `git init` → リモート作成 → 初回コミット → `lead enable` → ラベル作成 → 初期 Issue 起票を手作業で行う必要がある。

### 2.2 postmerge スモークが `lead-cli` 自身にハードコードされている

`DefaultSmoke` は `go build ./...` と `bash test/test_cli_skeleton.sh` を固定で実行する（`internal/finish/postmerge.go`）。これは `lead-cli` 自身のリポジトリでのみ成立し、他言語・新規プロジェクトではマージ直後のスモークが必ず失敗し、revert + incident Issue のフロー（issue #275）に入ってしまう。0→1 で言語もビルド系も未定の段階では、固定コマンドのスモークは成立しない。

### 2.3 ワークフロー不在時に `WaitChecks` が最大 30 分ブロックする

`WaitChecks`（`internal/finish/finish.go`）は `gh pr checks` のチェック一覧が空の間ポーリングを続け、タイムアウト（既定 30 分）まで待機する。チェック未登録の PR ではこれが正しい挙動だが、`.github/workflows/*.yml` がまだ存在しないリポジトリではチェックは**永遠に登録されない**ため、初期開発の `lead finish --merge` が必ずタイムアウトする。

---

## 3. 設計決定

### 3.1 `lead bootstrap`: 0→1 立ち上げのオーケストレーション

新規サブコマンド `lead bootstrap` を追加する。目的は「空ディレクトリ → `lead` が Issue 駆動開発を開始できる状態」までの一連の手順を、LLM エージェントへの委譲を前提にオーケストレーションすることである。

#### 3.1.1 スコープ

`lead bootstrap` が面倒を見る範囲:

1. カレントディレクトリの状態判定（Git リポジトリか / `origin` が GitHub リモートか / `lead enable` 済みか）。
2. GitHub リポジトリの作成と `origin` 設定。**リモート作成 API の再実装はしない**（親 Issue #323 のスコープ外）。`gh repo create` の実行または対話 LLM エージェントへの委譲で行う。
3. プロジェクトの初期スキャフォールド。言語・フレームワーク選択はユーザー（またはエージェントとの対話）に委ね、`lead` は固定テンプレートを持たない。
4. `lead enable`（lead-flow スキル・AGENTS.md ブロック・ラベル作成）の実行。
5. 初期 Issue の起票（プロジェクトのビジョン・最初のマイルストーンを `gh issue create`）。

#### 3.1.2 エージェント委譲の原則

- `lead` は「手順のオーケストレーションと状態確認」に徹し、リポジトリ作成・スキャフォールド・CI 設計といった**判断を要する作業は設定済みの LLM コーディングエージェント（agy / devin / opencode 等、`lead` のエージェント解決機構を再利用）に委譲する**。
- エージェントへの指示は、既存のプロンプト展開機構（`internal/ports` の AgentLauncher / `docs/prompts.md` の方針）に倣い、実行前にユーザーが確認・編集できる形で提示する（Human-in-the-loop、rfc-16 §3.2 と同じ原則）。
- 非対話環境（stdin が TTY でない）では、確認プロンプトを出さずに安全な既定値で進めるか、進められない段階で明示的に停止する。`lead enable` の「非対話 stdin での安全な no-op」方針（`internal/projinit`）と同じ規約に従う。

#### 3.1.3 冪等性

`lead bootstrap` は途中失敗からの再実行を前提とする。各段階は「既に満たされていればスキップ」で判定し（例: `origin` が既にあればリモート作成を飛ばす）、`git init`・`lead enable` のような既存の冪等コマンドをそのまま利用する。中間状態は `workflows.json`（`internal/state`）ではなく、リポジトリ自体の状態から都度導出する。bootstrap は「workflow が 1 件も無い段階」で動くため、workflow レコードには依存しない。

#### 3.1.4 対話未初期化ディレクトリ起動時の導線

`lead` を Git リポジトリ外、または GitHub リモートを持たないリポジトリ内で起動した場合（少なくとも `lead run` / `lead dispatch` / 引数なし起動）、エラーで即死せず次を提示する:

1. 現在の状態の要約（例: 「git リポジトリではありません」「origin が GitHub リモートではありません」）。
2. `lead bootstrap` の実行を促すメッセージと、実行コマンドの提示。
3. ユーザーが `lead bootstrap` を選択した場合はそのまま bootstrap フローへ遷移する。

この導線は「doctor で止まる」現状（親 Issue #323 §背景 1）を解消するものであり、`lead doctor` の診断ロジック（`internal/doctor`）は再利用するが、doctor 自体を bootstrap の前段として呼ぶかどうかは #327 の実装時に最小構成で決める。

### 3.2 postmerge スモークの LLM エージェント化

#### 3.2.1 現行の問題の整理

`PostMerge`（`internal/finish/postmerge.go`）の「マージコミットを detached worktree に展開 → スモーク実行 → 失敗時 revert + incident」という骨格は正しい。問題はスモークの中身（`DefaultSmoke`）が `lead-cli` 固有の固定コマンドである点だけである。**骨格は維持し、スモーク実行部分だけを差し替える**。

#### 3.2.2 設計: スモークをエージェントに委譲する

- `PostMergeOptions.Smoke` は既に差し替え可能（テスト用フック）なので、実装上は `DefaultSmoke` の中身を「LLM エージェントによる検証呼び出し」に置き換える形が最小差分である。
- スモーク用エージェントは `lead` のエージェント解決機構（設定 > `--agent` > 既定の優先順位、inbox 設定の `agents.impl` 等を参照）を再利用する。postmerge 専用のエージェント設定は**初版では設けない**（YAGNI。必要になれば `inbox.Config` に `agents.smoke` を追加する拡張点を残す）。
- エージェントへのプロンプトは「この worktree（マージコミットの detached HEAD）でプロジェクトをビルドし、動作確認のスモークを実行して、結果を報告せよ」という指示とする。具体的なビルド・テスト・起動コマンドの決定はエージェントに委ねる。`lead` はプロジェクト固有のコマンドを知らない前提を維持する。
- エージェントの終了ステータス（または明示的な合否出力）をスモークの成否とする。タイムアウトは既存の `DefaultPostMergeTimeout`（10 分）をそのまま使う。スモークがハングした場合は context キャンセルで打ち切る。

#### 3.2.3 失敗時の扱い

現行どおり: スモーク失敗 → revert コミット → `revert/pr-*` ブランチ push → revert PR 作成 + auto-merge → `incident` ラベル付き Issue 起票 → 対象 Issue を `blocked` に遷移。**この revert/incident フローは変更しない**。エージェント委譲によって「スモーク自体が実行できなかった」（エージェント未インストール・起動失敗）ケースと「スモークを実行して失敗した」ケースを区別する必要がある:

- エージェント起動失敗・環境エラー → スモーク未実行として扱い、**revert しない**。`PostMergeResult.SmokeFailed = false` のセットアップ系エラーとして返し、`lead finish` は merge 済み・スモーク未検証として記録する（incident は起票しない）。
- スモーク実行後の失敗（エージェントが「ビルド不可」「テスト失敗」と判定）→ 従来どおり `SmokeFailed = true` → revert + incident。

この区別は `PostMergeResult` に「スモーク未実行」状態を追加するか、`SmokeFailed` の意味を「実行して失敗」に限定してセットアップエラーと分けるかで実装する（#325 で決定。後者が既存構造に近い）。

#### 3.2.4 検証手段の記録（オプション）

エージェントが毎回ゼロから「どうビルドするか」を判断するのは非効率で、かつ誤判定の余地がある。リポジトリ側に検証手順（例: `AGENTS.md` の「検証」節や `lead` 設定ファイルの `smoke` フィールド）を書く文化を `lead enable` の生成物に含めることを推奨するが、**必須にはしない**。プロンプトには「リポジトリ内のドキュメントに検証手順があればそれに従え」という指示を含める。

### 3.3 `WaitChecks` のワークフロー有無判定

#### 3.3.1 判定方法

`WaitChecks` が空チェック一覧を待ち続ける問題に対し、「このリポジトリに CI ワークフローが存在するか」を一度だけ判定して分岐する。

- 判定は **`gh` のワークフロー一覧 API**（`gh workflow list` 相当、`ports.GhClient` にワークフロー列挙メソッドを追加）で行う。`.github/workflows` ディレクトリのファイル列挙ではなく API を使うのは、`ports.GhClient` の抽象（gh アダプタ）に揃えるため。
- ワークフローが **0 件** → チェックは永遠に登録されないので `WaitChecks` は即座に成功（チェック合格扱い）を返す。ただし「ワークフローはあるがこの PR では起動しなかった（paths フィルタ等）」ケースと区別する必要があるため、判定は「リポジトリにワークフローが 1 件以上あるか」のみとし、ワークフロー有り・チェック 0 件は従来どおり待機する（paths フィルタで意図的にスキップされた場合も、CI 未起動の異常と区別がつかないため安全側に倒す）。
- ワークフロー列挙自体が失敗した場合 → 従来どおりチェック待機にフォールバックする（判定不能を「ワークフロー無し」と誤認しない）。

#### 3.3.2 配置

判定ロジックは `WaitChecks` 内部ではなく呼び出し側（`finish.Run`）に置く案と、`WaitChecks` にワークフロー有無を渡す案がある。`WaitChecks` は `cli.go` の `ci-wait` 相当コマンドからも直接呼ばれている（`internal/cli/cli.go:2625`）ため、**両方の呼び出し経路で同じ判定が効くよう、判定は `WaitChecks` の内部（ポーリング開始前の 1 回）に組み込む**方が漏れがない。実装詳細（`ports.GhClient` へのメソッド追加か、別ポートか）は #326 で決める。

#### 3.3.3 「スキップは合格ではない」原則との整合

issue #273 で「skipped check は pass ではない」と定めた原則は維持する。本設計が扱うのは「チェックが**存在しない**（ワークフロー未整備）」ケースであり、「チェックが skip 判定された」ケースではない。ワークフロー 0 件のリポジトリでは skip 判定自体が起きないため矛盾しない。結果として `ChecksPassed = true` で返るが、これは「検証済み」ではなく「検証対象が存在しない」ことを意味する。`finish` のメッセージには「CI ワークフローが存在しないためチェック待機をスキップした」旨を含め、後から「CI が通った」と誤読されないようにする。

---

## 4. テスト方針

`docs/testing.md` と AGENTS.md のテスト品質ルール（振る舞いテスト必須・grep テスト禁止・ダミー PATH 注入）に従う。

### 4.1 #325（postmerge スモークの LLM エージェント化）

- **ユニット**: `internal/finish` の既存テスト（`postmerge_test.go`）と同じく、`PostMergeOptions.Smoke` / `Git` / `GhClient` を Fake・ダミーで差し替え、revert・incident・状態遷移の振る舞いを検証する。
- **新規分岐**: 「スモーク未実行（エージェント起動失敗）」と「スモーク実行して失敗」を区別するテストを追加し、前者で revert も incident も起きないことをアサートする。
- **エージェント呼び出し**: 実際のエージェント CLI は呼ばず、`testutil.InstallDummy` でダミーエージェントを PATH に置き、渡された引数・プロンプト内容・終了ステータスの伝播を検証する。
- **実機確認**: PR 作成前に `lead-cli` 自身のリポジトリで `lead finish` の postmerge が従来どおり動くこと（または同等の手動スモーク）を確認する。

### 4.2 #326（`WaitChecks` ワークフロー有無判定）

- `finish_test.go` の既存 `WaitChecks` テストと同じく、`ports.GhClient` の Fake で「ワークフロー 0 件 → 即成功」「ワークフロー有り・チェック 0 件 → タイムアウトまで待機」「ワークフロー列挙エラー → 従来どおり待機」の 3 分岐を検証する。
- タイムアウト経路は既存テストと同じく `sleep` / `interval` を注入して実時間を使わない。
- `skipped` バケットが引き続きエラーになること（issue #273 の回帰）を既存テストで担保する。

### 4.3 #327（`lead bootstrap`）

- カレントディレクトリ判定・各段階のスキップ判定は、一時ディレクトリを作り git リポジトリ有無・`origin` 有無を変化させて振る舞いを検証する。
- `gh` / エージェントは `testutil.InstallDummy` のダミーに置き換え、`gh repo create`・`gh issue create`・`lead enable` 相当の呼び出し引数をログで検証する。
- 未初期化ディレクトリ起動時の導線は、非対話 stdin で「bootstrap 提案メッセージが出て終了コード・出力が期待通り」であることを検証する（対話 TUI の分岐は非対話モードでのみテストし、`docs/testing.md` §4 の非対話 TUI テスト方針に従う）。
- bootstrap 全体の E2E は外部サービス（GitHub）に依存するため、テストスイートには含めず手動確認とする。

### 4.4 共通

- `./test/run-tests.sh` 全件パスを完了条件とする。
- 実装ソースへの `grep` テストは追加しない（`test/test_no_source_grep.sh` が検出する）。

---

## 5. 後続 Issue への要件マッピング

| Issue | 本 RFC の該当節 | 主な変更対象 |
|-------|----------------|-------------|
| #325 | §3.2 | `internal/finish/postmerge.go`（`DefaultSmoke` の置き換え）、`internal/ports`（エージェント呼び出し） |
| #326 | §3.3 | `internal/finish/finish.go`（`WaitChecks`）、`internal/ports`（ワークフロー列挙）、必要なら `bin/ci-wait` |
| #327 | §3.1 | `internal/cli/cli.go`（新規サブコマンド・未初期化導線）、`internal/projinit`（再利用）、場合により新規 `internal/bootstrap` パッケージ |

---

## 6. スコープ外

- リモート GitHub リポジトリ作成 API の `lead` 内再実装（親 Issue #323 のスコープ外。`gh repo create` またはエージェント委譲で代替）。
- 言語別スキャフォールドテンプレートの量産（非 LLM 環境向けフォールバックは設けない）。
- プロダクションコード・`README.md` の変更は本 Issue（#324）の対象外。#325〜#327 で実装する。
- `lead enable`・`lead run` 等の既存コマンドの仕様変更（bootstrap からの呼び出し方のみ定める）。

---

## 7. 決定事項まとめ

| 項目 | 決定 |
| :--- | :--- |
| 初期化導線 | `lead bootstrap` 新設 + 未初期化ディレクトリ起動時に bootstrap を提案 |
| bootstrap の責務 | 手順オーケストレーションと状態確認。リポジトリ作成・スキャフォールド・CI 設計は LLM エージェントに委譲 |
| bootstrap の冪等性 | 各段階を「満たされていればスキップ」で判定。workflow レコードに依存しない |
| postmerge スキップの骨格 | worktree 展開 → スモーク → revert/incident フローは維持。`DefaultSmoke` の中身のみ差し替え |
| postmerge スモーク | 設定済みコーディングエージェントに検証を委譲。プロジェクト固有コマンドは `lead` は持たない |
| スモーク未実行と失敗の区別 | エージェント起動失敗は revert しない。実行して失敗した場合のみ revert + incident |
| `WaitChecks` のワークフロー判定 | `gh` ワークフロー一覧 API で 0 件なら即成功。列挙失敗時は従来どおり待機 |
| skipped ≠ pass 原則 | issue #273 の原則は維持。「ワークフロー不在（チェック非存在）」と「skip 判定」を区別する |

---

## 8. 未解決事項（実装 Issue で決める）

- `ports.GhClient` へのワークフロー列挙メソッドの具体的なシグネチャ（#326）。
- postmerge スモークの「未実行」状態を `PostMergeResult` の新フィールドで表すか、既存フィールドの意味で区別するか（#325）。
- bootstrap の対話プロンプトの具体的な文言と、非対話時の既定値（#327）。
- bootstrap が作成する初期 Issue のテンプレート（#327）。

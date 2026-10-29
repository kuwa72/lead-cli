# 最初の Issue 群（下書き）

`docs/rfc-factory.md` §12 の8件。`lead new --body-file` でそのまま起票できる形にしてある。
起票時のラベルは本文末尾の `Labels` 行に従う（`risk:*` は必須）。

---

## 1. feat(ledger): 実行結果を runs.jsonl に残し lead stats で集計する

## Goal

dispatch / review / finish の結果を `<state-dir>/runs.jsonl` に1実行1行で追記し、`lead stats` で成功率・所要時間・revert 率を出せるようにする。

## Background

`workflows.json` は完了時にレコードを削除するため、成功率や所要時間を後から測れない。先行事例（Microsoft 67.9%、Ona 87%、T2D3）はいずれもこの計測を改善の起点にしている。

## Scope

- `internal/ledger` を追加（追記・読込・集計）
- dispatch / finish から `Append` を呼ぶ
- `lead stats [--json]` を追加
- 既存の `lead status` は変更しない

## スコープ外

- コスト（トークン）の計測
- ダッシュボード UI
- `workflows.json` のスキーマ変更

## 受入条件

- [ ] `go test ./internal/ledger/` が pass する
- [ ] `./test/test_stats.sh` が pass する（一時 state dir に3件追記し、`lead stats` の出力に `success rate: 66.7%` が出る）
- [ ] `lead stats --json` が `{"runs":3,"completed":2,...}` を返す
- [ ] dispatch の1回の実行で `runs.jsonl` が1行増える（ダミー gh / ダミーエージェントで検証）

## Test plan

- `internal/ledger` の単体テスト: 空ファイル、壊れた行のスキップ、集計の境界（0件でゼロ除算しない）
- `test/test_stats.sh`: 振る舞いテスト（ダミー PATH は不要。state dir を一時化）

## Files

`internal/ledger/ledger.go`, `internal/ledger/ledger_test.go`, `internal/cli/cli.go`, `internal/dispatch/dispatch.go`, `internal/finish/finish.go`, `test/test_stats.sh`, `test/run-tests.sh`

## Dependencies

なし

## Risk

追記のみで既存の状態ファイルには触れない。壊れた行は読み飛ばす（集計が落ちない）。

## Labels

`risk:a`, `ready`

---

## 2. fix(finish): skipping/neutral の CI チェックを合格に数えない

## Goal

`lead finish` の CI 待機で、`skipping` を合格として扱うのをやめる。

## Background

`internal/finish/finish.go` の `WaitChecks` は `case "pass", "skipping":` で skipping を合格にしている。cotomu の記事は、GitHub が skipped/neutral をマージ可能として扱うため、必須チェック名を指定するだけでは検査の実行を保証できないと指摘している。パスフィルター付きのジョブが skip されると、テストが走らないままマージされる。

## Scope

- `WaitChecks` の bucket 判定を `pass` のみ合格に変更
- `skipping` / `neutral` は不合格として理由を返す
- `internal/finish` のテストを更新

## スコープ外

- GitHub 側の必須チェック設定の変更
- `bin/ci-wait`（人間用）の変更

## 受入条件

- [ ] `go test ./internal/finish/` が pass する
- [ ] `./test/test_finish_checks.sh` が pass する（ダミー gh が `skipping` を返すと finish がマージせず exit 1）
- [ ] `pass` のみの場合は従来どおりマージする（同テストで確認）

## Test plan

- 既存の `finish_test.go` に bucket ごとの表テストを追加
- `test/test_finish_checks.sh` でダミー gh の JSON を差し替えて振る舞いを検証

## Files

`internal/finish/finish.go`, `internal/finish/finish_test.go`, `test/test_finish_checks.sh`

## Dependencies

なし

## Risk

必須チェックが実際に skip されるリポジトリでは、従来マージできていた PR が止まる。それが意図した挙動。

## Labels

`risk:a`, `ready`

---

## 3. feat(ci): vet と build を必須チェックに追加し、README と --help の一致を検証する

## Goal

CI で `go vet ./...` と `go build ./...` を実行し、README のサブコマンド表と `lead --help` の一致検証を必須チェックにする。

## Background

現行 CI は `./test/run-tests.sh` のみで、`run-tests.sh` の末尾の `go test ./...` に依存している。ビルドできない変更や vet 警告が通る余地がある。T2D3 は「ドキュメントと必須チェックの一致」を CI で検査しており、lead にも `internal/cli/readme_test.go` の下地がある。

## Scope

- `.github/workflows/ci.yml` に `go vet` と `go build` のステップを追加
- README 一致テストを CI で明示的に実行するステップを追加
- `test/run-tests.sh` の先頭に vet と build を追加（ローカルと CI を同じ経路にする）

## スコープ外

- golangci-lint などの新規ツール導入
- カバレッジ計測

## 受入条件

- [ ] `./test/run-tests.sh` が pass する
- [ ] `go vet ./...` が警告ゼロで終わる
- [ ] `go build ./...` が成功する
- [ ] CI のログに `go vet` と `go build` のステップが出る（PR の CI で確認）

## Test plan

- ローカルで `./test/run-tests.sh` を実行
- CI の該当 PR でステップ名を確認

## Files

`.github/workflows/ci.yml`, `test/run-tests.sh`

## Dependencies

なし

## Risk

`.github/` は保護パスに該当するため、`lead finish` は `--override-guardrail`（人間専用）なしでマージしない。

## Labels

`risk:b`, `ready`

---

## 4. feat(postmerge): マージ後にスモークを実行し、失敗したら自動 revert する

## Goal

`lead finish` のマージ後に main をビルドしてスモークを実行し、失敗したら `git revert` で戻して incident Issue を起票する。

## Background

承認を廃止するなら、壊れた変更が main に入ったときに自動で戻る手段が要る。Ona は Post-Merge Verifier を、moriwo は起動失敗2回での自動ロールバックを実装している。lead には現状この層が無い。

## Scope

- `finish` のマージ後に `postmerge` を実行
- main を一時 worktree にチェックアウトして `go build` と `./test/run-tests.sh` のスモークサブセットを実行
- 失敗時: `git revert --no-edit <merge-sha>` → push → incident Issue 起票（`P0` 相当のラベル）
- `lead postmerge <n>` を追加（手動再実行）
- ledger に `postmerge-failed` を記録

## スコープ外

- 本番デプロイの監視（CLI に本番環境は無い）
- リリースタグの自動化

## 受入条件

- [ ] `go test ./internal/finish/` が pass する
- [ ] `./test/test_postmerge.sh` が pass する（ダミー git でスモーク失敗を注入し、revert コミットと incident Issue の作成引数を検証）
- [ ] スモーク成功時は revert も Issue 作成も起きない（同テストで確認）
- [ ] `lead postmerge <n>` を手で実行しても同じ判定になる

## Test plan

- ダミー `git` / `gh` を PATH 注入し、`revert` と `issue create` に渡る引数をログで検証
- スモークコマンドはテスト用に差し替え可能にする（`Options.Smoke`）

## Files

`internal/finish/finish.go`, `internal/finish/postmerge.go`, `internal/finish/finish_test.go`, `internal/cli/cli.go`, `test/test_postmerge.sh`

## Dependencies

#1（ledger）

## Risk

revert が失敗すると main が壊れたまま残る。revert 失敗時は incident Issue に「手動復旧が必要」と明記して停止する。

## Labels

`risk:a`, `ready`

---

## 5. feat(scenario): シナリオを worktree 外に保存し、PR head で使い捨てテストを生成・実行する

## Goal

Issue ごとのシナリオをリポジトリ外（`<state-dir>/scenarios/<owner>_<repo>/<issue>.md`）に保存し、PR head の worktree で使い捨てテストを生成して実行する `internal/scenario` を追加する。

## Background

実装エージェントが書いたテストは、コードに合わせて書き換えられる（StrongDM「A test ... can be lazily rewritten to match the code」）。dark-factory は仕様から使い捨ての統合テストを生成しており、これが diff レビューの代わりになる。シナリオを worktree の外に置けば、実装側は編集できない。

## Scope

- `internal/scenario`: 保存・読込・パース（`## Steps` の番号付きリスト）
- 生成: シナリオと PR head の worktree をエージェントに渡し、テストファイルを一時ディレクトリに出力させる
- 実行: 生成物を worktree にコピーして実行し、シナリオごとの合否を返す
- 生成物は実行後に削除する（リポジトリに残さない）
- `lead scenario add <issue>` / `lead scenario list`

## スコープ外

- `lead review` への組み込み（#6）
- シナリオの自動生成（spec の担当）

## 受入条件

- [ ] `go test ./internal/scenario/` が pass する
- [ ] `./test/test_scenario.sh` が pass する（シナリオが worktree 外に作られ、実行後に worktree に生成物が残らないことを確認）
- [ ] ダミーエージェントが `SCENARIO 1: pass` / `SCENARIO 2: fail` を返したとき、結果が `1/2` になる
- [ ] シナリオ未設定の Issue では `0/0` を返し、エラーにしない

## Test plan

- パーサの単体テスト（番号なし、空、コードブロック入り）
- ダミーエージェント + 一時 worktree での振る舞いテスト
- 生成物が残らないことを `git status --porcelain` で確認

## Files

`internal/scenario/scenario.go`, `internal/scenario/scenario_test.go`, `internal/cli/cli.go`, `test/test_scenario.sh`

## Dependencies

なし

## Risk

生成テストが弱いと素通りする。verdict にシナリオごとの合否を残し、post-merge スモークと ledger で後から検証できるようにする。

## Labels

`risk:a`, `ready`

---

## 6. feat(review): diff レビューを助言に下げ、シナリオの合否を verdict の主条件にする

## Goal

`lead review` がシナリオ生成テストを実行し、`SCENARIOS: n/m` を verdict に含める。diff レビューは所見として残すが、合否の主条件にしない。

## Background

現行の review は「diff・Issue 本文・CI を読んで pass/needs-work」で、実装の自己申告に近い。dark-factory は diff のゴム判子ではなく仕様から生成したテストで判定している。

## Scope

- `review.Runner` から `internal/scenario` を呼ぶ
- プロンプトにシナリオ本文と「テストを生成して実行せよ」を追加
- 出力の最終行に `SCENARIOS: n/m` を追加
- `review:pass` は「シナリオ全通過 かつ diff レビューで重大な指摘なし」に限定
- シナリオ未設定なら diff レビューのみで判定（現行互換、`SCENARIOS: 0/0`）
- verdict は助言。`finish` のマージ可否には使わない（#7）

## スコープ外

- `lead finish` のゲート変更（#7）
- レビューエージェントの変更（`agents.review` のまま）

## 受入条件

- [ ] `go test ./internal/review/` が pass する
- [ ] `./test/test_review_scenario.sh` が pass する（ダミーエージェントが `SCENARIOS: 1/2` を返すと `review:needs-work`、`2/2` で `review:pass`）
- [ ] 既存の `test/test_review_gate.sh` / `test_review_head_sha.sh` が pass する
- [ ] review コメントにシナリオごとの合否が残る

## Test plan

- verdict パーサの単体テスト（`SCENARIOS` 行の有無、順序）
- ダミーエージェントでラベル付けの引数を検証

## Files

`internal/review/review.go`, `internal/review/review_test.go`, `internal/scenario/scenario.go`, `test/test_review_scenario.sh`

## Dependencies

#5

## Risk

シナリオ未設定の Issue は従来どおり diff レビューだけで pass しうる。移行中は許容し、#8 でシナリオ必須化した後に解消する。

## Labels

`risk:a`, `ready`

---

## 7. feat(finish): 承認を廃止し、CI・シナリオ・保護パスからマージ可否を計算する

## Goal

`lead finish <n> --merge` から `--approve` / `--no-review` と `awaiting-human` 経路を削除し、CI・シナリオ・保護パスの計算結果だけでマージ可否を決める。

## Background

承認が唯一の詰まり（実測: 5件が `awaiting-human`）。先行事例でも、承認を消した運用は「別のゲート」で置き換えている（T2D3 は types/lint/CI/merge queue、Ona は post-merge verifier）。

## Scope

- `finish/policy.go` を計算ゲートへ（`Risk` ラベル依存をやめる）
- 判定順: CI（pass のみ）→ シナリオ（あれば m/m）→ 保護パス → マージ
- `review:pass` を必須にしない（review は助言。CI が通ればマージする）
- `--approve` / `--no-review` / `awaiting-human` ラベルを削除
- 拒否時は exit 0 で理由を標準出力と Issue コメントに残す
- README / docs/config.md / docs/selfhost.md を更新（`AGENTS.md` は保護パスのため #9 で分離）

## スコープ外

- シナリオの生成（#5）
- post-merge（#4）

## 受入条件

- [ ] `go test ./internal/finish/` が pass する
- [ ] `./test/test_gate_auto_merge.sh` が pass する（CI pass + シナリオ 1/1 + 保護パス非該当 → `--approve` なしでマージ。`review:needs-work` でもマージする）
- [ ] シナリオ 1/2 のときマージせず exit 0 で理由を出す（同テスト）
- [ ] `--approve` を渡すと「unknown flag」で exit 1 になる（同テスト）
- [ ] `./test/run-tests.sh` が pass する（既存の `test_risk_gate.sh` / `test_finish_review_required.sh` を新仕様に更新）

## Test plan

- ダミー gh で checks / labels / head SHA を差し替え、マージ呼び出しの有無を検証
- 拒否理由が Issue コメントに渡る引数を検証

## Files

`internal/finish/finish.go`, `internal/finish/policy.go`, `internal/finish/finish_test.go`, `internal/cli/cli.go`, `README.md`, `docs/config.md`, `docs/selfhost.md`, `test/test_gate_auto_merge.sh`, `test/test_risk_gate.sh`, `test/test_finish_review_required.sh`

## Dependencies

#1, #2, #4, #6

## Risk

保護パス以外はすべて自動マージになる。壊れたときの唯一の復旧手段は #4 の revert。

## Labels

`risk:a`, `ready`

---

## 8. feat(spec): 起票前にリポジトリを読ませ、本文スキーマに Goal/Non-goals/シナリオを必須化する

## Goal

仕様 AI にリポジトリの現状（ファイル一覧・既存 Issue・直近マージ）を渡し、起票前にコードを読ませる。本文スキーマに `## Goal` / `## Non-goals` / `## シナリオ` を追加し、`issuecheck` でシナリオを必須にする。

## Background

現行の `spec` はリポジトリ内で動くが、プロンプトの根拠は一言と `AGENTS.md` だけ。On a は「仕様の質と必要な反復回数が逆相関する」と報告しており、Microsoft も「タスクの区切りがサイズより効く」と書いている。

## Scope

- `spec.PromptInput` に `Evidence` を追加（`git ls-files` の上位N、直近マージ10件、open 10件）
- プロンプトに「読んで確認したファイルだけ書く」制約を追加
- `bodyFormat` に Goal / Non-goals / シナリオを追加
- `issuecheck` で `## シナリオ`（`## Scenarios`）を必須化
- `lead new` のテンプレートにもシナリオ節を追加
- golden を更新

## スコープ外

- シナリオの自動生成（人間が書く。spec は下書きまで）
- 依存グラフの自動設定（GitHub の blockedBy を使う）

## 受入条件

- [ ] `go test ./internal/spec/ ./internal/issuecheck/ ./internal/cli/` が pass する
- [ ] `UPDATE_GOLDEN=1 go test ./internal/spec/` 後の差分に evidence ブロックとシナリオ節が入っている
- [ ] `./test/test_new_ready.sh` が pass する（シナリオ節のない本文を `lead new` が拒否する）
- [ ] `lead say --dry-run` の出力本文が `issuecheck.Check` を通る（新規テストで確認）

## Test plan

- golden の目視差分（意図しない文言変化がないか）
- `lead new --body-file` にシナリオなし／ありの2ケース

## Files

`internal/spec/spec.go`, `internal/spec/spec_test.go`, `internal/spec/testdata/*.golden`, `internal/issuecheck/issuecheck.go`, `internal/issuecheck/issuecheck_test.go`, `internal/cli/cli.go`, `test/test_new_ready.sh`

## Dependencies

なし

## Risk

シナリオ必須化により、既存の起票フローが止まる。`lead ready` も同じ検査を通るため、既存 Issue を `ready` にするにはシナリオ追記が要る。

## Labels

`risk:a`, `ready`

---

## 9. docs(agents): lead-flow ブロックから承認と review ゲートの記述を外す

## Goal

`AGENTS.md` の lead-flow 管理ブロックを、承認廃止とシナリオゲートに合わせて更新する。

## Background

#7 で `--approve` / `awaiting-human` を削除するが、`AGENTS.md` は保護パスなのでエージェントは編集できない。規約の記述と実装がずれると、エージェントが存在しないフラグを案内し続ける（T2D3 の「ドキュメントと必須チェックの一致」と同じ問題）。

## Scope

- lead-flow ブロックの手順 6〜8 を、`lead review` → `lead finish --merge`（承認なし）に更新
- `--approve` / `--no-review` / `awaiting-human` の記述を削除
- シナリオの置き場所と「編集しない」制約を追記

## スコープ外

- `.claude/` / `.devin/` など配布先スキルの更新（`lead enable` が再配布する）
- 他リポジトリへの反映

## 受入条件

- [ ] `./test/test_enable_flow_docs.sh` が pass する（管理ブロックの内容を新仕様に合わせて更新）
- [ ] `lead enable --yes` を再実行したとき、生成されるブロックが新しい内容になる（テストで確認）
- [ ] `grep -c 'approve' AGENTS.md` が 0（人間専用フラグの説明を除く）

## Test plan

- `internal/projinit/agents_snippet.md` の golden 更新
- `test/test_enable_flow_docs.sh` の期待値更新

## Files

`AGENTS.md`, `internal/projinit/agents_snippet.md`, `internal/projinit/init_test.go`, `test/test_enable_flow_docs.sh`

## Dependencies

#7

## Risk

保護パスに該当するため、マージには人間の `--override-guardrail` が必要。これが規約ファイル保護の意図した動作。

## Labels

`risk:b`, `ready`

---

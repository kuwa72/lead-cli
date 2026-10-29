# RFC: ソフトウェアファクトリー化（承認を消し、検証で置き換える）

- 状態: 提案（未コミット）
- 前提: 依頼者の決定（2026-10-29）
  1. レビューは「diff を読む AI」から「シナリオ生成テスト」へ寄せる
  2. 自動マージ条件は差分・影響範囲の計算へ寄せる
  3. 人間の承認は廃止する
- 調査: `~/research/agentic-software-factory/`（report.html, map.md, sources/）

---

## 1. 現状

ループ（起票 → 承認 → 実装 → レビュー → マージ → クローズ）は既に実装済み。欠けているのは3点だけ。

| 欠け | 現状 | 影響 |
|---|---|---|
| 入力の文脈 | `spec` はリポジトリ内で動くが、プロンプトの根拠は一言と `AGENTS.md` だけ | Issue の粒度・受入条件が不安定 |
| 履歴 | 完了時に `workflows.json` から削除 | 成功率・所要時間・revert 率を測れない |
| 検証の厚み | CI は `./test/run-tests.sh` のみ。レビューは diff を読むだけ | 「書けた ≠ 完了」の担保が弱い |

実測: open PR 5件はすべて `review:pass`、対応 Issue 5件はすべて `awaiting-human`。詰まりは承認だけ。

## 2. 先行事例から採るもの

| 出典 | 採る | 採らない | 理由 |
|---|---|---|---|
| dark-factory | 仕様から使い捨て統合テストを生成する機能レビュー、優先度、保護パス | Docker サンドボックス | worktree で分離済み。個人運用に Docker は重い |
| StrongDM | シナリオをコードベース外に置く（holdout）、通るまでループ | DTU、$1,000/日前提 | CLI に外部 SaaS は無い |
| T2D3 | 自動修正の限定条件、retry 1回→停止、docs とチェックの一致を CI で検査 | ratchet 38本、Mission Control UI | 最初から全部は重い |
| Ona | マージ後スモーク、インシデント→Issue、品質スコアによるバックログ | 16 automation の同時運用 | 個人規模では3本から |
| Microsoft | 着手は明示要求、成功率と revert 率の計測 | 人間レビュー必須 | 承認廃止と矛盾 |
| Spotify | plan/generate/review の分離 | 1,500 PR 規模の基盤 | 規模が違う |
| Findy | 依存グラフ→レイヤー→merge-gate | Team Lead/Worker の二重エージェント | `blockedBy` で同等。エージェントを増やすとコスト増 |
| pepabo | 出口（カンバン）、並列は5から | 板の新造 | `inbox` が既に出口 |
| moriwo | 聖域、検証3段、起動失敗で自動ロールバック | 自己進化 | 対象が違う |
| cotomu | skipped/neutral を合格にしない、承認後の差分再確認 | CODEOWNERS | 1人運用では不要 |
| openmercato | パイプラインの分割、claim lock、PR から追い Issue、マージの分離 | 25 スキル全部 | 必要なのは5つ |
| CyberAgent | ルールベース判定 + AI approve + auto-merge | Dependabot 専用 | 汎用化して使う |

## 3. 目標アーキテクチャ

```
ふわっと入力
   │
   ▼
[spec]  evidence: git ls-files / 既存Issue / AGENTS.md / 読むべきファイル
   │     本文: Goal / Background / Scope / Non-goals / 受入条件 / シナリオ / Test plan / Files / Deps / Risk
   ▼
needs-review ──▶ ready
   │
   ▼
[dispatch]  claim lock → worktree → headless agent
   │
   ▼
  PR
   │
   ├──▶ CI: vet + build + test（必須。skipping/neutral は不合格）
   ├──▶ [scenario]  worktree 外のシナリオ → PR head で使い捨てテスト生成・実行
   └──▶ [review]    diff レビュー（助言。合否はシナリオ）
   │
   ▼
[finish/gate]  CI ∧ シナリオ ∧ 保護パス非該当 → 自動マージ（承認なし）
   │
   ▼
[postmerge]  main をビルドしてスモーク → 失敗なら git revert + incident Issue
   │
   ▼
[ledger]  runs.jsonl に outcome を1行追記 → lead stats
```

シナリオは `$XDG_STATE_HOME/lead/scenarios/<owner>_<repo>/<issue>.md` に置く。worktree の外なので実装エージェントは書き換えられない。

## 4. モジュール

### 追加

| パッケージ | 責務 | 目安 |
|---|---|---|
| `internal/scenario` | シナリオの保存・読込・PR head での生成テスト実行 | 300行 |
| `internal/ledger` | `runs.jsonl` の追記・読込・集計 | 200行 |

### 変更

| パッケージ | 変更 |
|---|---|
| `internal/spec` | evidence 入力を追加。本文スキーマに Goal/Non-goals/シナリオ |
| `internal/issuecheck` | `## シナリオ`（または `## Scenarios`）を必須化。Goal/Non-goals は推奨（機械検査しない） |
| `internal/review` | シナリオ生成テストを実行。verdict に `SCENARIOS: n/m`。diff レビューは助言節へ |
| `internal/finish` | `policy.go` を計算ゲートへ。`awaiting-human` 経路と `--approve`/`--no-review` を削除。post-merge を追加 |
| `internal/dispatch` | claim lock（#262）、失敗分類、追い Issue 自動起票、ledger 追記 |
| `internal/cli` | `scenario` / `stats` / `postmerge` 追加、`approve` 系削除 |
| `.github/workflows/ci.yml` | `go vet` と `go build` を追加 |

## 5. データモデル

```go
// internal/ledger
type RunRecord struct {
    Repo      string    `json:"repo"`
    Issue     int       `json:"issue"`
    Agent     string    `json:"agent"`
    Mode      string    `json:"mode"`
    StartedAt time.Time `json:"started_at"`
    EndedAt   time.Time `json:"ended_at"`
    Outcome   string    `json:"outcome"` // completed|retry|blocked|guardrail|postmerge-failed
    Attempts  int       `json:"attempts"`
    PR        int       `json:"pr,omitempty"`
    Review    string    `json:"review,omitempty"`    // pass|needs-work|skipped
    Scenarios string    `json:"scenarios,omitempty"` // "3/4"
    HeadSHA   string    `json:"head_sha,omitempty"`
    DiffLines int       `json:"diff_lines,omitempty"`
    Files     int       `json:"files,omitempty"`
    RevertOf  string    `json:"revert_of,omitempty"`
}
```

シナリオ（Markdown、人間が読める形）:

```markdown
# Scenario: lead stats が成功率を表示する
## Steps
1. `lead stats` を空の状態で実行する → 終了コード 0、`no runs` を表示
2. 3件の RunRecord を追記して実行する → `success rate: 66.7%`
```

ゲートの判断は保存しない（GitHub と ledger から都度計算する）。

## 6. CLI

| コマンド | 変更 |
|---|---|
| `lead scenario add <issue>` | 新規。`$EDITOR` でシナリオ作成（worktree 外） |
| `lead scenario list` | 新規 |
| `lead review <pr>` | 意味変更。シナリオ生成テストを実行し `VERDICT` と `SCENARIOS: n/m` を出す |
| `lead finish <n> --merge` | `--approve` / `--no-review` を削除。ゲート計算で自動マージ |
| `lead postmerge <n>` | 新規（`finish` から自動呼び出し。手動再実行用） |
| `lead stats [--json]` | 新規。成功率・中央値・revert 率・シナリオ通過率 |
| `lead approve`（PR #268） | 不採用。クローズ |

`--override-guardrail` だけは人間専用として残す。エージェントが自分の規約を書き換えないための唯一の例外。

## 7. Issue 生成プロンプト

追加する evidence ブロック:

```
## リポジトリの現状（起票前に必ず読むこと）
- ファイル一覧: <git ls-files の上位N>
- 直近マージ: #264 fix(issuecheck) ... / #257 fix(install) ...
- open: #267 ... / #266 ...
- 着手前に読むべきファイル: 自分で選び、読んでから書くこと

推測でファイル名や関数名を書かない。読んで確認したものだけ書く。
```

本文スキーマ（`## シナリオ` が新しい必須項目）:

```markdown
## Goal
## Background
## Scope
## Non-goals
## 受入条件
- [ ] `./test/run-tests.sh` が pass する
## シナリオ
1. `<コマンド>` → <期待する出力・終了コード>
## Test plan
## Files
## Dependencies
## Risk
```

分割規則: 「シナリオが2つ以上の独立した目的に分かれるなら分割する」。

## 8. エージェント実行プロンプト（dispatch）

完了定義の変更点だけ:

1. 差分が大きくなりそうなら、着手前に分割案を Issue にコメントして停止する。
2. シナリオ（worktree 外）は編集しない。誤りだと思ったらコメントして停止する。
3. `lead review <PR>` の `SCENARIOS: n/m` が m/m になるまで直す。2周で通らなければ所見を Issue に貼って停止。
4. `lead finish <n> --merge` でマージする。承認フラグは存在しない。ゲートが拒否したら理由を Issue にコメントして停止する（拒否理由を回避するフラグは無い）。
5. 規約ファイル（`AGENTS.md`、`.github/`、`.claude/` 等）は、Issue で明示されていない限り編集しない。

## 9. 検証ゲート

| 層 | 条件 | 落ちたとき |
|---|---|---|
| 1. CI | `go vet` / `go build` / `./test/run-tests.sh` がすべて `pass` | マージしない。dispatch が修正して再試行 |
| 2. CI の取りこぼし | `skipping` / `neutral` / `cancel` は不合格 | マージしない（現状は `skipping` を合格にしている） |
| 3. シナリオ | シナリオがある場合のみ、生成テストが m/m。無ければこの層は飛ばす | マージしない。所見を返す |
| 4. 保護パス | `AGENTS.md` / `.github/` / `.claude/` 等を含まない | ハードブロック。人間の `--override-guardrail` のみ解除可 |
| 5. diff レビュー | 助言。`review:pass` は必須にしない（CI が通ればマージする） | ledger に記録するだけ |
| 6. post-merge | main をビルドし `lead --help` とスモークが通る | `git revert` + incident Issue |

## 10. 失敗・再開・ロールバック

| 失敗 | 扱い |
|---|---|
| CI 失敗 | 原因を要約して再試行（最大2回）。3回目で `blocked` + 追い Issue |
| シナリオ失敗 | review の所見で修正。2周で通らなければ `blocked` + 追い Issue |
| 保護パス該当 | 即 `blocked`。追い Issue は作らない（規約変更は Issue で明示） |
| エージェント stall | watchdog が kill → 再試行 → `blocked` |
| post-merge 失敗 | 自動 revert + incident Issue（P0）+ 以後の dispatch を停止 |
| 循環依存 | 既存どおりコメントして停止 |

ロールバックは post-merge revert が唯一の自動手段。リリースタグは post-merge 成功と24時間のインシデント無しを条件にする（Phase 5）。

## 11. 実装計画

| Phase | 内容 | 目安 |
|---|---|---|
| 0 | 滞留5件をマージ（`--approve` を最後に使う）。PR #268 をクローズ | 30分 |
| 1 | ledger、CI に vet/build、skipped 修正 | 2〜3日 |
| 2 | post-merge スモーク + 自動 revert（安全網を先に置く） | 2日 |
| 3 | scenario → review → finish の計算ゲート（ここで承認を削除） | 3〜4日 |
| 4 | spec の evidence とスキーマ（上流の品質） | 2日 |
| 5 | 失敗分類と追い Issue、依存監査、リリースノート | 3日 |

Phase 2 を Phase 3 より先に置く理由: 承認を消す前に、壊れたときに自動で戻る手段を用意する。

## 12. 最初の Issue 群

| # | タイトル | 依存 | risk |
|---|---|---|---|
| 1 | `feat(ledger): 実行結果を runs.jsonl に残し lead stats で集計する` | — | a |
| 2 | `fix(finish): skipping/neutral の CI チェックを合格に数えない` | — | a |
| 3 | `feat(ci): vet と build を必須チェックに追加し、README と --help の一致を検証する` | — | b（`.github/` 該当） |
| 4 | `feat(postmerge): マージ後にスモークを実行し、失敗したら自動 revert する` | 1 | a |
| 5 | `feat(scenario): シナリオを worktree 外に保存し、PR head で使い捨てテストを生成・実行する` | — | a |
| 6 | `feat(review): diff レビューを助言に下げ、シナリオの合否を verdict の主条件にする` | 5 | a |
| 7 | `feat(finish): 承認を廃止し、CI・シナリオ・保護パスからマージ可否を計算する` | 1,2,4,6 | a |
| 8 | `feat(spec): 起票前にリポジトリを読ませ、本文スキーマに Goal/Non-goals/シナリオを必須化する` | — | a |
| 9 | `docs(agents): lead-flow ブロックから承認と review ゲートの記述を外す` | 7 | b |

各 Issue の本文は `docs/rfc-factory-issues.md` に下書きを置く（起票時にそのまま使う）。

## 13. 設計判断と代替案

| 判断 | 理由 | 代替 |
|---|---|---|
| シナリオを worktree 外に置く | 実装エージェントがテストを合わせにいけない | リポジトリ内 + 保護パス（読めるので合わせにいける） |
| シナリオからテストを生成 | シナリオは自然言語で書ける。固定テストは書くコストが高い | シナリオを Go テストで直接書く |
| 承認を廃止 | 依頼者の決定。元々の思想も「CI が通れば自動マージ」。滞留の実測が根拠 | ペパボのマージGo（1クリック承認） |
| ledger は jsonl | 依存なし、追記のみ、grep できる | SQLite（集計は楽だがマイグレーションが増える） |
| ゲートは `finish` 内 | 判断を1か所に閉じる | 独立パッケージ（inbox からも見たいとき） |
| `--override-guardrail` だけ人間に残す | エージェントが自分の規約を書き換える事故を防ぐ | 全廃（`AGENTS.md` を書き換えられる） |
| サイズ上限は助言 | シナリオが合否を決めるので行数は代理指標でしかない | 50行超は自動マージしない（T2D3 の自動修正条件） |

## 13.1 人間の役割

人間が介入するのは次の2つだけ。

1. 詰まっている Issue（`blocked` ラベル、保護パス該当、循環依存）をどうにかするとき
2. マージ後の実機確認（`lead` の実際の操作、リリース前の確認）

`lead review` の `review:needs-work` はマージを止めない。実装エージェントが直すための助言として扱う。

## 14. 残余リスク

- シナリオ生成が弱いテストを作ると素通りする。緩和: post-merge スモーク、ledger の revert 率監視、シナリオは起票時に人間が書く（spec は下書き）。
- CLI に本番テレメトリが無いため、post-merge スモークは「壊れていない」の弱い証明でしかない。緩和: タグは soak 後。
- 滞留5件はシナリオ無しでマージされる。緩和: マージ後にシナリオを追い Issue で作る。
- 保護パスは `--override-guardrail` が残る。dispatch の完了定義には含めない（現行どおり）。

# 設定リファレンス (issue #233)

`lead` の設定は状態ディレクトリ直下の `inbox-config.json` に置く。
既定は `~/.local/state/lead/inbox-config.json`（`$XDG_STATE_HOME` があれば
`$XDG_STATE_HOME/lead/`、`$LEAD_STATE_FILE` を指定した場合はそのファイルと同じ
ディレクトリ）。通常は inbox の Settings ビュー（`,` / `S`）が書き込むが、
手で編集してもよい。JSON が不正な場合はエラーになる（既定値への暗黙フォールバックはしない）。

```json
{
  "agent": "claude",
  "agents": {"spec": "codex", "impl": "devin", "review": "gemini"},
  "agent_mode": "batch",
  "issue_creation": "direct",
  "notify_disabled": false,
  "stall_timeout": "30m",
  "max_runtime": "3h"
}
```

## キー

| キー | 型 | 既定値 | 説明 |
|---|---|---|---|
| `agent` | string | `agy` | 使用するコーディングエージェント。`agy` / `claude` / `codex` / `devin` / `opencode` / `gemini`。空文字は `agy` と同じ |
| `agents.spec` | string | `agent` | 仕様 AI（`lead say`）のエージェント。未設定は `agent` |
| `agents.impl` | string | `agent` | 実装 AI（dispatch）のエージェント。未設定は `agent` |
| `agents.review` | string | `agents.impl` → `agent` | 独立レビュー（`lead review`）のエージェント。未設定は `agents.impl`、それも未設定なら `agent`（issue #246） |
| `agent_mode` | string | `batch` | inbox / `lead dispatch` のエージェント実行モード。`interactive` / `batch` / `dangerous`。`lead run` は `--agent-mode`（既定 `interactive`）を使う |
| `issue_creation` | string | `direct` | `s` キーの起票方法。`direct`（そのまま起票）/ `ai`（仕様エージェント経由） |
| `notify_disabled` | bool | `false` | `true` でデスクトップ通知を止める |
| `stall_timeout` | duration string | `30m` | ヘッドレスエージェントのログがこの時間止まったらプロセスグループごと kill する。`0` 以下で無効 |
| `max_runtime` | duration string | `3h`（dispatch / inbox）、無制限（`lead say`） | ヘッドレス実行の壁時計上限。`0` 以下で無効 |

`stall_timeout` / `max_runtime` は Go の duration 文字列（`30m`, `3h`, `-1s` など）。
不正な文字列はエラーになる。`stall_timeout` は inbox・`lead dispatch`・`lead say` の
すべてに効く。`max_runtime` は dispatch / inbox が既定 3h、`lead say` は未設定なら無制限
（agy の `--print-timeout` には watchdog より先に発火しない 24h をバックストップとして渡す）。

## エージェント選択の優先順位

- `lead say`: `--agent` → `$LEAD_SPEC_AGENT` → `agents.spec` → `agent` → `agy`
- `lead review`: `--agent` → `agents.review` → `agents.impl` → `agent` → `agy`
- `lead run` / `lead dispatch` / 番号なし `run` のピッカー: `--agent` → `agents.impl` → `agent` → `agy`
- `lead resume`: `--agent` → 状態ファイルに記録された前回のエージェント → `agent` → `agy`

## 環境変数

| 変数 | 既定値 | 説明 |
|---|---|---|
| `LEAD_STATE_FILE` | `$XDG_STATE_HOME/lead/workflows.json` または `~/.local/state/lead/workflows.json` | 状態ファイルのパス。`inbox-config.json` は同じディレクトリに置かれる |
| `LEAD_SPEC_AGENT` | なし | `lead say` の仕様エージェント。`--agent` の次、`agent` より優先 |
| `LEAD_SPEC_PROGRESS_INTERVAL` | `10s` | `lead say` の進捗行の更新間隔。負値で無効 |
| `LEAD_NOTIFY` | なし | `0` / `false` / `off` / `no` で通知を無効化（`notify_disabled` と同じ） |
| `LEAD_THEME` | `terminal` | inbox の配色。`terminal` / `dark` / `light`。`NO_COLOR` / `TERM=dumb` が優先 |
| `LEAD_ISSUE_LIMIT` | `300` | `gh issue list` 1 回あたりの取得件数 |

## `lead finish` のマージ判定（承認なし）

`lead finish <n> --merge` は次の3つがすべて通るときだけマージする（issue #278）。

1. CI が pass（`skipping` / `fail` / `cancel` は不合格。`neutral` は待機し、
   タイムアウトする）。
2. `lead review <pr>` が記録した生成シナリオがすべて pass（`SCENARIOS: n/n`）。
   シナリオ未設定ならこの層は飛ばす。シナリオファイルがあるのにレビュー結果が
   無い場合はマージせず停止する。
3. PR が規約ファイル（`AGENTS.md`、`.github/`、`.claude/` 等）を含まない。

承認ステップと `risk:*` ラベルによるゲートは無い。`review:pass` は助言で、
単独ではマージを決めない。シナリオ・規約パスでマージを止めたときは exit 0 で
理由を標準出力と Issue のコメントに残す。CI 不合格は exit 1 で、pause としては
投稿しない。

`--override-guardrail` だけが人間専用フラグで、規約ファイル保護を解除する
唯一の手段。エージェントは付けてはならない。

シナリオを実行したいときに `lead review <pr>` を回す。`lead dispatch` の
完了定義はこの手順を含む。

## 関連

- 設定画面と inbox の仕様: [rfc-inbox-ux.md](rfc-inbox-ux.md)
- エージェント別プロンプトとタイムアウトの詳細: [prompts.md](prompts.md)

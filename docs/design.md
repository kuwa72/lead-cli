# アーキテクチャ & 設計判断

本ドキュメントは、`lead-cli`（`lead`）の設計思想、競合比較、画面構成、マルチエージェント選定理由、および今後の開発ロードマップをまとめたものです。

---

## 1. 背景と目的

- **課題**: コーディングエージェントを使った開発において、Issueの確認、ブランチ作成、プロンプトのコピペ、TDDの遵守、CIの監視とマージ作業を手動で行うとオーバーヘッドが大きい。また単一のエージェントに依存するとレート制限や障害、コスト増に直面する。
- **目的**: GitHub Issueをトリガーとして、Herdr等のマルチペイン機能と連携し、複数エージェントをローテーションさせながら TDD → PR作成 → CI通過 → マージまでを最小手数・高効率で回す。

---

## 2. 競合比較・作る意義

詳細な調査レポートは [`docs/rfc-24-competitor-analysis.md`](rfc-24-competitor-analysis.md) を参照。

| ツール / アプローチ | 特徴 | 課題・欠点 | `lead-cli` の強み |
| :--- | :--- | :--- | :--- |
| **`gh-dash` / `gh` ext** | TUI/CLIで一覧・操作 | Issue閲覧止まり。エージェントへのコンテキスト引渡しやTDD強制がない | **`fzf` プレビューから即座にブランチ作成＆エージェント起動。** |
| **Claude Code / Aider** | インライン対話型コーディング | 端末占有。Issue確認・TDD規約遵守・PR・CI待ち・マージが手動 | **左右ペインで人間が監視。Outer Loop（Issue〜マージ）を自動化。** |
| **Copilot Workspace** | Web UI完結で仕様→PR作成 | ローカル環境（テスト環境、独自スクリプト）との結合が弱くエージェント変更不可 | **ローカル環境で直接TDD実行。エージェント自由選択。** |
| **SWE-bench系自律フレームワーク** | Issue渡して放置でPR作成 | 完全自動化を狙いすぎて暴走しやすくトークン消費が激しい。監視しづらい | **マルチペインで人間の見守り・即時介入・事前レビューが手軽。** |

---

## 3. 設計方針とアーキテクチャ

### 3.1 AIネイティブ時代のIssue・コンテキスト管理
- **Issue粒度の再定義**: 人間時代の極小PRとは異なり、エージェントが自律走行しやすい「テスト込みで数ファイル・千行規模」を基本粒度と捉え、その受入条件をプロンプトとして動的注入する。
- **不変ルール**: [`AGENTS.md`](../AGENTS.md) にブランチ規約、TDD必須、PR・マージ完了定義のみを簡潔に定義。
- **動的コンテキスト注入**: `bin/lead work <issue_no>` 実行時に `gh issue view` からタイトル・本文・受け入れ条件を動的に取得し、エージェントの起動時プロンプトにピンポイント注入（トークン消費の最小化）。

### 3.2 画面構成 & マルチペインUX（人間の役割とエージェントの役割）
- **左ペイン（人間の司令塔）**:
  - `lead` 実行、`git status` / `git diff` によるリアルタイムなコード改変監視、手動テスト検証、`bin/ci-wait` によるCI追従。
- **右ペイン（エージェント作業場）**:
  - `herdr pane split --direction right --ratio 0.5` で生成。エージェント起動コマンドとプロンプトが**入力待機状態**で展開され、人間が確認・編集して `[Enter]` で開始（Human-in-the-loop）。

#### 3.2.1 fzf のキーバインドと画面上ガイド表示 (`--expect` & `--header`)
Issue一覧から2段階の選択（Issue選定→エージェント選定）を挟まず、1アクションで直感的に起動できるようにする：
- **常時キーガイド表示**: `--header` および `--header-first` を使用し、検索窓の直下に各キーの割り当てを固定表示（暗記不要）。
- **キー別のアクション発火 (`--expect`)**:
  - `[Enter]`: デフォルトエージェント（`agy`）で通常TDD開発
  - `[Ctrl-C]`: `claude` を指定して起動（複雑なタスク向け）
  - `[Ctrl-X]`: `codex` を指定して起動（軽量・高速タスク向け）
  - `[Ctrl-W]`: `git worktree` による分離ディレクトリで起動
  - `[Ctrl-O]`: ブラウザ（`gh issue view --web`）で開く

### 3.3 堅牢な CI 待機 & マージ
- **`bin/ci-wait`**:
  - `xyzzy/tools/ci-wait.sh` の知見を導入。
  - PR作成直後のチェック未登録ラグ、force-push時のrun再生成、GitHub APIのコンフリクト非同期判定遅延を吸収。
  - 手動ポーリング連打によるAPI制限や待ちぼうけを防止。

---

## 4. マルチエージェント・ローテーション戦略

環境内の複数エージェントを状況に応じて使い分ける：
- **`agy` (Antigravity CLI)**: デフォルトの標準エージェント。
- **`claude` (Claude Code)**: 複雑なリファクタリングや高難度ロジックの実装。
- **`codex`**: 軽量なスクリプト作成やクイックフィックス。
- **`devin` / `gemini`**: タスクの性質やAPIクォータ/レートリミット時の代替。

### 4.1 エージェント起動モード仕様 (`--agent-mode`, Issue #83)

| エージェント | 対話モード (`interactive`) | 自走/一括モード (`batch` / `dangerous`) | 権限・承認スキップ仕様 (実機確認済み) |
|---|---|---|---|
| `agy` | `agy -i "<prompt>"` | `agy --dangerously-skip-permissions -p "<prompt>"` | `--dangerously-skip-permissions`: ツール実行プロンプトを全自動承認。`-p` で非対話実行。 |
| `claude` | `claude "<prompt>"` | `claude -p --dangerously-skip-permissions "<prompt>"` | `--dangerously-skip-permissions`: ツール実行プロンプトを自動承認。`-p` で非対話実行。 |
| `codex` | `codex "<prompt>"` | `codex exec --dangerously-bypass-approvals-and-sandbox "<prompt>"` | `exec`: 非対話実行。`--dangerously-bypass-approvals-and-sandbox`: サンドボックスおよび確認プロンプトをバイパス。 |
| `gemini` | `gemini "<prompt>"` | `gemini -y "<prompt>"` | `-y` (`--yolo`): 全てのアクションを自動承認。 |
| `opencode` | `opencode "<prompt>"` | `opencode run --auto "<prompt>"` | `run --auto`: 自動承認で非対話実行。 |
| `devin` | `devin "<prompt>"` | `devin --permission-mode dangerous -p "<prompt>"` | `--permission-mode dangerous`: 全ツールを自動承認。`-p` で非対話実行。 |

---

## 5. ロードマップ & 今後のIssue候補

次のセッションで着手すべき改善点：
1. **Worktree分離機能の追加**: 同一リポジトリ内で複数Issueを並行して複数エージェントに作業させるための `git worktree` 自動作成・削除オプション。
2. **エージェント自動ローテーション/フォールバック**: レートリミット（429）やエラー検知時に自動でセカンダリエージェントを起動する仕組み。
3. **Issueステータス・ラベル自動更新**: `in-progress` などのラベル付与および進捗トラッキング。

---

## 6. 提供形態・基本UXモデルの決定（RFC #16）

> **§6.2 と §3.2 の UX モデル（Human-in-the-loop プロンプト確認・左右マルチペイン）は [rfc-inbox-ux.md](rfc-inbox-ux.md) で置き換えられた。** 人間のゲートは Issue 承認のみ、エージェントはヘッドレス並列、人間の常設画面は受信箱。§6.1 の提供形態は有効。

本節は [`docs/rfc-16-product-ux.md`](rfc-16-product-ux.md) で正式に決定・承認された内容を要約する。

### 6.1 提供形態

- **スタンドアロン単体 CLI バイナリ `lead`**: Go 製の単一静的バイナリ。`fzf` などの外部依存を組み込み TUI で排除する。
- **`gh extension` は採用しない**（`lead` の責務は GitHub 操作ではなくオーケストレーションであり、配布・リリースサイクルを `gh` エコシステムに縛る理由がない。詳細は RFC 2.2 節）。
- **配布経路**: GitHub Releases、Homebrew、インストールスクリプト（#26 で詳細設計）。
- **Herdr 環境自動検出**: `HERDR_ENV=1` および `herdr` コマンドの有無を判定し、Herdr がある場合は左右マルチペインを生成、ない場合はインラインフォールバックする。

### 6.2 基本UXモデル

- **1ストローク選定**: `go-fzf` 組み込み TUI で Issue 選択とエージェント決定を同時に行う。
- **Human-in-the-loop レビュー**: プロンプトを勝手に送信せず、入力欄に事前展開してユーザーが確認・発火する。
- **左右マルチペイン**: 左ペインで人間が `git diff` / テスト / CI 監視、右ペインでエージェントが作業する。

---

## 7. 自己外部操作機構（herdr 参考・Issue #46）

`lead` 自身を外部（別ペイン・別エージェント・スクリプト・リモート）から操作できる仕組みを持つ。モデルは herdr を参考にする。

### 7.1 herdr の参考点

- **永続 server + headless 実行**: セッション状態を保持する server を持ち、`server stop` / `reload-config` で制御する。
- **socket API**: `api schema`（API 定義の公開）/ `api snapshot`（実行時状態の取得）で外部から状態を覗ける。
- **CLI は薄いクライアント**: `pane` / `agent` / `tab` / `workspace` / `worktree` / `notification` / `session` の各サブコマンドは socket API 越しの操作である。
- **接続形態**: `--session`（名前付き永続セッション）、`--remote`（SSH 透過）、`--no-session`（単発実行の逃がし口）。

### 7.2 設計原則

1. **TUI と等価な非対話操作**: TUI でできる操作（Issue 一覧・選択、work ディスパッチ、状態確認、プロンプト投入、CI 待機状態の取得）は、非対話 CLI / API でも等価に実行できる。TUI 自体を API の一クライアントと位置づける。
2. **常駐を強制しない**: 単発実行モード（現行 CLI の使い方）を維持し、daemon 常駐は必要な場合のみ選択する。
3. **RFC-16 との関係**: RFC-16 §3 の UX モデル（1ストローク選定・Human-in-the-loop・左右マルチペイン）は維持し、本節はその操作面を外部に開く追加要件である。Human-in-the-loop を破る自動送信は行わない（プロンプト投入も確認待機を経る）。

### 7.3 構成案

- **`lead server`**: `workflows.json` の状態を保持する headless daemon（§5 の状態管理と結合）。
- **socket API**: `lead api schema`（定義公開）/ `lead api snapshot`（状態取得）。機械可読は `--json` で統一する。
- **操作プリミティブ案**: issue 一覧・選択、work ディスパッチ、状態取得、プロンプト投入、CI 待機状態、通知。
- **接続形態案**: 名前付きセッション、SSH 透過のリモート接続、単発実行モード。

### 7.4 セキュリティ方針

- socket はパーミッションで保護し、localhost からの接続に限定する。
- リモート認証基盤は自作せず SSH 透過に委譲する。

### 7.5 非目標

- daemon 常駐の強制、利用統計・テレメトリ収集。
- リモート認証基盤の自作。

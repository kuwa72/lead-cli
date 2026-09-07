# テスト方針 (issue #38)

RFC `docs/rfc-22-language-migration.md` §7 と AGENTS.md テスト品質ルールの実装基盤。
共有ヘルパーは `internal/testutil` に集約し、後続 Issue のテストから利用する。

## 1. ダミーコマンド PATH 注入 (`testutil.InstallDummy`)

- `logPath := testutil.InstallDummy(t, "gh", body)` —
  一時ディレクトリに実行可能ダミー `gh` を配置し、そのディレクトリを `PATH` 先頭に注入する。
- ダミー実行のたびに引数が1つずつ `<arg>` 行としてログに追記され、その後 `body`
  （シェル断片）が実行される。終了ステータスはそのまま伝播する。
- `testutil.LogLines(t, logPath)` / `LogText` で受け取った引数を厳密に検証する
  （`" $@ "` の結合ではなく `<arg>` 行単位なので、空白を含む引数の区切りも検証できる）。
- 不在系テスト（`gh`/`herdr` 未検出時のフォールバック）には `testutil.EmptyBin(t)` を使う。
  `PATH` 全体を空の一時ディレクトリに置換するため、実バイナリの混入がない。
  (`TempBin` は prepend のため実バイナリが残る点に注意)。

```go
logPath := testutil.InstallDummy(t, "herdr",
    `if [ "$1 $2" = "pane split" ]; then printf '{"result":{"pane":{"pane_id":"p1"}}}'; fi`)
// ... 実行 ...
if got := testutil.LogText(t, logPath); !strings.Contains(got, "<pane-123>") { ... }
```

## 2. インターフェースモック (`FakeGhClient` / `FakeHerdrRunner` / `FakeAgentLauncher`)

- `internal/ports` の各インターフェースに対するインメモリ Fake。
  固定値返却・呼び出し記録・エラー注入 (`ListErr` / `SplitErr` / `LaunchErr` 等) を持つ。
- `core` 系ロジック（後続 Issue）は外部 CLI を直接呼ばず、これら Fake に対して
  ユニットテストする。`var _ ports.GhClient = (*FakeGhClient)(nil)` で充足を保証。

## 3. Golden ファイル (`CheckGolden` / `AssertGolden`)

- プロンプト文面などの Chrysalis 出力は `test/testdata/*.golden` ではなく
  各パッケージの `testdata/*.golden` と比較する。
- 初回作成・意図的更新時は `UPDATE_GOLDEN=1 go test ./...` で再生成し、
  差分をレビューしてからコミットする（無条件の再生成コミットは禁止）。
- 不一致時は先頭20行の行単位 diff とバイト数を表示する。

## 4. 非対話 TUI テストモード方針

- TTY を奪う UI 本体（将来の `go-fzf` ラッパ等）は必ずインターフェースの背後に置き、
  テスト時は選択結果を固定値で返す Fake を注入する（RFC §7 TUI テストに沿う）。
- テストから実 TTY (`/dev/tty`) を開かない。対話挙動の確認は PR 前の実機実行
  （ダミー入力・非対話モード）で行い、その結果を PR 本文に記録する。

## 5. grep テスト禁止の CI 検出

- `test/test_no_source_grep.sh` が `test/test_*.sh` による実装ソース
  (`internal/`・`cmd/`・`bin/`・`.go`) への `grep` を検出する。
  検出時は CI (`./test/run-tests.sh` 経由) が失敗する。
- 正当な例外（文書言及の確認等）は行末 `# allow-grep: <理由>` で明示する。
- Go テスト側の等価物はレビュー観点とする:
  テストが `os.ReadFile` 等で `*.go` ソースを読み、文字列包含だけを
  アサートしていないか確認する（振る舞い＝実行・入出力・終了状態を主張すること）。

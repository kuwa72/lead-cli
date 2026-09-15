# RFC: Inbox 入力モードの IME 候補追従とハードウェアカーソル制御

- **Issue**: [#159](https://github.com/kuwa72/lead-cli/issues/159)
- **関連**: [#160](https://github.com/kuwa72/lead-cli/issues/160)（実装 Issue）
- **ステータス**: 調査完了・設計方針決定
- **更新日**: 2026-09-15

## 1. 症状と原因

`lead inbox` で `s`（新規イシュー）・`t`（返信）・`x`（却下理由）・`n`（バグ報告）を押すと `modeInput` に入り、画面下部に 1 行入力プロンプトが出る。この状態で日本語 IME を使うと、変換候補ウィンドウが入力キャレット位置ではなく画面左下（または最後にハードウェアカーソルがあった位置）に表示される。

原因は 2 つある。

1. **ハードウェアカーソルがキャレット位置にない。** Bubble Tea v0.24.2 の `standardRenderer.flush()` は、フレーム描画の最後に必ず `MoveCursor(linesRendered, 0)`（AltScreen 時）を発行し、カーソルを描画最終行の先頭に「駐車」する（`standard_renderer.go:247`）。アプリが任意座標へカーソルを移動させる公開 API は v0.24.2 には存在しない。
2. **カーソルは起動時から非表示。** `Program.initTerminal()` が `renderer.hideCursor()` を呼び `\x1b[?25l` を出す（`tty.go:27`）。`tea.ShowCursor` / `tea.HideCursor` は表示フラグ（DECTCEM）だけを切り替え、位置は変えられない。

`renderInputLine()` が描く反転ブロック（`\x1b[7m`）は画面文字列の一部であり、端末のハードウェアカーソルとは無関係の「仮想カーソル」。IME はこの仮想カーソルを認識できない。

同一症状は上流でも報告済みで、Bubble Tea 本体の既知制限と確認されている。

- charmbracelet/bubbletea#874「IME input in wrong position」: 「candidate window always at the left bottom side」、原因は「cursor kept in lower left + virtual cursor in Bubbles」。対応は v2 の real cursor で行われた。
- charmbracelet/bubbles#361: 同症例（fcitx5 候補が TUI 外に出る等）。

## 2. IME 候補ウィンドウのアンカー決定の仕組み

候補ウィンドウの位置はアプリから直接制御できない。OS の IME フレームワークが端末エミュレータに「テキスト挿入点の矩形」を問い合わせ、端末は**ハードウェアカーソルのセル位置**を返す。つまりアプリ側の唯一の制御手段は「ハードウェアカーソルを入力キャレットのセル座標に動かす」ことだけであり、専用の位置通知プロトコルは存在しない。

| 環境 | アンカー取得の仕組み | カーソル追従 |
|---|---|---|
| macOS Terminal.app / iTerm2 | NSTextInputClient の挿入点矩形 = ハードウェアカーソルセル | 可 |
| macOS WezTerm / kitty / Alacritty | 同上（各社の IM 統合経由） | 可 |
| Windows Terminal | TSF/IMM がコンソールカーソル位置を参照 | 可 |
| VTE 系（GNOME Terminal 等） | GTK IM モジュールのカーソル矩形 | 可 |
| Linux X11（ibus/fcitx5） | XIM spot location / IM モジュールのカーソル矩形 | 端末依存（WezTerm/kitty/最近の Alacritty は可） |
| Wayland | text-input-v3 のカーソル矩形 | 同上（kitty/foot/WezTerm/Alacritty は可） |
| tmux / GNU screen 内 | pane 内カーソルが外側端末へ中継 | 概ね可 |
| xterm / st 等 IME 未対応端末 | spot 未対応 | 不可 → 現状と同じ位置に出るだけで劣化しない |

変換中の preedit（未確定文字）描画と候補ウィンドウは端末・IME 側の仕事で、アプリには確定文字列だけが `KeyRunes` として届く。Kitty keyboard protocol 等のキー入力拡張は位置アンカーと無関係であり、本対応で必要なエスケープシーケンスは `CUP`（`CSI {row};{col}H`）と `DECTCEM`（`CSI ?25h/l`）のみ。OS・端末による差異は「追従できるか」の有無だけで、シーケンス自体は全端末共通。

## 3. Bubble Tea v0.24.2 での実現手段の検証

### 3.1 出力シーケンスの実測

`tea.NewProgram(m, tea.WithAltScreen(), tea.WithOutput(w))` で `w` に記録用 writer を挟んで実測した書き込み列（PoC、本調査で実施）:

```text
write 0: "\x1b[?25l"                     # initTerminal: カーソル非表示
write 1: "\x1b[?1049h"                   # AltScreen 開始
write 2-5: "\x1b[2J" "\x1b[H" "\x1b[?25l" "\x1b[?2004h"
write 6: "\x1b[H\rheader\r\nsummary\r\n…footer\r\n\x1b[5;H"   # 1フレーム = 1 Write、末尾が駐車 CUP
write 7: "\x1b[H\n\n\nNew issue > あいう█\r\nfooter\r\n\x1b[6;H"
teardown: "\x1b[2K" "\r" "\x1b[?2004l" "\x1b[?25h" … "\x1b[?1049l" "\x1b[?25h"
```

確認できた性質:

- フレームは `r.out.Write(buf.Bytes())` の**単一 Write** で出力され、末尾は必ず `CSI {linesRendered};H`（駐車）。
- `tea.WithOutput` に渡した writer は `termenv.NewOutput` で包まれるが `termenv.Output.Write` は素通し（`o.tty.Write(p)`）なので、全バイト列を観測・追記できる。
- レンダラー経由の端末書き込みはすべて `r.mtx` で直列化されており、追記が別フレームと混ざることはない。

### 3.2 検証済み: 出力ラッパーによる CUP 注入（採用案）

`WithOutput` に `io.Writer` ラッパーを渡し、各 Write の直後に `\x1b[?25h\x1b[{row};{col}H` を追記する。フレーム末尾の駐車 CUP が先に実行され、その直後に追加分が効くため、**結果としてハードウェアカーソルは常にキャレット位置に残る**。PoC で注入シーケンスが全書き込みの最後尾に来ることを確認済み。

キャレット座標はモデルが計算して共有変数（atomic）に書き、ラッパーが読むだけ。値渡しの `Update` との間でロックは不要。

## 4. 実現方式の比較

| 方式 | 実現性 | 保守性・リスク |
|---|---|---|
| **A. 出力ラッパーで CUP 注入**（採用） | v0.24.2 のまま動作。PoC 済み | 差分は `internal/inbox` 内に閉じる。座標計算を viewList と共有化する必要あり。ラッパーは「全 Write に追記」するだけで renderer の内部構造（駐車位置・単一 Write）に非依存 |
| B. `tea.ShowCursor` のみ | 位置を変えられない。カーソルが最終行頭に出るだけで IME アンカーは左下のまま | 不可 |
| C. View に CUP を埋め込む | フレーム末尾の駐車 CUP が必ず上書きする | 不可 |
| D. 入力行を最終行に並べ替え | 駐車位置が入力行先頭に来るが列は 0 固定。IME は追従するがキャレットではなくプロンプト頭 | 部分改善のみ。不採用 |
| E. カスタムレンダラー注入/フォーク | `renderer` インターフェースは非公開で注入口なし。`WithoutRenderer` + 自前レンダラーは全再実装 | 保守コスト大。不採用 |
| F. `tea.ExecProcess` で外部プロンプト | レンダラー停止中は端末が通常行編集となり IME 追従は自然に動く | TUI を抜ける UX 断絶。設計変更が大きい。不採用（最終 fallback としては有効） |
| G. Bubble Tea v2 移行 | v2 の `tea.View.Cursor`（`tea.NewCursor(x,y)`）で正式サポート。cursed renderer 採用 | `View() string` → `View() tea.View` の全面書き換え（inbox + `internal/tui`）。別 Issue 規模の移行判断。長期の正攻法だが #160 では扱わない |

## 5. 推奨設計（Issue #160 向け）

### 5.1 構成

```text
Run(m Model)
  caret := NewCaretTracker()                 // atomic に row/col/armed を保持
  m.opts.Caret = caret                       // Options に追加（nil = 無効・headless 既定）
  tea.NewProgram(m, tea.WithAltScreen(),
                 tea.WithOutput(caret.Wrap(os.Stdout)))
```

- `CaretTracker.Wrap(w io.Writer) io.Writer`: armed 中は各 `Write(p)` の後に `\x1b[?25h` + `CSI {row};{col}H` を同一 Write 相当で追記する。**書き込まれたバイト列に `\x1b[?1049l`（ExitAltScreen）を検出したら自動 disarm** する — モデル終了後の teardown 書き込みに追記が混ざると、メイン画面復帰後にカーソルが画面中段に残る（PoC で実測）。
- モデル側は `modeInput` 中のみ、Update 後に `caret.Arm(row, col)`、それ以外で `caret.Disarm()`。`Update` が値レシーバのため `Caret` はポインタ共有にする。

### 5.2 キャレット座標の計算

`viewList()` と同じ行構成から 1 始まりの絶対行を求める。

```text
row = 1(header) + 1(summary) + (needsEnable ? 1 : 0)
    + 1(rule) + 1(columnHeader) + bodyH + 1(rule) + logPaneHeight + 1(rule) + 1
col = 1 + cellWidth(prompt) + cellWidth(string(text[:cursor]))
```

- `cellWidth` は `lipgloss.Width` ベースで既存。全角混在は rune 数ではなく表示セル幅で数える（bubbles#906 と同じ落とし穴を避ける）。
- `bodyH` は `bodyHeightFor(footerLines + logPaneHeight)`、`footerLines` は `footer()` の実行数 — **viewList と同一の式を共有関数に切り出し**、二重管理によるずれを防ぐ。
- `tooSmall()` 時・modeInput 以外・行が描画高を超える場合は `ok=false` → disarm。
- renderer が高さ超過フレームの先頭行を捨てる場合（`flush()` の `newLines` 切り落とし）は、その分だけ row を減じる。通常は `tooSmall` ゲートで到達しない。

### 5.3 仮想カーソル（反転ブロック）の扱い

armed 時はハードウェアカーソルが実在するため、`renderInputLine()` の反転表示は二重カーソルになる。`Caret != nil` のときは反転ブロックを描かず平文 + 実カーソルにし、nil（headless・将来の非対応経路）では現行の反転表示を維持する。

### 5.4 適用範囲

`modeInput` を使う全プロンプト（`s` / `t` / `x` / `n`）に一律適用する。共通の `inputState` 経路なので個別対応は不要（#160 の open question への回答）。

## 6. Issue #160 の受入条件・TDD テスト方針

#160 の受入条件を以下で具体化する。

### 追加する受入条件

- [ ] `CaretTracker`（仮称）が armed 中に各 Write の末尾へ `?25h` + `CSI {row};{col}H` を追記し、disarm 中は追記しない
- [ ] 書き込みストリーム中の `?1049l` 検出で自動 disarm する（終了時にメイン画面へカーソルが残らない）
- [ ] キャレット座標が `viewList` と同一レイアウト式から計算され、全角文字は表示セル幅で数える
- [ ] armed 時の入力行は反転ブロックを描かず、disarmed/headless では従来通り反転表示する

### TDD ファーストステップ（この順で Red→Green）

1. `caret_test.go`（新規）: `CaretTracker.Wrap` に偽フレーム `"...\x1b[6;H"` を Write し、出力末尾が `\x1b[?25h\x1b[{row};{col}H` であることを断言。armed/disarm/`?1049l` 通過後の 3 ケース。
2. `inbox_test.go`: `s` 押下後の `m.caretPosition()` が期待 row/col を返す（prompt のみ・`text:あいう` で col +6・カーソル左移動・`tooSmall`・modeList で `ok=false`）。
3. mode 遷移で tracker の armed 状態が変わること（`s`→armed、`esc`/`enter`→disarmed）。Cmd の中身ではなく tracker 状態を見る。
4. `renderInputLine` が `Caret != nil` 時に反転シーケンスを含まないことを断言（生成文字列に `\x1b[7m` がないこと。出力文字列の断言でありソース grep ではない）。
5. シェル側: `LEAD_TEST_INBOX_KEYS` 経路は RunHeadless（tea.Program 非経由）のため escape 検証は Go 側に閉じ込める。実機確認は PR 前に `lead inbox` を起動し `s` で IME 変換して候補位置を目視確認し、結果を PR 本文に記録（AGENTS.md の実機確認ルール）。

## 7. 残課題・将来方針

- **Bubble Tea v2 移行**: v2.0.0（"The Cursed" / cellbuf renderer）では `View` が `tea.View` 構造体を返し `Cursor *tea.Cursor` で正式にカーソル位置を指定できる。Bubbles の textinput/textarea も `Cursor()` + `SetVirtualCursor(false)` 対応済み。本 RFC のラッパー方式は v1 系での局所対応であり、v2 移行時に削除して `tea.View.Cursor` へ置き換えるのが正攻法（移行は別 Issue で判断）。
- **マウス/リサイズ**: `WindowSizeMsg` ごとに座標は再計算されるため追加対応不要。`ExecProcess`（`e` の $EDITOR）は modeInput と同時に成立しないため干渉しない。
- **他 TUI（`internal/tui` selector 等）**: テキスト入力を持たないため対象外。今後入力 UI を増やす場合は `CaretTracker` を再利用できる。

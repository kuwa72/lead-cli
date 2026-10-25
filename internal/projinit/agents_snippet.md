## lead-flow（`/lead-flow <issue-number>`）

このプロジェクトでは `lead` を使った Issue 駆動開発フローを採用している。ユーザーが `/lead-flow <issue-number>` で作業を指示したら、以下を実行する。

1. `lead run <issue-number>` でブランチ・作業状態を作成する。
2. TDD：まず失敗するテストを追加し Red を確認してから実装し、Green を確認する。
3. `./test/run-tests.sh`（または `go test ./...`、プロジェクト固有のテストコマンド）をすべてパスさせる。
4. `git push -u origin $(git branch --show-current)` でブランチを push する。
5. `gh pr create --fill` で PR を作成する。
6. `bin/ci-wait <pr-number>`（または `gh pr checks`）で CI が pass するまで待つ。
7. CI 通過後に `lead review <pr-number>` で独立レビューを実行する。VERDICT: pass なら次へ進む。VERDICT: needs-work なら所見に従って修正し、最大2周まで再レビューする（3周目はしない）。2周で通らなければ、所見を `gh issue comment <issue-number> --body` に貼って終了する。
8. `lead finish <issue-number> --merge` でマージと Issue クローズまで行う（`gh pr merge` を直接実行しない。規約ファイル保護とリスク分級のガードレールを通すため）。
   - `risk:a` は `review:pass` がある場合のみ自動マージする。
   - `risk:b` とラベルなしは `awaiting-human` になり、人間が `--approve` で承認する。
9. 関連 Issue が closed になったことを確認する。

停止時の扱い：

- `--approve`、`--no-review`、`--override-guardrail` は人間専用（human-only）のフラグで、エージェントは付けない。
- `finish` が規約ファイル保護・レビュー未実施（`review:pass` なし）・承認待ち（`risk:b` / ラベルなし）で停止したら、自分で `gh pr merge` や `gh issue close` をせず、状況を `gh issue comment <issue-number> --body` に書いて終了する。

ルール：
- `main` には直接 push しない。`issue/<number>-<slug>` ブランチで作業する。
- 1 Issue に原則 1 ブランチ/PR とし、Issue 側で明示がない限り分割しない。
- `AGENTS.md`、`.agents/`、`.claude/`、`.devin/` などのプロジェクト規約ファイルは、当該 Issue で変更が明示されていない限り編集しない。

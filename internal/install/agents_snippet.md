## lead-flow（`/lead-flow <issue-number>`）

このプロジェクトでは `lead` を使った Issue 駆動開発フローを採用している。ユーザーが `/lead-flow <issue-number>` で作業を指示したら、以下を実行する。

1. `lead work <issue-number>` でブランチ・作業状態を作成する。
2. TDD：まず失敗するテストを追加し Red を確認してから実装し、Green を確認する。
3. `./test/run-tests.sh`（または `go test ./...`、プロジェクト固有のテストコマンド）をすべてパスさせる。
4. `git push -u origin $(git branch --show-current)` でブランチを push する。
5. `gh pr create --fill` で PR を作成する。
6. `bin/ci-wait <pr-number>`（または `gh pr checks`）で CI が pass するまで待つ。
7. `gh pr merge --squash`（または `lead finish <issue-number> --merge`）でマージする。
8. 関連 Issue が closed になったことを確認する。

ルール：
- `main` には直接 push しない。`issue/<number>-<slug>` ブランチで作業する。
- 1 Issue に原則 1 ブランチ/PR とし、Issue 側で明示がない限り分割しない。
- `AGENTS.md`、`.claude/`、`.devin/` などのプロジェクト規約ファイルは、当該 Issue で変更が明示されていない限り編集しない。

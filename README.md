# lead

`lead` is an Issue-driven coding orchestrator: an approved GitHub Issue becomes a branch, a TDD implementation, a PR, CI wait, and a squash merge.

Without a subcommand, `lead` opens the inbox (docs/rfc-inbox-ux.md §5). The inbox is the human gate: you approve an issue to start a headless coding agent.

## Inbox

`lead` needs an interactive terminal. Run `--help` or a subcommand when not in a TTY.

Sections:

- レビュー待ち (`needs-review`): issues waiting for human approval
- 止まってる (`blocked`): agents that failed or are stuck
- 最近マージ (`merged`, collapsible): recently merged issues for staging verification
- 実行中 (`running`, collapsible): currently running agents

### Keys

| Key | Action |
|---|---|
| `a` | approve: `needs-review` → `ready` |
| `e` | edit body with `$EDITOR`, then approve |
| `x` | reject and close the issue |
| `t` | add a comment; spec or impl agent will re-run |
| `p` | peek at the agent screen with herdr or `$PAGER` |
| `n` | report real-machine NG and spawn a follow-up issue |
| `r` | open `AGENTS.md` in `$EDITOR` |
| `s` | create a new `needs-review` issue (direct, or `!one-liner` for AI spec) |
| `o` | open the selected issue in a browser |
| `m` | cycle agent dispatch mode (`batch` / `dangerous` / `interactive`) |
| `g` | cycle active agent (`agy` / `claude` / `codex` / `devin` / etc.) |
| `J`/`K` | scroll preview pane down/up |
| `Enter` | show the full issue body / PR diff |
| `z` | toggle section expand/collapse |
| `R` | refresh |
| `?` | show help |
| `q` | quit |

## Subcommands

| Command | Purpose |
|---|---|
| `lead run [n]` | start an issue with an interactive agent (`--mode implement/review`, interactive picker without `n`) |
| `lead lgtm <n>` | add `lgtm` label and post LGTM comment |
| `lead unlgtm <n>` | remove `lgtm` label |
| `lead dispatch` | hand `ready` issues to headless agents |
| `lead status` | show active workflows |
| `lead say <one-liner>` | create `needs-review` issues from a one-liner |
| `lead finish <n>` | wait CI, squash-merge the PR, and close the issue |
| `lead clean <n>` | remove the workflow worktree and state |
| `lead init` | install the `AGENTS.md` management block |
| `lead setup` | interactive environment setup |
| `lead doctor` | diagnose `gh`, agents, herdr, and branch protection |
| `lead version` | show version |

## Install and setup

```sh
# from a clone
bin/install-local
# or from a release
curl -fsSL https://raw.githubusercontent.com/kuwa72/lead-cli/main/install.sh | sh
lead setup --write
```

`lead init` adds the `AGENTS.md` rule block; `lead setup --write` installs shell completion and a keybinding.

## Conventions

- `AGENTS.md` is the single entry point for project conventions.
- Work follows the issue-driven TDD, branch-per-PR, CI-wait, squash-merge flow in `AGENTS.md`.
- The full inbox UX spec is in `docs/rfc-inbox-ux.md`.
- Self-hosting and dogfooding workflow is documented in `docs/selfhost.md`.

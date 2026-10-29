# lead

`lead` is an Issue-driven coding orchestrator: an approved GitHub Issue becomes a branch, a TDD implementation, a PR, CI wait, and a squash merge.

Without a subcommand, `lead` opens the inbox (docs/rfc-inbox-ux.md §5). The inbox is the human gate: you approve an issue to start a headless coding agent.

## Inbox

`lead` needs an interactive terminal. Run `--help` or a subcommand when not in a TTY.

Sections:

- レビュー待ち (`needs-review`): issues waiting for human approval
- 止まってる (`blocked`): agents that failed or are stuck; `lead resume <issue>` reconnects to the existing branch and worktree
- 最近マージ (`merged`, collapsible): recently merged issues for staging verification
- 実行中 (`running`, collapsible): currently running agents

### Keys

| Key | Action |
|---|---|
| `a` | approve: `needs-review` → `ready` |
| `e` | edit body with `$EDITOR`, then approve |
| `x` | reject and close the issue |
| `t` | add a comment; spec or impl agent will re-run |
| `p` | peek at the agent log with `$PAGER` |
| `n` | report real-machine NG and spawn a follow-up issue |
| `r` | open `AGENTS.md` in `$EDITOR` |
| `s` | create a new `needs-review` issue (direct, or `!one-liner` for AI spec) |
| `,` / `S` | open Settings view (configure Issue creation default, agent, mode) |
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
| `lead resume <issue\|branch\|pr>` | reconnect to existing work without creating a new branch or PR |
| `lead lgtm <n>` | add `lgtm` label and post LGTM comment |
| `lead unlgtm <n>` | remove `lgtm` label |
| `lead dispatch` | hand `ready` issues to headless agents |
| `lead status` | show active workflows |
| `lead stats` | aggregate recorded runs (success rate, duration) |
| `lead say <one-liner>` | create `needs-review` issues from a one-liner |
| `lead new` | create an issue from a validated template (`--ready` enqueues it directly) |
| `lead ready <n>` | validate an existing issue body and move it to `ready` |
| `lead finish <n>` | wait CI, squash-merge the PR, and close the issue |
| `lead postmerge <n>` | re-run the post-merge smoke; revert main and file an incident when it fails |
| `lead review <pr>` | run an independent headless review; label `review:pass` / `review:needs-work` |
| `lead scenario` | store issue scenarios outside the worktree; generate and run throwaway tests (`add` / `list` / `run`) |
| `lead clean <n>` | remove the workflow worktree and state |
| `lead peek <n>` | open the running agent log with `$PAGER` |
| `lead stop <n>` | stop a running agent (kill, clean up, record interruption) |
| `lead server` | run the headless socket API daemon |
| `lead api <schema\|snapshot\|call>` | query the socket API |
| `lead enable` | enable this repository (`AGENTS.md` management block, lead-flow skill to `.agents/skills` + detected agents' skills dirs, generated-path ignores in `.git/info/exclude`) |
| `lead disable` | disable this repository (remove lead-flow skill from all known skills dirs and the managed ignore block) |
| `lead setup` | set up your user environment (shell completions, keybinding) |
| `lead completion <shell>` | print a completion script (bash, zsh, fish, powershell) |
| `lead doctor` | diagnose `gh`, agents, and branch protection |
| `lead update` | replace this binary with the latest release (direct installs only) |
| `lead version` | show version |

## Creating issues

`lead new` opens `$EDITOR` with a template and validates the body before
creating anything. `lead ready <n>` applies the same validation to an existing
issue and then swaps `needs-review` for `ready`.

A body passes when it has all of:

- a `## 受入条件` section with at least one `- [ ]` item
- at least one executable acceptance condition in that section: a
  backtick-quoted command (`` `./test/run-tests.sh` ``) or a test path
  (`test/`, `*_test.go`, `*.spec.*`, …)
- a `## スコープ外` section
- a `## シナリオ` (`## Scenarios`) section with at least one step that contains
  a backtick-quoted command or a test path (the executable check is the same
  as the acceptance condition)
- a risk label: `risk:a` or `risk:b` (`lead new --risk`, default `b`; for
  `lead ready` the label must already be on the issue)

On failure the command creates or relabels nothing, lists every missing item,
and exits 1.

```sh
lead new                       # $EDITOR; labels risk:b + needs-review
lead new --risk a --ready      # labels risk:a + ready (skips needs-review)
lead new --body-file body.md   # non-interactive; title from the first heading
lead ready 254                 # validate #254 and move it to ready
```

Without `--body-file`, `lead new` needs an interactive terminal; non-TTY runs
fail with a hint (the same rule as the inbox).

## Install and setup

```sh
# from a clone
bin/install-local
# or from a release
curl -fsSL https://raw.githubusercontent.com/kuwa72/lead-cli/main/install.sh | sh
lead setup --write          # user environment ($HOME only)
lead enable                 # this repository (skill + AGENTS.md block)
```

`lead setup --write` sets up your user environment (shell completion and a keybinding, `$HOME` only); `lead enable` enables this repository (the `AGENTS.md` rule block and the lead-flow skill, written to `.agents/skills/lead-flow` plus the skills directory of each agent detected on `PATH` or configured in `inbox-config.json`).

### Generated files and `git status`

`lead enable` offers to add the paths it generates to the repository's local git exclude file (`.git/info/exclude`, resolved with `git rev-parse --git-path info/exclude`):

```
.worktrees/
.agents/skills/lead-flow/
.claude/skills/lead-flow/
...
```

This keeps `git status` clean after enabling: `.worktrees/` (used by `lead run --worktree` and `lead dispatch`) and the lead-flow skill copies no longer appear as untracked. The prompt defaults to yes and `--yes` applies it without asking. Decline if the project wants to commit its skills; the command prints the entries and a hint so the choice is explicit. `lead disable` removes the managed block and leaves foreign exclude entries untouched.

## Configuration

Settings are stored in `inbox-config.json` next to the state file
(`~/.local/state/lead/inbox-config.json` by default; `$XDG_STATE_HOME` and
`$LEAD_STATE_FILE` move it). The file is normally written by the inbox
Settings view (`,` / `S`), but can be edited by hand.

Keys, types, defaults, and the environment variables that override them are
documented in [docs/config.md](docs/config.md).

## Merge gate (computed, no approval)

`lead finish <n> --merge` merges when three checks pass (issue #278):

1. CI is green — `skipping`, `fail`, and `cancel` are failures; `neutral`
   keeps waiting and times out. `./test/run-tests.sh` must pass.
2. The generated scenarios recorded by `lead review <pr>` are all passing
   (`SCENARIOS: n/n`). No scenario configured skips this layer. If a
   scenario file exists but no review result was recorded, finish pauses
   instead of merging.
3. The PR does not touch protected convention files (`AGENTS.md`, `.github/`,
   `.claude/`, …).

There is no approval step and no `risk:*` label gate any more; `review:pass`
is advisory (it decides nothing on its own). When a scenario or protected
path stops an attempted merge (`--merge` or the auto path), `lead finish`
exits 0, prints the reason, and comments it on the issue. A failing CI check
is an error (exit 1) and is not posted as a pause.

`--override-guardrail` is the only human-only flag: it is the one way past the
protected-file guardrail. An unattended agent must never pass it.

Run `lead review <pr>` when you want the generated scenario tests to run;
`lead dispatch` keeps that step in its completion definition.

## Conventions

- `AGENTS.md` is the single entry point for project conventions.
- Work follows the issue-driven TDD, branch-per-PR, CI-wait, squash-merge flow in `AGENTS.md`.
- The full inbox UX spec is in `docs/rfc-inbox-ux.md`.
- Self-hosting and dogfooding workflow is documented in `docs/selfhost.md`.

---
name: lead-flow
description: "Drive a GitHub Issue through the lead issue-driven TDD workflow. Trigger with `/lead-flow <issue-number>`."
---

# /lead-flow

This skill tells you how to work on a GitHub Issue using `lead`.

## Trigger

`/lead-flow <issue-number>`

If the issue number is omitted, ask the user for one or run `lead run` to open the picker.

## Steps

1. Start the lead workflow:

   ```bash
   lead run <issue-number>
   ```

   If you need a specific agent, add `--agent <name>`.

2. Apply TDD:
   - Add a failing test (or a failing test script) that demonstrates the missing behavior.
   - Run it to confirm Red.
   - Implement the smallest change that makes it Green.
   - Refactor, keeping tests passing.

3. Run the full local test suite:

   ```bash
   ./test/run-tests.sh
   ```

   If the project has no `test/run-tests.sh`, use `go test ./...` or the project-specific test command.

4. Push the branch and open a PR:

   ```bash
   git push -u origin $(git branch --show-current)
   gh pr create --fill
   ```

5. Wait for CI to pass:

   ```bash
   bin/ci-wait <pr-number>
   ```

   If `bin/ci-wait` is not available, poll `gh pr checks` until all pass.

6. Run the independent review after CI passes:

   ```bash
   lead review <pr-number>
   ```

   Fix until `SCENARIOS: n/m` is m/m. There is no two-round cap; the diff
   verdict is advisory. If the scenario cannot be made to pass, post the
   findings to the issue with `gh issue comment <issue-number> --body` and stop.

7. Merge and close through `lead finish` (it enforces the computed gate):

   ```bash
   lead finish <issue-number> --merge
   ```

   CI pass, every generated scenario passing, and no protected files are the
   only conditions. There is no approval step and no `risk:*` gate; a
   `review:pass` is advisory. Run `lead review <pr>` to generate and run the
   scenario tests; the comment records `SCENARIOS: n/m`.

   `--override-guardrail` is the only human-only flag: it is the one way past
   the protected-file guardrail. Never pass it from an agent, and do not run
   `gh pr merge` yourself.

8. Confirm the related Issue is closed. If `finish` stopped (protected files or
   a failing scenario), do not run `gh pr merge` or `gh issue close` yourself:
   comment the situation on the issue with
   `gh issue comment <issue-number> --body` and stop.

9. Report the PR URL and merge commit to the user.

## Rules

- Work on the `issue/<number>-<slug>` branch. Do not push to `main`.
- Do not edit `AGENTS.md`, `.agents/`, `.claude/`, `.devin/`, or other project rule files unless the current issue explicitly requires it.
- One issue per branch/PR unless the issue says otherwise.
- If a command asks for a password or TTY, stop and ask the user.

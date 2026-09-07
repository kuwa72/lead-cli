package server

// Schema documents the socket API (served by `lead api schema`).
// Machine-readable (--json unified); hand-maintained beside the ops above.
const apiSchema = `{
  "version": 1,
  "transport": "unix socket, one JSON request per connection",
  "ops": [
    {"name": "snapshot", "args": {}, "desc": "workflows, staged prompts, notifications"},
    {"name": "issues.list", "args": {}, "desc": "open issues (number, title)"},
    {"name": "work.dispatch", "args": {"issue": 36, "mode?": "implement", "branch?": "name", "part?": "slug", "worktree?": "path|auto", "agent?": "agy"}, "desc": "branch + worktree + state record (idempotent)"},
    {"name": "prompt.stage", "args": {"issue": 36, "prompt": "text"}, "desc": "stage a prompt for human review; never auto-sent (no send op by design)"},
    {"name": "ci.status", "args": {"pr": 7}, "desc": "check rows plus all_pass"},
    {"name": "notify", "args": {"message": "text"}, "desc": "append a notification (cap 100)"}
  ]
}
`

// Schema returns the API definition.
func Schema() string { return apiSchema + "\n" }

package ghcli

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kuwa72/lead-cli/internal/ports"
	"github.com/kuwa72/lead-cli/internal/testutil"
)

// specServeBody answers the spec-AI calls (issue #94).
const specServeBody = `if [ "$1 $2" = "issue create" ]; then
  echo "Creating issue in o/r"; echo "https://github.com/o/r/issues/123"
elif [ "$1 $2" = "issue view" ] && [ "$5" = "comments" ]; then
  printf '{"comments":[{"author":{"login":"kuwa72"},"body":"first","createdAt":"2026-09-08T00:00:00Z"},{"author":{"login":"bot"},"body":"second","createdAt":"2026-09-08T01:00:00Z"}]}'
elif [ "$1 $2" = "issue edit" ]; then
  cat "$7" > "$(dirname "$0")/edit-body.txt"
else
  echo "unexpected: $@" >&2; exit 3
fi`

func TestIssueCreate_ParsesNumberFromURL(t *testing.T) {
	logPath := testutil.InstallDummy(t, "gh", specServeBody)
	ref, err := New().IssueCreate(context.Background(), "feat: t", "line1\nline2", []string{"needs-review"})
	if err != nil {
		t.Fatalf("IssueCreate: %v", err)
	}
	if ref.Number != 123 || ref.URL != "https://github.com/o/r/issues/123" {
		t.Errorf("ref = %+v", ref)
	}
	want := []string{"<issue>", "<create>", "<--title>", "<feat: t>", "<--body>", "<line1", "line2>", "<--label>", "<needs-review>"}
	got := testutil.LogLines(t, logPath)
	if len(got) != len(want) {
		t.Fatalf("argv lines = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("argv[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestIssueCreate_RejectsUnparseableOutput(t *testing.T) {
	testutil.InstallDummy(t, "gh", `echo "no url here"`)
	if _, err := New().IssueCreate(context.Background(), "t", "b", nil); err == nil {
		t.Error("unparseable output accepted")
	}
}

func TestIssueComments_DecodesAuthorBodyCreatedAt(t *testing.T) {
	logPath := testutil.InstallDummy(t, "gh", specServeBody)
	got, err := New().IssueComments(context.Background(), 7)
	if err != nil {
		t.Fatalf("IssueComments: %v", err)
	}
	if len(got) != 2 || got[0].Author != "kuwa72" || got[0].Body != "first" || got[0].CreatedAt != "2026-09-08T00:00:00Z" || got[1].Author != "bot" {
		t.Errorf("comments = %+v", got)
	}
	log := testutil.LogText(t, logPath)
	for _, want := range []string{"<issue>", "<view>", "<7>", "<--json>", "<comments>"} {
		if !strings.Contains(log, want) {
			t.Errorf("gh args log missing %q, got:\n%s", want, log)
		}
	}
}

func TestIssueEdit_UsesBodyFile(t *testing.T) {
	logPath := testutil.InstallDummy(t, "gh", specServeBody)
	if err := New().IssueEdit(context.Background(), 7, "new title", "new\nbody"); err != nil {
		t.Fatalf("IssueEdit: %v", err)
	}
	got := testutil.LogLines(t, logPath)
	if len(got) != 7 || got[0] != "<issue>" || got[1] != "<edit>" || got[2] != "<7>" || got[3] != "<--title>" || got[4] != "<new title>" || got[5] != "<--body-file>" {
		t.Fatalf("argv = %q", got)
	}
	captured, err := os.ReadFile(filepath.Join(filepath.Dir(logPath), "edit-body.txt"))
	if err != nil {
		t.Fatalf("dummy did not capture body file: %v", err)
	}
	if string(captured) != "new\nbody" {
		t.Errorf("body file = %q", captured)
	}
	if _, err := os.Stat(strings.Trim(got[6], "<>")); !os.IsNotExist(err) {
		t.Errorf("temp body file not removed: %v", err)
	}
}

// ghServeBody serves canned issue JSON for list/view like the real `gh`.
const ghServeBody = `if [ "$1 $2" = "issue list" ]; then
  printf '[{"number":50,"title":"Tracking issue"},{"number":36,"title":"ports adapter"}]'
elif [ "$1 $2" = "issue view" ]; then
  printf '{"number":36,"title":"ports adapter","body":"hello body","state":"OPEN"}'
else
  echo "unexpected: $@" >&2; exit 3
fi`

func TestListMergedSince_CallsGhIssueListClosed(t *testing.T) {
	body := `if [ "$1 $2" = "issue list" ] && [ "$4" = "closed" ]; then
  printf '[{"number":42,"title":"feat: done","closedAt":"2026-09-08T10:30:00Z"}]'
else
  echo "unexpected: $@" >&2; exit 3
fi`
	logPath := testutil.InstallDummy(t, "gh", body)

	got, err := New().ListMergedSince(context.Background(), time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("ListMergedSince: %v", err)
	}
	if len(got) != 1 || got[0].Number != 42 || got[0].Title != "feat: done" {
		t.Fatalf("ListMergedSince = %+v, want one merged issue 42", got)
	}
	want := time.Date(2026, 9, 8, 10, 30, 0, 0, time.UTC)
	if !got[0].MergedAt.Equal(want) {
		t.Errorf("MergedAt = %v, want %v", got[0].MergedAt, want)
	}

	log := testutil.LogText(t, logPath)
	for _, want := range []string{
		"<issue>", "<list>", "<--state>", "<closed>",
		"<--limit>", "<300>", "<--json>", "<number,title,closedAt>",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("gh args log missing %q, got:\n%s", want, log)
		}
	}
}

func TestListMergedSince_FiltersBySince(t *testing.T) {
	body := `printf '[{"number":1,"title":"old","closedAt":"2026-09-08T09:00:00Z"},{"number":2,"title":"new","closedAt":"2026-09-08T11:00:00Z"}]'`
	testutil.InstallDummy(t, "gh", body)

	got, err := New().ListMergedSince(context.Background(), time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("ListMergedSince: %v", err)
	}
	if len(got) != 1 || got[0].Number != 2 {
		t.Errorf("ListMergedSince = %+v, want issue 2", got)
	}
}

func TestListOpen_CallsGhWithExpectedArgs(t *testing.T) {
	logPath := testutil.InstallDummy(t, "gh", ghServeBody)

	got, err := New().ListOpen(context.Background())
	if err != nil {
		t.Fatalf("ListOpen: %v", err)
	}
	if len(got) != 2 || got[0].Number != 50 || got[0].Title != "Tracking issue" ||
		got[1].Number != 36 || got[1].Title != "ports adapter" {
		t.Fatalf("ListOpen = %+v, want issues 50 and 36 with titles", got)
	}

	log := testutil.LogText(t, logPath)
	for _, want := range []string{
		"<issue>", "<list>", "<--state>", "<open>",
		"<--limit>", "<300>", "<--json>", "<number,title>",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("gh args log missing %q, got:\n%s", want, log)
		}
	}
}

func TestListOpen_LimitConfiguration(t *testing.T) {
	logPath := testutil.InstallDummy(t, "gh", ghServeBody)

	// Custom limit on Client
	client := &Client{Limit: 250}
	if _, err := client.ListOpen(context.Background()); err != nil {
		t.Fatalf("ListOpen: %v", err)
	}
	log := testutil.LogText(t, logPath)
	if !strings.Contains(log, "<--limit>") || !strings.Contains(log, "<250>") {
		t.Errorf("expected limit 250 in log, got:\n%s", log)
	}

	// Environment variable override
	t.Setenv("LEAD_ISSUE_LIMIT", "500")
	clientEnv := New()
	if _, err := clientEnv.ListOpen(context.Background()); err != nil {
		t.Fatalf("ListOpen: %v", err)
	}
	logEnv := testutil.LogText(t, logPath)
	if !strings.Contains(logEnv, "<500>") {
		t.Errorf("expected limit 500 from env in log, got:\n%s", logEnv)
	}
}

func TestView_CallsGhViewWithJSONFields(t *testing.T) {
	logPath := testutil.InstallDummy(t, "gh", ghServeBody)

	got, err := New().View(context.Background(), 36)
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if got.Number != 36 || got.Title != "ports adapter" || got.Body != "hello body" || got.State != "OPEN" {
		t.Fatalf("View = %+v, want number/title/body/state", got)
	}

	log := testutil.LogText(t, logPath)
	for _, want := range []string{
		"<issue>", "<view>", "<36>", "<--json>", "<number,title,body,state>",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("gh args log missing %q, got:\n%s", want, log)
		}
	}
}

func TestListOpen_MissingGhReturnsTypedError(t *testing.T) {
	testutil.EmptyBin(t) // no gh on PATH

	_, err := New().ListOpen(context.Background())
	if err == nil {
		t.Fatal("ListOpen with no gh = nil error, want typed not-found error")
	}
	if !ports.IsBinaryNotFound(err) {
		t.Fatalf("ListOpen error = %v (%T), want BinaryNotFoundError for graceful fallback", err, err)
	}
}

func TestView_PropagatesGhFailure(t *testing.T) {
	testutil.InstallDummy(t, "gh", "echo \"issue not found\" >&2; exit 1")

	_, err := New().View(context.Background(), 999)
	if err == nil {
		t.Fatal("View with failing gh = nil error, want propagation")
	}
	if ports.IsBinaryNotFound(err) {
		t.Fatalf("View error = %v, must not be classified as missing binary", err)
	}
	if !strings.Contains(err.Error(), "issue not found") {
		t.Errorf("View error = %q, want gh stderr surfaced", err)
	}
}

// prServeBody serves canned PR/CI JSON like the real `gh`.
const prServeBody = `if [ "$1 $2" = "pr checks" ]; then
  printf '[{"bucket":"pass","name":"test","state":"SUCCESS"},{"bucket":"pending","name":"lint","state":"PENDING"}]'
elif [ "$1 $2" = "pr view" ]; then
  if echo "$*" | grep -q 'body'; then
    printf '{"body":"## Acceptance\\n- [ ] task\\n"}'
  else
    printf '{"number":7,"state":"OPEN","mergeable":"MERGEABLE","mergeStateStatus":"CLEAN"}'
  fi
elif [ "$1 $2" = "pr merge" ]; then
  printf '{"number":7,"state":"MERGED"}'
elif [ "$1 $2" = "issue close" ]; then
  :
elif [ "$1 $2" = "issue comment" ]; then
  :
elif [ "$1" = "api" ]; then
  printf 'true'
else
  echo "unexpected: $@" >&2; exit 3
fi`

func TestPrChecks_CallsGhWithExpectedArgs(t *testing.T) {
	logPath := testutil.InstallDummy(t, "gh", prServeBody)

	got, err := New().PrChecks(context.Background(), 7)
	if err != nil {
		t.Fatalf("PrChecks: %v", err)
	}
	if len(got) != 2 || got[0].Name != "test" || got[0].Bucket != "pass" ||
		got[1].Name != "lint" || got[1].Bucket != "pending" {
		t.Fatalf("PrChecks = %+v, want parsed buckets", got)
	}
	log := testutil.LogText(t, logPath)
	for _, want := range []string{"<pr>", "<checks>", "<7>", "<--json>", "<bucket,name,state>"} {
		if !strings.Contains(log, want) {
			t.Errorf("gh args log missing %q, got:\n%s", want, log)
		}
	}
}

func TestPrInfo_CallsGhPrView(t *testing.T) {
	logPath := testutil.InstallDummy(t, "gh", prServeBody)

	got, err := New().PrInfo(context.Background(), 7)
	if err != nil {
		t.Fatalf("PrInfo: %v", err)
	}
	if got.Number != 7 || got.State != "OPEN" || got.Mergeable != "MERGEABLE" {
		t.Fatalf("PrInfo = %+v, want number/state/mergeable", got)
	}
	log := testutil.LogText(t, logPath)
	for _, want := range []string{"<pr>", "<view>", "<7>", "<--json>", "<number,state,mergeable,mergeStateStatus>"} {
		if !strings.Contains(log, want) {
			t.Errorf("gh args log missing %q, got:\n%s", want, log)
		}
	}
}

func TestPrBody_CallsGhWithExpectedArgs(t *testing.T) {
	logPath := testutil.InstallDummy(t, "gh", prServeBody)

	body, err := New().PrBody(context.Background(), 7)
	if err != nil {
		t.Fatalf("PrBody: %v", err)
	}
	if !strings.Contains(body, "## Acceptance") || !strings.Contains(body, "- [ ] task") {
		t.Errorf("body = %q, want Acceptance checklist", body)
	}

	if lines := testutil.LogLines(t, logPath); !reflect.DeepEqual(lines, []string{"<pr>", "<view>", "<7>", "<--json>", "<body>"}) {
		t.Errorf("gh argv = %q", lines)
	}
}

func TestPrBody_DecodesBody(t *testing.T) {
	testutil.InstallDummy(t, "gh", `printf '{"body":"- [ ] task\\n"}'`)

	body, err := New().PrBody(context.Background(), 5)
	if err != nil {
		t.Fatalf("PrBody: %v", err)
	}
	if body != "- [ ] task\n" {
		t.Errorf("body = %q, want '- [ ] task\\n'", body)
	}
}

func TestPrMerge_UsesSquashAndDeleteBranch(t *testing.T) {
	logPath := testutil.InstallDummy(t, "gh", prServeBody)

	if err := New().PrMerge(context.Background(), 7); err != nil {
		t.Fatalf("PrMerge: %v", err)
	}
	log := testutil.LogText(t, logPath)
	for _, want := range []string{"<pr>", "<merge>", "<7>", "<--squash>", "<--delete-branch>"} {
		if !strings.Contains(log, want) {
			t.Errorf("gh args log missing %q, got:\n%s", want, log)
		}
	}
}

func TestRepoAllowsAutoMerge_ParsesAPIBoolean(t *testing.T) {
	logPath := testutil.InstallDummy(t, "gh", prServeBody)

	ok, err := New().RepoAllowsAutoMerge(context.Background(), "o/r")
	if err != nil || !ok {
		t.Fatalf("RepoAllowsAutoMerge = %v, %v; want true, nil", ok, err)
	}
	log := testutil.LogText(t, logPath)
	for _, want := range []string{"<api>", "<repos/o/r>", "<--jq>", "<.allow_auto_merge>"} {
		if !strings.Contains(log, want) {
			t.Errorf("gh args log missing %q, got:\n%s", want, log)
		}
	}
}

// protectionServeBody answers the repository-side gate calls (issue #67).
// PROT_MODE=404 makes the classic protection endpoint fail like gh does
// for an unprotected branch; PROT_MODE=none also empties the rulesets.
const protectionServeBody = `if [ "$1 $2" = "pr view" ]; then
  printf 'AGENTS.md\n.github/workflows/ci.yml\ninternal/x.go\n'
elif [ "$1" = "api" ]; then
  case "$2" in
    repos/o/r) printf 'main\n' ;;
    repos/o/r/branches/main/protection)
      if [ -n "$PROT_MODE" ]; then echo "gh: Branch not protected (HTTP 404)" >&2; exit 1; fi
      printf '{"required_status_checks":{"contexts":["test"],"checks":[{"context":"test"},{"context":"lint"}]},"required_pull_request_reviews":{"required_approving_review_count":0}}' ;;
    repos/o/r/rules/branches/main)
      if [ "$PROT_MODE" = "none" ]; then printf '[]'; else printf '[{"type":"deletion"},{"type":"pull_request","parameters":{}},{"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"lint"},{"context":"e2e"}]}}]'; fi ;;
    *) echo "unexpected api path: $2" >&2; exit 3 ;;
  esac
else
  echo "unexpected: $@" >&2; exit 3
fi`

func TestPrFiles_CallsGhPrViewFiles(t *testing.T) {
	logPath := testutil.InstallDummy(t, "gh", protectionServeBody)
	got, err := New().PrFiles(context.Background(), 7)
	if err != nil {
		t.Fatalf("PrFiles: %v", err)
	}
	want := []string{"AGENTS.md", ".github/workflows/ci.yml", "internal/x.go"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("PrFiles = %q, want %q", got, want)
	}
	if lines := testutil.LogLines(t, logPath); !reflect.DeepEqual(lines, []string{"<pr>", "<view>", "<7>", "<--json>", "<files>", "<--jq>", "<.files[].path>"}) {
		t.Errorf("gh argv = %q", lines)
	}
}

func TestRepoDefaultBranch_UsesRepoAPI(t *testing.T) {
	logPath := testutil.InstallDummy(t, "gh", protectionServeBody)
	got, err := New().RepoDefaultBranch(context.Background(), "o/r")
	if err != nil || got != "main" {
		t.Fatalf("RepoDefaultBranch = %q, %v; want main", got, err)
	}
	if lines := testutil.LogLines(t, logPath); !reflect.DeepEqual(lines, []string{"<api>", "<repos/o/r>", "<--jq>", "<.default_branch>"}) {
		t.Errorf("gh argv = %q", lines)
	}
}

func TestBranchProtection_MergesClassicAndRulesets(t *testing.T) {
	logPath := testutil.InstallDummy(t, "gh", protectionServeBody)
	got, err := New().BranchProtection(context.Background(), "o/r", "main")
	if err != nil {
		t.Fatalf("BranchProtection: %v", err)
	}
	want := ports.BranchProtection{Protected: true, RequiresPR: true, RequiredChecks: []string{"test", "lint", "e2e"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("BranchProtection = %+v, want %+v", got, want)
	}
	log := testutil.LogText(t, logPath)
	for _, want := range []string{"<repos/o/r/branches/main/protection>", "<repos/o/r/rules/branches/main>"} {
		if !strings.Contains(log, want) {
			t.Errorf("gh argv log missing %q:\n%s", want, log)
		}
	}
}

func TestBranchProtection_404FallsBackToRulesets(t *testing.T) {
	t.Setenv("PROT_MODE", "404")
	testutil.InstallDummy(t, "gh", protectionServeBody)
	got, err := New().BranchProtection(context.Background(), "o/r", "main")
	if err != nil {
		t.Fatalf("BranchProtection: %v (404 must not be an error)", err)
	}
	want := ports.BranchProtection{Protected: true, RequiresPR: true, RequiredChecks: []string{"lint", "e2e"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("BranchProtection = %+v, want rulesets only %+v", got, want)
	}
}

func TestBranchProtection_UnprotectedWhenBothEmpty(t *testing.T) {
	t.Setenv("PROT_MODE", "none")
	testutil.InstallDummy(t, "gh", protectionServeBody)
	got, err := New().BranchProtection(context.Background(), "o/r", "main")
	if err != nil {
		t.Fatalf("BranchProtection: %v", err)
	}
	if got.Protected || got.RequiresPR || len(got.RequiredChecks) != 0 {
		t.Errorf("BranchProtection = %+v, want unprotected", got)
	}
}

func TestBranchProtection_PropagatesNon404Failure(t *testing.T) {
	testutil.InstallDummy(t, "gh", `echo "gh: Must have admin rights (HTTP 403)" >&2; exit 1`)
	_, err := New().BranchProtection(context.Background(), "o/r", "main")
	if err == nil || !strings.Contains(err.Error(), "HTTP 403") {
		t.Errorf("err = %v, want 403 surfaced", err)
	}
}

func TestIssueClose_AndComment(t *testing.T) {
	logPath := testutil.InstallDummy(t, "gh", prServeBody)

	if err := New().IssueClose(context.Background(), 36); err != nil {
		t.Fatalf("IssueClose: %v", err)
	}
	if err := New().IssueComment(context.Background(), 36, "done"); err != nil {
		t.Fatalf("IssueComment: %v", err)
	}
	log := testutil.LogText(t, logPath)
	for _, want := range []string{"<issue>", "<close>", "<36>", "<comment>", "<--body>", "<done>"} {
		if !strings.Contains(log, want) {
			t.Errorf("gh args log missing %q, got:\n%s", want, log)
		}
	}
}

func TestAuthStatus_AndApiUser(t *testing.T) {
	body := "if [ \"$1 $2\" = \"auth status\" ]; then :\n" +
		"elif [ \"$1 $2\" = \"api user\" ]; then printf 'octocat'\n" +
		"else echo \"unexpected: $@\" >&2; exit 3\nfi"
	logPath := testutil.InstallDummy(t, "gh", body)

	if err := New().AuthStatus(context.Background()); err != nil {
		t.Fatalf("AuthStatus: %v", err)
	}
	login, err := New().ApiUser(context.Background())
	if err != nil || login != "octocat" {
		t.Fatalf("ApiUser = %q, %v; want octocat, nil", login, err)
	}
	log := testutil.LogText(t, logPath)
	for _, want := range []string{"<auth>", "<status>", "<api>", "<user>", "<--jq>", "<.login>"} {
		if !strings.Contains(log, want) {
			t.Errorf("gh args log missing %q, got:\n%s", want, log)
		}
	}
}

func TestAuthStatus_PropagatesFailure(t *testing.T) {
	testutil.InstallDummy(t, "gh", "echo 'not logged in' >&2; exit 1")

	if err := New().AuthStatus(context.Background()); err == nil {
		t.Fatal("AuthStatus on failing gh = nil, want error")
	}
}

func TestLatestReleaseTag(t *testing.T) {
	body := "if [ \"$1\" = \"api\" ]; then printf 'v0.2.0'\n" +
		"else echo \"unexpected: $@\" >&2; exit 3\nfi"
	logPath := testutil.InstallDummy(t, "gh", body)

	got, err := New().LatestReleaseTag(context.Background(), "o/r")
	if err != nil || got != "v0.2.0" {
		t.Fatalf("LatestReleaseTag = %q, %v; want v0.2.0, nil", got, err)
	}
	log := testutil.LogText(t, logPath)
	for _, want := range []string{"<api>", "<repos/o/r/releases/latest>", "<--jq>", "<.tag_name>"} {
		if !strings.Contains(log, want) {
			t.Errorf("gh args log missing %q, got:\n%s", want, log)
		}
	}
}

func TestIssueEditBody_FeedsBodyOnStdin(t *testing.T) {
	logPath := testutil.InstallDummy(t, "gh", `cat > "$(dirname "$0")/stdin.txt"`)
	testutil.ClearLog(t, logPath)

	body := "-- starts with dashes --\nline two\n"
	if err := New().IssueEditBody(context.Background(), 7, body); err != nil {
		t.Fatalf("IssueEditBody: %v", err)
	}
	log := testutil.LogText(t, logPath)
	if want := "<issue>\n<edit>\n<7>\n<--body-file>\n<->\n"; log != want {
		t.Errorf("gh argv = %q, want %q", log, want)
	}
	got, err := os.ReadFile(filepath.Join(filepath.Dir(logPath), "stdin.txt"))
	if err != nil {
		t.Fatalf("dummy gh did not capture stdin: %v", err)
	}
	if string(got) != body {
		t.Errorf("stdin body = %q, want %q", got, body)
	}
}

func TestBrowseIssue_OpensWebView(t *testing.T) {
	logPath := testutil.InstallDummy(t, "gh", ":")
	testutil.ClearLog(t, logPath)

	if err := New().BrowseIssue(context.Background(), 36); err != nil {
		t.Fatalf("BrowseIssue: %v", err)
	}
	log := testutil.LogText(t, logPath)
	for _, want := range []string{"<issue>", "<view>", "<36>", "<--web>"} {
		if !strings.Contains(log, want) {
			t.Errorf("gh args log missing %q, got:\n%s", want, log)
		}
	}
}

func TestPrDiff_CallsGhWithExpectedArgs(t *testing.T) {
	body := "if [ \"$1 $2 $3\" = \"pr diff 42\" ]; then printf 'diff --git a/foo.go b/foo.go\\n+line'\n" +
		"else echo \"unexpected: $@\" >&2; exit 3\nfi"
	logPath := testutil.InstallDummy(t, "gh", body)
	testutil.ClearLog(t, logPath)

	got, err := New().PrDiff(context.Background(), 42)
	if err != nil {
		t.Fatalf("PrDiff: %v", err)
	}
	want := "diff --git a/foo.go b/foo.go\n+line"
	if got != want {
		t.Errorf("PrDiff = %q, want %q", got, want)
	}
	log := testutil.LogText(t, logPath)
	for _, wantArg := range []string{"<pr>", "<diff>", "<42>"} {
		if !strings.Contains(log, wantArg) {
			t.Errorf("gh args log missing %q, got:\n%s", wantArg, log)
		}
	}
}


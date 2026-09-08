package ghcli

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/kuwa72/lead-cli/internal/ports"
	"github.com/kuwa72/lead-cli/internal/testutil"
)

// ghServeBody serves canned issue JSON for list/view like the real `gh`.
const ghServeBody = `if [ "$1 $2" = "issue list" ]; then
  printf '[{"number":50,"title":"Tracking issue"},{"number":36,"title":"ports adapter"}]'
elif [ "$1 $2" = "issue view" ]; then
  printf '{"number":36,"title":"ports adapter","body":"hello body","state":"OPEN"}'
else
  echo "unexpected: $@" >&2; exit 3
fi`

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
		"<--limit>", "<50>", "<--json>", "<number,title>",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("gh args log missing %q, got:\n%s", want, log)
		}
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
  printf '{"number":7,"state":"OPEN","mergeable":"MERGEABLE","mergeStateStatus":"CLEAN"}'
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

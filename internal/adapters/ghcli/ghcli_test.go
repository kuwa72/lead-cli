package ghcli

import (
	"context"
	"os"
	"path/filepath"
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

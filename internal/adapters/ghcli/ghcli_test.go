package ghcli

import (
	"context"
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

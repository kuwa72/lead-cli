package herdr

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/kuwa72/lead-cli/internal/ports"
	"github.com/kuwa72/lead-cli/internal/testutil"
)

// mkDummyHerdr installs a dummy `herdr` serving splitJSON for `pane split`.
func mkDummyHerdr(t *testing.T, splitJSON string) string {
	t.Helper()
	return testutil.InstallDummy(t, "herdr",
		`if [ "$1 $2" = "pane split" ]; then printf '%s' '`+splitJSON+`'; elif [ "$1 $2" = "pane send-text" ]; then :; else echo "unexpected: $@" >&2; exit 3; fi`)
}

func TestSplit_CallsPaneSplitAndParsesPaneID(t *testing.T) {
	logPath := mkDummyHerdr(t, `{"result":{"pane":{"pane_id":"pane-123"}}}`)

	got, err := New().Split(context.Background(), ports.DirectionRight, 0.5)
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	if got != "pane-123" {
		t.Fatalf("Split = %q, want pane-123 parsed from herdr JSON", got)
	}

	log := testutil.LogText(t, logPath)
	for _, want := range []string{
		"<pane>", "<split>", "<--direction>", "<right>", "<--ratio>", "<0.5>",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("herdr args log missing %q, got:\n%s", want, log)
		}
	}
}

func TestSendText_PreparesCommandWithoutAutoSend(t *testing.T) {
	logPath := mkDummyHerdr(t, `{}`)

	err := New().SendText(context.Background(), "pane-123", `agy -i "hello"`)
	if err != nil {
		t.Fatalf("SendText: %v", err)
	}

	log := testutil.LogText(t, logPath)
	for _, want := range []string{"<pane>", "<send-text>", "<pane-123>", "<agy -i \"hello\">"} {
		if !strings.Contains(log, want) {
			t.Errorf("herdr args log missing %q, got:\n%s", want, log)
		}
	}
	// Human-in-the-loop: the command must be prepared for review, never
	// auto-sent via `pane run` (which appends Enter).
	if strings.Contains(log, "<run>") {
		t.Errorf("must not invoke `herdr pane run` (auto-send); got:\n%s", log)
	}
}

func TestSplit_EmptyPaneIDFails(t *testing.T) {
	mkDummyHerdr(t, `{"result":{}}`)

	_, err := New().Split(context.Background(), ports.DirectionRight, 0.5)
	if err == nil {
		t.Fatal("Split with empty pane_id = nil error, want failure")
	}
}

func TestSplit_MissingHerdrReturnsTypedError(t *testing.T) {
	testutil.EmptyBin(t) // no herdr on PATH

	_, err := New().Split(context.Background(), ports.DirectionRight, 0.5)
	if err == nil {
		t.Fatal("Split with no herdr = nil error, want typed not-found error")
	}
	if !ports.IsBinaryNotFound(err) {
		t.Fatalf("Split error = %v (%T), want BinaryNotFoundError for graceful fallback", err, err)
	}
}

func TestNewRunner_SelectsInlineWhenHerdrEnvUnset(t *testing.T) {
	t.Setenv("HERDR_ENV", "")
	mkDummyHerdr(t, `{"result":{"pane":{"pane_id":"pane-123"}}}`)

	r := NewRunner()
	if _, ok := r.(*InlineRunner); !ok {
		t.Fatalf("NewRunner with HERDR_ENV unset = %T, want *InlineRunner fallback", r)
	}
}

func TestNewRunner_SelectsInlineWhenHerdrMissing(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	testutil.EmptyBin(t) // no herdr on PATH

	r := NewRunner()
	if _, ok := r.(*InlineRunner); !ok {
		t.Fatalf("NewRunner with herdr missing = %T, want *InlineRunner fallback", r)
	}
}

func TestNewRunner_SelectsHerdrRunnerWhenAvailable(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	mkDummyHerdr(t, `{"result":{"pane":{"pane_id":"pane-123"}}}`)

	r := NewRunner()
	runner, ok := r.(*Runner)
	if !ok {
		t.Fatalf("NewRunner with HERDR_ENV=1 and herdr present = %T, want *Runner", r)
	}
	id, err := runner.Split(context.Background(), ports.DirectionRight, 0.5)
	if err != nil || id != "pane-123" {
		t.Fatalf("Split via selected runner = %q, %v; want pane-123, nil", id, err)
	}
}

func TestInlineRunner_SurfacesCommandForReview(t *testing.T) {
	var buf bytes.Buffer
	in := &InlineRunner{Out: &buf}

	id, err := in.Split(context.Background(), ports.DirectionRight, 0.5)
	if err != nil {
		t.Fatalf("Inline Split: %v", err)
	}
	if id != InlinePaneID {
		t.Fatalf("Inline Split = %q, want %q sentinel", id, InlinePaneID)
	}
	if err := in.SendText(context.Background(), id, `agy -i "hello"`); err != nil {
		t.Fatalf("Inline SendText: %v", err)
	}
	if !strings.Contains(buf.String(), `agy -i "hello"`) {
		t.Errorf("Inline SendText wrote %q, want agent command surfaced for review", buf.String())
	}
}

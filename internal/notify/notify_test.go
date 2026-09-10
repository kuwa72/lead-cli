package notify_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/kuwa72/lead-cli/internal/notify"
	"github.com/kuwa72/lead-cli/internal/testutil"
)

func TestOSNotifier_CallsNotifySendWhenAvailable(t *testing.T) {
	logPath := testutil.InstallDummy(t, "notify-send", ":")
	testutil.ClearLog(t, logPath)

	var buf bytes.Buffer
	n := notify.New(notify.Options{Out: &buf})

	if err := n.Notify("Task Done", "PR created"); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	log := testutil.LogText(t, logPath)
	if !strings.Contains(log, "<Task Done>") || !strings.Contains(log, "<PR created>") {
		t.Fatalf("notify-send args missing title/message, got:\n%s", log)
	}
}

func TestOSNotifier_FallbackToTerminalBellWhenNoDesktopTool(t *testing.T) {
	testutil.EmptyBin(t) // no notify-send or osascript

	var buf bytes.Buffer
	n := notify.New(notify.Options{Out: &buf})

	if err := n.Notify("Task Done", "PR created"); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "\a") {
		t.Errorf("expected terminal bell in output, got %q", out)
	}
	if !strings.Contains(out, "Task Done") {
		t.Errorf("expected title in OSC sequence, got %q", out)
	}
}

func TestOSNotifier_DisabledByEnv(t *testing.T) {
	logPath := testutil.InstallDummy(t, "notify-send", ":")
	testutil.ClearLog(t, logPath)

	t.Setenv("LEAD_NOTIFY", "0")
	var buf bytes.Buffer
	n := notify.New(notify.Options{Out: &buf})

	if err := n.Notify("Task Done", "PR created"); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	if log := testutil.LogText(t, logPath); log != "" {
		t.Fatalf("expected no notify-send when disabled, got:\n%s", log)
	}
	if buf.Len() != 0 {
		t.Fatalf("expected no terminal output when disabled, got %q", buf.String())
	}
}

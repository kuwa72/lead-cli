package logtail

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func writeLog(t *testing.T, lines int) string {
	t.Helper()
	var b strings.Builder
	for i := 1; i <= lines; i++ {
		b.WriteString("line " + strconv.Itoa(i) + "\n")
	}
	p := filepath.Join(t.TempDir(), "agent.log")
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTailReturnsLastLines(t *testing.T) {
	p := writeLog(t, 10)
	got := Tail(p, 3, 1<<20)
	if got != "line 8\nline 9\nline 10\n" {
		t.Errorf("Tail = %q, want last 3 lines", got)
	}
}

func TestTailCapsBytes(t *testing.T) {
	p := writeLog(t, 100)
	got := Tail(p, 1000, 100)
	if len(got) > 100 {
		t.Errorf("Tail len = %d, want within 100 bytes", len(got))
	}
	if !strings.HasSuffix(got, "line 100\n") {
		t.Errorf("Tail must end at the log end, got %q", got)
	}
}

func TestTailMissingFileIsEmpty(t *testing.T) {
	if got := Tail(filepath.Join(t.TempDir(), "missing.log"), 30, 8192); got != "" {
		t.Errorf("Tail missing = %q, want empty", got)
	}
	if got := Tail("", 30, 8192); got != "" {
		t.Errorf("Tail empty path = %q, want empty", got)
	}
}

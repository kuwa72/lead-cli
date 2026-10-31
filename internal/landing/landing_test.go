package landing

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteListClear(t *testing.T) {
	dir := t.TempDir()
	st := State{Issue: 302, PR: 303, Stage: StageReview, PID: 42, StartedAt: time.Now()}
	if err := Write(dir, st); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := List(dir)
	if err != nil || len(got) != 1 {
		t.Fatalf("List = %+v, %v; want one", got, err)
	}
	if got[0].Issue != 302 || got[0].Stage != StageReview || got[0].PR != 303 {
		t.Errorf("state = %+v", got[0])
	}
	if _, err := os.Stat(Path(dir, 302)); err != nil {
		t.Errorf("file missing: %v", err)
	}
	if err := Clear(dir, 302); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if got, _ := List(dir); len(got) != 0 {
		t.Errorf("List after Clear = %+v, want empty", got)
	}
	if err := Clear(dir, 302); err != nil {
		t.Errorf("Clear(missing) = %v, want nil", err)
	}
}

func TestListIgnoresCorruptAndForeignFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(Dir(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"1.json":     "{broken",
		"note.txt":   "ignore",
		"2.json.tmp": `{"issue":2}`,
	} {
		if err := os.WriteFile(filepath.Join(Dir(dir), name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := List(dir)
	if err != nil || len(got) != 0 {
		t.Fatalf("List = %+v, %v; want empty", got, err)
	}
}

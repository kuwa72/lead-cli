package inbox

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSeenStore_LoadSaveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := &SeenStore{Path: filepath.Join(dir, "inbox-seen.json")}

	st, err := s.Load()
	if err != nil {
		t.Fatalf("Load missing: %v", err)
	}
	if !st.LastSeenAt.IsZero() || len(st.Confirmed) != 0 {
		t.Errorf("new state not empty: %+v", st)
	}

	st.LastSeenAt = time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	st.Confirmed = []int{12, 34}
	if err := s.Save(st); err != nil {
		t.Fatalf("Save: %v", err)
	}

	st2, err := s.Load()
	if err != nil {
		t.Fatalf("Load saved: %v", err)
	}
	if !st2.LastSeenAt.Equal(st.LastSeenAt) {
		t.Errorf("LastSeenAt = %v, want %v", st2.LastSeenAt, st.LastSeenAt)
	}
	if !reflect.DeepEqual(st2.Confirmed, []int{12, 34}) {
		t.Errorf("Confirmed = %v, want [12 34]", st2.Confirmed)
	}

	// Confirm idempotency and sorting.
	if err := s.Confirm(56); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	st3, _ := s.Load()
	if !reflect.DeepEqual(st3.Confirmed, []int{12, 34, 56}) {
		t.Errorf("Confirmed after confirm = %v, want [12 34 56]", st3.Confirmed)
	}
}

func TestSeenStore_CorruptRefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	s := &SeenStore{Path: filepath.Join(dir, "inbox-seen.json")}
	if err := os.WriteFile(s.Path, []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Load(); err == nil {
		t.Fatal("corrupt file accepted")
	} else if !strings.Contains(err.Error(), "corrupt") {
		t.Errorf("error = %q, want 'corrupt'", err.Error())
	}

	if err := s.Confirm(1); err == nil {
		t.Error("Confirm over corrupt file = nil, want refusal")
	}
	raw, _ := os.ReadFile(s.Path)
	if string(raw) != "{broken" {
		t.Errorf("corrupt file overwritten: %q", raw)
	}
}

func TestSeenStore_ResolveSeenPath(t *testing.T) {
	t.Setenv("LEAD_STATE_FILE", "/tmp/custom/wf.json")
	if got := ResolveSeenPath(); got != "/tmp/custom/inbox-seen.json" {
		t.Errorf("ResolveSeenPath = %q, want /tmp/custom/inbox-seen.json", got)
	}
}

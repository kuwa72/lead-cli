// Package landing records the in-flight landing station (CI wait -> review ->
// finish) outside workflows.json, so `lead status` and the inbox can show it
// while the implementation agent is already gone (issue #304).
package landing

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Stages of the landing station.
const (
	StageCI     = "ci"
	StageReview = "review"
	StageFinish = "finish"
)

// State is one issue's in-flight landing.
type State struct {
	Issue     int       `json:"issue"`
	PR        int       `json:"pr"`
	Stage     string    `json:"stage"`
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
}

// Dir returns the landing state directory under the lead state dir.
func Dir(stateDir string) string { return filepath.Join(stateDir, "landing") }

// Path returns one issue's landing file.
func Path(stateDir string, issue int) string {
	return filepath.Join(Dir(stateDir), fmt.Sprintf("%d.json", issue))
}

// Write stores the current stage atomically (temp file + rename).
func Write(stateDir string, s State) error {
	if err := os.MkdirAll(Dir(stateDir), 0o755); err != nil {
		return fmt.Errorf("landing: mkdir: %w", err)
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("landing: encode: %w", err)
	}
	f, err := os.CreateTemp(Dir(stateDir), ".landing-*.tmp")
	if err != nil {
		return fmt.Errorf("landing: temp: %w", err)
	}
	tmp := f.Name()
	if _, err := f.Write(raw); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("landing: write: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("landing: close: %w", err)
	}
	if err := os.Rename(tmp, Path(stateDir, s.Issue)); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("landing: rename: %w", err)
	}
	return nil
}

// Clear removes one issue's landing state. Missing is not an error.
func Clear(stateDir string, issue int) error {
	if err := os.Remove(Path(stateDir, issue)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("landing: clear: %w", err)
	}
	return nil
}

// List returns the landing states, ordered by issue number.
func List(stateDir string) ([]State, error) {
	entries, err := os.ReadDir(Dir(stateDir))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("landing: list: %w", err)
	}
	var out []State
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if _, err := strconv.Atoi(strings.TrimSuffix(e.Name(), ".json")); err != nil {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(Dir(stateDir), e.Name()))
		if err != nil {
			continue
		}
		var s State
		if err := json.Unmarshal(raw, &s); err != nil || s.Issue <= 0 {
			continue
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Issue < out[j].Issue })
	return out, nil
}

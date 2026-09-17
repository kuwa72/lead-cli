package inbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"
)

// LoadConfig reads an inbox-config.json file. A missing file yields the
// zero Config (all defaults); malformed JSON is an error naming the file.
// This is the shared loader for every headless launch path (inbox,
// `lead say`, `lead dispatch`, issue #215).
func LoadConfig(path string) (Config, error) {
	var c Config
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return c, nil
		}
		return c, fmt.Errorf("inbox-config: %w", err)
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, fmt.Errorf("inbox-config: %s: %w", path, err)
	}
	return c, nil
}

// StallTimeoutOr parses the stall_timeout setting (a Go duration string
// like "30m") and returns def when unset. Non-positive values disable the
// stall check, matching dispatch's StuckAfter semantics.
func (c Config) StallTimeoutOr(def time.Duration) (time.Duration, error) {
	return durationOr(c.StallTimeout, "stall_timeout", def)
}

// MaxRuntimeOr parses the max_runtime setting (a Go duration string like
// "3h") and returns def when unset. Pass 0 as def to leave the wall-clock
// check disabled (`lead say` has no runtime cap by default).
func (c Config) MaxRuntimeOr(def time.Duration) (time.Duration, error) {
	return durationOr(c.MaxRuntime, "max_runtime", def)
}

func durationOr(raw, key string, def time.Duration) (time.Duration, error) {
	if raw == "" {
		return def, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("inbox-config: %s: %w", key, err)
	}
	return d, nil
}

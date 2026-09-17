package inbox

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadConfig_MissingFileIsZeroConfig(t *testing.T) {
	cfg, err := LoadConfig(filepath.Join(t.TempDir(), "inbox-config.json"))
	if err != nil {
		t.Fatalf("missing file: %v", err)
	}
	if cfg.Agent != "" || cfg.StallTimeout != "" || cfg.MaxRuntime != "" {
		t.Errorf("cfg = %+v, want zero", cfg)
	}
}

func TestLoadConfig_ReadsStallSettings(t *testing.T) {
	p := filepath.Join(t.TempDir(), "inbox-config.json")
	data := `{"agent":"claude","agent_mode":"batch","notify_disabled":true,"stall_timeout":"45m","max_runtime":"2h"}`
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent != "claude" || cfg.AgentMode != "batch" || !cfg.NotifyDisabled {
		t.Errorf("cfg = %+v", cfg)
	}
	stall, err := cfg.StallTimeoutOr(30 * time.Minute)
	if err != nil || stall != 45*time.Minute {
		t.Errorf("StallTimeoutOr = %s, %v", stall, err)
	}
	maxRun, err := cfg.MaxRuntimeOr(3 * time.Hour)
	if err != nil || maxRun != 2*time.Hour {
		t.Errorf("MaxRuntimeOr = %s, %v", maxRun, err)
	}
}

func TestLoadConfig_InvalidJSONIsAnError(t *testing.T) {
	p := filepath.Join(t.TempDir(), "inbox-config.json")
	if err := os.WriteFile(p, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(p); err == nil {
		t.Error("invalid JSON accepted")
	}
}

func TestConfig_DurationDefaultsAndErrors(t *testing.T) {
	var zero Config
	stall, err := zero.StallTimeoutOr(30 * time.Minute)
	if err != nil || stall != 30*time.Minute {
		t.Errorf("empty stall_timeout = %s, %v; want default 30m", stall, err)
	}
	maxRun, err := zero.MaxRuntimeOr(0)
	if err != nil || maxRun != 0 {
		t.Errorf("empty max_runtime = %s, %v; want 0 (disabled)", maxRun, err)
	}
	bad := Config{StallTimeout: "soon", MaxRuntime: "later"}
	if _, err := bad.StallTimeoutOr(time.Minute); err == nil {
		t.Error("invalid stall_timeout accepted")
	}
	if _, err := bad.MaxRuntimeOr(0); err == nil {
		t.Error("invalid max_runtime accepted")
	}
	// Non-positive values disable the check (same semantics as dispatch).
	off := Config{StallTimeout: "-1s"}
	stall, err = off.StallTimeoutOr(30 * time.Minute)
	if err != nil || stall != -time.Second {
		t.Errorf("negative stall_timeout = %s, %v", stall, err)
	}
}

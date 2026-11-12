package agent

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLauncher_StdinPassedToInteractiveAgent(t *testing.T) {
	binDir := t.TempDir()
	script := filepath.Join(binDir, "agy")
	body := `#!/bin/sh
echo "ARGS:$*"
input=$(cat)
echo "STDIN:$input"
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	l := &Launcher{
		LookPath: func(name string) (string, error) {
			return filepath.Join(binDir, name), nil
		},
		Stdin:  strings.NewReader("interactive reply\n"),
		Stdout: &stdout,
		Stderr: &stderr,
		Dir:    binDir,
	}

	if err := l.LaunchForMode(context.Background(), "agy", "boot prompt", "interactive"); err != nil {
		t.Fatalf("LaunchForMode = %v (stderr=%s)", err, stderr.String())
	}
	got := stdout.String()
	if !strings.Contains(got, "ARGS:-i boot prompt") {
		t.Errorf("stdout = %q, want ARGS:-i boot prompt", got)
	}
	if !strings.Contains(got, "STDIN:interactive reply") {
		t.Errorf("stdout = %q, want stdin forwarded to child process", got)
	}
}

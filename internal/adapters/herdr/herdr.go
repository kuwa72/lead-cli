// Package herdr implements ports.HerdrRunner via the `herdr` CLI,
// plus an InlineRunner fallback for environments without Herdr.
package herdr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/kuwa72/lead-cli/internal/ports"
)

// InlinePaneID is the sentinel pane ID returned by InlineRunner.Split.
const InlinePaneID = "inline"

// Runner calls `herdr pane split/send-text`. Zero value is usable.
type Runner struct {
	// Bin is the herdr binary name or path. Empty means "herdr".
	Bin string
	// LookPath resolves the binary; defaults to exec.LookPath.
	LookPath func(name string) (string, error)
}

// New returns a Runner with defaults.
func New() *Runner { return &Runner{} }

var _ ports.HerdrRunner = (*Runner)(nil)

func (r *Runner) bin() string {
	if r.Bin != "" {
		return r.Bin
	}
	return "herdr"
}

func (r *Runner) lookPath() func(string) (string, error) {
	if r.LookPath != nil {
		return r.LookPath
	}
	return exec.LookPath
}

func (r *Runner) run(ctx context.Context, args ...string) ([]byte, error) {
	bin := r.bin()
	if _, err := r.lookPath()(bin); err != nil {
		return nil, &ports.BinaryNotFoundError{Binary: bin}
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return nil, fmt.Errorf("herdr %s: %w: %s", strings.Join(args, " "), err, msg)
		}
		return nil, fmt.Errorf("herdr %s: %w", strings.Join(args, " "), err)
	}
	return stdout.Bytes(), nil
}

// Split runs `herdr pane split --direction <dir> --ratio <ratio>`
// and parses the new pane ID from `.result.pane.pane_id`.
func (r *Runner) Split(ctx context.Context, dir ports.Direction, ratio float64) (string, error) {
	if dir == "" {
		dir = ports.DirectionRight
	}
	if dir != ports.DirectionRight && dir != ports.DirectionDown {
		return "", fmt.Errorf("herdr pane split: invalid direction %q", dir)
	}
	out, err := r.run(ctx, "pane", "split",
		"--direction", string(dir),
		"--ratio", strconv.FormatFloat(ratio, 'f', -1, 64))
	if err != nil {
		return "", err
	}
	var parsed struct {
		Result struct {
			Pane struct {
				PaneID string `json:"pane_id"`
			} `json:"pane"`
		} `json:"result"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &parsed); err != nil {
		return "", fmt.Errorf("herdr pane split: decode JSON: %w", err)
	}
	if parsed.Result.Pane.PaneID == "" {
		return "", fmt.Errorf("herdr pane split: empty .result.pane.pane_id in %q", strings.TrimSpace(string(out)))
	}
	return parsed.Result.Pane.PaneID, nil
}

// SendText runs `herdr pane send-text <paneID> <text>`.
// The text is prepared in the pane for human review; it is NOT
// auto-sent (that would require `pane run`, which is never invoked).
func (r *Runner) SendText(ctx context.Context, paneID string, text string) error {
	if paneID == "" {
		return fmt.Errorf("herdr pane send-text: empty pane ID")
	}
	_, err := r.run(ctx, "pane", "send-text", paneID, text)
	return err
}

// Peek opens an agent log in a new herdr tab, or moves an existing pane
// into a new tab. Issue #103.
//
// When logPath is set it is preferred: `herdr tab create --focus` creates a
// focused tab, then `herdr pane run <pane> tail -f <log>` streams the log.
// When only pane is set, `herdr pane move <pane> --new-tab --focus` opens the
// existing pane in a new focused tab.
func (r *Runner) Peek(ctx context.Context, logPath, pane string) error {
	if logPath != "" {
		out, err := r.run(ctx, "tab", "create", "--focus")
		if err != nil {
			return err
		}
		rootPane, err := parseTabCreateRootPane(out)
		if err != nil {
			return err
		}
		_, err = r.run(ctx, "pane", "run", rootPane, "tail", "-f", logPath)
		return err
	}
	if pane != "" {
		_, err := r.run(ctx, "pane", "move", pane, "--new-tab", "--focus")
		if err != nil {
			if strings.Contains(err.Error(), "pane_not_found") {
				return &ports.PaneNotFoundError{Pane: pane}
			}
			return err
		}
		return nil
	}
	return fmt.Errorf("herdr peek: logPath and pane are both empty")
}

func parseTabCreateRootPane(out []byte) (string, error) {
	var parsed struct {
		Result struct {
			RootPane struct {
				PaneID string `json:"pane_id"`
			} `json:"root_pane"`
		} `json:"result"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &parsed); err != nil {
		return "", fmt.Errorf("herdr tab create: decode JSON: %w", err)
	}
	if parsed.Result.RootPane.PaneID == "" {
		return "", fmt.Errorf("herdr tab create: empty .result.root_pane.pane_id in %q", strings.TrimSpace(string(out)))
	}
	return parsed.Result.RootPane.PaneID, nil
}

// InlineRunner is the fallback when Herdr is unavailable
// (HERDR_ENV unset or herdr missing): the agent command is surfaced
// on Out (default os.Stdout) for the user to review and run inline.
type InlineRunner struct {
	Out io.Writer
}

var _ ports.HerdrRunner = (*InlineRunner)(nil)

func (r *InlineRunner) out() io.Writer {
	if r.Out != nil {
		return r.Out
	}
	return os.Stdout
}

// Split has no pane to create inline; it returns the InlinePaneID sentinel.
func (r *InlineRunner) Split(ctx context.Context, dir ports.Direction, ratio float64) (string, error) {
	return InlinePaneID, nil
}

// SendText surfaces the prepared command for human review.
func (r *InlineRunner) SendText(ctx context.Context, paneID string, text string) error {
	_, err := fmt.Fprintln(r.out(), text)
	return err
}

// Peek is not supported by the inline fallback.
func (r *InlineRunner) Peek(ctx context.Context, logPath, pane string) error {
	return &ports.BinaryNotFoundError{Binary: "herdr"}
}

// Available reports whether the Herdr path can be used:
// HERDR_ENV=1 and a `herdr` binary on PATH (legacy bin/hgf condition).
func Available() bool {
	return os.Getenv("HERDR_ENV") == "1" && herdrOnPath()
}

func herdrOnPath() bool {
	_, err := exec.LookPath("herdr")
	return err == nil
}

// NewRunner selects the Herdr path when available, else the inline fallback.
func NewRunner() ports.HerdrRunner {
	if Available() {
		return New()
	}
	return &InlineRunner{}
}

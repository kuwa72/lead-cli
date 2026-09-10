package notify

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// Notifier delivers user-facing notifications for agent milestones.
type Notifier interface {
	Notify(title, message string) error
}

// Options configures the notification behavior.
type Options struct {
	Disabled bool
	Out      io.Writer
}

// OSNotifier implements desktop and terminal notifications.
type OSNotifier struct {
	opts Options
}

// New creates a new OSNotifier with the given options.
func New(opts Options) *OSNotifier {
	if opts.Out == nil {
		opts.Out = os.Stderr
	}
	return &OSNotifier{opts: opts}
}

// IsDisabled reports whether notifications are disabled by options or environment.
func (n *OSNotifier) IsDisabled() bool {
	if n.opts.Disabled {
		return true
	}
	val := strings.ToLower(strings.TrimSpace(os.Getenv("LEAD_NOTIFY")))
	if val == "0" || val == "false" || val == "off" || val == "no" {
		return true
	}
	return false
}

// Notify triggers desktop notification if available, otherwise falls back to terminal bell/OSC.
func (n *OSNotifier) Notify(title, message string) error {
	if n.IsDisabled() {
		return nil
	}

	// 1. Try notify-send (Linux / BSD)
	if path, err := exec.LookPath("notify-send"); err == nil && path != "" {
		cmd := exec.Command(path, title, message)
		if err := cmd.Run(); err == nil {
			return nil
		}
	}

	// 2. Try osascript (macOS)
	if runtime.GOOS == "darwin" {
		if path, err := exec.LookPath("osascript"); err == nil && path != "" {
			script := fmt.Sprintf("display notification %q with title %q", message, title)
			cmd := exec.Command(path, "-e", script)
			if err := cmd.Run(); err == nil {
				return nil
			}
		}
	}

	// 3. Fallback to terminal OSC 777 / OSC 9 and bell
	if n.opts.Out != nil {
		// OSC 777 (widely supported by modern terminal emulators like Ghostty, WezTerm, Foot, Kitty)
		fmt.Fprintf(n.opts.Out, "\x1b]777;notify;%s;%s\x1b\\", title, message)
		// OSC 9 (iTerm2, Windows Terminal, ConEmu)
		fmt.Fprintf(n.opts.Out, "\x1b]9;%s: %s\x1b\\", title, message)
		// Terminal bell
		fmt.Fprintf(n.opts.Out, "\a")
	}

	return nil
}

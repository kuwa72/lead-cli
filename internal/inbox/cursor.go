package inbox

import (
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"
)

// caretSync shares the input caret's terminal coordinates between the model
// (which computes them while rendering the input line) and caretWriter
// (which re-asserts the hardware cursor after every renderer write).
// Bubble Tea v0.24 has no cursor-position command and its standard renderer
// always parks the cursor at the start of the last rendered line, so the
// caret position has to be re-applied after each frame (issue #160).
type caretSync struct {
	mu   sync.Mutex
	row  int
	col  int
	live bool
}

func (c *caretSync) set(row, col int) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.row, c.col, c.live = row, col, true
	c.mu.Unlock()
}

func (c *caretSync) clear() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.live = false
	c.mu.Unlock()
}

// pos returns the 1-based terminal row/column the caret should occupy.
func (c *caretSync) pos() (row, col int, live bool) {
	if c == nil {
		return 0, 0, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.row, c.col, c.live
}

// caretWriter wraps the program's output file. While the input caret is live
// it appends a CUP (cursor position) sequence to every write, so the
// hardware cursor — and the IME candidate window anchored to it — follows
// the text caret instead of resting at the bottom of the frame.
//
// It implements termenv's File interface so exec'd processes ($EDITOR,
// $PAGER) keep their terminal handle. Because it is not an *os.File, Bubble
// Tea's built-in resize handling no longer sees a TTY; Run compensates via
// forwardResize.
type caretWriter struct {
	f     *os.File
	caret *caretSync
}

func (w *caretWriter) Write(p []byte) (int, error) {
	n, err := w.f.Write(p)
	if row, col, live := w.caret.pos(); live {
		_, _ = fmt.Fprintf(w.f, "\x1b[%d;%dH", row, col)
	}
	return n, err
}

func (w *caretWriter) Read(p []byte) (int, error) { return w.f.Read(p) }

func (w *caretWriter) Fd() uintptr { return w.f.Fd() }

// forwardResize re-implements the program's resize handling: Bubble Tea only
// installs it when the program output is an *os.File, which caretWriter is
// not.
func forwardResize(p *tea.Program, f *os.File) {
	go func() {
		send := func() {
			if w, h, err := term.GetSize(int(f.Fd())); err == nil {
				p.Send(tea.WindowSizeMsg{Width: w, Height: h})
			}
		}
		send()
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, sigWinch)
		defer signal.Stop(sig)
		for range sig {
			send()
		}
	}()
}

// sigWinch is syscall.SIGWINCH. The literal keeps this file compiling on
// Windows, where the constant does not exist and the signal never fires.
const sigWinch = syscall.Signal(28)

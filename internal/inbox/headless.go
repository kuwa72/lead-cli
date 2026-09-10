package inbox

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Shell runs a foreground process that needs the terminal ($EDITOR).
// The real one hands the TTY over via tea.ExecProcess; tests inject a fake.
type Shell interface {
	Exec(c *exec.Cmd, done func(error) tea.Msg) tea.Cmd
}

// TeaShell suspends the bubbletea renderer around the process.
type TeaShell struct{}

// Exec implements Shell.
func (TeaShell) Exec(c *exec.Cmd, done func(error) tea.Msg) tea.Cmd {
	return tea.ExecProcess(c, tea.ExecCallback(done))
}

// SyncShell runs the process inline on the current stdio (headless mode,
// where no renderer owns the terminal).
type SyncShell struct{}

// Exec implements Shell.
func (SyncShell) Exec(c *exec.Cmd, done func(error) tea.Msg) tea.Cmd {
	return func() tea.Msg {
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
		return done(c.Run())
	}
}

// ParseKey turns one LEAD_TEST_INBOX_KEYS token into a key message:
// a single rune ("a"), a named key ("enter", "esc", "up", "down", "tab",
// "backspace", "ctrl+c", "space"), or "text:<runes>" typed one by one
// (handled by RunHeadless; ParseKey returns the whole run as KeyRunes).
func ParseKey(tok string) (tea.KeyMsg, error) {
	if strings.HasPrefix(tok, "text:") {
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(strings.TrimPrefix(tok, "text:"))}, nil
	}
	if len([]rune(tok)) == 1 {
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(tok)}, nil
	}
	named := map[string]tea.KeyType{
		"enter": tea.KeyEnter, "esc": tea.KeyEsc, "escape": tea.KeyEsc,
		"up": tea.KeyUp, "down": tea.KeyDown, "left": tea.KeyLeft, "right": tea.KeyRight,
		"tab": tea.KeyTab, "backspace": tea.KeyBackspace, "ctrl+c": tea.KeyCtrlC,
		"space": tea.KeySpace, "pgup": tea.KeyPgUp, "pgdown": tea.KeyPgDown,
	}
	if kt, ok := named[strings.ToLower(tok)]; ok {
		return tea.KeyMsg{Type: kt}, nil
	}
	return tea.KeyMsg{}, fmt.Errorf("unknown key token %q", tok)
}

// Drain runs cmd and every cmd it transitively produces, synchronously,
// feeding messages back into the model. It returns the settled model and
// whether a Quit was requested. Tick cmds are not scheduled by New unless
// Options.Refresh is set, so headless runs never block here.
func Drain(m Model, cmd tea.Cmd) (Model, bool) {
	queue := []tea.Cmd{cmd}
	quit := false
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		switch v := c().(type) {
		case nil:
		case tea.BatchMsg:
			queue = append(queue, v...)
		case tea.QuitMsg:
			quit = true
		default:
			next, ncmd := m.Update(v)
			m = next.(Model)
			queue = append(queue, ncmd)
		}
	}
	return m, quit
}

// RunHeadless replays keys against the model without a terminal and writes
// the final screen to out (LEAD_TEST_INBOX_KEYS hook for shell tests).
func RunHeadless(m Model, keys []string, out io.Writer) error {
	m.opts.PreviewDelay = 0
	m.opts.Headless = true
	if wEnv := os.Getenv("LEAD_TEST_INBOX_WIDTH"); wEnv != "" {
		if n, err := strconv.Atoi(wEnv); err == nil && n > 0 {
			m.width = n
		}
	}
	if hEnv := os.Getenv("LEAD_TEST_INBOX_HEIGHT"); hEnv != "" {
		if n, err := strconv.Atoi(hEnv); err == nil && n > 0 {
			m.height = n
		}
	}
	mode, profile, err := ResolveMode(m.opts.Theme, os.Getenv("NO_COLOR"), os.Getenv("COLORTERM"), os.Getenv("TERM"))
	if err != nil {
		return err
	}
	m.theme = NewTheme(mode, profile, out)
	m, quit := Drain(m, m.Init())
	if _, err := io.WriteString(out, m.View()); err != nil {
		return err
	}
	for _, tok := range keys {
		if quit {
			break
		}
		msg, err := ParseKey(tok)
		if err != nil {
			return err
		}
		next, cmd := m.Update(msg)
		m, quit = Drain(next.(Model), cmd)
	}
	_, err = io.WriteString(out, m.View())
	return err
}

// Run opens the interactive inbox on the terminal.
func Run(m Model) error {
	if m.opts.PreviewDelay <= 0 {
		m.opts.PreviewDelay = 150 * time.Millisecond
	}
	mode, profile, err := ResolveMode(m.opts.Theme, os.Getenv("NO_COLOR"), os.Getenv("COLORTERM"), os.Getenv("TERM"))
	if err != nil {
		return err
	}
	m.theme = NewTheme(mode, profile, os.Stdout)
	_, err = tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

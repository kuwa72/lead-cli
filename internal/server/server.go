// Package server implements `lead server` and the socket API (issue #48).
// See docs/design.md §7.
//
// Model: a headless daemon holds workflows state behind a unix socket.
// localhost-only by construction (AF_UNIX has no network surface);
// the socket dir is 0700 and the socket 0600. Human-in-the-loop is
// preserved: prompt.stage stores text for review, there is deliberately
// no remote send/exec operation. Single-run CLI mode keeps working
// without any server.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/kuwa72/lead-cli/internal/ports"
	"github.com/kuwa72/lead-cli/internal/state"
	"github.com/kuwa72/lead-cli/internal/workflow"
)

// Ops served over the socket.
const (
	OpSnapshot     = "snapshot"
	OpIssuesList   = "issues.list"
	OpWorkDispatch = "work.dispatch"
	OpPromptStage  = "prompt.stage"
	OpCIStatus     = "ci.status"
	OpNotify       = "notify"
)

// Request is one client call.
type Request struct {
	Op   string          `json:"op"`
	Args json.RawMessage `json:"args,omitempty"`
}

// Response is one server reply.
type Response struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error string          `json:"error,omitempty"`
}

// Deps injects the server environment.
type Deps struct {
	Gh        ports.GhClient
	Git       workflow.GitRunner
	StateFile string
	WorkDir   string
	Version   string
}

type stagedPrompt struct {
	ID     string    `json:"id"`
	Issue  int       `json:"issue"`
	Prompt string    `json:"prompt"`
	At     time.Time `json:"at"`
}

type notification struct {
	At      time.Time `json:"at"`
	Message string    `json:"message"`
}

// Server is a listening socket API daemon.
type Server struct {
	sock     string
	listener net.Listener
	deps     Deps

	mu            sync.Mutex
	staged        []stagedPrompt
	stagedCounter int
	notifications []notification
}

// Listen creates the socket (dir 0700, socket 0600, stale file replaced).
func Listen(sock string, deps Deps) (*Server, error) {
	dir := filepath.Dir(sock)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("server: socket dir: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("server: socket dir: %w", err)
	}
	if _, err := os.Stat(sock); err == nil {
		if err := os.Remove(sock); err != nil {
			return nil, fmt.Errorf("server: stale socket: %w", err)
		}
	}
	l, err := net.Listen("unix", sock)
	if err != nil {
		return nil, fmt.Errorf("server: listen %s: %w", sock, err)
	}
	if err := os.Chmod(sock, 0o600); err != nil {
		l.Close()
		return nil, fmt.Errorf("server: socket perms: %w", err)
	}
	return &Server{sock: sock, listener: l, deps: deps}, nil
}

// Addr returns the socket path.
func (s *Server) Addr() string { return s.sock }

// Close stops the listener and removes the socket file.
func (s *Server) Close() error {
	err := s.listener.Close()
	os.Remove(s.sock)
	return err
}

// Serve accepts connections until Close. One request per connection.
func (s *Server) Serve() error {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return err
		}
		go s.serveConn(conn)
	}
}

func (s *Server) serveConn(conn net.Conn) {
	defer conn.Close()
	var req Request
	dec := json.NewDecoder(conn)
	if err := dec.Decode(&req); err != nil {
		writeResponse(conn, Response{OK: false, Error: fmt.Sprintf("bad request: %v", err)})
		return
	}
	data, err := s.handle(req)
	if err != nil {
		writeResponse(conn, Response{OK: false, Error: err.Error()})
		return
	}
	writeResponse(conn, Response{OK: true, Data: data})
}

func writeResponse(conn net.Conn, resp Response) {
	raw, err := json.Marshal(resp)
	if err != nil {
		return
	}
	_, _ = conn.Write(append(raw, '\n'))
}

func mustJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`null`)
	}
	return raw
}

func (s *Server) handle(req Request) (json.RawMessage, error) {
	ctx := context.Background()
	switch req.Op {
	case OpSnapshot:
		return s.snapshot()
	case OpIssuesList:
		return s.issuesList(ctx)
	case OpWorkDispatch:
		return s.workDispatch(ctx, req.Args)
	case OpPromptStage:
		return s.promptStage(req.Args)
	case OpCIStatus:
		return s.ciStatus(ctx, req.Args)
	case OpNotify:
		return s.notify(req.Args)
	default:
		return nil, fmt.Errorf("unknown op %q (see `lead api schema`)", req.Op)
	}
}

func (s *Server) store() *state.Store { return &state.Store{Path: s.deps.StateFile} }

func (s *Server) snapshot() (json.RawMessage, error) {
	all, err := s.store().List()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return mustJSON(map[string]any{
		"version":       s.deps.Version,
		"workflows":     all,
		"staged":        s.staged,
		"notifications": s.notifications,
	}), nil
}

func (s *Server) issuesList(ctx context.Context) (json.RawMessage, error) {
	if s.deps.Gh == nil {
		return nil, fmt.Errorf("issues.list: no gh client")
	}
	summaries, err := s.deps.Gh.ListOpen(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, len(summaries))
	for i, item := range summaries {
		items[i] = map[string]any{"number": item.Number, "title": item.Title}
	}
	return mustJSON(map[string]any{"issues": items}), nil
}

func (s *Server) workDispatch(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Issue    int    `json:"issue"`
		Mode     string `json:"mode"`
		Branch   string `json:"branch"`
		Part     string `json:"part"`
		Worktree string `json:"worktree"`
		Agent    string `json:"agent"`
	}
	if err := json.Unmarshal(argsOrEmpty(args), &a); err != nil {
		return nil, fmt.Errorf("work.dispatch: bad args: %w", err)
	}
	if a.Issue <= 0 {
		return nil, fmt.Errorf("work.dispatch: issue is required")
	}
	if s.deps.Gh == nil {
		return nil, fmt.Errorf("work.dispatch: no gh client")
	}
	iss, err := s.deps.Gh.View(ctx, a.Issue)
	if err != nil {
		return nil, err
	}
	res, err := workflow.Start(ctx, s.deps.Git, s.store(), workflow.StartOptions{
		Issue: iss, Mode: a.Mode, Branch: a.Branch, Part: a.Part,
		Worktree: a.Worktree, WorkDir: s.deps.WorkDir,
	})
	if err != nil {
		return nil, err
	}
	return mustJSON(map[string]any{
		"issue": a.Issue, "branch": res.Branch, "worktree": res.Worktree,
		"status": string(res.Status), "agent": a.Agent,
	}), nil
}

func (s *Server) promptStage(args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Issue  int    `json:"issue"`
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal(argsOrEmpty(args), &a); err != nil {
		return nil, fmt.Errorf("prompt.stage: bad args: %w", err)
	}
	if a.Issue <= 0 || a.Prompt == "" {
		return nil, fmt.Errorf("prompt.stage: issue and prompt are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stagedCounter++
	id := fmt.Sprintf("st-%d", s.stagedCounter)
	s.staged = append(s.staged, stagedPrompt{ID: id, Issue: a.Issue, Prompt: a.Prompt, At: time.Now()})
	// Human-in-the-loop: staged prompts wait for review in `snapshot`;
	// there is no send/exec op by design.
	return mustJSON(map[string]any{"id": id}), nil
}

func (s *Server) ciStatus(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		PR int `json:"pr"`
	}
	if err := json.Unmarshal(argsOrEmpty(args), &a); err != nil {
		return nil, fmt.Errorf("ci.status: bad args: %w", err)
	}
	if a.PR <= 0 {
		return nil, fmt.Errorf("ci.status: pr is required")
	}
	if s.deps.Gh == nil {
		return nil, fmt.Errorf("ci.status: no gh client")
	}
	checks, err := s.deps.Gh.PrChecks(ctx, a.PR)
	if err != nil {
		return nil, err
	}
	rows := make([]map[string]any, len(checks))
	allPass := len(checks) > 0
	for i, c := range checks {
		rows[i] = map[string]any{"name": c.Name, "bucket": c.Bucket, "state": c.State}
		if c.Bucket != "pass" && c.Bucket != "skipping" {
			allPass = false
		}
	}
	return mustJSON(map[string]any{"pr": a.PR, "checks": rows, "all_pass": allPass}), nil
}

func (s *Server) notify(args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(argsOrEmpty(args), &a); err != nil {
		return nil, fmt.Errorf("notify: bad args: %w", err)
	}
	if a.Message == "" {
		return nil, fmt.Errorf("notify: message is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notifications = append(s.notifications, notification{At: time.Now(), Message: a.Message})
	if len(s.notifications) > 100 {
		s.notifications = s.notifications[len(s.notifications)-100:]
	}
	return mustJSON(map[string]any{"ok": true}), nil
}

func argsOrEmpty(args json.RawMessage) []byte {
	if len(args) == 0 {
		return []byte(`{}`)
	}
	return args
}

// Package audit appends one JSONL line per privileged democtl event
// (docs/demos.md §Audit). Append-only: the UI reads the file, nothing
// rewrites it. Secret material — session keys, cookies, client secrets —
// never enters an entry; details carry sizes, counts, and names only.
package audit

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Entry is one audit line. TS is RFC3339 UTC.
type Entry struct {
	TS     string         `json:"ts"`
	Actor  string         `json:"actor"`
	Action string         `json:"action"`
	Target string         `json:"target"`
	Args   map[string]any `json:"args,omitempty"`
}

// Log is an append-only JSONL writer, safe for concurrent use.
type Log struct {
	mu sync.Mutex
	f  *os.File
}

// Open creates the parent directory if needed and opens path for
// appending (mode 0600 — the file holds accountability data, not
// secrets, but broad readability buys nothing).
func Open(path string) (*Log, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("audit: create dir: %w", err)
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("audit: open %s: %w", path, err)
	}
	return &Log{f: f}, nil
}

// Event appends one line. It is best-effort by design: an audit write
// failure is logged and surfaced to the caller via the returned error,
// but never panics — the service stays up, the gap is visible in logs.
func (l *Log) Event(actor, action, target string, args map[string]any) error {
	if l == nil {
		return nil
	}
	e := Entry{
		TS:     time.Now().UTC().Format(time.RFC3339),
		Actor:  actor,
		Action: action,
		Target: target,
		Args:   args,
	}
	line, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("audit: marshal: %w", err)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := l.f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("audit: write: %w", err)
	}
	return nil
}

// MustEvent logs a failed audit write via slog and otherwise behaves like
// Event — for call sites where the mutation already happened and the
// handler only has logging left to do.
func (l *Log) MustEvent(actor, action, target string, args map[string]any) {
	if err := l.Event(actor, action, target, args); err != nil {
		slog.Error("audit write failed", "action", action, "target", target, "err", err)
	}
}

// Close closes the underlying file.
func (l *Log) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Close()
}

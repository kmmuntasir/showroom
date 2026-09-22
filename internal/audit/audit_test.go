package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestEventRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit", "audit.jsonl")
	l, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer l.Close()

	if err := l.Event("pm@example.com", "demo.deploy", "acme-demo", map[string]any{"size_bytes": 1234}); err != nil {
		t.Fatalf("Event: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var e Entry
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatalf("unmarshal %q: %v", raw, err)
	}
	if e.Actor != "pm@example.com" || e.Action != "demo.deploy" || e.Target != "acme-demo" {
		t.Errorf("entry = %+v", e)
	}
	if !strings.HasSuffix(e.TS, "Z") {
		t.Errorf("TS %q is not RFC3339 UTC", e.TS)
	}
}

func TestOpenCreatesFileWithTightPerms(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "audit.jsonl")
	l, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer l.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("perm = %o, want 600", perm)
	}
}

func TestConcurrentEventsStayLineDelimited(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	l, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer l.Close()

	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			_ = l.Event("a@b.c", "demo.deploy", "t", nil)
		})
	}
	wg.Wait()

	raw, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) != 50 {
		t.Errorf("got %d lines, want 50 (interleaved writes would fuse lines)", len(lines))
	}
}

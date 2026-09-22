package upload

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"democtl/internal/config"
)

// zentry is one archive entry for the test zip builder. mode 0 means
// "let the writer decide" — regular file for plain names, directory for
// trailing-slash names.
type zentry struct {
	name string
	data string
	mode os.FileMode
}

// buildZip produces an in-memory zip archive.
func buildZip(t *testing.T, entries ...zentry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if e.mode != 0 {
			h.SetMode(e.mode)
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatalf("create header %q: %v", e.name, err)
		}
		if _, err := w.Write([]byte(e.data)); err != nil {
			t.Fatalf("write %q: %v", e.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

// zipBody wraps buildZip output as a Publish reader for table cases.
func zipBody(entries ...zentry) func(*testing.T) io.Reader {
	return func(t *testing.T) io.Reader { return bytes.NewReader(buildZip(t, entries...)) }
}

// newStore returns a Store over a fresh temp dir; mutate adjusts the
// limits for a case.
func newStore(t *testing.T, mutate func(*config.Limits)) *Store {
	t.Helper()
	lim := config.DefaultLimits()
	if mutate != nil {
		mutate(&lim)
	}
	return &Store{DataDir: t.TempDir(), Limits: lim}
}

func assertEmptyDir(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	if len(entries) != 0 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("%s is not empty: %v", dir, names)
	}
}

// assertNoDebris: a failed deploy leaves tmp/ empty and releases/ with
// no partial tree (docs/demos.md §Upload pipeline).
func assertNoDebris(t *testing.T, s *Store) {
	t.Helper()
	assertEmptyDir(t, filepath.Join(s.DataDir, tmpDir))
	assertEmptyDir(t, filepath.Join(s.DataDir, releasesDir))
}

// currentLink returns the demos/<name>/current symlink path.
func currentLink(s *Store, name string) string {
	return filepath.Join(s.DataDir, demosDir, name, currentName)
}

func TestPublishHappyPath(t *testing.T) {
	t.Parallel()
	s := newStore(t, nil)
	body := buildZip(t,
		zentry{name: "index.html", data: "<h1>alpha</h1>"},
		zentry{name: "assets/app.js", data: "console.log(1)"},
		zentry{name: "assets/css/site.css", data: "body{}"},
		zentry{name: "empty/", data: ""},
	)

	res, err := s.Publish(7, "alpha", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if res.Dir == "" || !strings.HasPrefix(res.Dir, "7-") {
		t.Fatalf("Result.Dir = %q, want non-empty 7-…", res.Dir)
	}
	if res.FileCount != 3 {
		t.Errorf("FileCount = %d, want 3", res.FileCount)
	}
	if want := int64(len("<h1>alpha</h1>") + len("console.log(1)") + len("body{}")); res.SizeBytes != want {
		t.Errorf("SizeBytes = %d, want %d", res.SizeBytes, want)
	}

	releaseDir := filepath.Join(s.DataDir, releasesDir, res.Dir)
	for file, want := range map[string]string{
		"index.html":          "<h1>alpha</h1>",
		"assets/app.js":       "console.log(1)",
		"assets/css/site.css": "body{}",
	} {
		got, err := os.ReadFile(filepath.Join(releaseDir, filepath.FromSlash(file)))
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		if string(got) != want {
			t.Errorf("%s = %q, want %q", file, got, want)
		}
	}
	if info, err := os.Stat(filepath.Join(releaseDir, "empty")); err != nil || !info.IsDir() {
		t.Errorf("empty dir not extracted: %v", err)
	}

	target, err := os.Readlink(currentLink(s, "alpha"))
	if err != nil {
		t.Fatalf("readlink current: %v", err)
	}
	if want := filepath.Join("..", "..", releasesDir, res.Dir); target != want {
		t.Errorf("current -> %q, want %q", target, want)
	}
	if resolved := filepath.Join(s.DataDir, demosDir, "alpha", target); resolved != releaseDir {
		t.Errorf("current resolves to %q, want %q", resolved, releaseDir)
	}

	// The staging temp zip is gone after a successful publish.
	assertEmptyDir(t, filepath.Join(s.DataDir, tmpDir))
}

func TestPublishTwiceActivatesNewest(t *testing.T) {
	t.Parallel()
	s := newStore(t, nil)
	first := buildZip(t, zentry{name: "index.html", data: "v1"})
	second := buildZip(t,
		zentry{name: "index.html", data: "v2"},
		zentry{name: "app.js", data: "a"},
	)

	if _, err := s.Publish(3, "beta", bytes.NewReader(first)); err != nil {
		t.Fatalf("first Publish: %v", err)
	}
	res2, err := s.Publish(3, "beta", bytes.NewReader(second))
	if err != nil {
		t.Fatalf("second Publish: %v", err)
	}

	target, err := os.Readlink(currentLink(s, "beta"))
	if err != nil {
		t.Fatalf("readlink current: %v", err)
	}
	if want := filepath.Join("..", "..", releasesDir, res2.Dir); target != want {
		t.Fatalf("current -> %q, want %q", target, want)
	}
	got, err := os.ReadFile(filepath.Join(s.DataDir, releasesDir, res2.Dir, "index.html"))
	if err != nil {
		t.Fatalf("read deployed index.html: %v", err)
	}
	if string(got) != "v2" {
		t.Errorf("deployed index.html = %q, want %q", got, "v2")
	}
}

func TestPublishStripsSingleTopLevelDir(t *testing.T) {
	t.Parallel()
	s := newStore(t, nil)
	// The classic mistake: zipping the dist/ folder itself. The single
	// shared prefix carrying index.html is stripped (docs/demos.md
	// §Upload pipeline step 3).
	body := buildZip(t,
		zentry{name: "dist/", data: ""},
		zentry{name: "dist/index.html", data: "<p>hi</p>"},
		zentry{name: "dist/assets/app.js", data: "a"},
	)

	res, err := s.Publish(4, "gamma", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	releaseDir := filepath.Join(s.DataDir, releasesDir, res.Dir)
	got, err := os.ReadFile(filepath.Join(releaseDir, "index.html"))
	if err != nil {
		t.Fatalf("read stripped index.html: %v", err)
	}
	if string(got) != "<p>hi</p>" {
		t.Errorf("index.html = %q, want %q", got, "<p>hi</p>")
	}
	if _, err := os.Stat(filepath.Join(releaseDir, "assets", "app.js")); err != nil {
		t.Errorf("stripped assets/app.js: %v", err)
	}
	if _, err := os.Stat(filepath.Join(releaseDir, "dist")); !os.IsNotExist(err) {
		t.Errorf("dist/ should not survive stripping, stat err = %v", err)
	}
}

func TestPublishRejections(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		limits  func(*config.Limits)
		body    func(*testing.T) io.Reader
		wantErr error
	}{
		{"parent traversal", nil, zipBody(zentry{name: "../evil.txt", data: "x"}), ErrBadEntry},
		{"dotdot mid path", nil, zipBody(zentry{name: "assets/../../evil.txt", data: "x"}), ErrBadEntry},
		{"absolute entry", nil, zipBody(zentry{name: "/abs.txt", data: "x"}), ErrBadEntry},
		{"backslash entry", nil, zipBody(zentry{name: `a\b.txt`, data: "x"}), ErrBadEntry},
		{"symlink entry", nil, zipBody(zentry{name: "link", data: "index.html", mode: os.ModeSymlink | 0o777}), ErrBadEntry},
		{"non-regular entry", nil, zipBody(zentry{name: "fifo", data: "", mode: os.ModeNamedPipe | 0o644}), ErrBadEntry},
		{"too many files", func(l *config.Limits) { l.MaxFiles = 2 }, zipBody(
			zentry{name: "index.html", data: "i"},
			zentry{name: "a.txt", data: "a"},
			zentry{name: "b.txt", data: "b"},
		), ErrTooManyFiles},
		{"single file too large", func(l *config.Limits) { l.MaxFileBytes = 8 }, zipBody(
			zentry{name: "index.html", data: strings.Repeat("x", 100)},
		), ErrFileTooLarge},
		{"total too large", func(l *config.Limits) { l.MaxTotalBytes = 20 }, zipBody(
			zentry{name: "index.html", data: strings.Repeat("x", 15)},
			zentry{name: "app.js", data: strings.Repeat("y", 15)},
		), ErrTotalTooLarge},
		{"no index.html", nil, zipBody(zentry{name: "readme.txt", data: "hi"}), ErrNoIndexHTML},
		{"empty archive", nil, zipBody(), ErrNoIndexHTML},
		{"not a zip", nil, func(*testing.T) io.Reader { return strings.NewReader("this was never a zip file") }, ErrNotAZip},
		{"zip too large", func(l *config.Limits) { l.MaxZipBytes = 16 }, func(*testing.T) io.Reader {
			return bytes.NewReader(make([]byte, 64))
		}, ErrZipTooLarge},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newStore(t, tc.limits)

			_, err := s.Publish(9, "rej", tc.body(t))
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Publish error = %v, want %v", err, tc.wantErr)
			}

			assertNoDebris(t, s)
		})
	}
}

func TestSwitchCurrent(t *testing.T) {
	t.Parallel()
	s := newStore(t, nil)
	res, err := s.Publish(5, "delta", bytes.NewReader(buildZip(t,
		zentry{name: "index.html", data: "v1"},
	)))
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	// A hand-made older release stands in for the previous deploy.
	old := "5-1000000000"
	if err := os.MkdirAll(filepath.Join(s.DataDir, releasesDir, old), 0o755); err != nil {
		t.Fatalf("mkdir old release: %v", err)
	}
	if err := os.WriteFile(filepath.Join(s.DataDir, releasesDir, old, "index.html"), []byte("v0"), 0o644); err != nil {
		t.Fatalf("write old release: %v", err)
	}

	// Rollback to it, then forward again.
	if err := s.SwitchCurrent("delta", old); err != nil {
		t.Fatalf("SwitchCurrent rollback: %v", err)
	}
	target, err := os.Readlink(currentLink(s, "delta"))
	if err != nil {
		t.Fatalf("readlink current: %v", err)
	}
	if want := filepath.Join("..", "..", releasesDir, old); target != want {
		t.Fatalf("current -> %q, want %q", target, want)
	}
	got, err := os.ReadFile(filepath.Join(s.DataDir, releasesDir, old, "index.html"))
	if err != nil || string(got) != "v0" {
		t.Fatalf("rolled-back index.html = %q (%v), want v0", got, err)
	}

	if err := s.SwitchCurrent("delta", res.Dir); err != nil {
		t.Fatalf("SwitchCurrent forward: %v", err)
	}
	got, err = os.ReadFile(filepath.Join(s.DataDir, releasesDir, res.Dir, "index.html"))
	if err != nil || string(got) != "v1" {
		t.Fatalf("restored index.html = %q (%v), want v1", got, err)
	}

	for _, bad := range []struct{ name, dir string }{
		{"delta", "sub/dir"},
		{"delta", ".."},
		{"delta", "."},
		{"delta", "missing-1"},
		{"bad/name", res.Dir},
	} {
		if err := s.SwitchCurrent(bad.name, bad.dir); err == nil {
			t.Errorf("SwitchCurrent(%q, %q) succeeded, want error", bad.name, bad.dir)
		}
	}
}

func TestRemoveReleaseDir(t *testing.T) {
	t.Parallel()
	s := newStore(t, nil)
	res, err := s.Publish(6, "eps", bytes.NewReader(buildZip(t,
		zentry{name: "index.html", data: "x"},
	)))
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if err := s.RemoveReleaseDir(res.Dir); err != nil {
		t.Fatalf("RemoveReleaseDir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.DataDir, releasesDir, res.Dir)); !os.IsNotExist(err) {
		t.Fatalf("release dir still present, stat err = %v", err)
	}

	for _, bad := range []string{"sub/dir", "..", ".", ""} {
		if err := s.RemoveReleaseDir(bad); err == nil {
			t.Errorf("RemoveReleaseDir(%q) succeeded, want error", bad)
		}
	}
}

func TestRemoveDemoDirs(t *testing.T) {
	t.Parallel()
	s := newStore(t, nil)
	r1, err := s.Publish(1, "one", bytes.NewReader(buildZip(t,
		zentry{name: "index.html", data: "1"},
	)))
	if err != nil {
		t.Fatalf("Publish one: %v", err)
	}
	r2, err := s.Publish(2, "two", bytes.NewReader(buildZip(t,
		zentry{name: "index.html", data: "2"},
	)))
	if err != nil {
		t.Fatalf("Publish two: %v", err)
	}
	// Boundary fixtures: another release of demo 1 goes too, while demo
	// 11's releases must survive — "1-" never prefixes "11-…".
	for _, dir := range []string{"1-old", "11-other"} {
		if err := os.MkdirAll(filepath.Join(s.DataDir, releasesDir, dir), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	if err := s.RemoveDemoDirs(1, "one"); err != nil {
		t.Fatalf("RemoveDemoDirs: %v", err)
	}

	for _, gone := range []string{
		filepath.Join(s.DataDir, demosDir, "one"),
		filepath.Join(s.DataDir, releasesDir, r1.Dir),
		filepath.Join(s.DataDir, releasesDir, "1-old"),
	} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("%s still present, stat err = %v", gone, err)
		}
	}
	for _, kept := range []string{
		filepath.Join(s.DataDir, demosDir, "two"),
		filepath.Join(s.DataDir, releasesDir, r2.Dir),
		filepath.Join(s.DataDir, releasesDir, "11-other"),
	} {
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("%s should survive: %v", kept, err)
		}
	}

	if err := s.RemoveDemoDirs(2, "../escape"); err == nil {
		t.Error("RemoveDemoDirs with traversal name succeeded, want error")
	}
}

func TestRenameDemoDir(t *testing.T) {
	t.Parallel()
	s := newStore(t, nil)
	res, err := s.Publish(8, "oldname", bytes.NewReader(buildZip(t,
		zentry{name: "index.html", data: "renamed"},
	)))
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if err := s.RenameDemoDir("oldname", "newname"); err != nil {
		t.Fatalf("RenameDemoDir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.DataDir, demosDir, "oldname")); !os.IsNotExist(err) {
		t.Errorf("demos/oldname still present, stat err = %v", err)
	}
	target, err := os.Readlink(currentLink(s, "newname"))
	if err != nil {
		t.Fatalf("readlink new current: %v", err)
	}
	if want := filepath.Join("..", "..", releasesDir, res.Dir); target != want {
		t.Errorf("current -> %q, want %q", target, want)
	}
	got, err := os.ReadFile(filepath.Join(s.DataDir, releasesDir, res.Dir, "index.html"))
	if err != nil || string(got) != "renamed" {
		t.Errorf("index.html = %q (%v), want renamed", got, err)
	}

	// Never-deployed demo: nothing to rename, no error.
	if err := s.RenameDemoDir("ghost", "ghost2"); err != nil {
		t.Errorf("RenameDemoDir of undeployed demo: %v", err)
	}

	// Plain-basename safety is re-verified regardless of pre-validation.
	for _, bad := range [][2]string{
		{"sub/dir", "x"},
		{"x", ".."},
		{"", "x"},
	} {
		if err := s.RenameDemoDir(bad[0], bad[1]); err == nil {
			t.Errorf("RenameDemoDir(%q, %q) succeeded, want error", bad[0], bad[1])
		}
	}
}

func TestCleanTmp(t *testing.T) {
	t.Parallel()
	s := newStore(t, nil)
	tmp := filepath.Join(s.DataDir, tmpDir)
	if err := os.MkdirAll(filepath.Join(tmp, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "junk.zip"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "sub", "deep"), []byte("y"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := s.CleanTmp(); err != nil {
		t.Fatalf("CleanTmp: %v", err)
	}
	assertEmptyDir(t, tmp)
}

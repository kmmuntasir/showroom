package serving

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testDomain = "example.com"

// writeFixture builds a demo release tree in a temp dir and returns its
// path. deep/route/page.html exists but is never the SPA fallback — the
// fallback is the ROOT index.html only (docs/demos.md §Static serving).
func writeFixture(t *testing.T) string {
	t.Helper()

	files := map[string]string{
		"index.html":            "<!doctype html><html><body>demo root index</body></html>\n",
		"readme.txt":            "0123456789abcdefghij",  // exactly 20 bytes
		"assets/app.Bq7x.js":    "console.log('app');\n", // exactly 20 bytes
		".hidden":               "dotfile\n",
		"deep/route/index.html": "<!doctype html><html><body>route index</body></html>\n",
		"deep/route/page.html":  "<!doctype html><html><body>deep page</body></html>\n",
	}
	dir := t.TempDir()
	for name, body := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "emptydir"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// newHandler builds the Server exactly as the server package will compose
// it, as an http.Handler.
func newHandler(dir string) http.Handler {
	var h http.Handler = &Server{
		BaseDomain: testDomain,
		Resolve: func(label string) (string, bool) {
			if label == "demo" {
				return dir, true
			}
			return "", false
		},
	}
	return h
}

func serve(h http.Handler, host, target string, hdr http.Header) *http.Response {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Host = host
	if hdr != nil {
		req.Header = hdr
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Result()
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestServeHTTPFiles(t *testing.T) {
	dir := writeFixture(t)
	h := newHandler(dir)
	rootIndex := mustRead(t, filepath.Join(dir, "index.html"))

	tests := []struct {
		name       string
		host       string
		target     string
		wantStatus int
		wantBody   string
		wantCache  string
		wantType   string
	}{
		{
			name:       "root serves index.html",
			host:       "demo.example.com",
			target:     "/",
			wantStatus: http.StatusOK,
			wantBody:   rootIndex,
			wantCache:  "no-cache",
			wantType:   "text/html; charset=utf-8",
		},
		{
			name:       "index.html directly",
			host:       "demo.example.com",
			target:     "/index.html",
			wantStatus: http.StatusOK,
			wantBody:   rootIndex,
			wantCache:  "no-cache",
			wantType:   "text/html; charset=utf-8",
		},
		{
			name:       "content-hashed asset is immutable",
			host:       "demo.example.com",
			target:     "/assets/app.Bq7x.js",
			wantStatus: http.StatusOK,
			wantBody:   "console.log('app');\n",
			wantCache:  "public, max-age=31536000, immutable",
			wantType:   "text/javascript; charset=utf-8",
		},
		{
			name:       "txt gets short cache window",
			host:       "demo.example.com",
			target:     "/readme.txt",
			wantStatus: http.StatusOK,
			wantBody:   "0123456789abcdefghij",
			wantCache:  "public, max-age=3600",
			wantType:   "text/plain; charset=utf-8",
		},
		{
			name:       "spa fallback serves ROOT index on unknown path",
			host:       "demo.example.com",
			target:     "/route/deep",
			wantStatus: http.StatusOK,
			wantBody:   rootIndex,
			wantCache:  "no-cache",
			wantType:   "text/html; charset=utf-8",
		},
		{
			name:       "directory request serves its own index.html",
			host:       "demo.example.com",
			target:     "/deep/route",
			wantStatus: http.StatusOK,
			wantBody:   mustRead(t, filepath.Join(dir, "deep/route/index.html")),
			wantCache:  "public, max-age=3600",
			wantType:   "text/html; charset=utf-8",
		},
		{
			name:       "nested file under directory",
			host:       "demo.example.com",
			target:     "/deep/route/page.html",
			wantStatus: http.StatusOK,
			wantBody:   "<!doctype html><html><body>deep page</body></html>\n",
			wantCache:  "public, max-age=3600",
			wantType:   "text/html; charset=utf-8",
		},
		{
			name:       "dotfile is 404",
			host:       "demo.example.com",
			target:     "/.hidden",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "path under dotfile is 404",
			host:       "demo.example.com",
			target:     "/.hidden/x",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "dot directory is 404",
			host:       "demo.example.com",
			target:     "/.well-known/asset",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "encoded traversal is 404",
			host:       "demo.example.com",
			target:     "/..%2f..%2fetc%2fpasswd",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "plain dot-dot traversal is 404",
			host:       "demo.example.com",
			target:     "/../../../../etc/passwd",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "directory without index.html is 404, no listing",
			host:       "demo.example.com",
			target:     "/emptydir",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "assets directory without index.html is 404",
			host:       "demo.example.com",
			target:     "/assets/",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "missing nested index.html falls back to ROOT index",
			host:       "demo.example.com",
			target:     "/nope/index.html",
			wantStatus: http.StatusOK,
			wantBody:   rootIndex,
			wantCache:  "no-cache",
		},
		{
			name:       "wrong host is 404",
			host:       "evil.example.com",
			target:     "/",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "multi-level subdomain is 404",
			host:       "sub.multi.example.com",
			target:     "/",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "base domain itself is 404",
			host:       "example.com",
			target:     "/",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "unknown label is 404",
			host:       "ghost.example.com",
			target:     "/",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "host with port resolves",
			host:       "demo.example.com:8443",
			target:     "/",
			wantStatus: http.StatusOK,
			wantBody:   rootIndex,
			wantCache:  "no-cache",
		},
		{
			name:       "host case is normalized",
			host:       "DEMO.EXAMPLE.COM",
			target:     "/",
			wantStatus: http.StatusOK,
			wantBody:   rootIndex,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := serve(h, tt.host, tt.target, nil)
			defer resp.Body.Close()

			body := readBody(t, resp)
			if resp.StatusCode != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", resp.StatusCode, tt.wantStatus, body)
			}
			if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
			}

			if tt.wantStatus == http.StatusNotFound {
				if body != "not found\n" {
					t.Errorf("404 body = %q, want %q", body, "not found\n")
				}
				if ct := resp.Header.Get("Content-Type"); ct != "text/plain; charset=utf-8" {
					t.Errorf("404 Content-Type = %q, want text/plain; charset=utf-8", ct)
				}
				return
			}

			if tt.wantBody != "" && body != tt.wantBody {
				t.Errorf("body = %q, want %q", body, tt.wantBody)
			}
			if tt.wantCache != "" && resp.Header.Get("Cache-Control") != tt.wantCache {
				t.Errorf("Cache-Control = %q, want %q", resp.Header.Get("Cache-Control"), tt.wantCache)
			}
			if tt.wantType != "" && resp.Header.Get("Content-Type") != tt.wantType {
				t.Errorf("Content-Type = %q, want %q", resp.Header.Get("Content-Type"), tt.wantType)
			}
		})
	}
}

func TestServeHTTPRange(t *testing.T) {
	dir := writeFixture(t)
	h := newHandler(dir)

	hdr := http.Header{}
	hdr.Set("Range", "bytes=0-3")
	resp := serve(h, "demo.example.com", "/readme.txt", hdr)
	defer resp.Body.Close()

	body := readBody(t, resp)
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusPartialContent)
	}
	if body != "0123" {
		t.Errorf("body = %q, want %q", body, "0123")
	}
	if cr := resp.Header.Get("Content-Range"); cr != "bytes 0-3/20" {
		t.Errorf("Content-Range = %q, want bytes 0-3/20", cr)
	}
}

func TestServeHTTPNoRootIndexNoFallbackLoop(t *testing.T) {
	// A release without index.html must 404 on /index.html, not fall back
	// onto the same missing file.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	var h http.Handler = &Server{
		BaseDomain: testDomain,
		Resolve: func(label string) (string, bool) {
			if label == "demo" {
				return dir, true
			}
			return "", false
		},
	}

	for _, target := range []string{"/", "/index.html", "/missing"} {
		resp := serve(h, "demo.example.com", target, nil)
		body := readBody(t, resp)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s status = %d, want 404", target, resp.StatusCode)
		}
		if body != "not found\n" {
			t.Errorf("GET %s body = %q, want %q", target, body, "not found\n")
		}
	}
}

func TestServeHTTPMissingRelease(t *testing.T) {
	dir := writeFixture(t)
	var h http.Handler = &Server{
		BaseDomain: testDomain,
		Resolve: func(label string) (string, bool) {
			if label == "demo" {
				return filepath.Join(dir, "does-not-exist"), true
			}
			return "", false
		},
	}

	resp := serve(h, "demo.example.com", "/", nil)
	defer resp.Body.Close()

	body := readBody(t, resp)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
	if body != "not found\n" {
		t.Errorf("body = %q, want %q", body, "not found\n")
	}
	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
}

func TestNotFoundNeverEchoesInput(t *testing.T) {
	dir := writeFixture(t)
	h := newHandler(dir)

	target := "/..%2f..%2fsecret-label%2fMYSECRET"
	resp := serve(h, "ghost.example.com", target, nil)
	defer resp.Body.Close()

	body := readBody(t, resp)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
	if strings.Contains(body, "SECRET") || strings.Contains(body, "ghost") {
		t.Errorf("404 body echoes request input: %q", body)
	}
}

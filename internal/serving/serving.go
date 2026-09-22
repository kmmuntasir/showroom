// Package serving serves uploaded demo builds on <name>.example.com.
// Every file open goes through an os.Root anchored at the demo's current
// release directory (path traversal is impossible by construction), unknown
// paths fall back to the demo's root index.html so react-router demos
// deep-link correctly, dotfiles and directory listings never leave the disk
// (docs/demos.md §Static serving of a demo, §Architecture host routing).
//
// The package never echoes request input into a response and never logs —
// every failure is the same generic "not found" line, so the static hosts
// disclose nothing about which labels exist.
package serving

import (
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"strings"
)

// indexName is the SPA entry every release must ship; the upload pipeline
// enforces it as the zip root marker (docs/demos.md §Limits).
const indexName = "index.html"

// Cache-Control policy (docs/demos.md §Static serving): the HTML shell must
// revalidate so deploys are instant, Vite content-hashed assets are
// immutable, everything else gets a short shared-cache window.
const (
	cacheIndex   = "no-cache"
	cacheAssets  = "public, max-age=31536000, immutable"
	cacheDefault = "public, max-age=3600"
)

// contentTypes pins the mappings demo builds rely on, consulted before
// mime.TypeByExtension so behaviour never depends on the host OS mime
// database (/etc/mime.types varies between systems).
var contentTypes = map[string]string{
	".html": "text/html; charset=utf-8",
	".js":   "text/javascript; charset=utf-8",
	".mjs":  "text/javascript; charset=utf-8",
	".css":  "text/css; charset=utf-8",
	".json": "application/json",
	".svg":  "image/svg+xml",
	".wasm": "application/wasm",
	".webp": "image/webp",
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".ico":  "image/x-icon",
	".txt":  "text/plain; charset=utf-8",
}

// Resolver maps a subdomain label to the absolute directory of its current
// release; ok=false means the label is not a live demo.
type Resolver func(label string) (dir string, ok bool)

// Server serves one demo per single-label subdomain of BaseDomain. It
// implements http.Handler; the host mux in the server package routes the
// Zoraxy wildcard *.example.com traffic here after the control host is
// matched (docs/demos.md §Architecture host routing).
type Server struct {
	BaseDomain string // e.g. "example.com"
	Resolve    Resolver
}

// ServeHTTP serves r for <label>.<BaseDomain>. Any failure — wrong host,
// unknown label, missing release, rejected path, missing file — is the same
// generic 404.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Set on every response, 200 or 404, before anything is written.
	w.Header().Set("X-Content-Type-Options", "nosniff")

	label, ok := s.labelFromHost(r.Host)
	if !ok {
		notFound(w)
		return
	}

	dir, ok := s.Resolve(label)
	if !ok {
		notFound(w)
		return
	}

	root, err := os.OpenRoot(dir)
	if err != nil {
		// The release directory vanished (deleted mid-request or broken
		// symlink) — indistinguishable from a dead demo for the visitor.
		notFound(w)
		return
	}
	defer root.Close()

	cleaned := cleanURLPath(r.URL.Path)
	if cleaned == "" {
		notFound(w)
		return
	}

	f, servedPath, fsName, ok := openForServe(root, cleaned)
	if !ok {
		notFound(w)
		return
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil || st.IsDir() {
		notFound(w)
		return
	}

	if ct := contentType(fsName); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("Cache-Control", cacheControl(servedPath))
	http.ServeContent(w, r, path.Base(fsName), st.ModTime(), f)
}

// labelFromHost extracts the single-label subdomain for BaseDomain.
func (s *Server) labelFromHost(host string) (string, bool) {
	return LabelFromHost(host, s.BaseDomain)
}

// LabelFromHost extracts the single-label subdomain of baseDomain from a
// Host header (lowercased, port stripped); ok=false unless the host is
// exactly <label>.<baseDomain> with a single dotless label. Multi-level
// subdomains (label containing ".") are out of scope by design — the
// Zoraxy wildcard route only ever produces one label (docs/demos.md
// §Architecture host routing). The server package's privacy gate uses the
// same parse to look demos up before serving.
func LabelFromHost(host, baseDomain string) (string, bool) {
	host = strings.ToLower(host)
	if i := strings.LastIndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	suffix := "." + strings.ToLower(baseDomain)
	if !strings.HasSuffix(host, suffix) {
		return "", false
	}
	label := strings.TrimSuffix(host, suffix)
	if label == "" || strings.Contains(label, ".") {
		return "", false
	}
	return label, true
}

// cleanURLPath normalizes the request path and rejects any segment starting
// with "." — dotfiles and .well-known are never served, and the check runs
// on the path as received, before cleaning, so encoded traversal
// ("/..%2f..%2fetc%2fpasswd" → "/../../etc/passwd") is rejected outright
// instead of cleaned into an in-root path. It returns "" for rejected paths.
func cleanURLPath(p string) string {
	if p == "" {
		p = "/"
	}
	for _, seg := range strings.Split(p, "/") {
		if strings.HasPrefix(seg, ".") {
			return ""
		}
	}
	cleaned := path.Clean(p)
	if !strings.HasPrefix(cleaned, "/") {
		return "/"
	}
	return cleaned
}

// openForServe opens the file to serve for cleaned (an absolute URL path).
// It returns the open file, the canonical URL path actually served (drives
// Cache-Control), and the file's name under the root (drives Content-Type).
//
// A missing non-index path falls back to the demo ROOT index.html — not the
// nearest directory index — so SPA deep links render. A directory resolves
// to its own index.html; listings are never generated. ok=false means 404.
func openForServe(root *os.Root, cleaned string) (f *os.File, servedPath, fsName string, ok bool) {
	if cleaned == "/" {
		f, ok := openRegular(root, indexName)
		return f, "/", indexName, ok
	}

	name := strings.TrimPrefix(cleaned, "/")
	f, err := root.Open(name)
	if err != nil {
		// SPA fallback, but never for /index.html itself — that would turn
		// "shipped a broken entry" into a loop of the same missing file.
		if !errors.Is(err, fs.ErrNotExist) || name == indexName {
			return nil, "", "", false
		}
		fb, ok := openRegular(root, indexName)
		return fb, "/" + indexName, indexName, ok
	}

	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, "", "", false
	}
	if !st.IsDir() {
		return f, cleaned, name, true
	}
	f.Close()

	// Directory: serve its own index.html, never a listing; a directory
	// without one is a plain 404 (no SPA fallback here — the demo ships
	// index.html at its root or it does not render).
	dirIndex := path.Join(name, indexName)
	di, ok := openRegular(root, dirIndex)
	return di, cleaned, dirIndex, ok
}

// openRegular opens name under root and requires it to not be a directory.
func openRegular(root *os.Root, name string) (*os.File, bool) {
	f, err := root.Open(name)
	if err != nil {
		return nil, false
	}
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		f.Close()
		return nil, false
	}
	return f, true
}

// contentType resolves Content-Type for a root-relative file name: the
// explicit table first, then the system mime database as a fallback for
// extensions demo builds ship beyond the pinned set.
func contentType(name string) string {
	ext := strings.ToLower(path.Ext(name))
	if ct, ok := contentTypes[ext]; ok {
		return ct
	}
	return mime.TypeByExtension(ext)
}

// cacheControl maps the served URL path to its Cache-Control policy.
func cacheControl(servedPath string) string {
	switch {
	case servedPath == "/" || servedPath == "/"+indexName:
		return cacheIndex
	case strings.HasPrefix(servedPath, "/assets/"):
		return cacheAssets
	default:
		return cacheDefault
	}
}

// notFound writes the generic 404 — constant body, nothing echoed.
func notFound(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	io.WriteString(w, "not found\n")
}

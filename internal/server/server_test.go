package server_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"democtl/internal/audit"
	"democtl/internal/auth"
	"democtl/internal/config"
	"democtl/internal/server"
	"democtl/internal/store"
	"democtl/internal/upload"
	"golang.org/x/oauth2"
)

// harness is a full democtl stack — real sqlite, real filesystem, real
// serving — with only Google stubbed behind an httptest server.
type harness struct {
	ts   *httptest.Server
	stub *httptest.Server
	// client never follows redirects: the login flow's cookies live on the
	// 302/303 responses themselves, which a following client would discard.
	client *http.Client
	dbPath string
	audit  string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()

	// Stub Google: /token answers the code exchange, /userinfo the identity.
	var identity string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"access_token":"test-token","token_type":"Bearer"}`)
		case "/userinfo":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, identity)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(stub.Close)
	identity = `{"sub":"sub-1","email":"pm@example.com","email_verified":true,"hd":"example.com"}`

	cfg := config.Config{
		Listen:             "127.0.0.1:0",
		ControlHost:        "demos.example.com",
		BaseDomain:         "example.com",
		DataDir:            filepath.Join(dir, "data"),
		DBPath:             filepath.Join(dir, "democtl.db"),
		AuditPath:          filepath.Join(dir, "audit.jsonl"),
		SessionKey:         strings.Repeat("k", 32),
		GoogleClientID:     "cid",
		GoogleClientSecret: "csecret",
		WorkspaceDomain:    "example.com",
		Limits:             config.DefaultLimits(),
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	auditLog, err := audit.Open(cfg.AuditPath)
	if err != nil {
		t.Fatalf("audit.Open: %v", err)
	}
	t.Cleanup(func() { auditLog.Close() })

	authn := auth.New(auth.Deps{
		Cfg:          cfg,
		Sessions:     st,
		Google:       oauth2.Endpoint{AuthURL: stub.URL + "/auth", TokenURL: stub.URL + "/token"},
		UserInfoURL:  stub.URL + "/userinfo",
		SecureCookie: false,
	})
	srv := &server.Server{
		Cfg:     cfg,
		DB:      st,
		Audit:   auditLog,
		Uploads: &upload.Store{DataDir: cfg.DataDir, Limits: cfg.Limits},
		Auth:    authn,
		Web: fstest.MapFS{
			"index.html": &fstest.MapFile{Data: []byte("<html>dashboard</html>")},
		},
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &harness{
		ts:     ts,
		stub:   stub,
		client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		dbPath: cfg.DBPath,
		audit:  cfg.AuditPath,
	}
}

// do issues a request against the harness with an explicit Host header —
// host multiplexing is the whole game here.
func (h *harness) do(t *testing.T, method, host, path string, body io.Reader, headers map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, h.ts.URL+path, body)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	req.Host = host
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

const (
	controlHost = "demos.example.com"
	demoHost    = "acme.example.com"
)

// login drives the real OAuth round-trip against the stub and returns the
// session cookie value.
func (h *harness) login(t *testing.T) string {
	t.Helper()

	// BeginLogin: 302 to Google + state cookie.
	req, _ := http.NewRequest("GET", h.ts.URL+"/login", nil)
	req.Host = controlHost
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("GET /login: %v", err)
	}
	var state string
	for _, c := range resp.Cookies() {
		if c.Name == auth.StateCookieName {
			state = c.Value
		}
	}
	resp.Body.Close()
	if state == "" {
		t.Fatal("no state cookie from /login")
	}
	redirect, _ := url.Parse(resp.Header.Get("Location"))
	if !strings.Contains(redirect.RawQuery, "hd=example.com") {
		t.Errorf("consent URL missing hd hint: %s", redirect)
	}

	// Callback: state cookie + code → session cookie.
	req2, _ := http.NewRequest("GET", h.ts.URL+"/oauth2/callback?code=good&state="+url.QueryEscape(state), nil)
	req2.Host = controlHost
	req2.AddCookie(&http.Cookie{Name: auth.StateCookieName, Value: state})
	resp2, err := h.client.Do(req2)
	if err != nil {
		t.Fatalf("GET /oauth2/callback: %v", err)
	}
	defer resp2.Body.Close()
	var session string
	for _, c := range resp2.Cookies() {
		if c.Name == auth.CookieName && c.Value != "" {
			session = c.Value
		}
	}
	if session == "" {
		t.Fatalf("no session cookie from callback (status %d)", resp2.StatusCode)
	}
	return session
}

// csrf fetches the session's token via /api/me.
func (h *harness) csrf(t *testing.T, session string) string {
	t.Helper()
	resp := h.do(t, "GET", controlHost, "/api/me", nil, map[string]string{"Cookie": auth.CookieName + "=" + session})
	defer resp.Body.Close()
	var me struct {
		Email     string `json:"email"`
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&me); err != nil {
		t.Fatalf("/api/me decode: %v", err)
	}
	if me.Email != "pm@example.com" || me.CSRFToken == "" {
		t.Fatalf("/api/me = %+v", me)
	}
	return me.CSRFToken
}

func zipWith(entries map[string]string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range entries {
		fw, _ := zw.Create(name)
		fw.Write([]byte(content))
	}
	zw.Close()
	return buf.Bytes()
}

func (h *harness) deploy(t *testing.T, session, csrfToken, demoName string, zipBytes []byte, wantStatus int) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("zip", "dist.zip")
	fw.Write(zipBytes)
	mw.Close()
	resp := h.do(t, "POST", controlHost, "/api/demos/"+demoName+"/deploy", &buf, map[string]string{
		"Cookie":       auth.CookieName + "=" + session,
		"X-CSRF-Token": csrfToken,
		"Content-Type": mw.FormDataContentType(),
	})
	if resp.StatusCode != wantStatus {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("deploy status = %d, want %d (body: %s)", resp.StatusCode, wantStatus, body)
	}
	return resp
}

func TestHealthzAnyHost(t *testing.T) {
	h := newHarness(t)
	for _, host := range []string{controlHost, "192.0.2.10:5000"} {
		resp := h.do(t, "GET", host, "/healthz", nil, nil)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || string(body) != "ok\n" {
			t.Errorf("host %s: %d %q", host, resp.StatusCode, body)
		}
	}
}

func TestUnknownDemoHostIs404(t *testing.T) {
	h := newHarness(t)
	resp := h.do(t, "GET", "nope.example.com", "/", nil, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestUnauthenticatedIs401Envelope(t *testing.T) {
	h := newHarness(t)
	resp := h.do(t, "GET", controlHost, "/api/demos", nil, nil)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
	if want := `{"error":{"message":"unauthenticated"}}`; strings.TrimSpace(string(body)) != want {
		t.Errorf("body = %q, want %q", body, want)
	}
}

func TestDemoLifecycleEndToEnd(t *testing.T) {
	h := newHarness(t)
	session := h.login(t)
	csrfToken := h.csrf(t, session)
	authHeaders := map[string]string{
		"Cookie":       auth.CookieName + "=" + session,
		"X-CSRF-Token": csrfToken,
	}

	// Create.
	createBody := strings.NewReader(`{"name":"acme"}`)
	resp := h.do(t, "POST", controlHost, "/api/demos", createBody, mergeHeaders(authHeaders, map[string]string{"Content-Type": "application/json"}))
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", resp.StatusCode)
	}

	// Validation matrix.
	for name, want := range map[string]int{
		`{"name":"www"}`:  http.StatusConflict, // reserved
		`{"name":"Bad!"}`: http.StatusBadRequest,
		`{"name":"acme"}`: http.StatusConflict, // taken
		`not json`:        http.StatusBadRequest,
	} {
		r := h.do(t, "POST", controlHost, "/api/demos", strings.NewReader(name), mergeHeaders(authHeaders, map[string]string{"Content-Type": "application/json"}))
		r.Body.Close()
		if r.StatusCode != want {
			t.Errorf("create %s: status = %d, want %d", name, r.StatusCode, want)
		}
	}

	// Deploy v1 → live on the demo host through serving.
	h.deploy(t, session, csrfToken, "acme", zipWith(map[string]string{"index.html": "v1"}), http.StatusCreated)
	resp = h.do(t, "GET", demoHost, "/", nil, nil)
	v1, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(v1) != "v1" {
		t.Errorf("demo host after deploy: %d %q", resp.StatusCode, v1)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("index Cache-Control = %q, want no-cache", cc)
	}

	// SPA fallback: unknown path serves index.
	resp = h.do(t, "GET", demoHost, "/route/deep", nil, nil)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != "v1" {
		t.Errorf("SPA fallback: %d %q", resp.StatusCode, body)
	}

	// Deploy v2 → rollback → v1 live again.
	h.deploy(t, session, csrfToken, "acme", zipWith(map[string]string{"index.html": "v2"}), http.StatusCreated)
	resp = h.do(t, "POST", controlHost, "/api/demos/acme/rollback", nil, authHeaders)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("rollback status = %d", resp.StatusCode)
	}
	resp = h.do(t, "GET", demoHost, "/", nil, nil)
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "v1" {
		t.Errorf("after rollback body = %q, want v1", body)
	}

	// Rollback again: with two releases the target equals current — an
	// idempotent 200, still serving v1.
	resp = h.do(t, "POST", controlHost, "/api/demos/acme/rollback", nil, authHeaders)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("second rollback status = %d, want 200 (idempotent)", resp.StatusCode)
	}
	resp = h.do(t, "GET", demoHost, "/", nil, nil)
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "v1" {
		t.Errorf("after second rollback body = %q, want v1", body)
	}

	// Rename: old host dies, new host serves; audit records it.
	resp = h.do(t, "PATCH", controlHost, "/api/demos/acme", strings.NewReader(`{"name":"acme2"}`), mergeHeaders(authHeaders, map[string]string{"Content-Type": "application/json"}))
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("rename status = %d", resp.StatusCode)
	}
	if r := h.do(t, "GET", demoHost, "/", nil, nil); r.StatusCode != 404 {
		t.Errorf("old host after rename = %d, want 404", r.StatusCode)
		r.Body.Close()
	}
	if r := h.do(t, "GET", "acme2.example.com", "/", nil, nil); r.StatusCode != 200 {
		t.Errorf("new host after rename = %d, want 200", r.StatusCode)
		r.Body.Close()
	}

	// List shows one demo with a last release.
	resp = h.do(t, "GET", controlHost, "/api/demos", nil, authHeaders)
	var list struct {
		Demos []struct {
			Name        string `json:"name"`
			LastRelease *struct {
				SizeBytes int64 `json:"size_bytes"`
			} `json:"last_release"`
		} `json:"demos"`
	}
	json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if len(list.Demos) != 1 || list.Demos[0].Name != "acme2" || list.Demos[0].LastRelease == nil {
		t.Errorf("list = %+v", list)
	}

	// Delete → 204, host dead, list empty.
	resp = h.do(t, "DELETE", controlHost, "/api/demos/acme2", nil, authHeaders)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d", resp.StatusCode)
	}
	if r := h.do(t, "GET", "acme2.example.com", "/", nil, nil); r.StatusCode != 404 {
		t.Errorf("host after delete = %d, want 404", r.StatusCode)
		r.Body.Close()
	}

	// Audit log carries the whole lifecycle.
	raw, err := os.ReadFile(h.audit)
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	for _, action := range []string{"auth.login", "demo.create", "demo.deploy", "demo.rollback", "demo.rename", "demo.delete"} {
		if !strings.Contains(string(raw), `"`+action+`"`) {
			t.Errorf("audit missing %q", action)
		}
	}
}

func TestDeployRejectsZipWithoutIndex(t *testing.T) {
	h := newHarness(t)
	session := h.login(t)
	csrfToken := h.csrf(t, session)

	resp := h.do(t, "POST", controlHost, "/api/demos", strings.NewReader(`{"name":"acme"}`), map[string]string{
		"Cookie": auth.CookieName + "=" + session, "X-CSRF-Token": csrfToken, "Content-Type": "application/json",
	})
	resp.Body.Close()

	h.deploy(t, session, csrfToken, "acme", zipWith(map[string]string{"other.txt": "x"}), http.StatusBadRequest)
}

func TestMutationWithoutCSRFIs403(t *testing.T) {
	h := newHarness(t)
	session := h.login(t)

	resp := h.do(t, "POST", controlHost, "/api/demos", strings.NewReader(`{"name":"acme"}`), map[string]string{
		"Cookie": auth.CookieName + "=" + session, "Content-Type": "application/json",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}

func TestControlHostServesSPA(t *testing.T) {
	h := newHarness(t)
	resp := h.do(t, "GET", controlHost, "/", nil, nil)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "dashboard") {
		t.Errorf("control SPA: %d %q", resp.StatusCode, body)
	}
}

// mergeHeaders is a small test convenience for stacking auth + content type.
func mergeHeaders(base, extra map[string]string) map[string]string {
	out := maps.Clone(base)
	maps.Copy(out, extra)
	return out
}

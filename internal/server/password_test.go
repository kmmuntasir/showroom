package server_test

// Password-mode coverage: POST /api/login, GET /api/auth-info, the OAuth
// endpoints going dark, and the superadmin-only user management surface.
// The google-mode harness in server_test.go is reused where a cross-mode
// assertion is needed.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"democtl/internal/audit"
	"democtl/internal/auth"
	"democtl/internal/config"
	"democtl/internal/server"
	"democtl/internal/store"
	"democtl/internal/upload"
)

const (
	superadminEmail    = "admin@example.com"
	superadminPassword = "superadmin-password-1"
	plainEmail         = "user@example.com"
	plainPassword      = "plainuser-password-1"
)

// passwordHarness is the control plane in DEMOCTL_AUTH_MODE=password with
// one superadmin and one plain user — no Google stub, OAuth is unreachable.
type passwordHarness struct {
	ts     *httptest.Server
	client *http.Client
}

func newPasswordHarness(t *testing.T) *passwordHarness {
	t.Helper()
	dir := t.TempDir()

	cfg := config.Config{
		Listen:        "127.0.0.1:0",
		ControlHost:   "demos.example.com",
		BaseDomain:    "example.com",
		DataDir:       filepath.Join(dir, "data"),
		DBPath:        filepath.Join(dir, "democtl.db"),
		AuditPath:     filepath.Join(dir, "audit.jsonl"),
		SessionKey:    strings.Repeat("k", 32),
		AuthMode:      config.AuthModePassword,
		AdminEmail:    superadminEmail,
		AdminPassword: superadminPassword,
		Limits:        config.DefaultLimits(),
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	// The main.go bootstrap, verbatim: hash the env credential and ensure
	// the superadmin.
	hash, err := auth.HashPassword(cfg.AdminPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if _, _, err := st.EnsureSuperadmin(cfg.AdminEmail, hash, 1); err != nil {
		t.Fatalf("EnsureSuperadmin: %v", err)
	}
	plainHash, err := auth.HashPassword(plainPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if _, err := st.CreateUser(plainEmail, plainHash, store.RoleUser, 2); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	auditLog, err := audit.Open(cfg.AuditPath)
	if err != nil {
		t.Fatalf("audit.Open: %v", err)
	}
	t.Cleanup(func() { auditLog.Close() })

	authn := auth.New(auth.Deps{Cfg: cfg, Sessions: st, SecureCookie: false})
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
	return &passwordHarness{
		ts:     ts,
		client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}
}

func (h *passwordHarness) do(t *testing.T, method, path string, body io.Reader, headers map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, h.ts.URL+path, body)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	req.Host = controlHost
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(body)
}

// login posts credentials and returns the session cookie value and the
// decoded login response.
func (h *passwordHarness) login(t *testing.T, email, password string) (string, map[string]any) {
	t.Helper()
	resp := h.do(t, "POST", "/api/login",
		strings.NewReader(`{"email":`+quote(email)+`,"password":`+quote(password)+`}`),
		map[string]string{"Content-Type": "application/json"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("login status = %d, want 200 (body: %s)", resp.StatusCode, body)
	}
	var data map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		t.Fatalf("login decode: %v", err)
	}
	var session string
	for _, c := range resp.Cookies() {
		if c.Name == auth.CookieName && c.Value != "" {
			session = c.Value
		}
	}
	if session == "" {
		t.Fatal("no session cookie from /api/login")
	}
	return session, data
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func (h *passwordHarness) csrf(t *testing.T, session string) string {
	t.Helper()
	resp := h.do(t, "GET", "/api/me", nil, map[string]string{"Cookie": auth.CookieName + "=" + session})
	defer resp.Body.Close()
	var me struct {
		Email         string `json:"email"`
		CSRFToken     string `json:"csrf_token"`
		Role          string `json:"role"`
		IsSuperadmin  bool   `json:"is_superadmin"`
		AuthMode      string `json:"auth_mode"`
		Authenticated bool   `json:"authenticated"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&me); err != nil {
		t.Fatalf("/api/me decode: %v", err)
	}
	return me.CSRFToken
}

func (h *passwordHarness) authHeaders(t *testing.T, session string) map[string]string {
	t.Helper()
	return map[string]string{
		"Cookie":       auth.CookieName + "=" + session,
		"X-CSRF-Token": h.csrf(t, session),
	}
}

func TestPasswordAuthInfoIsPublic(t *testing.T) {
	h := newPasswordHarness(t)
	resp := h.do(t, "GET", "/api/auth-info", nil, nil)
	body := readBody(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(body, `"auth_mode":"password"`) {
		t.Errorf("body = %s, want auth_mode password", body)
	}
}

func TestPasswordLoginRoundTrip(t *testing.T) {
	h := newPasswordHarness(t)
	session, data := h.login(t, superadminEmail, superadminPassword)
	if data["email"] != superadminEmail || data["role"] != store.RoleSuperadmin {
		t.Errorf("login response = %v", data)
	}
	csrfToken, _ := data["csrf_token"].(string)
	if csrfToken == "" {
		t.Error("login response carries no csrf_token")
	}

	// /api/me reflects the password identity.
	resp := h.do(t, "GET", "/api/me", nil, map[string]string{"Cookie": auth.CookieName + "=" + session})
	var me struct {
		Email        string `json:"email"`
		Role         string `json:"role"`
		IsSuperadmin bool   `json:"is_superadmin"`
		AuthMode     string `json:"auth_mode"`
	}
	json.NewDecoder(resp.Body).Decode(&me)
	resp.Body.Close()
	if me.Email != superadminEmail || me.Role != store.RoleSuperadmin || !me.IsSuperadmin || me.AuthMode != "password" {
		t.Errorf("/api/me = %+v", me)
	}
}

func TestPasswordLoginFailures(t *testing.T) {
	h := newPasswordHarness(t)
	jsonHeaders := map[string]string{"Content-Type": "application/json"}
	for name, payload := range map[string]struct {
		body string
		want int
	}{
		"wrong password": {`{"email":"admin@example.com","password":"not-the-password1"}`, http.StatusUnauthorized},
		"unknown email":   {`{"email":"nobody@example.com","password":"whatever-password"}`, http.StatusUnauthorized},
		"empty password":  {`{"email":"admin@example.com","password":""}`, http.StatusBadRequest},
		"empty email":     {`{"email":"","password":"whatever-password"}`, http.StatusBadRequest},
		"malformed JSON":  {`not json`, http.StatusBadRequest},
	} {
		resp := h.do(t, "POST", "/api/login", strings.NewReader(payload.body), jsonHeaders)
		body := readBody(t, resp)
		if resp.StatusCode != payload.want {
			t.Errorf("%s: status = %d, want %d (body: %s)", name, resp.StatusCode, payload.want, body)
		}
		for _, c := range resp.Cookies() {
			if c.Name == auth.CookieName && c.Value != "" {
				t.Errorf("%s: session cookie set on failure", name)
			}
		}
	}
}

func TestPasswordModeDisablesOAuth(t *testing.T) {
	h := newPasswordHarness(t)
	if resp := h.do(t, "GET", "/login", nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /login = %d, want 404", resp.StatusCode)
		resp.Body.Close()
	} else {
		resp.Body.Close()
	}
	resp := h.do(t, "GET", "/oauth2/callback?code=x&state=y", nil, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("callback = %d, want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/?auth_error=auth_disabled" {
		t.Errorf("callback Location = %q", loc)
	}
}

func TestPasswordLoginDisabledInGoogleMode(t *testing.T) {
	h := newHarness(t)
	resp := h.do(t, "POST", controlHost, "/api/login",
		strings.NewReader(`{"email":"a@example.com","password":"whatever-password"}`),
		map[string]string{"Content-Type": "application/json"})
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (body: %s)", resp.StatusCode, body)
	}
}

func TestUserManagementLifecycle(t *testing.T) {
	h := newPasswordHarness(t)
	admin, _ := h.login(t, superadminEmail, superadminPassword)
	adminHeaders := h.authHeaders(t, admin)
	jsonHeaders := mergeHeaders(adminHeaders, map[string]string{"Content-Type": "application/json"})

	// Create a plain user.
	resp := h.do(t, "POST", "/api/users",
		strings.NewReader(`{"email":"new@example.com","password":"newuser-password-1","role":"user"}`), jsonHeaders)
	createdBody := readBody(t, resp)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d (body: %s)", resp.StatusCode, createdBody)
	}
	var created struct {
		User struct {
			ID    int64  `json:"id"`
			Email string `json:"email"`
			Role  string `json:"role"`
		} `json:"user"`
	}
	if err := json.Unmarshal([]byte(createdBody), &created); err != nil {
		t.Fatalf("create decode: %v", err)
	}
	if created.User.Email != "new@example.com" || created.User.Role != "user" || created.User.ID == 0 {
		t.Fatalf("created = %+v", created)
	}
	if strings.Contains(createdBody, "password_hash") || strings.Contains(createdBody, "newuser-password-1") {
		t.Error("create response leaks password material")
	}

	// Validation matrix.
	for name, payload := range map[string]struct {
		body string
		want int
	}{
		"duplicate email": {`{"email":"new@example.com","password":"another-password-1"}`, http.StatusConflict},
		"bad email":        {`{"email":"not-an-email","password":"another-password-1"}`, http.StatusBadRequest},
		"short password":   {`{"email":"short@example.com","password":"short"}`, http.StatusBadRequest},
		"bad role":         {`{"email":"r@example.com","password":"another-password-1","role":"owner"}`, http.StatusBadRequest},
		"malformed JSON":   {`not json`, http.StatusBadRequest},
	} {
		r := h.do(t, "POST", "/api/users", strings.NewReader(payload.body), jsonHeaders)
		b := readBody(t, r)
		if r.StatusCode != payload.want {
			t.Errorf("create %s: status = %d, want %d (body: %s)", name, r.StatusCode, payload.want, b)
		}
	}

	// List shows all three accounts with no password material.
	resp = h.do(t, "GET", "/api/users", nil, adminHeaders)
	listBody := readBody(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("list status = %d", resp.StatusCode)
	}
	var list struct {
		Users []struct {
			Email string `json:"email"`
			Role  string `json:"role"`
		} `json:"users"`
	}
	if err := json.Unmarshal([]byte(listBody), &list); err != nil {
		t.Fatalf("list decode: %v", err)
	}
	if len(list.Users) != 3 {
		t.Errorf("users = %+v, want 3", list.Users)
	}
	if strings.Contains(listBody, "password_hash") {
		t.Error("list response leaks password hashes")
	}

	// The new user can log in and manage demos but not users.
	newSession, _ := h.login(t, "new@example.com", "newuser-password-1")
	newHeaders := map[string]string{"Cookie": auth.CookieName + "=" + newSession}
	if resp := h.do(t, "GET", "/api/users", nil, newHeaders); resp.StatusCode != http.StatusForbidden {
		t.Errorf("plain user list = %d, want 403", resp.StatusCode)
		resp.Body.Close()
	} else {
		resp.Body.Close()
	}
	if resp := h.do(t, "POST", "/api/users", strings.NewReader(`{"email":"x@example.com","password":"another-password-1"}`),
		mergeHeaders(newHeaders, map[string]string{"Content-Type": "application/json", "X-CSRF-Token": h.csrf(t, newSession)})); resp.StatusCode != http.StatusForbidden {
		t.Errorf("plain user create = %d, want 403", resp.StatusCode)
		resp.Body.Close()
	} else {
		resp.Body.Close()
	}

	// Unauthenticated user routes are 401, and mutations without CSRF 403.
	if resp := h.do(t, "GET", "/api/users", nil, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("anonymous list = %d, want 401", resp.StatusCode)
		resp.Body.Close()
	} else {
		resp.Body.Close()
	}
	if resp := h.do(t, "POST", "/api/users",
		strings.NewReader(`{"email":"x@example.com","password":"another-password-1"}`),
		map[string]string{"Cookie": auth.CookieName + "=" + admin, "Content-Type": "application/json"}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("create without CSRF = %d, want 403", resp.StatusCode)
		resp.Body.Close()
	} else {
		resp.Body.Close()
	}

	// Reset the new user's password; the old one stops working.
	resp = h.do(t, "POST", "/api/users/"+strconv.FormatInt(created.User.ID, 10)+"/password",
		strings.NewReader(`{"password":"rotated-password-1"}`), jsonHeaders)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("reset status = %d", resp.StatusCode)
	}
	if resp := h.do(t, "POST", "/api/login",
		strings.NewReader(`{"email":"new@example.com","password":"newuser-password-1"}`),
		map[string]string{"Content-Type": "application/json"}); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("old password after reset = %d, want 401", resp.StatusCode)
		resp.Body.Close()
	} else {
		resp.Body.Close()
	}
	if _, data := h.login(t, "new@example.com", "rotated-password-1"); data["email"] != "new@example.com" {
		t.Errorf("login with rotated password = %v", data)
	}

	// Delete the user; their session dies with the account.
	resp = h.do(t, "DELETE", "/api/users/"+strconv.FormatInt(created.User.ID, 10), nil, adminHeaders)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d", resp.StatusCode)
	}
	if resp := h.do(t, "GET", "/api/me", nil, newHeaders); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("/api/me after delete = %d, want 401", resp.StatusCode)
		resp.Body.Close()
	} else {
		resp.Body.Close()
	}
	if resp := h.do(t, "DELETE", "/api/users/999999", nil, adminHeaders); resp.StatusCode != http.StatusNotFound {
		t.Errorf("delete unknown = %d, want 404", resp.StatusCode)
		resp.Body.Close()
	} else {
		resp.Body.Close()
	}
}

func (h *passwordHarness) userIDByEmail(t *testing.T, headers map[string]string, email string) int64 {
	t.Helper()
	resp := h.do(t, "GET", "/api/users", nil, headers)
	var list struct {
		Users []struct {
			ID    int64  `json:"id"`
			Email string `json:"email"`
		} `json:"users"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		resp.Body.Close()
		t.Fatalf("list decode: %v", err)
	}
	resp.Body.Close()
	for _, u := range list.Users {
		if u.Email == email {
			return u.ID
		}
	}
	t.Fatalf("user %q missing from list", email)
	return 0
}

func (h *passwordHarness) createUser(t *testing.T, headers map[string]string, email, password, role string) int64 {
	t.Helper()
	resp := h.do(t, "POST", "/api/users",
		strings.NewReader(`{"email":`+quote(email)+`,"password":`+quote(password)+`,"role":`+quote(role)+`}`),
		mergeHeaders(headers, map[string]string{"Content-Type": "application/json"}))
	var created struct {
		User struct {
			ID int64 `json:"id"`
		} `json:"user"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		resp.Body.Close()
		t.Fatalf("create decode: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create %s status = %d", email, resp.StatusCode)
	}
	return created.User.ID
}
func TestUserManagementSelfDeleteGuard(t *testing.T) {
	h := newPasswordHarness(t)
	admin, _ := h.login(t, superadminEmail, superadminPassword)
	adminHeaders := h.authHeaders(t, admin)

	selfID := h.userIDByEmail(t, adminHeaders, superadminEmail)

	// Self-delete is refused even though we are a superadmin.
	resp := h.do(t, "DELETE", "/api/users/"+strconv.FormatInt(selfID, 10), nil, adminHeaders)
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("self delete = %d, want 409 (body: %s)", resp.StatusCode, body)
	}
	// Still there and still able to act.
	h.userIDByEmail(t, adminHeaders, superadminEmail)
}

func TestLastSuperadminDeleteRefused(t *testing.T) {
	h := newPasswordHarness(t)
	admin, _ := h.login(t, superadminEmail, superadminPassword)
	adminHeaders := h.authHeaders(t, admin)

	// A second superadmin deletes the bootstrap one (two exist → fine),
	// leaving itself as the last superadmin.
	extraID := h.createUser(t, adminHeaders, "extra@example.com", "extra-password-1", store.RoleSuperadmin)
	extra, _ := h.login(t, "extra@example.com", "extra-password-1")
	extraHeaders := h.authHeaders(t, extra)
	adminID := h.userIDByEmail(t, extraHeaders, superadminEmail)
	_ = extraID
	_ = adminID
	resp := h.do(t, "DELETE", "/api/users/"+strconv.FormatInt(adminID, 10), nil, extraHeaders)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete one of two superadmins = %d, want 204", resp.StatusCode)
	}

	// Minting another superadmin ends extra's lastness, so deleting it now
	// succeeds — the guard only protects the final superadmin. A non-self
	// delete of the last superadmin is unreachable over HTTP (the requester
	// would have to be a second superadmin); the store-level guard is
	// covered by TestDeleteUserProtectsLastSuperadmin.
	h.createUser(t, extraHeaders, "final@example.com", "final-password-1", store.RoleSuperadmin)
	final, _ := h.login(t, "final@example.com", "final-password-1")
	finalHeaders := h.authHeaders(t, final)
	lastID := h.userIDByEmail(t, finalHeaders, "extra@example.com")
	resp = h.do(t, "DELETE", "/api/users/"+strconv.FormatInt(lastID, 10), nil, finalHeaders)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete formerly-last superadmin = %d, want 204", resp.StatusCode)
	}

	// `final` is now the last superadmin; its own delete attempt is the
	// only reachable one and must be refused (self-delete guard, with the
	// last-superadmin guard beneath it).
	finalID := h.userIDByEmail(t, finalHeaders, "final@example.com")
	resp = h.do(t, "DELETE", "/api/users/"+strconv.FormatInt(finalID, 10), nil, finalHeaders)
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("delete last superadmin = %d, want 409 (body: %s)", resp.StatusCode, body)
	}
	// And the account survived the refusal.
	h.userIDByEmail(t, finalHeaders, "final@example.com")
}

func TestGoogleSessionCannotManageUsers(t *testing.T) {
	h := newHarness(t)
	session := h.login(t)
	csrfToken := h.csrf(t, session)
	headers := map[string]string{
		"Cookie":       auth.CookieName + "=" + session,
		"X-CSRF-Token": csrfToken,
	}
	if resp := h.do(t, "GET", controlHost, "/api/users", nil, headers); resp.StatusCode != http.StatusForbidden {
		t.Errorf("google list users = %d, want 403", resp.StatusCode)
		resp.Body.Close()
	} else {
		resp.Body.Close()
	}
}

// demoHeaders builds cookie+CSRF(+JSON) headers for a password-mode
// session; csrfFor is the harness csrf helper over /api/me.
func (h *passwordHarness) demoHeaders(t *testing.T, session string, jsonBody bool) map[string]string {
	t.Helper()
	headers := map[string]string{
		"Cookie":       auth.CookieName + "=" + session,
		"X-CSRF-Token": h.csrf(t, session),
	}
	if jsonBody {
		headers["Content-Type"] = "application/json"
	}
	return headers
}

// loginAsUser creates a fresh password user via the superadmin and logs in
// as them — multi-actor permission tests.
func (h *passwordHarness) loginAsUser(t *testing.T, adminHeaders map[string]string, email string) string {
	t.Helper()
	h.createUser(t, adminHeaders, email, email+"-password-1", store.RoleUser)
	session, _ := h.login(t, email, email+"-password-1")
	return session
}

func TestDemoManagementPermissions(t *testing.T) {
	h := newPasswordHarness(t)
	admin, _ := h.login(t, superadminEmail, superadminPassword)
	adminHeaders := h.demoHeaders(t, admin, true)

	owner := h.loginAsUser(t, adminHeaders, "owner@example.com")
	stranger := h.loginAsUser(t, adminHeaders, "stranger@example.com")

	// The owner creates a demo.
	resp := h.do(t, "POST", "/api/demos", strings.NewReader(`{"name":"mine"}`), h.demoHeaders(t, owner, true))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("owner create = %d (body: %s)", resp.StatusCode, readBody(t, resp))
	}
	resp.Body.Close()

	strangerHeaders := h.demoHeaders(t, stranger, true)

	// A deploy attempt by the stranger is refused before any upload work.
	deployBody := strings.NewReader(`{"x":1}`) // body shape irrelevant: 403 comes first
	resp = h.do(t, "POST", "/api/demos/mine/deploy", deployBody, strangerHeaders)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("stranger deploy = %d, want 403", resp.StatusCode)
	}
	resp.Body.Close()

	// Rollback, rename, privacy, delete — all owner/superadmin only.
	for name, req := range map[string]struct {
		method string
		path   string
		body   string
	}{
		"rollback": {"POST", "/api/demos/mine/rollback", ""},
		"rename":   {"PATCH", "/api/demos/mine", `{"name":"stolen"}`},
		"privacy":  {"PATCH", "/api/demos/mine", `{"private":true}`},
		"delete":   {"DELETE", "/api/demos/mine", ""},
	} {
		var body io.Reader
		if req.body != "" {
			body = strings.NewReader(req.body)
		}
		resp := h.do(t, req.method, req.path, body, strangerHeaders)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("stranger %s = %d, want 403 (body: %s)", name, resp.StatusCode, readBody(t, resp))
		}
		resp.Body.Close()
	}

	// The demo survived every refusal under its original name and owner.
	resp = h.do(t, "GET", "/api/demos", nil, map[string]string{"Cookie": auth.CookieName + "=" + owner})
	var list struct {
		Demos []struct {
			Name      string `json:"name"`
			CreatedBy string `json:"created_by"`
			Private   bool   `json:"private"`
		} `json:"demos"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatalf("list decode: %v", err)
	}
	resp.Body.Close()
	found := false
	for _, d := range list.Demos {
		if d.Name == "mine" {
			found = true
			if d.CreatedBy != "owner@example.com" || d.Private {
				t.Errorf("demo mutated by refusals: %+v", d)
			}
		}
	}
	if !found {
		t.Fatal("demo missing from list after refused mutations")
	}

	// The owner manages their own demo: privacy on…
	ownerHeaders := h.demoHeaders(t, owner, true)
	resp = h.do(t, "PATCH", "/api/demos/mine", strings.NewReader(`{"private":true}`), ownerHeaders)
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"private":true`) {
		t.Fatalf("owner enable privacy = %d %s", resp.StatusCode, body)
	}
	// …the superadmin can manage it too…
	resp = h.do(t, "PATCH", "/api/demos/mine", strings.NewReader(`{"private":false}`), adminHeaders)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("superadmin disable privacy = %d", resp.StatusCode)
	}
	// …and delete it.
	resp = h.do(t, "DELETE", "/api/demos/mine", nil, h.demoHeaders(t, admin, false))
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("superadmin delete = %d, want 204", resp.StatusCode)
	}
}

func TestRenameResponseCarriesNewName(t *testing.T) {
	h := newPasswordHarness(t)
	admin, _ := h.login(t, superadminEmail, superadminPassword)
	headers := h.demoHeaders(t, admin, true)

	resp := h.do(t, "POST", "/api/demos", strings.NewReader(`{"name":"before"}`), headers)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d", resp.StatusCode)
	}
	resp = h.do(t, "PATCH", "/api/demos/before", strings.NewReader(`{"name":"after"}`), headers)
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rename = %d (body: %s)", resp.StatusCode, body)
	}
	var data struct {
		Demo struct {
			Name string `json:"name"`
		} `json:"demo"`
	}
	if err := json.Unmarshal([]byte(body), &data); err != nil || data.Demo.Name != "after" {
		t.Errorf("rename response = %s, want the updated name", body)
	}
}

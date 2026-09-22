package auth

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"democtl/internal/config"
	"democtl/internal/store"
)

// goodUser is the identity a compliant Workspace sign-in produces.
var goodUser = identity{
	Sub:           "google-sub-1",
	Email:         "pm@example.com",
	EmailVerified: true,
	HD:            "example.com",
}

func testConfig() config.Config {
	return config.Config{
		ControlHost:        "demos.example.com",
		BaseDomain:         "example.com",
		GoogleClientID:     "test-client-id",
		GoogleClientSecret: "test-client-secret",
		WorkspaceDomain:    "example.com",
	}
}

// stubIDP is a stand-in Google: a /token endpoint that always grants an
// access token and a /userinfo endpoint serving whatever the test staged.
type stubIDP struct {
	srv  *httptest.Server
	mu   sync.Mutex
	info identity
}

func newStubIDP(t *testing.T, info identity) *stubIDP {
	t.Helper()
	idp := &stubIDP{info: info}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Literal token value — it exists only inside this test process
		// and is never printed anywhere.
		_, _ = io.WriteString(w, `{"access_token":"stub-access-token","token_type":"bearer","expires_in":3600}`)
	})
	mux.HandleFunc("GET /userinfo", func(w http.ResponseWriter, _ *http.Request) {
		idp.mu.Lock()
		info := idp.info
		idp.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(info)
	})
	idp.srv = httptest.NewServer(mux)
	t.Cleanup(idp.srv.Close)
	return idp
}

func (s *stubIDP) endpoint() oauth2.Endpoint {
	return oauth2.Endpoint{AuthURL: s.srv.URL + "/auth", TokenURL: s.srv.URL + "/token"}
}

func (s *stubIDP) setInfo(info identity) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.info = info
}

// newAuth wires an Authenticator against a fresh temp-dir sqlite and the
// stub IdP; mutate adjusts Deps per test. now is pinned so expiry is
// deterministic.
func newAuth(t *testing.T, info identity, mutate func(*Deps)) (*Authenticator, *stubIDP, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "auth-test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	idp := newStubIDP(t, info)
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	d := Deps{
		Cfg:          testConfig(),
		Sessions:     st,
		Now:          func() time.Time { return now },
		Google:       idp.endpoint(),
		UserInfoURL:  idp.srv.URL + "/userinfo",
		SecureCookie: true,
	}
	if mutate != nil {
		mutate(&d)
	}
	return New(d), idp, st
}

// login drives a full successful Callback and returns the Session the
// authenticator reports plus the raw cookie value set on the response.
func login(t *testing.T, a *Authenticator) (Session, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/oauth2/callback?code=one-time-code&state=st4te", nil)
	req.AddCookie(&http.Cookie{Name: StateCookieName, Value: "st4te"})
	sess, err := a.Callback(rec, req)
	if err != nil {
		t.Fatalf("Callback: %v", err)
	}
	raw := sessionCookieValue(t, rec)
	if raw == "" {
		t.Fatal("no session cookie set on success")
	}
	return sess, raw
}

// sessionCookieValue extracts the session cookie's value from the recorder
// without ever printing it.
func sessionCookieValue(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	for _, c := range setCookies(t, rec) {
		if c.Name == CookieName {
			return c.Value
		}
	}
	return ""
}

// setCookies parses every Set-Cookie header; on a parse failure the raw
// line is never included in the error (no cookie values in test output).
func setCookies(t *testing.T, rec *httptest.ResponseRecorder) []*http.Cookie {
	t.Helper()
	var out []*http.Cookie
	for i, line := range rec.Result().Header.Values("Set-Cookie") {
		c, err := http.ParseSetCookie(line)
		if err != nil {
			t.Fatalf("ParseSetCookie[%d]: %v", i, err)
		}
		out = append(out, c)
	}
	return out
}

func hasStateClearing(t *testing.T, rec *httptest.ResponseRecorder) bool {
	t.Helper()
	for _, c := range setCookies(t, rec) {
		if c.Name == StateCookieName {
			return c.MaxAge < 0
		}
	}
	return false
}

func hasSessionCookie(rec *httptest.ResponseRecorder) bool {
	for _, line := range rec.Result().Header.Values("Set-Cookie") {
		if strings.HasPrefix(line, CookieName+"=") {
			return true
		}
	}
	return false
}

func withSessionCookie(req *http.Request, raw string) *http.Request {
	req.AddCookie(&http.Cookie{Name: CookieName, Value: raw})
	return req
}

func TestBeginLogin(t *testing.T) {
	a, idp, _ := newAuth(t, goodUser, nil)

	rec := httptest.NewRecorder()
	a.BeginLogin(rec, httptest.NewRequest("GET", "/login", nil))

	if rec.Code != http.StatusFound {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusFound)
	}
	u, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	if u.Scheme+"://"+u.Host+u.Path != idp.srv.URL+"/auth" {
		t.Errorf("auth URL = %s://%s%s, want the stub /auth endpoint", u.Scheme, u.Host, u.Path)
	}
	q := u.Query()
	if got := q.Get("client_id"); got != "test-client-id" {
		t.Errorf("client_id = %q", got)
	}
	// redirect_uri is always https + control host, never derived from the
	// request or the (http) test server.
	if got := q.Get("redirect_uri"); got != "https://demos.example.com/oauth2/callback" {
		t.Errorf("redirect_uri = %q", got)
	}
	if got := q.Get("response_type"); got != "code" {
		t.Errorf("response_type = %q", got)
	}
	if got := q.Get("scope"); got != "openid email" {
		t.Errorf("scope = %q", got)
	}
	if got := q.Get("hd"); got != "example.com" {
		t.Errorf("hd = %q", got)
	}
	state := q.Get("state")
	if len(state) != 32 { // 128-bit, hex — 32 chars
		t.Errorf("state length = %d, want 32", len(state))
	}

	var sc *http.Cookie
	for _, c := range setCookies(t, rec) {
		if c.Name == StateCookieName {
			sc = c
		}
	}
	if sc == nil {
		t.Fatal("no state cookie set")
	}
	if sc.Value != state {
		t.Error("state cookie value does not match the query state")
	}
	if !sc.HttpOnly || !sc.Secure {
		t.Error("state cookie missing HttpOnly/Secure")
	}
	if sc.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax", sc.SameSite)
	}
	if sc.Path != "/" {
		t.Errorf("Path = %q, want /", sc.Path)
	}
	if sc.MaxAge != int(StateTTL.Seconds()) {
		t.Errorf("MaxAge = %d, want %d", sc.MaxAge, int(StateTTL.Seconds()))
	}
	if sc.Domain != "" {
		t.Error("state cookie carries a Domain attribute")
	}
}

func TestCallbackHappyPath(t *testing.T) {
	a, _, _ := newAuth(t, goodUser, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/oauth2/callback?code=one-time-code&state=st4te", nil)
	req.AddCookie(&http.Cookie{Name: StateCookieName, Value: "st4te"})
	sess, err := a.Callback(rec, req)
	if err != nil {
		t.Fatalf("Callback: %v", err)
	}
	if sess.GoogleEmail != goodUser.Email || sess.GoogleSub != goodUser.Sub {
		t.Errorf("returned Session identity = sub %q email %q", sess.GoogleSub, sess.GoogleEmail)
	}
	if len(sess.IDHash) != 64 || len(sess.CSRFToken) != 64 {
		t.Errorf("Session field lengths: idhash %d csrf %d, want 64 each", len(sess.IDHash), len(sess.CSRFToken))
	}

	var sc *http.Cookie
	for _, c := range setCookies(t, rec) {
		// Host-only is load-bearing (docs/demos.md §Auth step 3): any
		// Domain attribute would leak the session to demo subdomains.
		if c.Domain != "" {
			t.Error("Set-Cookie carries a Domain attribute")
		}
		if c.Name == CookieName {
			sc = c
		}
	}
	if sc == nil {
		t.Fatal("no session cookie set")
	}
	if !sc.HttpOnly || !sc.Secure {
		t.Error("session cookie missing HttpOnly/Secure")
	}
	if sc.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax", sc.SameSite)
	}
	if sc.Path != "/" {
		t.Errorf("Path = %q, want /", sc.Path)
	}
	if sc.MaxAge != int(SessionTTL.Seconds()) {
		t.Errorf("MaxAge = %d, want %d", sc.MaxAge, int(SessionTTL.Seconds()))
	}
	if len(sc.Value) != 64 { // 32 bytes hex
		t.Errorf("session cookie length = %d, want 64", len(sc.Value))
	}
	if !hasStateClearing(t, rec) {
		t.Error("state cookie not cleared on success")
	}

	got, ok := a.Session(withSessionCookie(httptest.NewRequest("GET", "/", nil), sc.Value))
	if !ok {
		t.Fatal("Session: not resolved from the new cookie")
	}
	if got != sess {
		// Field values (id hash, CSRF token) are never printed.
		t.Error("resolved Session differs from the one Callback returned")
	}
}

func TestCallbackWithoutTLSOmitsSecure(t *testing.T) {
	a, _, _ := newAuth(t, goodUser, func(d *Deps) { d.SecureCookie = false })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/oauth2/callback?code=c&state=s", nil)
	req.AddCookie(&http.Cookie{Name: StateCookieName, Value: "s"})
	if _, err := a.Callback(rec, req); err != nil {
		t.Fatalf("Callback: %v", err)
	}
	for _, c := range setCookies(t, rec) {
		if c.Secure {
			t.Errorf("cookie %s sets Secure with SecureCookie=false", c.Name)
		}
	}
}

func TestCallbackStateMismatch(t *testing.T) {
	a, _, _ := newAuth(t, goodUser, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/oauth2/callback?code=c&state=forged", nil)
	req.AddCookie(&http.Cookie{Name: StateCookieName, Value: "expected"})
	_, err := a.Callback(rec, req)

	if !errors.Is(err, ErrBadState) {
		t.Errorf("err = %v, want ErrBadState", err)
	}
	if errors.Is(err, ErrWorkspaceMismatch) {
		t.Error("state failure must not surface as a workspace error")
	}
	if !hasStateClearing(t, rec) {
		t.Error("state cookie not cleared on mismatch")
	}
	if hasSessionCookie(rec) {
		t.Error("session cookie set on state mismatch")
	}
}

func TestCallbackStateCookieMissing(t *testing.T) {
	a, _, _ := newAuth(t, goodUser, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/oauth2/callback?code=c&state=st4te", nil)
	_, err := a.Callback(rec, req)

	if !errors.Is(err, ErrBadState) {
		t.Errorf("err = %v, want ErrBadState", err)
	}
	if !hasStateClearing(t, rec) {
		t.Error("state cookie not cleared when absent")
	}
	if hasSessionCookie(rec) {
		t.Error("session cookie set without state cookie")
	}
}

func TestCallbackStateParamMissing(t *testing.T) {
	a, _, _ := newAuth(t, goodUser, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/oauth2/callback?code=c", nil)
	req.AddCookie(&http.Cookie{Name: StateCookieName, Value: "st4te"})
	if _, err := a.Callback(rec, req); !errors.Is(err, ErrBadState) {
		t.Errorf("err = %v, want ErrBadState", err)
	}
	if !hasStateClearing(t, rec) {
		t.Error("state cookie not cleared when the query state is missing")
	}
}

func TestCallbackWorkspaceEnforcement(t *testing.T) {
	cases := []struct {
		name    string
		info    identity
		wantErr bool
	}{
		{"matching domain", goodUser, false},
		{"wrong domain", identity{Sub: "s", Email: "attacker@gmail.com", EmailVerified: true, HD: "gmail.com"}, true},
		// Everything after the LAST "@" — here the empty string — is the
		// domain, so trailing-@ addresses can never smuggle a match.
		{"multi-@ trailing at", identity{Sub: "s", Email: "a@evil.com@", EmailVerified: true}, true},
		{"email not verified", identity{Sub: "s", Email: "pm@example.com", EmailVerified: false, HD: "example.com"}, true},
		{"hd absent but email domain matches", identity{Sub: "s", Email: "pm@example.com", EmailVerified: true}, false},
		{"empty email", identity{Sub: "s", EmailVerified: true, HD: "example.com"}, true},
		{"no @ at all", identity{Sub: "s", Email: "not-an-email", EmailVerified: true}, true},
		{"domain case-insensitive", identity{Sub: "s", Email: "PM@Example.com", EmailVerified: true}, false},
		{"hd lies, email decides", identity{Sub: "s", Email: "pm@example.com", EmailVerified: true, HD: "evil.example"}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, _, _ := newAuth(t, tc.info, nil)

			rec := httptest.NewRecorder()
			req := httptest.NewRequest("GET", "/oauth2/callback?code=c&state=s", nil)
			req.AddCookie(&http.Cookie{Name: StateCookieName, Value: "s"})
			sess, err := a.Callback(rec, req)

			if tc.wantErr {
				if !errors.Is(err, ErrWorkspaceMismatch) {
					t.Fatalf("err = %v, want ErrWorkspaceMismatch", err)
				}
				if errors.Is(err, ErrBadState) {
					t.Error("workspace rejection must not surface as a state error")
				}
				if sess != (Session{}) {
					t.Error("Session returned alongside a rejection")
				}
				if hasSessionCookie(rec) {
					t.Error("session cookie set on workspace rejection")
				}
				return
			}
			if err != nil {
				t.Fatalf("Callback: %v", err)
			}
			raw := sessionCookieValue(t, rec)
			if raw == "" {
				t.Fatal("no session cookie on the accepted path")
			}
			got, ok := a.Session(withSessionCookie(httptest.NewRequest("GET", "/", nil), raw))
			if !ok {
				t.Fatal("session did not resolve")
			}
			if got.GoogleEmail != tc.info.Email {
				t.Errorf("stored email does not match the accepted identity")
			}
		})
	}
}

func TestEmailDomain(t *testing.T) {
	cases := []struct{ email, want string }{
		{"pm@example.com", "example.com"},
		{"PM@EXAMPLE.COM", "example.com"},
		{"a@evil.com@", ""}, // last "@" wins
		{"no-at-sign", ""},
		{"@leading", "leading"}, // spec: everything after the LAST "@"
		{"", ""},
	}
	for _, tc := range cases {
		if got := emailDomain(tc.email); got != tc.want {
			t.Errorf("emailDomain(%q) = %q, want %q", tc.email, got, tc.want)
		}
	}
}

func TestCSRF(t *testing.T) {
	a, _, _ := newAuth(t, goodUser, nil)
	sess, raw := login(t, a)

	cases := []struct {
		name   string
		header string
		want   bool
	}{
		{"matching", sess.CSRFToken, true},
		{"mismatching", sess.CSRFToken + "x", false},
		{"totally different", "deadbeef", false},
		{"missing", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := withSessionCookie(httptest.NewRequest("POST", "/api/demos", nil), raw)
			if tc.header != "" {
				req.Header.Set(csrfHeader, tc.header)
			}
			got, ok := a.Session(req)
			if !ok {
				t.Fatal("session did not resolve")
			}
			if have := CSRF(req, got); have != tc.want {
				t.Errorf("CSRF = %v, want %v", have, tc.want)
			}
			// An empty session token never validates anything.
			if CSRF(req, Session{}) {
				t.Error("CSRF true against an empty Session")
			}
		})
	}
}

func TestRequireSession(t *testing.T) {
	t.Run("unauthenticated 401 envelope", func(t *testing.T) {
		a, _, _ := newAuth(t, goodUser, nil)

		called := false
		rec := httptest.NewRecorder()
		a.RequireSession(func(http.ResponseWriter, *http.Request) { called = true })(
			rec, httptest.NewRequest("GET", "/", nil))

		if called {
			t.Error("next invoked without a session")
		}
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
		}
		if got := rec.Body.String(); got != `{"error":{"message":"unauthenticated"}}` {
			t.Errorf("body = %q", got)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
	})

	t.Run("session in context", func(t *testing.T) {
		a, _, _ := newAuth(t, goodUser, nil)
		want, raw := login(t, a)

		var got Session
		var ok bool
		rec := httptest.NewRecorder()
		a.RequireSession(func(_ http.ResponseWriter, r *http.Request) {
			got, ok = FromContext(r.Context())
		})(rec, withSessionCookie(httptest.NewRequest("GET", "/", nil), raw))

		if !ok {
			t.Fatal("FromContext: not present")
		}
		if got != want {
			t.Error("FromContext Session differs from the logged-in identity")
		}
	})
}

func TestLogoutRevokes(t *testing.T) {
	a, _, st := newAuth(t, goodUser, nil)
	want, raw := login(t, a)

	req := withSessionCookie(httptest.NewRequest("POST", "/logout", nil), raw)
	if _, ok := a.Session(req); !ok {
		t.Fatal("session should resolve before logout")
	}
	rec := httptest.NewRecorder()
	a.Logout(rec, req)

	if _, ok := a.Session(req); ok {
		t.Error("session still resolves after logout")
	}
	if _, err := st.SessionByIDHash(want.IDHash); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("store row err = %v, want ErrNotFound", err)
	}
	var sc *http.Cookie
	for _, c := range setCookies(t, rec) {
		if c.Name == CookieName {
			sc = c
		}
	}
	if sc == nil || sc.MaxAge >= 0 {
		t.Error("session cookie not expired on logout")
	}
}

func TestLogoutWithoutCookie(t *testing.T) {
	a, _, _ := newAuth(t, goodUser, nil)

	rec := httptest.NewRecorder()
	a.Logout(rec, httptest.NewRequest("POST", "/logout", nil)) // must not error or panic

	var sc *http.Cookie
	for _, c := range setCookies(t, rec) {
		if c.Name == CookieName {
			sc = c
		}
	}
	if sc == nil || sc.MaxAge >= 0 {
		t.Error("logout without a cookie should still expire the cookie")
	}
}

func TestSessionExpiry(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	a, _, st := newAuth(t, goodUser, nil)
	want, raw := login(t, a)

	live := withSessionCookie(httptest.NewRequest("GET", "/", nil), raw)
	a.now = func() time.Time { return now.Add(SessionTTL - time.Second) }
	if _, ok := a.Session(live); !ok {
		t.Fatal("session expired one second early")
	}

	// At exactly ExpiresAt the session is stale — and the row is deleted
	// lazily on that check.
	a.now = func() time.Time { return now.Add(SessionTTL) }
	if _, ok := a.Session(live); ok {
		t.Fatal("session still live at ExpiresAt")
	}
	if _, err := st.SessionByIDHash(want.IDHash); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("expired row err = %v, want ErrNotFound (lazy delete)", err)
	}
}

func TestSessionUnknownCookie(t *testing.T) {
	a, _, _ := newAuth(t, goodUser, nil)

	req := withSessionCookie(httptest.NewRequest("GET", "/", nil), strings.Repeat("ab", 32))
	if _, ok := a.Session(req); ok {
		t.Error("unknown cookie resolved to a session")
	}
}

func TestNewDefaults(t *testing.T) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "defaults.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	a := New(Deps{Cfg: testConfig(), Sessions: st})
	if a.now == nil {
		t.Error("Now default not applied")
	}
	if a.endpoint.AuthURL != google.Endpoint.AuthURL || a.endpoint.TokenURL != google.Endpoint.TokenURL {
		t.Error("Google endpoint default not applied")
	}
	if a.userInfoURL != defaultUserInfoURL {
		t.Errorf("UserInfoURL default = %q, want the Google OIDC userinfo URL", a.userInfoURL)
	}
}

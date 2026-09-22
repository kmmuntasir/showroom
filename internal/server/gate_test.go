package server_test

// Per-demo privacy gate coverage: the access page, the verify endpoint,
// the gate cookie, rotation killing old cookies, disable opening the demo,
// and the management permissions that surround it.

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"democtl/internal/auth"
	"democtl/internal/config"
	"democtl/internal/store"
)

// createPrivate makes a private demo over the API and returns the
// one-time access key from the create response.
func createPrivate(t *testing.T, h *harness, session, csrfToken, name string) string {
	t.Helper()
	resp := h.do(t, "POST", controlHost, "/api/demos",
		strings.NewReader(`{"name":`+quote(name)+`,"private":true}`),
		map[string]string{
			"Cookie":       auth.CookieName + "=" + session,
			"X-CSRF-Token": csrfToken,
			"Content-Type": "application/json",
		})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create private demo = %d (body: %s)", resp.StatusCode, body)
	}
	var created struct {
		Demo struct {
			Private bool `json:"private"`
		} `json:"demo"`
		AccessKey string `json:"access_key"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("create decode: %v", err)
	}
	if !created.Demo.Private {
		t.Errorf("created demo not private: %s", body)
	}
	if len(created.AccessKey) < 15 {
		t.Errorf("access_key = %q, want a generated key", created.AccessKey)
	}
	if strings.Contains(string(body), "access_key_hash") {
		t.Error("create response leaks the key hash")
	}
	return created.AccessKey
}

// verifyKey posts a key to the demo host's verify endpoint and returns the
// response plus the gate cookie value ("" when none was set).
func verifyKey(t *testing.T, h *harness, host, key string, wantStatus int) string {
	t.Helper()
	resp := h.do(t, "POST", host, "/.democtl/access/verify",
		strings.NewReader(`{"key":`+quote(key)+`}`),
		map[string]string{"Content-Type": "application/json"})
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("verify = %d, want %d (body: %s)", resp.StatusCode, wantStatus, body)
	}
	if wantStatus == http.StatusOK {
		for _, c := range resp.Cookies() {
			if c.Name == "democtl_access" {
				return c.Value
			}
		}
		t.Fatal("no gate cookie on successful verify")
	}
	for _, c := range resp.Cookies() {
		if c.Name == "democtl_access" && c.Value != "" {
			t.Error("gate cookie set on failed verify")
		}
	}
	return ""
}

func gateHeadersOK(t *testing.T, resp *http.Response) {
	t.Helper()
	if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	if xr := resp.Header.Get("X-Robots-Tag"); !strings.Contains(xr, "noindex") {
		t.Errorf("X-Robots-Tag = %q, want noindex", xr)
	}
}

func TestPrivateDemoGateEndToEnd(t *testing.T) {
	h := newHarness(t)
	session := h.login(t)
	csrfToken := h.csrf(t, session)
	key := createPrivate(t, h, session, csrfToken, "acme")

	// Locked: / renders the access page at the typed URL, not the demo.
	resp := h.do(t, "GET", demoHost, "/", nil, nil)
	page := readBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("locked / = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(page, "This demo is private") || !strings.Contains(page, "acme.example.com") {
		t.Errorf("access page content = %.120q", page)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type = %q", ct)
	}
	gateHeadersOK(t, resp)
	if resp.Header.Get("Content-Security-Policy") == "" {
		t.Error("access page carries no CSP")
	}

	// Deep paths bounce to the gate with a ?next= bookmark.
	resp = h.do(t, "GET", demoHost, "/deep/route?x=1", nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("locked deep path = %d, want 302", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); !strings.HasPrefix(loc, "/?next=") {
		t.Errorf("Location = %q, want /?next=...", loc)
	}

	// Wrong key: 401, no cookie. Correct key: cookie with the house
	// attributes (host-only by the absent Domain attribute).
	verifyKey(t, h, demoHost, "wrong-key-attempt-1", http.StatusUnauthorized)
	resp = h.do(t, "POST", demoHost, "/.democtl/access/verify",
		strings.NewReader(`{"key":`+quote(key)+`}`),
		map[string]string{"Content-Type": "application/json"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("verify = %d, want 200", resp.StatusCode)
	}
	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "democtl_access" {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("no gate cookie from verify")
	}
	if !cookie.HttpOnly || cookie.Path != "/" || cookie.MaxAge != 7*24*3600 {
		t.Errorf("cookie = %+v, want HttpOnly Path=/ Max-Age=7d", cookie)
	}
	if cookie.Domain != "" {
		t.Errorf("cookie Domain = %q, want host-only", cookie.Domain)
	}
	if cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("cookie SameSite = %v, want Lax", cookie.SameSite)
	}
	gateCookie := "democtl_access=" + cookie.Value

	// Deploy; the unlocked cookie serves the content with private headers.
	h.deploy(t, session, csrfToken, "acme", zipWith(map[string]string{"index.html": "gate-v1"}), http.StatusCreated)
	resp = h.do(t, "GET", demoHost, "/", nil, map[string]string{"Cookie": gateCookie})
	if body := readBody(t, resp); resp.StatusCode != http.StatusOK || body != "gate-v1" {
		t.Errorf("unlocked / = %d %q", resp.StatusCode, body)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "private, no-store" {
		t.Errorf("unlocked Cache-Control = %q, want private no-store", cc)
	}
	if xr := resp.Header.Get("X-Robots-Tag"); xr != "noindex, nofollow" {
		t.Errorf("unlocked X-Robots-Tag = %q", xr)
	}

	// Without the cookie the demo stays locked even though it is deployed.
	resp = h.do(t, "GET", demoHost, "/", nil, nil)
	if body := readBody(t, resp); body == "gate-v1" {
		t.Error("locked demo served content without the gate cookie")
	}

	// Rotating the key invalidates the issued cookie.
	resp = h.do(t, "PATCH", controlHost, "/api/demos/acme",
		strings.NewReader(`{"rotate_key":true}`),
		map[string]string{
			"Cookie":       auth.CookieName + "=" + session,
			"X-CSRF-Token": csrfToken,
			"Content-Type": "application/json",
		})
	patchBody := readBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rotate = %d (body: %s)", resp.StatusCode, patchBody)
	}
	var rotated struct {
		AccessKey string `json:"access_key"`
	}
	if err := json.Unmarshal([]byte(patchBody), &rotated); err != nil || rotated.AccessKey == "" || rotated.AccessKey == key {
		t.Fatalf("rotate response = %s, want a fresh key", patchBody)
	}
	resp = h.do(t, "GET", demoHost, "/", nil, map[string]string{"Cookie": gateCookie})
	if body := readBody(t, resp); body == "gate-v1" {
		t.Error("old gate cookie still unlocks after rotation")
	}
	newCookie := "democtl_access=" + verifyKey(t, h, demoHost, rotated.AccessKey, http.StatusOK)
	resp = h.do(t, "GET", demoHost, "/", nil, map[string]string{"Cookie": newCookie})
	if body := readBody(t, resp); body != "gate-v1" {
		t.Errorf("new key does not unlock: %d %q", resp.StatusCode, body)
	}

	// Disabling privacy opens the demo with the normal public caching.
	resp = h.do(t, "PATCH", controlHost, "/api/demos/acme",
		strings.NewReader(`{"private":false}`),
		map[string]string{
			"Cookie":       auth.CookieName + "=" + session,
			"X-CSRF-Token": csrfToken,
			"Content-Type": "application/json",
		})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("disable privacy = %d", resp.StatusCode)
	}
	resp = h.do(t, "GET", demoHost, "/", nil, nil) // no cookie at all
	if body := readBody(t, resp); resp.StatusCode != http.StatusOK || body != "gate-v1" {
		t.Errorf("public demo / = %d %q", resp.StatusCode, body)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("public Cache-Control = %q, want no-cache", cc)
	}
	if resp.Header.Get("X-Robots-Tag") != "" {
		t.Error("public response carries X-Robots-Tag")
	}

	// The audit log saw the privacy transitions and never a key.
	auditBytes, err := os.ReadFile(h.audit)
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	audit := string(auditBytes)
	if !strings.Contains(audit, "demo.privacy") {
		t.Error("audit lacks demo.privacy events")
	}
	if strings.Contains(audit, key) || strings.Contains(audit, rotated.AccessKey) {
		t.Error("audit contains an access key plaintext")
	}
}

func TestPrivateDemoNeverDeployed(t *testing.T) {
	h := newHarness(t)
	session := h.login(t)
	key := createPrivate(t, h, session, h.csrf(t, session), "ghost")
	ghost := "ghost.example.com"

	// Locked and undeployed: the gate page, never a distinction between
	// "exists" and "deployed".
	resp := h.do(t, "GET", ghost, "/", nil, nil)
	if body := readBody(t, resp); !strings.Contains(body, "This demo is private") {
		t.Errorf("undeployed private demo / = %d %.80q", resp.StatusCode, body)
	}
	cookie := "democtl_access=" + verifyKey(t, h, ghost, key, http.StatusOK)
	resp = h.do(t, "GET", ghost, "/", nil, map[string]string{"Cookie": cookie})
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unlocked undeployed demo = %d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestUpdateDemoValidation(t *testing.T) {
	h := newHarness(t)
	session := h.login(t)
	csrfToken := h.csrf(t, session)
	jsonHeaders := func() map[string]string {
		return map[string]string{
			"Cookie":       auth.CookieName + "=" + session,
			"X-CSRF-Token": csrfToken,
			"Content-Type": "application/json",
		}
	}

	// Public demo to start.
	resp := h.do(t, "POST", controlHost, "/api/demos",
		strings.NewReader(`{"name":"val"}`), jsonHeaders())
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d", resp.StatusCode)
	}

	cases := []struct {
		name string
		body string
		want int
	}{
		{"empty body", `{}`, http.StatusBadRequest},
		{"rename mixed with privacy", `{"name":"v2","private":true}`, http.StatusBadRequest},
		{"rotate on public demo", `{"rotate_key":true}`, http.StatusBadRequest},
		{"rotate alongside disable", `{"private":false,"rotate_key":true}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		resp := h.do(t, "PATCH", controlHost, "/api/demos/val",
			strings.NewReader(tc.body), jsonHeaders())
		body := readBody(t, resp)
		if resp.StatusCode != tc.want {
			t.Errorf("%s: PATCH = %d, want %d (body: %s)", tc.name, resp.StatusCode, tc.want, body)
		}
	}

	// Enable → replaying enable is a conflict, never a silent rotation.
	resp = h.do(t, "PATCH", controlHost, "/api/demos/val",
		strings.NewReader(`{"private":true}`), jsonHeaders())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("enable = %d", resp.StatusCode)
	}
	var enabled struct {
		AccessKey string `json:"access_key"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&enabled); err != nil || enabled.AccessKey == "" {
		t.Fatal("enable response carries no one-time key")
	}
	resp.Body.Close()
	resp = h.do(t, "PATCH", controlHost, "/api/demos/val",
		strings.NewReader(`{"private":true}`), jsonHeaders())
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("re-enable = %d, want 409", resp.StatusCode)
	}
	resp.Body.Close()

	// Disable → the list reflects it.
	resp = h.do(t, "PATCH", controlHost, "/api/demos/val",
		strings.NewReader(`{"private":false}`), jsonHeaders())
	resp.Body.Close()
	resp = h.do(t, "GET", controlHost, "/api/demos", nil,
		map[string]string{"Cookie": auth.CookieName + "=" + session})
	var list struct {
		Demos []struct {
			Name    string `json:"name"`
			Private bool   `json:"private"`
		} `json:"demos"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatalf("list decode: %v", err)
	}
	resp.Body.Close()
	for _, d := range list.Demos {
		if d.Name == "val" && d.Private {
			t.Error("list still reports val private after disable")
		}
	}
}

func TestGoogleSuperadminManagesAnyDemo(t *testing.T) {
	h := newHarnessWithCfg(t, func(c *config.Config) {
		c.GoogleSuperadminEmails = []string{"pm@example.com"}
	})
	session := h.login(t)

	// /api/me reports the superadmin flag.
	resp := h.do(t, "GET", controlHost, "/api/me", nil,
		map[string]string{"Cookie": auth.CookieName + "=" + session})
	var me struct {
		IsSuperadmin bool `json:"is_superadmin"`
	}
	json.NewDecoder(resp.Body).Decode(&me)
	resp.Body.Close()
	if !me.IsSuperadmin {
		t.Fatal("listed google session is not superadmin in /api/me")
	}

	// A demo owned by someone else, seeded straight into the store.
	other, err := store.Open(h.dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer other.Close()
	if _, err := other.CreateDemo("foreign", "teammate@example.com", false, "", 1); err != nil {
		t.Fatalf("seed demo: %v", err)
	}

	csrfToken := h.csrf(t, session)
	resp = h.do(t, "DELETE", controlHost, "/api/demos/foreign", nil,
		map[string]string{"Cookie": auth.CookieName + "=" + session, "X-CSRF-Token": csrfToken})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("google superadmin delete foreign demo = %d, want 204", resp.StatusCode)
	}

	// User management stays password-mode-only for a google superadmin.
	resp = h.do(t, "GET", controlHost, "/api/users", nil,
		map[string]string{"Cookie": auth.CookieName + "=" + session})
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "password auth mode") {
		t.Errorf("google superadmin /api/users = %d %q, want 403 password-mode-only", resp.StatusCode, body)
	}
}

func TestGoogleNonOwnerCannotManage(t *testing.T) {
	h := newHarness(t) // no GOOGLE_SUPERADMIN_EMAILS: plain workspace member
	session := h.login(t)
	csrfToken := h.csrf(t, session)

	other, err := store.Open(h.dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer other.Close()
	if _, err := other.CreateDemo("foreign", "teammate@example.com", false, "", 1); err != nil {
		t.Fatalf("seed demo: %v", err)
	}

	headers := map[string]string{
		"Cookie":       auth.CookieName + "=" + session,
		"X-CSRF-Token": csrfToken,
		"Content-Type": "application/json",
	}
	resp := h.do(t, "PATCH", controlHost, "/api/demos/foreign",
		strings.NewReader(`{"name":"stolen"}`), headers)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("non-owner rename = %d, want 403", resp.StatusCode)
	}
	resp.Body.Close()
	resp = h.do(t, "DELETE", controlHost, "/api/demos/foreign", nil, headers)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("non-owner delete = %d, want 403", resp.StatusCode)
	}
	resp.Body.Close()

	// Still listed, still intact.
	still, err := other.DemoByName("foreign")
	if err != nil || still.CreatedBy != "teammate@example.com" {
		t.Errorf("foreign demo damaged: %+v err=%v", still, err)
	}
}

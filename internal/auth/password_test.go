package auth

import (
	"database/sql"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"democtl/internal/store"
)

// newPasswordAuth wires an Authenticator against a fresh temp-dir sqlite
// with one superadmin and one plain user.
func newPasswordAuth(t *testing.T) (*Authenticator, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "password-test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	mustCreate := func(email, password, role string) {
		t.Helper()
		hash, err := HashPassword(password)
		if err != nil {
			t.Fatalf("HashPassword: %v", err)
		}
		if _, err := st.CreateUser(email, hash, role, now.Unix()); err != nil {
			t.Fatalf("CreateUser %s: %v", email, err)
		}
	}
	mustCreate("admin@example.com", "superadmin-password-1", store.RoleSuperadmin)
	mustCreate("user@example.com", "plainuser-password-1", store.RoleUser)

	a := New(Deps{
		Cfg:          testConfig(),
		Sessions:     st,
		Now:          func() time.Time { return now },
		SecureCookie: false,
	})
	return a, st
}

func passwordLogin(t *testing.T, a *Authenticator, email, password string) (Session, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	sess, err := a.LoginWithPassword(rec, email, password)
	if err != nil {
		t.Fatalf("LoginWithPassword: %v", err)
	}
	raw := sessionCookieValue(t, rec)
	if raw == "" {
		t.Fatal("no session cookie set on success")
	}
	return sess, raw
}

func TestHashAndCheckPassword(t *testing.T) {
	hash, err := HashPassword("a-long-enough-password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if strings.Contains(hash, "a-long-enough-password") {
		t.Error("hash contains the cleartext password")
	}
	if !CheckPassword(hash, "a-long-enough-password") {
		t.Error("correct password does not verify")
	}
	if CheckPassword(hash, "a-long-enough-passworX") {
		t.Error("wrong password verifies")
	}
	if CheckPassword(hash, "") || CheckPassword("", "a-long-enough-password") {
		t.Error("empty password or hash verifies")
	}
	// bcrypt salts: the same password hashes differently twice.
	other, _ := HashPassword("a-long-enough-password")
	if hash == other {
		t.Error("identical hashes — no salt?")
	}
}

func TestHashPasswordRejectsShort(t *testing.T) {
	if _, err := HashPassword(strings.Repeat("x", MinPasswordLength-1)); err == nil {
		t.Error("short password: want error, got nil")
	}
	if _, err := HashPassword(strings.Repeat("x", MinPasswordLength)); err != nil {
		t.Errorf("boundary password: %v", err)
	}
}

func TestPasswordLoginHappyPath(t *testing.T) {
	a, _ := newPasswordAuth(t)

	rec := httptest.NewRecorder()
	sess, err := a.LoginWithPassword(rec, "user@example.com", "plainuser-password-1")
	if err != nil {
		t.Fatalf("LoginWithPassword: %v", err)
	}
	if sess.Email != "user@example.com" || sess.Role != store.RoleUser {
		t.Errorf("sess = %+v", sess)
	}
	if sess.IsSuperadmin() {
		t.Error("plain user session reports superadmin")
	}
	if sess.Actor() != "user@example.com" {
		t.Errorf("Actor = %q", sess.Actor())
	}
	raw := sessionCookieValue(t, rec)
	if raw == "" {
		t.Fatal("no session cookie set")
	}
	// Case-insensitive email.
	rec2 := httptest.NewRecorder()
	if _, err := a.LoginWithPassword(rec2, "USER@EXAMPLE.COM", "plainuser-password-1"); err != nil {
		t.Errorf("uppercase email login: %v", err)
	}

	got, ok := a.Session(withSessionCookie(httptest.NewRequest("GET", "/", nil), raw))
	if !ok {
		t.Fatal("Session: not resolved from the new cookie")
	}
	if got != sess {
		t.Error("resolved Session differs from the one LoginWithPassword returned")
	}
}

func TestPasswordLoginSuperadmin(t *testing.T) {
	a, _ := newPasswordAuth(t)
	sess, _ := passwordLogin(t, a, "admin@example.com", "superadmin-password-1")
	if !sess.IsSuperadmin() {
		t.Error("superadmin session does not report superadmin")
	}
	if sess.UserID == 0 {
		t.Error("password session has no UserID")
	}
}

func TestPasswordLoginFailures(t *testing.T) {
	a, _ := newPasswordAuth(t)

	cases := []struct {
		name     string
		email    string
		password string
	}{
		{"wrong password", "user@example.com", "plainuser-password-2"},
		{"unknown email", "nobody@example.com", "whatever-password"},
		{"empty password", "user@example.com", ""},
		{"empty email", "", "plainuser-password-1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			sess, err := a.LoginWithPassword(rec, tc.email, tc.password)
			if !errors.Is(err, ErrInvalidCredentials) {
				t.Fatalf("err = %v, want ErrInvalidCredentials", err)
			}
			if sess != (Session{}) {
				t.Error("Session returned alongside a rejection")
			}
			if hasSessionCookie(rec) {
				t.Error("session cookie set on failed login")
			}
		})
	}
}

func TestPasswordSessionDiesWithUser(t *testing.T) {
	a, st := newPasswordAuth(t)
	sess, raw := passwordLogin(t, a, "user@example.com", "plainuser-password-1")

	u, err := st.UserByEmail("user@example.com")
	if err != nil {
		t.Fatalf("UserByEmail: %v", err)
	}
	if err := st.DeleteUser(u.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	_ = sess
	if _, ok := a.Session(withSessionCookie(httptest.NewRequest("GET", "/", nil), raw)); ok {
		t.Error("session of a deleted user still resolves")
	}
}

func TestPasswordSessionReflectsRoleChange(t *testing.T) {
	a, st := newPasswordAuth(t)
	_, raw := passwordLogin(t, a, "user@example.com", "plainuser-password-1")

	req := withSessionCookie(httptest.NewRequest("GET", "/", nil), raw)
	if got, _ := a.Session(req); got.IsSuperadmin() {
		t.Fatal("plain user already superadmin")
	}
	// Promote directly in the store (as EnsureSuperadmin would); the live
	// session must pick it up without re-login.
	u, _ := st.UserByEmail("user@example.com")
	if _, _, err := st.EnsureSuperadmin(u.Email, u.PasswordHash, 2); err != nil {
		t.Fatalf("EnsureSuperadmin: %v", err)
	}
	if got, ok := a.Session(req); !ok || !got.IsSuperadmin() {
		t.Error("promoted user session does not report superadmin")
	}
}

func TestPasswordSessionRowShape(t *testing.T) {
	a, st := newPasswordAuth(t)
	sess, _ := passwordLogin(t, a, "admin@example.com", "superadmin-password-1")

	row, err := st.SessionByIDHash(sess.IDHash)
	if err != nil {
		t.Fatalf("SessionByIDHash: %v", err)
	}
	if !row.UserID.Valid || row.UserID != (sql.NullInt64{Int64: sess.UserID, Valid: true}) {
		t.Errorf("row UserID = %+v, want the owning user", row.UserID)
	}
	if row.GoogleSub != "" {
		t.Errorf("row GoogleSub = %q, want empty for password sessions", row.GoogleSub)
	}
}

func TestGoogleSessionHasNoRole(t *testing.T) {
	a, _, _ := newAuth(t, goodUser, nil)
	sess, _ := login(t, a)
	if sess.IsSuperadmin() {
		t.Error("google session reports superadmin")
	}
	if sess.Actor() != goodUser.Email {
		t.Errorf("Actor = %q, want %q", sess.Actor(), goodUser.Email)
	}
	if sess.Role != "" || sess.UserID != 0 {
		t.Errorf("google sess role/user = %q/%d, want empty/0", sess.Role, sess.UserID)
	}
}

func TestGoogleSuperadminByEmail(t *testing.T) {
	// goodUser's email is listed in GOOGLE_SUPERADMIN_EMAILS — the session
	// must carry superadmin both at issue and on later re-resolution.
	a, _, _ := newAuth(t, goodUser, func(d *Deps) {
		d.Cfg.GoogleSuperadminEmails = []string{"someone-else@example.com", goodUser.Email}
	})
	sess, raw := login(t, a)
	if !sess.IsSuperadmin() {
		t.Error("listed google session is not superadmin")
	}
	resolved, ok := a.Session(withSessionCookie(httptest.NewRequest("GET", "/", nil), raw))
	if !ok || !resolved.IsSuperadmin() {
		t.Errorf("re-resolved google session superadmin = %v, ok = %v", resolved.IsSuperadmin(), ok)
	}
	if resolved.Role != "" || resolved.UserID != 0 {
		t.Errorf("google superadmin session gained role/user: %+v", resolved)
	}

	// Case-insensitive match (config lowercases at load; emails compare lowered).
	a2, _, _ := newAuth(t, goodUser, func(d *Deps) {
		d.Cfg.GoogleSuperadminEmails = []string{"PM@EXAMPLE.COM"}
	})
	sess2, _ := login(t, a2)
	if !sess2.IsSuperadmin() {
		t.Error("case-differing google email not treated as superadmin")
	}

	// Unlisted: no superadmin (the default state).
	a3, _, _ := newAuth(t, goodUser, nil)
	sess3, _ := login(t, a3)
	if sess3.IsSuperadmin() {
		t.Error("unlisted google session reports superadmin")
	}
}

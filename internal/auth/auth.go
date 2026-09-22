// Package auth implements democtl's control-plane login: Google OAuth for
// the company Workspace, sqlite-backed server-side sessions, and
// per-session CSRF (docs/demos.md §Auth). Two properties are load-bearing:
//
//   - The session cookie is host-only (no Domain attribute) on the control
//     host, so a deployed demo on <name>.example.com is cross-origin and
//     can never touch control-plane state.
//   - Membership is decided server-side from the verified email's domain;
//     the hd= consent hint is never treated as a control.
//
// Cookie, token, and client-secret values never enter logs or errors.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"democtl/internal/config"
	"democtl/internal/store"
)

const (
	// CookieName is the host-only session cookie on the control host.
	CookieName = "democtl_session"
	// StateCookieName carries the OAuth state between BeginLogin and the
	// callback — CSRF for the login flow itself.
	StateCookieName = "democtl_oauth_state"
	// SessionTTL is how long a session lives (docs/demos.md §Auth).
	SessionTTL = 7 * 24 * time.Hour
	// StateTTL bounds the OAuth round-trip.
	StateTTL = 10 * time.Minute
)

const (
	csrfHeader         = "X-CSRF-Token"
	defaultUserInfoURL = "https://openidconnect.googleapis.com/v1/userinfo"
	// outbound Google calls get a deadline; nothing hangs a handler on a
	// silent upstream.
	oauthCallTimeout = 15 * time.Second
	maxUserInfoBytes = 64 << 10
	stateBytes       = 16 // 128-bit state, hex-encoded
	sessionIDBytes   = 32 // session id and CSRF token, hex-encoded
	callbackPath     = "/oauth2/callback"
)

// Sentinel errors the HTTP layer maps: ErrBadState → the login flow is
// stale or forged (restart it), ErrWorkspaceMismatch → 403 (authenticated
// Google identity, but outside the configured Workspace).
var (
	ErrBadState          = errors.New("auth: oauth state check failed")
	ErrWorkspaceMismatch = errors.New("auth: email not in workspace domain")
)

// Session is the authenticated identity the rest of the service sees.
// Google sessions carry GoogleSub/GoogleEmail with UserID 0; password
// sessions carry UserID/Email/Role. Email is always populated — it is the
// audit actor and the dashboard identity in both modes.
type Session struct {
	IDHash      string
	CSRFToken   string
	GoogleSub   string
	GoogleEmail string
	UserID      int64  // owning local user; 0 for Google sessions
	Email       string // actor email in both modes
	Role        string // store.RoleSuperadmin / store.RoleUser; "" for Google sessions
	// Superadmin is computed at construction: a local superadmin role
	// (password mode) or a configured GOOGLE_SUPERADMIN_EMAIL match
	// (google mode). It gates demo management in both modes and user
	// management in password mode.
	Superadmin bool
}

// Actor returns the email audit entries and created_by fields attribute to
// the session — Email in both modes, falling back to the Google column for
// sessions written before Email existed.
func (s Session) Actor() string {
	if s.Email != "" {
		return s.Email
	}
	return s.GoogleEmail
}

// IsSuperadmin reports whether the session may manage any demo (both auth
// modes) and local users (password mode only — the /api/users surface is
// additionally gated on the auth mode itself).
func (s Session) IsSuperadmin() bool {
	return s.Superadmin
}

// isGoogleSuperadmin reports whether a Google sign-in email is on the
// configured superadmin list (config.GoogleSuperadminEmails, already
// lowercased at load).
func (a *Authenticator) isGoogleSuperadmin(email string) bool {
	if email == "" {
		return false
	}
	email = strings.ToLower(email)
	for _, listed := range a.cfg.GoogleSuperadminEmails {
		if strings.ToLower(listed) == email {
			return true
		}
	}
	return false
}

// Deps wires the authenticator. Zero values pick the production defaults;
// the injectable fields exist for tests only.
type Deps struct {
	Cfg      config.Config
	Sessions *store.Store
	Now      func() time.Time // injectable clock; default time.Now
	Google   oauth2.Endpoint  // injectable for tests; default google.Endpoint
	// UserInfoURL is the OIDC userinfo endpoint; injectable for tests,
	// defaults to Google's.
	UserInfoURL string
	// SecureCookie sets the Secure attribute on both cookies. Production
	// wiring must set true — the control host is only ever served through
	// Zoraxy TLS. Tests set false to run plain httptest.
	SecureCookie bool
}

// Authenticator owns the login flow and session resolution.
type Authenticator struct {
	cfg          config.Config
	sessions     *store.Store
	now          func() time.Time
	endpoint     oauth2.Endpoint
	userInfoURL  string
	secureCookie bool
}

// New builds an Authenticator, applying the Deps defaults.
func New(d Deps) *Authenticator {
	now := d.Now
	if now == nil {
		now = time.Now
	}
	endpoint := d.Google
	if endpoint.AuthURL == "" && endpoint.TokenURL == "" {
		endpoint = google.Endpoint
	}
	infoURL := d.UserInfoURL
	if infoURL == "" {
		infoURL = defaultUserInfoURL
	}
	return &Authenticator{
		cfg:          d.Cfg,
		sessions:     d.Sessions,
		now:          now,
		endpoint:     endpoint,
		userInfoURL:  infoURL,
		secureCookie: d.SecureCookie,
	}
}

// oauthConfig is the client for both legs of the flow. The redirect URI is
// always https:// + the control host: the host is only reachable through
// Zoraxy TLS termination (docs/demos.md §Google OAuth), and Google
// registered exactly this redirect URI.
func (a *Authenticator) oauthConfig() *oauth2.Config {
	return &oauth2.Config{
		ClientID:     a.cfg.GoogleClientID,
		ClientSecret: a.cfg.GoogleClientSecret,
		Endpoint:     a.endpoint,
		RedirectURL:  "https://" + a.cfg.ControlHost + callbackPath,
		Scopes:       []string{"openid", "email"},
	}
}

// BeginLogin starts the OAuth round-trip: a fresh 128-bit state in a
// short-lived cookie, then a 302 to Google's consent screen with hd= as
// the workspace hint (docs/demos.md §Auth step 1).
func (a *Authenticator) BeginLogin(w http.ResponseWriter, r *http.Request) {
	state, err := randomHex(stateBytes)
	if err != nil {
		slog.Error("auth: state generation failed", "err", err)
		writeError(w, http.StatusInternalServerError, "oauth state generation failed")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     StateCookieName,
		Value:    state,
		Path:     "/",
		MaxAge:   int(StateTTL.Seconds()),
		HttpOnly: true,
		Secure:   a.secureCookie,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r,
		a.oauthConfig().AuthCodeURL(state, oauth2.SetAuthURLParam("hd", a.cfg.WorkspaceDomain)),
		http.StatusFound)
}

// Callback completes the login: state check, code exchange, identity
// fetch, workspace enforcement, then session creation and the session
// cookie. On success it returns the created Session — the caller needs it
// for the audit actor, and cannot read it back off the request because the
// cookie was set on the response. On any error it returns (Session{}, err),
// no session cookie is set, and the state cookie is cleared on every path
// out of this method.
func (a *Authenticator) Callback(w http.ResponseWriter, r *http.Request) (Session, error) {
	if err := a.consumeState(w, r); err != nil {
		return Session{}, err
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		return Session{}, fmt.Errorf("auth: callback without code")
	}

	ctx, cancel := context.WithTimeout(context.Background(), oauthCallTimeout)
	defer cancel()
	conf := a.oauthConfig()

	token, err := conf.Exchange(ctx, code)
	if err != nil {
		return Session{}, fmt.Errorf("auth: code exchange: %w", err)
	}
	id, err := a.fetchIdentity(ctx, token)
	if err != nil {
		return Session{}, err
	}
	if err := a.checkWorkspace(id); err != nil {
		return Session{}, err
	}

	rawID, err := randomHex(sessionIDBytes)
	if err != nil {
		return Session{}, fmt.Errorf("auth: session id: %w", err)
	}
	csrf, err := randomHex(sessionIDBytes)
	if err != nil {
		return Session{}, fmt.Errorf("auth: csrf token: %w", err)
	}
	now := a.now()
	sess := Session{
		IDHash:      hashID(rawID), // raw id never stored — sha256 at rest
		CSRFToken:   csrf,
		GoogleSub:   id.Sub,
		GoogleEmail: id.Email,
		Email:       id.Email,
		Superadmin:  a.isGoogleSuperadmin(id.Email),
	}
	if err := a.sessions.CreateSession(store.Session{
		IDHash:      sess.IDHash,
		CSRFToken:   sess.CSRFToken,
		GoogleSub:   sess.GoogleSub,
		GoogleEmail: sess.GoogleEmail,
		CreatedAt:   now.Unix(),
		ExpiresAt:   now.Add(SessionTTL).Unix(),
	}); err != nil {
		return Session{}, fmt.Errorf("auth: create session: %w", err)
	}

	// Host-only cookie — no Domain attribute, ever (docs/demos.md §Auth
	// step 3): set on demos.example.com it is invisible to pages on
	// <demo>.example.com, which is what keeps a deployed demo from
	// touching control-plane state.
	setSessionCookie(w, a.secureCookie, rawID)
	return sess, nil
}

// consumeState validates the state cookie against the ?state parameter
// (constant-time) and clears the cookie whichever way the check goes — a
// used or rejected state must never be replayable.
func (a *Authenticator) consumeState(w http.ResponseWriter, r *http.Request) error {
	c, err := r.Cookie(StateCookieName)
	query := r.URL.Query().Get("state")
	mismatch := err != nil || query == "" ||
		subtle.ConstantTimeCompare([]byte(c.Value), []byte(query)) != 1
	a.clearStateCookie(w)
	if mismatch {
		return fmt.Errorf("%w: stale or forged login flow", ErrBadState)
	}
	return nil
}

func (a *Authenticator) clearStateCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     StateCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   a.secureCookie,
		SameSite: http.SameSiteLaxMode,
	})
}

// identity is the slice of Google's userinfo response this service needs.
type identity struct {
	Sub           string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	HD            string `json:"hd"` // consent hint; decoded but never enforced
}

// fetchIdentity calls the OIDC userinfo endpoint with the exchanged access
// token. The token itself never appears in an error or a log.
func (a *Authenticator) fetchIdentity(ctx context.Context, token *oauth2.Token) (identity, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.userInfoURL, nil)
	if err != nil {
		return identity{}, fmt.Errorf("auth: userinfo request: %w", err)
	}
	token.SetAuthHeader(req)
	client := &http.Client{Timeout: oauthCallTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return identity{}, fmt.Errorf("auth: userinfo fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return identity{}, fmt.Errorf("auth: userinfo status %d", resp.StatusCode)
	}
	var id identity
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxUserInfoBytes)).Decode(&id); err != nil {
		return identity{}, fmt.Errorf("auth: userinfo decode: %w", err)
	}
	return id, nil
}

// checkWorkspace is the membership gate (docs/demos.md §Auth step 2): the
// verified email's domain — everything after the last "@" — must equal the
// configured Workspace domain. hd is a consent hint, not a control.
func (a *Authenticator) checkWorkspace(id identity) error {
	switch {
	case id.Email == "":
		return fmt.Errorf("auth: no email in identity: %w", ErrWorkspaceMismatch)
	case !id.EmailVerified:
		return fmt.Errorf("auth: email not verified: %w", ErrWorkspaceMismatch)
	case emailDomain(id.Email) != strings.ToLower(strings.TrimSpace(a.cfg.WorkspaceDomain)):
		return fmt.Errorf("auth: email domain outside the workspace: %w", ErrWorkspaceMismatch)
	}
	return nil
}

// emailDomain returns the lowercased domain after the LAST "@" —
// "a@evil.com@" yields "" and is rejected, never split on the first "@".
func emailDomain(email string) string {
	i := strings.LastIndexByte(email, '@')
	if i < 0 {
		return ""
	}
	return strings.ToLower(email[i+1:])
}

// Logout revokes the server-side session behind the cookie and expires the
// cookie. A missing cookie is not an error — logout is idempotent.
func (a *Authenticator) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(CookieName); err == nil && c.Value != "" {
		if err := a.sessions.DeleteSession(hashID(c.Value)); err != nil {
			slog.Error("auth: logout session delete failed", "err", err)
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   a.secureCookie,
		SameSite: http.SameSiteLaxMode,
	})
}

// Session resolves the request's cookie to a logged-in identity. Expired
// sessions report false and are deleted lazily; lookup errors are logged
// without any cookie material and report false. Password sessions are
// resolved against the users table on every request, so a deleted user (or
// a role change) takes effect immediately: sessions of a removed user stop
// resolving and are deleted lazily.
func (a *Authenticator) Session(r *http.Request) (Session, bool) {
	c, err := r.Cookie(CookieName)
	if err != nil || c.Value == "" {
		return Session{}, false
	}
	hash := hashID(c.Value)
	row, err := a.sessions.SessionByIDHash(hash)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			slog.Error("auth: session lookup failed", "err", err)
		}
		return Session{}, false
	}
	if row.ExpiresAt <= a.now().Unix() {
		if err := a.sessions.DeleteSession(hash); err != nil {
			slog.Error("auth: expired session delete failed", "err", err)
		}
		return Session{}, false
	}
	sess := Session{
		IDHash:      row.IDHash,
		CSRFToken:   row.CSRFToken,
		GoogleSub:   row.GoogleSub,
		GoogleEmail: row.GoogleEmail,
		Email:       row.GoogleEmail,
	}
	if row.UserID.Valid {
		user, err := a.sessions.UserByID(row.UserID.Int64)
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				slog.Error("auth: session user lookup failed", "err", err)
			} else if derr := a.sessions.DeleteSession(hash); derr != nil {
				slog.Error("auth: orphaned session delete failed", "err", derr)
			}
			return Session{}, false
		}
		sess.UserID = user.ID
		sess.Email = user.Email
		sess.Role = user.Role
		sess.Superadmin = user.Role == store.RoleSuperadmin
	} else {
		sess.Superadmin = a.isGoogleSuperadmin(row.GoogleEmail)
	}
	return sess, true
}

// CSRF reports whether the request carries the session's X-CSRF-Token
// (docs/demos.md §Auth step 4 — every mutation carries it). The compare is
// constant-time; both sides must be non-empty.
func CSRF(r *http.Request, sess Session) bool {
	tok := r.Header.Get(csrfHeader)
	if tok == "" || sess.CSRFToken == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(tok), []byte(sess.CSRFToken)) == 1
}

// RequireSession gates a handler on a valid session and hands the identity
// to it via FromContext; unauthenticated requests get the house error
// envelope with 401.
func (a *Authenticator) RequireSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, ok := a.Session(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthenticated")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, sess)))
	}
}

// ctxKey is the unexported context key RequireSession stores the Session
// under — other packages can only read it through FromContext.
type ctxKey struct{}

// FromContext returns the Session RequireSession placed in the context.
func FromContext(ctx context.Context) (Session, bool) {
	sess, ok := ctx.Value(ctxKey{}).(Session)
	return sess, ok
}

// randomHex returns n crypto/rand bytes hex-encoded.
func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("auth: rand: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// hashID is the at-rest form of a session id: sha256 hex of the raw cookie
// value, which is itself never stored.
func hashID(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// writeError emits the house error envelope ({ "error": { "message" } }).
// Messages are fixed literals — nothing user- or secret-derived.
func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"error":{"message":%q}}`, msg)
}

// Password login for the password auth mode: bcrypt-hashed local accounts
// from the users table. Hashing parameters follow the bcrypt defaults
// (cost 10); hashes are opaque strings to every other package —
// cleartext passwords cross exactly one boundary (the login/create/reset
// handlers into HashPassword/CheckPassword) and never enter logs, errors,
// or audit entries.
package auth

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"democtl/internal/store"
)

// MinPasswordLength is the minimum accepted cleartext password for local
// accounts (login, create, reset). The bootstrap env credential enforces a
// stricter floor in the config package.
const MinPasswordLength = 8

// ErrInvalidCredentials is the only login-failure signal: unknown email
// and wrong password are indistinguishable on purpose (no enumeration).
var ErrInvalidCredentials = errors.New("auth: invalid email or password")

// HashPassword bcrypt-hashes a cleartext password after enforcing the
// minimum length.
func HashPassword(password string) (string, error) {
	if len(password) < MinPasswordLength {
		return "", fmt.Errorf("auth: password must be at least %d characters", MinPasswordLength)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("auth: hash password: %w", err)
	}
	return string(hash), nil
}

// CheckPassword reports whether the cleartext password matches the stored
// bcrypt hash. The password never appears in an error or a log.
func CheckPassword(passwordHash, password string) bool {
	if passwordHash == "" || password == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(password)) == nil
}

// ValidateLoginInput rejects empty credentials before any store lookup so
// malformed requests get a 400 instead of a 401.
func ValidateLoginInput(email, password string) error {
	if strings.TrimSpace(email) == "" || password == "" {
		return fmt.Errorf("auth: email and password are required")
	}
	return nil
}

// LoginWithPassword authenticates a local user and, on success, issues a
// session exactly like the OAuth callback does (server-side row + host-only
// cookie). On any failure it returns (Session{}, err) with no cookie set;
// unknown emails and wrong passwords both surface as
// ErrInvalidCredentials.
func (a *Authenticator) LoginWithPassword(w http.ResponseWriter, email, password string) (Session, error) {
	user, err := a.sessions.UserByEmail(email)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Session{}, ErrInvalidCredentials
		}
		slog.Error("auth: password login lookup failed", "err", err)
		return Session{}, fmt.Errorf("auth: login lookup: %w", err)
	}
	if !CheckPassword(user.PasswordHash, password) {
		return Session{}, ErrInvalidCredentials
	}
	now := a.now()
	sess, rawID, err := buildSession(user.ID, user.Email, user.Role, "", user.Email)
	sess.Superadmin = user.Role == store.RoleSuperadmin
	if err != nil {
		return Session{}, err
	}
	if err := a.sessions.CreateSession(store.Session{
		IDHash:      sess.IDHash,
		CSRFToken:   sess.CSRFToken,
		GoogleSub:   "",
		GoogleEmail: user.Email, // NOT NULL column; mirrors the owner email
		UserID:      sql.NullInt64{Int64: user.ID, Valid: true},
		CreatedAt:   now.Unix(),
		ExpiresAt:   now.Add(SessionTTL).Unix(),
	}); err != nil {
		return Session{}, fmt.Errorf("auth: create session: %w", err)
	}
	setSessionCookie(w, a.secureCookie, rawID)
	return sess, nil
}

// buildSession mints the id/CSRF pair and the Session value both login
// paths share. The raw cookie id is returned alongside — it is set on the
// cookie and never stored.
func buildSession(userID int64, email, role, googleSub, googleEmail string) (Session, string, error) {
	rawID, err := randomHex(sessionIDBytes)
	if err != nil {
		return Session{}, "", fmt.Errorf("auth: session id: %w", err)
	}
	csrf, err := randomHex(sessionIDBytes)
	if err != nil {
		return Session{}, "", fmt.Errorf("auth: csrf token: %w", err)
	}
	return Session{
		IDHash:      hashID(rawID), // raw id never stored — sha256 at rest
		CSRFToken:   csrf,
		GoogleSub:   googleSub,
		GoogleEmail: googleEmail,
		UserID:      userID,
		Email:       email,
		Role:        role,
	}, rawID, nil
}

// setSessionCookie writes the host-only session cookie — no Domain
// attribute, ever (see Callback): set on the control host it stays
// invisible to pages on <demo>.<base>.
func setSessionCookie(w http.ResponseWriter, secure bool, rawID string) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    rawID,
		Path:     "/",
		MaxAge:   int(SessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

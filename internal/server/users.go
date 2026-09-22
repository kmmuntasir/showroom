// User management (password auth mode only): superadmin-gated handlers for
// listing, creating, deleting, and resetting passwords of local accounts.
// Every handler runs behind RequireSession + requireSuperadmin + CSRF, in
// that order. Password hashes never leave the server — list/create
// responses carry the public user shape only.
package server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"democtl/internal/auth"
	"democtl/internal/config"
	"democtl/internal/store"
)

// requireSuperadmin gates a handler on a superadmin session. It runs after
// RequireSession, so the session is always in context here. Local user
// management exists only in password auth mode: a GOOGLE_SUPERADMIN_EMAIL
// match is a superadmin for demo management but never for this surface —
// there are no local accounts behind a Google deployment to manage.
func (s *Server) requireSuperadmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.Cfg.AuthMode != config.AuthModePassword {
			writeErr(w, http.StatusForbidden, "user management is available in password auth mode only")
			return
		}
		sess, _ := auth.FromContext(r.Context())
		if !sess.IsSuperadmin() {
			writeErr(w, http.StatusForbidden, "superadmin only")
			return
		}
		next(w, r)
	}
}

// handleAuthInfo is public (no session): the login page needs the active
// auth mode before it knows which form to render.
func (s *Server) handleAuthInfo(w http.ResponseWriter, _ *http.Request) {
	mode := s.Cfg.AuthMode
	if mode == "" {
		mode = config.AuthModeGoogle
	}
	writeJSON(w, http.StatusOK, map[string]any{"auth_mode": mode})
}

// handleLogin authenticates a local user (password mode only). Like the
// OAuth callback it creates the session, so it carries no CSRF requirement
// — there is no session to bind a token to yet.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.Cfg.AuthMode == config.AuthModePassword {
		s.passwordLogin(w, r)
		return
	}
	writeErr(w, http.StatusNotFound, "password login disabled")
}

func (s *Server) passwordLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := auth.ValidateLoginInput(body.Email, body.Password); err != nil {
		writeErr(w, http.StatusBadRequest, "email and password are required")
		return
	}
	sess, err := s.Auth.LoginWithPassword(w, body.Email, body.Password)
	if errors.Is(err, auth.ErrInvalidCredentials) {
		writeErr(w, http.StatusUnauthorized, "invalid email or password")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.Audit.MustEvent(sess.Actor(), "auth.login", sess.Actor(), nil)
	writeJSON(w, http.StatusOK, map[string]any{
		"email":      sess.Email,
		"role":       sess.Role,
		"csrf_token": sess.CSRFToken,
	})
}

type userJSON struct {
	ID        int64  `json:"id"`
	Email     string `json:"email"`
	Role      string `json:"role"`
	CreatedAt int64  `json:"created_at"`
}

func userToJSON(u store.User) userJSON {
	return userJSON{ID: u.ID, Email: u.Email, Role: u.Role, CreatedAt: u.CreatedAt}
}

func (s *Server) handleListUsers(w http.ResponseWriter, _ *http.Request) {
	users, err := s.DB.ListUsers()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]userJSON, 0, len(users))
	for _, u := range users {
		out = append(out, userToJSON(u))
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": out})
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	sess, _ := auth.FromContext(r.Context())
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	email := strings.ToLower(strings.TrimSpace(body.Email))
	if email == "" || !strings.Contains(email, "@") || strings.Contains(email, " ") {
		writeErr(w, http.StatusBadRequest, "invalid email address")
		return
	}
	role := strings.ToLower(strings.TrimSpace(body.Role))
	if role == "" {
		role = store.RoleUser
	}
	if role != store.RoleUser && role != store.RoleSuperadmin {
		writeErr(w, http.StatusBadRequest, "role must be user or superadmin")
		return
	}
	// Fixed message — the cleartext password never appears in an error.
	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}
	u, err := s.DB.CreateUser(email, hash, role, time.Now().UTC().Unix())
	if errors.Is(err, store.ErrEmailTaken) {
		writeErr(w, http.StatusConflict, "email already registered")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.Audit.MustEvent(sess.Actor(), "user.create", u.Email, map[string]any{"role": u.Role})
	writeJSON(w, http.StatusCreated, map[string]any{"user": userToJSON(u)})
}

func (s *Server) userIDFromPath(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "invalid user id")
		return 0, false
	}
	return id, true
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	sess, _ := auth.FromContext(r.Context())
	id, ok := s.userIDFromPath(w, r)
	if !ok {
		return
	}
	if id == sess.UserID {
		writeErr(w, http.StatusConflict, "cannot delete your own account")
		return
	}
	target, err := s.DB.UserByID(id)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "no such user")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if err := s.DB.DeleteUser(id); errors.Is(err, store.ErrLastSuperadmin) {
		writeErr(w, http.StatusConflict, "cannot remove the last superadmin")
		return
	} else if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.Audit.MustEvent(sess.Actor(), "user.delete", target.Email, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleResetUserPassword(w http.ResponseWriter, r *http.Request) {
	sess, _ := auth.FromContext(r.Context())
	id, ok := s.userIDFromPath(w, r)
	if !ok {
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	// Fixed message — the cleartext password never appears in an error.
	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}
	target, err := s.DB.UserByID(id)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "no such user")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if err := s.DB.UpdateUserPassword(id, hash, time.Now().UTC().Unix()); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	// The owner is no longer the only one who knows this credential: kill
	// every session of the target, wherever it is logged in.
	if _, err := s.DB.DeleteUserSessionsExcept(id, ""); err != nil {
		slog.Error("revoke sessions after password reset", "err", err)
	}
	s.Audit.MustEvent(sess.Actor(), "user.password_reset", target.Email, nil)
	writeJSON(w, http.StatusOK, map[string]any{"user": userToJSON(target)})
}

// handlePasswordChange is POST /api/me/password: self-service password
// change (password auth mode). The current password authorizes the change
// — a stolen session alone must not be able to lock the real owner out.
// Success revokes every OTHER session of the user; the acting session
// survives so the change does not log the user out.
func (s *Server) handlePasswordChange(w http.ResponseWriter, r *http.Request) {
	if s.Cfg.AuthMode != config.AuthModePassword {
		writeErr(w, http.StatusNotFound, "password auth disabled")
		return
	}
	sess, _ := auth.FromContext(r.Context())
	if sess.UserID == 0 {
		writeErr(w, http.StatusForbidden, "no local password to change")
		return
	}
	var body struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	user, err := s.DB.UserByID(sess.UserID)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if !auth.CheckPassword(user.PasswordHash, body.CurrentPassword) {
		writeErr(w, http.StatusUnauthorized, "current password is incorrect")
		return
	}
	// Fixed message — the new password never appears in an error.
	hash, err := auth.HashPassword(body.NewPassword)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}
	if err := s.DB.UpdateUserPassword(user.ID, hash, time.Now().UTC().Unix()); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if _, err := s.DB.DeleteUserSessionsExcept(user.ID, sess.IDHash); err != nil {
		slog.Error("revoke sessions after password change", "err", err)
	}
	s.Audit.MustEvent(sess.Actor(), "auth.password_change", user.Email, nil)
	w.WriteHeader(http.StatusNoContent)
}

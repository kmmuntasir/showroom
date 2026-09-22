// Package server wires democtl's HTTP surface (docs/demos.md §Architecture):
// one listener that multiplexes by Host — the control dashboard
// (DEMOCTL_CONTROL_HOST) and the wildcard demo sites — plus the /healthz
// probe that answers regardless of Host. Handlers stay HTTP-only:
// validation, session/CSRF gates, and JSON shaping; storage and filesystem
// work live in store/upload.
package server

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"democtl/internal/audit"
	"democtl/internal/auth"
	"democtl/internal/config"
	"democtl/internal/demonames"
	"democtl/internal/serving"
	"democtl/internal/store"
	"democtl/internal/upload"
)

// Server carries democtl's wired dependencies.
type Server struct {
	Cfg     config.Config
	DB      *store.Store
	Audit   *audit.Log
	Uploads *upload.Store
	Auth    *auth.Authenticator
	Web     fs.FS // embedded dashboard SPA (web.Dist())
	// SecureCookie sets the Secure attribute on the demo-host access
	// cookie, mirroring auth.Deps.SecureCookie for the session cookie.
	// Production wiring must set true (behind Zoraxy TLS); tests false.
	SecureCookie bool

	// Access-key verify rate limiter state (gate.go). Guarded by mu;
	// lazily initialized so every existing construction site keeps
	// working.
	gateMu      sync.Mutex
	gateWindows map[string]gateWindow
}

// Handler builds the full host-multiplexing handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Host-agnostic health probe — the runbook curls it bare (no Host header).
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("ok\n"))
	})

	// Everything else on the domain is a potential demo site; serving
	// resolves via sqlite and 404s generically for unknown hosts. The
	// privacy gate wraps it: private demos answer with the access page
	// until the visitor holds a gate cookie (gate.go).
	demoStatic := &serving.Server{BaseDomain: s.Cfg.BaseDomain, Resolve: s.resolveDemoDir}
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hostOnly(r.Host) == s.Cfg.ControlHost {
			s.controlMux().ServeHTTP(w, r)
			return
		}
		s.demoGate(demoStatic).ServeHTTP(w, r)
	}))

	return mux
}

// resolveDemoDir maps a subdomain label to its current release directory.
// The demos/<name>/current symlink is the serving pointer (docs/demos.md
// decision D6): rollback moves it without touching the DB, so serving must
// follow the link rather than the newest release row. The label comes back
// from sqlite (demonames-validated at creation), never raw from the Host.
func (s *Server) resolveDemoDir(label string) (string, bool) {
	demo, err := s.DB.DemoByName(label)
	if err != nil {
		return "", false
	}
	link := filepath.Join(s.Cfg.DataDir, "demos", demo.Name, "current")
	real, err := filepath.EvalSymlinks(link)
	if err != nil {
		return "", false // never deployed, or release dir removed
	}
	return real, true
}

func (s *Server) controlMux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /login", s.handleLoginBegin)
	mux.HandleFunc("GET /oauth2/callback", s.handleCallback)
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("GET /api/auth-info", s.handleAuthInfo)
	mux.HandleFunc("GET /api/me", s.handleMe)
	// Self-service password change: session, then CSRF, like every
	// mutation; the current password authorizes it (users.go).
	mux.HandleFunc("POST /api/me/password", s.Auth.RequireSession(s.requireCSRF(s.handlePasswordChange)))
	mux.HandleFunc("GET /api/demos", s.Auth.RequireSession(s.handleListDemos))
	// Every mutation carries the session CSRF token (docs/demos.md §Auth
	// step 4) — session first, then CSRF, then the handler.
	mux.HandleFunc("POST /logout", s.Auth.RequireSession(s.requireCSRF(s.handleLogout)))
	mux.HandleFunc("POST /api/demos", s.Auth.RequireSession(s.requireCSRF(s.handleCreateDemo)))
	mux.HandleFunc("POST /api/demos/{name}/deploy", s.Auth.RequireSession(s.requireCSRF(s.handleDeploy)))
	mux.HandleFunc("POST /api/demos/{name}/rollback", s.Auth.RequireSession(s.requireCSRF(s.handleRollback)))
	mux.HandleFunc("PATCH /api/demos/{name}", s.Auth.RequireSession(s.requireCSRF(s.handleUpdateDemo)))
	mux.HandleFunc("DELETE /api/demos/{name}", s.Auth.RequireSession(s.requireCSRF(s.handleDelete)))
	// User management is superadmin-only (password mode): session, then
	// role; mutations additionally carry the CSRF token like every other
	// mutation. The list is a GET and needs no CSRF.
	mux.HandleFunc("GET /api/users", s.Auth.RequireSession(s.requireSuperadmin(s.handleListUsers)))
	mux.HandleFunc("POST /api/users", s.Auth.RequireSession(s.requireSuperadmin(s.requireCSRF(s.handleCreateUser))))
	mux.HandleFunc("DELETE /api/users/{id}", s.Auth.RequireSession(s.requireSuperadmin(s.requireCSRF(s.handleDeleteUser))))
	mux.HandleFunc("POST /api/users/{id}/password", s.Auth.RequireSession(s.requireSuperadmin(s.requireCSRF(s.handleResetUserPassword))))
	mux.Handle("/", s.spaHandler())
	return mux
}

// requireCSRF gates a handler on the session's X-CSRF-Token. It runs after
// RequireSession, so the session is always in context here.
func (s *Server) requireCSRF(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, _ := auth.FromContext(r.Context())
		if !auth.CSRF(r, sess) {
			writeErr(w, http.StatusForbidden, "csrf")
			return
		}
		next(w, r)
	}
}

// handleLoginBegin starts the Google OAuth flow — google mode only. In
// password mode there is no OAuth to begin and the endpoint is dead.
func (s *Server) handleLoginBegin(w http.ResponseWriter, r *http.Request) {
	if s.Cfg.AuthMode == config.AuthModePassword {
		writeErr(w, http.StatusNotFound, "oauth login disabled")
		return
	}
	s.Auth.BeginLogin(w, r)
}

// handleCallback finishes OAuth; success lands back on the dashboard,
// failure carries the reason in a query param the SPA can toast. In
// password mode the flow can never have started, so the callback is dead.
func (s *Server) handleCallback(w http.ResponseWriter, r *http.Request) {
	if s.Cfg.AuthMode == config.AuthModePassword {
		http.Redirect(w, r, "/?auth_error=auth_disabled", http.StatusSeeOther)
		return
	}
	sess, err := s.Auth.Callback(w, r)
	if err != nil {
		slog.Warn("oauth callback failed", "err", err)
		reason := "login_failed"
		if errors.Is(err, auth.ErrWorkspaceMismatch) {
			reason = "workspace_domain"
		}
		http.Redirect(w, r, "/?auth_error="+reason, http.StatusSeeOther)
		return
	}
	s.Audit.MustEvent(sess.Actor(), "auth.login", sess.Actor(), nil)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	sess, _ := auth.FromContext(r.Context())
	s.Auth.Logout(w, r)
	s.Audit.MustEvent(sess.Actor(), "auth.logout", sess.Actor(), nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.Auth.Session(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	mode := s.Cfg.AuthMode
	if mode == "" {
		mode = config.AuthModeGoogle
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": true,
		"email":         sess.Actor(),
		"csrf_token":    sess.CSRFToken,
		"auth_mode":     mode,
		"role":          sess.Role,
		"is_superadmin": sess.IsSuperadmin(),
		"base_domain":   s.Cfg.BaseDomain,
	})
}

// releaseJSON mirrors the shape the SPA was coded against
// (docs/demos.md §democtl service HTTP surface).
type releaseJSON struct {
	UploadedAt int64  `json:"uploaded_at"`
	UploadedBy string `json:"uploaded_by"`
	SizeBytes  int64  `json:"size_bytes"`
	FileCount  int64  `json:"file_count"`
}

type demoJSON struct {
	Name         string       `json:"name"`
	CreatedBy    string       `json:"created_by"`
	CreatedAt    int64        `json:"created_at"`
	UpdatedAt    int64        `json:"updated_at"`
	Private      bool         `json:"private"`
	LastRelease  *releaseJSON `json:"last_release"`
	ReleaseCount int64        `json:"release_count"`
}

func demoToJSON(d store.DemoWithLatest) demoJSON {
	out := demoJSON{
		Name: d.Name, CreatedBy: d.CreatedBy, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
		Private: d.Private, ReleaseCount: d.ReleaseCount,
	}
	if d.LastRelease != nil {
		out.LastRelease = &releaseJSON{
			UploadedAt: d.LastRelease.UploadedAt,
			UploadedBy: d.LastRelease.UploadedBy,
			SizeBytes:  d.LastRelease.SizeBytes,
			FileCount:  d.LastRelease.FileCount,
		}
	}
	return out
}

func (s *Server) handleListDemos(w http.ResponseWriter, _ *http.Request) {
	list, err := s.DB.ListDemos()
	if err != nil {
		slog.Error("list demos", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	demos := make([]demoJSON, 0, len(list))
	for _, d := range list {
		demos = append(demos, demoToJSON(d))
	}
	writeJSON(w, http.StatusOK, map[string]any{"demos": demos})
}

func (s *Server) handleCreateDemo(w http.ResponseWriter, r *http.Request) {
	sess, _ := auth.FromContext(r.Context())
	var body struct {
		Name    string `json:"name"`
		Private bool   `json:"private"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := demonames.Check(body.Name); err != nil {
		if errors.Is(err, demonames.ErrReserved) {
			writeErr(w, http.StatusConflict, "name is reserved")
			return
		}
		writeErr(w, http.StatusBadRequest, "name must be 2-32 chars: lowercase letters, digits, hyphens")
		return
	}
	// A private demo gets a server-generated access key up front; the
	// plaintext is returned exactly once, below, and only the hash is
	// stored.
	var accessKey, keyHash string
	if body.Private {
		key, hash, err := generateAccessKey()
		if err != nil {
			slog.Error("generate access key", "err", err)
			writeErr(w, http.StatusInternalServerError, "internal error")
			return
		}
		accessKey, keyHash = key, hash
	}
	d, err := s.DB.CreateDemo(body.Name, sess.Actor(), body.Private, keyHash, time.Now().UTC().Unix())
	if errors.Is(err, store.ErrNameTaken) {
		writeErr(w, http.StatusConflict, "name already taken")
		return
	}
	if err != nil {
		slog.Error("create demo", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.Audit.MustEvent(sess.Actor(), "demo.create", body.Name, map[string]any{"private": body.Private})
	resp := map[string]any{"demo": demoToJSON(store.DemoWithLatest{Demo: d})}
	if accessKey != "" {
		// The plaintext key rides exactly one response — this one.
		resp["access_key"] = accessKey
	}
	writeJSON(w, http.StatusCreated, resp)
}

func (s *Server) demoFromPath(w http.ResponseWriter, r *http.Request) (store.Demo, bool) {
	demo, err := s.DB.DemoByName(r.PathValue("name"))
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "no such demo")
		return store.Demo{}, false
	}
	if err != nil {
		slog.Error("fetch demo", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return store.Demo{}, false
	}
	return demo, true
}

// canManageDemo reports whether the session may mutate the demo: its
// creator, or a superadmin (a local superadmin in password mode, or a
// GOOGLE_SUPERADMIN_EMAIL match in google mode). Creating demos and
// viewing the list stay open to every authenticated user.
func (s *Server) canManageDemo(sess auth.Session, demo store.Demo) bool {
	return sess.IsSuperadmin() || sess.Actor() == demo.CreatedBy
}

// requireManageDemo writes the 403 envelope unless the session may manage
// the demo; handlers call it right after resolving the demo.
func (s *Server) requireManageDemo(w http.ResponseWriter, sess auth.Session, demo store.Demo) bool {
	if !s.canManageDemo(sess, demo) {
		writeErr(w, http.StatusForbidden, "only the demo owner or a superadmin can manage this demo")
		return false
	}
	return true
}

func (s *Server) handleDeploy(w http.ResponseWriter, r *http.Request) {
	sess, _ := auth.FromContext(r.Context())
	demo, ok := s.demoFromPath(w, r)
	if !ok {
		return
	}
	if !s.requireManageDemo(w, sess, demo) {
		return
	}
	// The whole multipart body is bounded by the zip cap plus form overhead;
	// upload.Publish enforces the real caps while streaming.
	r.Body = http.MaxBytesReader(w, r.Body, s.Cfg.Limits.MaxZipBytes+(1<<20))
	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(w, http.StatusBadRequest, "expected multipart/form-data with a zip field")
		return
	}
	var found bool
	var res upload.Result
	for {
		p, err := mr.NextPart()
		if err != nil {
			break
		}
		if p.FormName() != "zip" {
			continue
		}
		found = true
		res, err = s.Uploads.Publish(demo.ID, demo.Name, p)
		if err != nil {
			writeErr(w, uploadStatus(err), uploadMessage(err))
			return
		}
		break
	}
	if !found {
		writeErr(w, http.StatusBadRequest, "missing zip field")
		return
	}

	now := time.Now().UTC().Unix()
	if _, err := s.DB.AddRelease(demo.ID, res.Dir, sess.Actor(), res.SizeBytes, res.FileCount, now); err != nil {
		slog.Error("record release", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	// Keep current + one rollback target; drop the rest, files included.
	if pruned, err := s.DB.PruneReleases(demo.ID, s.Cfg.Limits.ReleaseKeep); err == nil {
		for _, p := range pruned {
			_ = s.Uploads.RemoveReleaseDir(p.Dir)
		}
	}
	_ = s.DB.TouchDemo(demo.ID, now)
	s.Audit.MustEvent(sess.Actor(), "demo.deploy", demo.Name, map[string]any{
		"size_bytes": res.SizeBytes, "file_count": res.FileCount, "dir": res.Dir,
	})
	writeJSON(w, http.StatusCreated, map[string]any{"release": releaseJSON{
		UploadedAt: now, UploadedBy: sess.Actor(), SizeBytes: res.SizeBytes, FileCount: res.FileCount,
	}})
}

func (s *Server) handleRollback(w http.ResponseWriter, r *http.Request) {
	sess, _ := auth.FromContext(r.Context())
	demo, ok := s.demoFromPath(w, r)
	if !ok {
		return
	}
	if !s.requireManageDemo(w, sess, demo) {
		return
	}
	rels, err := s.DB.ReleasesFor(demo.ID)
	if err != nil {
		slog.Error("list releases", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if len(rels) < 2 {
		writeErr(w, http.StatusConflict, "no previous release to roll back to")
		return
	}
	target := rels[1]
	if err := s.Uploads.SwitchCurrent(demo.Name, target.Dir); err != nil {
		slog.Error("rollback switch", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.Audit.MustEvent(sess.Actor(), "demo.rollback", demo.Name, map[string]any{"dir": target.Dir})
	writeJSON(w, http.StatusOK, map[string]any{"release": releaseJSON{
		UploadedAt: target.UploadedAt, UploadedBy: target.UploadedBy,
		SizeBytes: target.SizeBytes, FileCount: target.FileCount,
	}})
}

// handleUpdateDemo is PATCH /api/demos/{name}: rename, privacy toggle, or
// key rotation. Management is owner-or-superadmin for every branch
// (requireManageDemo). Pointers distinguish "absent" from "false".
// Semantics: private=true enables the gate and mints a fresh key (400 if
// already private — rotating is rotate_key's job, so an idempotent replay
// can never silently invalidate shared keys); private=false disables and
// forgets the key; rotate_key=true mints a new key for an already-private
// demo. Enabling or rotating returns the plaintext access_key exactly
// once; only the sha256 lands in the database.
func (s *Server) handleUpdateDemo(w http.ResponseWriter, r *http.Request) {
	sess, _ := auth.FromContext(r.Context())
	demo, ok := s.demoFromPath(w, r)
	if !ok {
		return
	}
	if !s.requireManageDemo(w, sess, demo) {
		return
	}
	var body struct {
		Name      *string `json:"name"`
		Private   *bool   `json:"private"`
		RotateKey *bool   `json:"rotate_key"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	wantRotate := body.RotateKey != nil && *body.RotateKey
	if body.Name == nil && body.Private == nil && !wantRotate {
		writeErr(w, http.StatusBadRequest, "provide a field to update: name, private, or rotate_key")
		return
	}
	if body.Name != nil && (body.Private != nil || wantRotate) {
		writeErr(w, http.StatusBadRequest, "rename and privacy changes must be sent separately")
		return
	}
	now := time.Now().UTC().Unix()

	if body.Name != nil {
		if err := demonames.Check(*body.Name); err != nil {
			if errors.Is(err, demonames.ErrReserved) {
				writeErr(w, http.StatusConflict, "name is reserved")
				return
			}
			writeErr(w, http.StatusBadRequest, "name must be 2-32 chars: lowercase letters, digits, hyphens")
			return
		}
		if err := s.DB.RenameDemo(demo.ID, *body.Name, now); err != nil {
			if errors.Is(err, store.ErrNameTaken) {
				writeErr(w, http.StatusConflict, "name already taken")
				return
			}
			slog.Error("rename demo", "err", err)
			writeErr(w, http.StatusInternalServerError, "internal error")
			return
		}
		// The on-disk parent dir is named for the label; the relative
		// current symlink keeps working under the new name untouched.
		if err := s.Uploads.RenameDemoDir(demo.Name, *body.Name); err != nil {
			slog.Error("rename demo dir", "err", err)
		}
		s.Audit.MustEvent(sess.Actor(), "demo.rename", *body.Name, map[string]any{"from": demo.Name})
		updated := demo
		updated.Name = *body.Name
		updated.UpdatedAt = now
		writeJSON(w, http.StatusOK, map[string]any{"demo": demoToJSON(store.DemoWithLatest{Demo: updated})})
		return
	}

	// Privacy branch: enable / disable / rotate.
	var (
		enable  = body.Private != nil && *body.Private
		disable = body.Private != nil && !*body.Private
	)
	switch {
	case disable && wantRotate:
		writeErr(w, http.StatusBadRequest, "an access key cannot be rotated on a public demo")
		return
	case enable && demo.Private:
		writeErr(w, http.StatusConflict, "demo is already private — use rotate_key to change the key")
		return
	case wantRotate && !demo.Private:
		writeErr(w, http.StatusBadRequest, "demo is not private — set private to true first")
		return
	}
	var (
		resp     = map[string]any{}
		rotated  = wantRotate
		privMode = demo.Private
	)
	if enable || wantRotate {
		key, hash, err := generateAccessKey()
		if err != nil {
			slog.Error("generate access key", "err", err)
			writeErr(w, http.StatusInternalServerError, "internal error")
			return
		}
		if err := s.DB.SetDemoPrivacy(demo.ID, true, hash, now); err != nil {
			slog.Error("set demo privacy", "err", err)
			writeErr(w, http.StatusInternalServerError, "internal error")
			return
		}
		resp["access_key"] = key // plaintext exactly once
		privMode = true
	} else { // disable
		if err := s.DB.SetDemoPrivacy(demo.ID, false, "", now); err != nil {
			slog.Error("set demo privacy", "err", err)
			writeErr(w, http.StatusInternalServerError, "internal error")
			return
		}
		privMode = false
	}
	s.Audit.MustEvent(sess.Actor(), "demo.privacy", demo.Name, map[string]any{"private": privMode, "rotated": rotated})
	updated := demo
	updated.Private = privMode
	updated.AccessKeyHash = ""
	updated.UpdatedAt = now
	resp["demo"] = demoToJSON(store.DemoWithLatest{Demo: updated})
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	sess, _ := auth.FromContext(r.Context())
	demo, ok := s.demoFromPath(w, r)
	if !ok {
		return
	}
	if !s.requireManageDemo(w, sess, demo) {
		return
	}
	// Files first, then rows: a failed DB delete leaves visible files (the
	// safe direction); the reverse would strand rows pointing at nothing.
	if err := s.Uploads.RemoveDemoDirs(demo.ID, demo.Name); err != nil {
		slog.Error("remove demo dirs", "err", err)
	}
	if err := s.DB.DeleteDemo(demo.ID); err != nil {
		slog.Error("delete demo", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.Audit.MustEvent(sess.Actor(), "demo.delete", demo.Name, nil)
	w.WriteHeader(http.StatusNoContent)
}

// spaHandler serves the embedded dashboard SPA with an index.html fallback
// for client-side routes.
func (s *Server) spaHandler() http.Handler {
	fileServer := http.FileServerFS(s.Web)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(s.Web, path); err != nil {
			r2 := new(http.Request)
			*r2 = *r
			r2.URL.Path = "/"
			fileServer.ServeHTTP(w, r2)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}

func hostOnly(hostport string) string {
	host := strings.ToLower(hostport)
	if i := strings.LastIndex(host, ":"); i >= 0 && !strings.Contains(host[i:], "]") {
		host = host[:i]
	}
	return host
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"message": msg}})
}

func uploadStatus(err error) int {
	switch {
	case errors.Is(err, upload.ErrNotAZip), errors.Is(err, upload.ErrBadEntry),
		errors.Is(err, upload.ErrNoIndexHTML):
		return http.StatusBadRequest
	case errors.Is(err, upload.ErrZipTooLarge), errors.Is(err, upload.ErrTotalTooLarge),
		errors.Is(err, upload.ErrFileTooLarge), errors.Is(err, upload.ErrTooManyFiles):
		return http.StatusRequestEntityTooLarge
	default:
		return http.StatusInternalServerError
	}
}

func uploadMessage(err error) string {
	switch {
	case errors.Is(err, upload.ErrNotAZip):
		return "not a valid zip archive"
	case errors.Is(err, upload.ErrBadEntry):
		return "archive contains an unsupported or unsafe entry"
	case errors.Is(err, upload.ErrNoIndexHTML):
		return "archive has no index.html at its root"
	case errors.Is(err, upload.ErrZipTooLarge):
		return "zip exceeds the size limit"
	case errors.Is(err, upload.ErrTotalTooLarge):
		return "extracted size exceeds the limit"
	case errors.Is(err, upload.ErrFileTooLarge):
		return "a file inside the archive exceeds the size limit"
	case errors.Is(err, upload.ErrTooManyFiles):
		return "archive contains too many files"
	default:
		return "internal error"
	}
}

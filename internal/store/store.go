// Package store is democtl's sqlite persistence (docs/demos.md §democtl
// service): one *sql.DB at boot, WAL + busy_timeout + foreign_keys=ON,
// parameterized queries only, timestamps as unix seconds UTC.
package store

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound, ErrNameTaken and ErrPrivateKeyNeedsKey are the sentinel
// errors call sites branch on; everything else is a wrapped driver error.
var (
	ErrNotFound            = errors.New("store: not found")
	ErrNameTaken           = errors.New("store: name taken")
	ErrPrivateKeyNeedsKey  = errors.New("store: private demo requires an access key")
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Store wraps the single database handle.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the sqlite database at path and applies
// pending migrations.
func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	// A single writer is the sqlite reality; one spare connection keeps
	// WAL contention predictable.
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(2)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database handle.
func (s *Store) Close() error { return s.db.Close() }

// migrate applies every embedded migration newer than the recorded
// schema version, each inside its own transaction.
func (s *Store) migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("store: schema_migrations: %w", err)
	}

	entries, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("store: glob migrations: %w", err)
	}
	sort.Strings(entries) // filename order == migration order

	for _, name := range entries {
		version := strings.TrimSuffix(strings.TrimPrefix(name, "migrations/"), ".sql")
		var done int
		if err := s.db.QueryRow(
			`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, version,
		).Scan(&done); err != nil {
			return fmt.Errorf("store: check %s: %w", version, err)
		}
		if done == 1 {
			continue
		}
		body, err := fs.ReadFile(migrationsFS, name)
		if err != nil {
			return fmt.Errorf("store: read %s: %w", version, err)
		}
		tx, err := s.db.Begin()
		if err != nil {
			return fmt.Errorf("store: begin %s: %w", version, err)
		}
		if _, err := tx.Exec(string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("store: apply %s: %w", version, err)
		}
		if _, err := tx.Exec(
			`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
			version, time.Now().UTC().Unix(),
		); err != nil {
			tx.Rollback()
			return fmt.Errorf("store: record %s: %w", version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("store: commit %s: %w", version, err)
		}
	}
	return nil
}

// Demo is one hosted demo.
type Demo struct {
	ID        int64
	Name      string
	CreatedBy string
	CreatedAt int64
	UpdatedAt int64
	// Private gates the demo's host behind the access-key page; the key is
	// stored as AccessKeyHash (sha256 hex) and must be empty when public.
	Private       bool
	AccessKeyHash string
}

// Release is one uploaded build of a demo.
type Release struct {
	ID         int64
	DemoID     int64
	Dir        string // basename under DataDir/releases/
	UploadedBy string
	UploadedAt int64
	SizeBytes  int64
	FileCount  int64
}

// DemoWithLatest is a dashboard row: the demo plus its most recent
// release (nil when never deployed) and the total release count (drives
// the dashboard's rollback affordance).
type DemoWithLatest struct {
	Demo
	LastRelease  *Release
	ReleaseCount int64
}

func isUnique(err error) bool {
	// modernc.org/sqlite surfaces constraint violations as plain errors;
	// match on the standard message rather than importing driver internals.
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

func wrapNoRows(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// CreateDemo inserts a demo; ErrNameTaken on the unique violation. A
// private demo must carry a key hash (defense in depth — the handler
// generates and validates the key first) and returns ErrPrivateKeyNeedsKey
// otherwise.
func (s *Store) CreateDemo(name, createdBy string, private bool, keyHash string, now int64) (Demo, error) {
	if private && keyHash == "" {
		return Demo{}, ErrPrivateKeyNeedsKey
	}
	if !private {
		keyHash = ""
	}
	res, err := s.db.Exec(
		`INSERT INTO demos (name, created_by, private, access_key_hash, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		name, createdBy, private, keyHash, now, now,
	)
	if isUnique(err) {
		return Demo{}, ErrNameTaken
	}
	if err != nil {
		return Demo{}, fmt.Errorf("store: create demo: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Demo{}, fmt.Errorf("store: create demo id: %w", err)
	}
	return Demo{ID: id, Name: name, CreatedBy: createdBy, CreatedAt: now, UpdatedAt: now,
		Private: private, AccessKeyHash: keyHash}, nil
}

// SetDemoPrivacy changes the gate state. private=true requires a non-empty
// keyHash; private=false always clears the stored hash — disabling the gate
// forgets the key, and old gate cookies stop validating against anything.
func (s *Store) SetDemoPrivacy(id int64, private bool, keyHash string, now int64) error {
	if private && keyHash == "" {
		return ErrPrivateKeyNeedsKey
	}
	if !private {
		keyHash = ""
	}
	res, err := s.db.Exec(
		`UPDATE demos SET private = ?, access_key_hash = ?, updated_at = ? WHERE id = ?`,
		private, keyHash, now, id,
	)
	if err != nil {
		return fmt.Errorf("store: set demo privacy: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RenameDemo changes only the subdomain label (docs/demos.md: the only
// editable field).
func (s *Store) RenameDemo(id int64, newName string, now int64) error {
	_, err := s.db.Exec(`UPDATE demos SET name = ?, updated_at = ? WHERE id = ?`, newName, now, id)
	if isUnique(err) {
		return ErrNameTaken
	}
	if err != nil {
		return fmt.Errorf("store: rename demo: %w", err)
	}
	return nil
}

// TouchDemo bumps updated_at (e.g. after a deploy).
func (s *Store) TouchDemo(id int64, now int64) error {
	_, err := s.db.Exec(`UPDATE demos SET updated_at = ? WHERE id = ?`, now, id)
	if err != nil {
		return fmt.Errorf("store: touch demo: %w", err)
	}
	return nil
}

// DeleteDemo removes the demo row; releases follow by ON DELETE CASCADE.
func (s *Store) DeleteDemo(id int64) error {
	res, err := s.db.Exec(`DELETE FROM demos WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete demo: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

const demoCols = `id, name, created_by, created_at, updated_at, private, access_key_hash`

func scanDemo(row interface{ Scan(...any) error }) (Demo, error) {
	var d Demo
	var private int64
	if err := row.Scan(&d.ID, &d.Name, &d.CreatedBy, &d.CreatedAt, &d.UpdatedAt,
		&private, &d.AccessKeyHash); err != nil {
		return Demo{}, err
	}
	d.Private = private == 1
	return d, nil
}

// DemoByName resolves a Host label to a demo — the static-serving lookup.
func (s *Store) DemoByName(name string) (Demo, error) {
	d, err := scanDemo(s.db.QueryRow(`SELECT `+demoCols+` FROM demos WHERE name = ?`, name))
	return d, wrapNoRows(err)
}

// DemoByID fetches by primary key.
func (s *Store) DemoByID(id int64) (Demo, error) {
	d, err := scanDemo(s.db.QueryRow(`SELECT `+demoCols+` FROM demos WHERE id = ?`, id))
	return d, wrapNoRows(err)
}

// ListDemos returns all demos with their latest release (if any), ordered
// by name — the dashboard's live list.
func (s *Store) ListDemos() ([]DemoWithLatest, error) {
	rows, err := s.db.Query(`
		SELECT d.id, d.name, d.created_by, d.created_at, d.updated_at, d.private, d.access_key_hash,
		       r.id, r.dir, r.uploaded_by, r.uploaded_at, r.size_bytes, r.file_count,
		       (SELECT COUNT(*) FROM releases rc WHERE rc.demo_id = d.id)
		FROM demos d
		LEFT JOIN releases r ON r.id = (
			SELECT id FROM releases WHERE demo_id = d.id
			ORDER BY uploaded_at DESC, id DESC LIMIT 1
		)
		ORDER BY d.name`)
	if err != nil {
		return nil, fmt.Errorf("store: list demos: %w", err)
	}
	defer rows.Close()

	var out []DemoWithLatest
	for rows.Next() {
		var d DemoWithLatest
		var private int64
		var relID, relUploadedAt, relSize, relFiles sql.NullInt64
		var relDir, relBy sql.NullString
		if err := rows.Scan(&d.ID, &d.Name, &d.CreatedBy, &d.CreatedAt, &d.UpdatedAt, &private, &d.AccessKeyHash,
			&relID, &relDir, &relBy, &relUploadedAt, &relSize, &relFiles, &d.ReleaseCount); err != nil {
			return nil, fmt.Errorf("store: list demos scan: %w", err)
		}
		d.Private = private == 1
		if relID.Valid {
			d.LastRelease = &Release{
				ID: relID.Int64, DemoID: d.ID, Dir: relDir.String, UploadedBy: relBy.String,
				UploadedAt: relUploadedAt.Int64, SizeBytes: relSize.Int64, FileCount: relFiles.Int64,
			}
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// AddRelease records a published build.
func (s *Store) AddRelease(demoID int64, dir, uploadedBy string, sizeBytes, fileCount, now int64) (Release, error) {
	res, err := s.db.Exec(
		`INSERT INTO releases (demo_id, dir, uploaded_by, uploaded_at, size_bytes, file_count)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		demoID, dir, uploadedBy, now, sizeBytes, fileCount,
	)
	if err != nil {
		return Release{}, fmt.Errorf("store: add release: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Release{}, fmt.Errorf("store: add release id: %w", err)
	}
	return Release{
		ID: id, DemoID: demoID, Dir: dir, UploadedBy: uploadedBy,
		UploadedAt: now, SizeBytes: sizeBytes, FileCount: fileCount,
	}, nil
}

// ReleasesFor lists a demo's releases, newest first.
func (s *Store) ReleasesFor(demoID int64) ([]Release, error) {
	rows, err := s.db.Query(
		`SELECT id, demo_id, dir, uploaded_by, uploaded_at, size_bytes, file_count
		 FROM releases WHERE demo_id = ? ORDER BY uploaded_at DESC, id DESC`, demoID)
	if err != nil {
		return nil, fmt.Errorf("store: releases for %d: %w", demoID, err)
	}
	defer rows.Close()

	var out []Release
	for rows.Next() {
		var r Release
		if err := rows.Scan(&r.ID, &r.DemoID, &r.Dir, &r.UploadedBy, &r.UploadedAt, &r.SizeBytes, &r.FileCount); err != nil {
			return nil, fmt.Errorf("store: releases scan: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// PruneReleases deletes all but the newest keep releases and returns the
// pruned rows so the caller can remove their directories.
func (s *Store) PruneReleases(demoID int64, keep int) ([]Release, error) {
	if keep < 1 {
		return nil, fmt.Errorf("store: prune keep must be >= 1, got %d", keep)
	}
	rows, err := s.db.Query(
		`SELECT id, demo_id, dir, uploaded_by, uploaded_at, size_bytes, file_count
		 FROM releases WHERE demo_id = ? AND id NOT IN (
			SELECT id FROM releases WHERE demo_id = ?
			ORDER BY uploaded_at DESC, id DESC LIMIT ?
		 ) ORDER BY id`, demoID, demoID, keep)
	if err != nil {
		return nil, fmt.Errorf("store: prune select: %w", err)
	}
	defer rows.Close()

	var prune []Release
	for rows.Next() {
		var r Release
		if err := rows.Scan(&r.ID, &r.DemoID, &r.Dir, &r.UploadedBy, &r.UploadedAt, &r.SizeBytes, &r.FileCount); err != nil {
			return nil, fmt.Errorf("store: prune scan: %w", err)
		}
		prune = append(prune, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, r := range prune {
		if _, err := s.db.Exec(`DELETE FROM releases WHERE id = ?`, r.ID); err != nil {
			return prune, fmt.Errorf("store: prune delete %d: %w", r.ID, err)
		}
	}
	return prune, nil
}

// Session is a server-side login session. IDHash is the sha256 hex of the
// cookie value — the raw cookie id is never stored (house rule: tokens
// hashed at rest). UserID is the owning local user in password mode and
// NULL for Google OAuth sessions (which predate the users table and are
// identified by the Google columns instead).
type Session struct {
	IDHash      string
	CSRFToken   string
	GoogleSub   string
	GoogleEmail string
	UserID      sql.NullInt64
	CreatedAt   int64
	ExpiresAt   int64
}

// CreateSession stores a new session.
func (s *Store) CreateSession(sess Session) error {
	var userID any
	if sess.UserID.Valid {
		userID = sess.UserID.Int64
	}
	_, err := s.db.Exec(
		`INSERT INTO sessions (id_hash, csrf_token, google_sub, google_email, user_id, created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		sess.IDHash, sess.CSRFToken, sess.GoogleSub, sess.GoogleEmail, userID, sess.CreatedAt, sess.ExpiresAt,
	)
	if err != nil {
		return fmt.Errorf("store: create session: %w", err)
	}
	return nil
}

// SessionByIDHash resolves a cookie id (already hashed) to a session.
func (s *Store) SessionByIDHash(idHash string) (Session, error) {
	var sess Session
	err := s.db.QueryRow(
		`SELECT id_hash, csrf_token, google_sub, google_email, user_id, created_at, expires_at
		 FROM sessions WHERE id_hash = ?`, idHash,
	).Scan(&sess.IDHash, &sess.CSRFToken, &sess.GoogleSub, &sess.GoogleEmail, &sess.UserID, &sess.CreatedAt, &sess.ExpiresAt)
	return sess, wrapNoRows(err)
}

// DeleteSession revokes a session (logout).
func (s *Store) DeleteSession(idHash string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE id_hash = ?`, idHash)
	if err != nil {
		return fmt.Errorf("store: delete session: %w", err)
	}
	return nil
}

// DeleteExpiredSessions removes stale rows; returns how many went.
func (s *Store) DeleteExpiredSessions(now int64) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM sessions WHERE expires_at < ?`, now)
	if err != nil {
		return 0, fmt.Errorf("store: delete expired sessions: %w", err)
	}
	n, err := res.RowsAffected()
	return n, err
}

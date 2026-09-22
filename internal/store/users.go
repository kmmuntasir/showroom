// Users back the password auth mode: local email/password accounts with a
// role. Exactly one capability differs by role — user management (list,
// create, delete, password reset) is superadmin-only; every other control
// operation is open to any authenticated user in either auth mode.
//
// Emails are lowercased before insert and lookup so uniqueness is
// case-insensitive by construction. Password hashes are opaque strings to
// this package (bcrypt, owned by the auth package) — the store never sees
// a cleartext password.
package store

import (
	"errors"
	"fmt"
	"strings"
)

// Roles for local users.
const (
	RoleSuperadmin = "superadmin"
	RoleUser       = "user"
)

// ErrEmailTaken and ErrLastSuperadmin are the sentinel errors call sites
// branch on; everything else is a wrapped driver error.
var (
	ErrEmailTaken     = errors.New("store: email taken")
	ErrLastSuperadmin = errors.New("store: cannot remove the last superadmin")
)

// User is one local login account.
type User struct {
	ID           int64
	Email        string
	PasswordHash string
	Role         string
	CreatedAt    int64
	UpdatedAt    int64
}

// normalizeEmail lowercases and trims so "PM@Example.com" and
// "pm@example.com" are the same account.
func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func validRole(role string) bool {
	return role == RoleSuperadmin || role == RoleUser
}

func validUserEmail(email string) bool {
	return email != "" && strings.Contains(email, "@") && !strings.Contains(email, " ")
}

func scanUser(row interface{ Scan(...any) error }) (User, error) {
	var u User
	if err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Role, &u.CreatedAt, &u.UpdatedAt); err != nil {
		return User{}, err
	}
	return u, nil
}

const userCols = `id, email, password_hash, role, created_at, updated_at`

// CreateUser inserts a local account; ErrEmailTaken on the unique
// violation. passwordHash must already be a bcrypt hash — the store never
// hashes.
func (s *Store) CreateUser(email, passwordHash, role string, now int64) (User, error) {
	email = normalizeEmail(email)
	if !validUserEmail(email) {
		return User{}, fmt.Errorf("store: invalid email")
	}
	if !validRole(role) {
		return User{}, fmt.Errorf("store: invalid role %q", role)
	}
	if passwordHash == "" {
		return User{}, fmt.Errorf("store: empty password hash")
	}
	res, err := s.db.Exec(
		`INSERT INTO users (email, password_hash, role, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?)`,
		email, passwordHash, role, now, now,
	)
	if isUnique(err) {
		return User{}, ErrEmailTaken
	}
	if err != nil {
		return User{}, fmt.Errorf("store: create user: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return User{}, fmt.Errorf("store: create user id: %w", err)
	}
	return User{ID: id, Email: email, PasswordHash: passwordHash, Role: role, CreatedAt: now, UpdatedAt: now}, nil
}

// UserByEmail fetches by (case-insensitive) email.
func (s *Store) UserByEmail(email string) (User, error) {
	u, err := scanUser(s.db.QueryRow(`SELECT `+userCols+` FROM users WHERE email = ?`, normalizeEmail(email)))
	return u, wrapNoRows(err)
}

// UserByID fetches by primary key.
func (s *Store) UserByID(id int64) (User, error) {
	u, err := scanUser(s.db.QueryRow(`SELECT `+userCols+` FROM users WHERE id = ?`, id))
	return u, wrapNoRows(err)
}

// ListUsers returns all local accounts ordered by email — the user
// management screen.
func (s *Store) ListUsers() ([]User, error) {
	rows, err := s.db.Query(`SELECT ` + userCols + ` FROM users ORDER BY email`)
	if err != nil {
		return nil, fmt.Errorf("store: list users: %w", err)
	}
	defer rows.Close()

	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list users scan: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// CountSuperadmins reports how many superadmin accounts exist — the delete
// guard consults it.
func (s *Store) CountSuperadmins() (int64, error) {
	var n int64
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE role = ?`, RoleSuperadmin).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count superadmins: %w", err)
	}
	return n, nil
}

// DeleteUser removes a local account; its sessions follow by ON DELETE
// CASCADE. Removing the last superadmin is refused with ErrLastSuperadmin
// — a deployment must always have someone able to manage users.
func (s *Store) DeleteUser(id int64) error {
	u, err := s.UserByID(id)
	if err != nil {
		return err
	}
	if u.Role == RoleSuperadmin {
		n, err := s.CountSuperadmins()
		if err != nil {
			return err
		}
		if n <= 1 {
			return ErrLastSuperadmin
		}
	}
	res, err := s.db.Exec(`DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete user: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateUserPassword replaces the stored bcrypt hash (password reset by a
// superadmin); ErrNotFound for an unknown id.
func (s *Store) UpdateUserPassword(id int64, passwordHash string, now int64) error {
	if passwordHash == "" {
		return fmt.Errorf("store: empty password hash")
	}
	res, err := s.db.Exec(`UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`, passwordHash, now, id)
	if err != nil {
		return fmt.Errorf("store: update user password: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// EnsureSuperadmin creates the bootstrap superadmin from the env
// credentials when no account holds that email yet. It never changes an
// existing account's password — rotation happens through user management,
// not by rebooting with a different env value. If the email exists with a
// lesser role (only possible via manual DB edits — the bootstrap runs
// before any user can be created), it is promoted so the deployment always
// has a superadmin behind the configured address. created reports whether
// a row was inserted.
func (s *Store) EnsureSuperadmin(email, passwordHash string, now int64) (user User, created bool, err error) {
	email = normalizeEmail(email)
	if !validUserEmail(email) {
		return User{}, false, fmt.Errorf("store: invalid email")
	}
	if passwordHash == "" {
		return User{}, false, fmt.Errorf("store: empty password hash")
	}
	u, err := s.CreateUser(email, passwordHash, RoleSuperadmin, now)
	if err == nil {
		return u, true, nil
	}
	// CreateUser already maps the unique violation to ErrEmailTaken; any
	// other failure (validation, driver) propagates.
	if !errors.Is(err, ErrEmailTaken) {
		return User{}, false, err
	}
	existing, err := s.UserByEmail(email)
	if err != nil {
		return User{}, false, err
	}
	if existing.Role != RoleSuperadmin {
		if _, err := s.db.Exec(`UPDATE users SET role = ?, updated_at = ? WHERE id = ?`, RoleSuperadmin, now, existing.ID); err != nil {
			return User{}, false, fmt.Errorf("store: promote superadmin: %w", err)
		}
		existing.Role = RoleSuperadmin
		existing.UpdatedAt = now
	}
	return existing, false, nil
}

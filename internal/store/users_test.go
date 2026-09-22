package store

import (
	"database/sql"
	"errors"
	"testing"
)

// hashForTests is a stand-in bcrypt hash — the store treats hashes as
// opaque strings, so tests never pay the bcrypt cost here.
const hashForTests = "bcrypt-hash-placeholder"

func TestCreateAndFetchUser(t *testing.T) {
	s := openTest(t)
	u, err := s.CreateUser("PM@Example.com", hashForTests, RoleUser, 100)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if u.ID == 0 {
		t.Fatal("no rowid assigned")
	}
	if u.Email != "pm@example.com" {
		t.Errorf("Email = %q, want lowercased", u.Email)
	}
	byEmail, err := s.UserByEmail("pm@example.com")
	if err != nil {
		t.Fatalf("UserByEmail: %v", err)
	}
	if byEmail.ID != u.ID || byEmail.PasswordHash != hashForTests || byEmail.Role != RoleUser {
		t.Errorf("byEmail = %+v", byEmail)
	}
	// Lookup is case-insensitive too.
	if _, err := s.UserByEmail("PM@EXAMPLE.COM"); err != nil {
		t.Errorf("uppercase lookup: %v", err)
	}
	byID, err := s.UserByID(u.ID)
	if err != nil {
		t.Fatalf("UserByID: %v", err)
	}
	if byID.Email != "pm@example.com" {
		t.Errorf("byID = %+v", byID)
	}
}

func TestCreateUserValidation(t *testing.T) {
	s := openTest(t)
	cases := []struct {
		name         string
		email        string
		hash         string
		role         string
		wantSentinel error
	}{
		{"duplicate email", "a@example.com", hashForTests, RoleUser, ErrEmailTaken},
		{"duplicate email case-insensitive", "A@EXAMPLE.COM", hashForTests, RoleUser, ErrEmailTaken},
		{"bad email", "not-an-email", hashForTests, RoleUser, nil},
		{"empty email", "", hashForTests, RoleUser, nil},
		{"bad role", "b@example.com", hashForTests, "admin", nil},
		{"empty hash", "c@example.com", "", RoleUser, nil},
	}
	if _, err := s.CreateUser("a@example.com", hashForTests, RoleUser, 1); err != nil {
		t.Fatalf("seed CreateUser: %v", err)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.CreateUser(tc.email, tc.hash, tc.role, 2)
			if err == nil {
				t.Fatal("want error, got nil")
			}
			if tc.wantSentinel != nil && !errors.Is(err, tc.wantSentinel) {
				t.Errorf("err = %v, want %v", err, tc.wantSentinel)
			}
		})
	}
}

func TestListUsersOrdered(t *testing.T) {
	s := openTest(t)
	for _, email := range []string{"z@example.com", "a@example.com", "m@example.com"} {
		if _, err := s.CreateUser(email, hashForTests, RoleUser, 1); err != nil {
			t.Fatalf("CreateUser %s: %v", email, err)
		}
	}
	list, err := s.ListUsers()
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(list) != 3 || list[0].Email != "a@example.com" || list[1].Email != "m@example.com" || list[2].Email != "z@example.com" {
		t.Errorf("order = %+v, want a,m,z", list)
	}
}

func TestDeleteUser(t *testing.T) {
	s := openTest(t)
	u, _ := s.CreateUser("a@example.com", hashForTests, RoleUser, 1)
	if err := s.DeleteUser(u.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if _, err := s.UserByID(u.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("after delete err = %v, want ErrNotFound", err)
	}
	if err := s.DeleteUser(u.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("double delete err = %v, want ErrNotFound", err)
	}
	if err := s.DeleteUser(999999); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown id err = %v, want ErrNotFound", err)
	}
}

func TestDeleteUserProtectsLastSuperadmin(t *testing.T) {
	s := openTest(t)
	only, _ := s.CreateUser("only@example.com", hashForTests, RoleSuperadmin, 1)
	if err := s.DeleteUser(only.ID); !errors.Is(err, ErrLastSuperadmin) {
		t.Errorf("deleting the only superadmin err = %v, want ErrLastSuperadmin", err)
	}
	// Still there.
	if _, err := s.UserByID(only.ID); err != nil {
		t.Errorf("superadmin removed despite guard: %v", err)
	}
	second, _ := s.CreateUser("second@example.com", hashForTests, RoleSuperadmin, 2)
	if err := s.DeleteUser(only.ID); err != nil {
		t.Errorf("deleting one of two superadmins: %v", err)
	}
	if _, err := s.UserByID(second.ID); err != nil {
		t.Errorf("remaining superadmin missing: %v", err)
	}
	// A plain user never trips the guard.
	plain, _ := s.CreateUser("plain@example.com", hashForTests, RoleUser, 3)
	if err := s.DeleteUser(plain.ID); err != nil {
		t.Errorf("deleting a plain user: %v", err)
	}
}

func TestUpdateUserPassword(t *testing.T) {
	s := openTest(t)
	u, _ := s.CreateUser("a@example.com", "old-hash", RoleUser, 1)
	if err := s.UpdateUserPassword(u.ID, "new-hash", 99); err != nil {
		t.Fatalf("UpdateUserPassword: %v", err)
	}
	got, _ := s.UserByID(u.ID)
	if got.PasswordHash != "new-hash" {
		t.Errorf("hash = %q, want new-hash", got.PasswordHash)
	}
	if got.UpdatedAt != 99 {
		t.Errorf("UpdatedAt = %d, want 99", got.UpdatedAt)
	}
	if err := s.UpdateUserPassword(999999, "x", 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown id err = %v, want ErrNotFound", err)
	}
	if err := s.UpdateUserPassword(u.ID, "", 1); err == nil {
		t.Error("empty hash: want error, got nil")
	}
}

func TestEnsureSuperadmin(t *testing.T) {
	s := openTest(t)
	first, created, err := s.EnsureSuperadmin("Admin@Example.com", "hash-1", 10)
	if err != nil {
		t.Fatalf("EnsureSuperadmin: %v", err)
	}
	if !created {
		t.Error("first call: want created=true")
	}
	if first.Email != "admin@example.com" || first.Role != RoleSuperadmin {
		t.Errorf("first = %+v", first)
	}

	// Second boot with the same (or a different) env password must not
	// rotate the stored hash.
	second, created, err := s.EnsureSuperadmin("admin@example.com", "hash-2", 20)
	if err != nil {
		t.Fatalf("second EnsureSuperadmin: %v", err)
	}
	if created {
		t.Error("second call: want created=false")
	}
	if second.ID != first.ID || second.PasswordHash != "hash-1" {
		t.Errorf("second = %+v, want the original row untouched", second)
	}

	if n, _ := s.CountSuperadmins(); n != 1 {
		t.Errorf("superadmin count = %d, want 1", n)
	}
}

func TestEnsureSuperadminPromotesDemoted(t *testing.T) {
	s := openTest(t)
	u, _ := s.CreateUser("admin@example.com", hashForTests, RoleUser, 1)
	got, created, err := s.EnsureSuperadmin("admin@example.com", "hash-2", 2)
	if err != nil {
		t.Fatalf("EnsureSuperadmin: %v", err)
	}
	if created {
		t.Error("want created=false for an existing email")
	}
	if got.ID != u.ID || got.Role != RoleSuperadmin {
		t.Errorf("got = %+v, want the row promoted to superadmin", got)
	}
}

func TestDeleteUserCascadesSessions(t *testing.T) {
	s := openTest(t)
	u, _ := s.CreateUser("a@example.com", hashForTests, RoleUser, 1)
	sess := Session{
		IDHash: "sess-for-deleted-user", CSRFToken: "c", GoogleSub: "",
		GoogleEmail: u.Email, CreatedAt: 1, ExpiresAt: 500,
	}
	sess.UserID.Int64 = u.ID
	sess.UserID.Valid = true
	if err := s.CreateSession(sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := s.DeleteUser(u.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if _, err := s.SessionByIDHash("sess-for-deleted-user"); !errors.Is(err, ErrNotFound) {
		t.Errorf("session survived its user: %v", err)
	}
}

func TestDeleteUserSessionsExcept(t *testing.T) {
	s := openTest(t)
	u, _ := s.CreateUser("a@example.com", hashForTests, RoleUser, 1)
	mk := func(hash string) {
		t.Helper()
		sess := Session{
			IDHash: hash, CSRFToken: "c", GoogleEmail: u.Email,
			CreatedAt: 1, ExpiresAt: 500,
		}
		sess.UserID = sql.NullInt64{Int64: u.ID, Valid: true}
		if err := s.CreateSession(sess); err != nil {
			t.Fatalf("CreateSession %s: %v", hash, err)
		}
	}
	mk("keep-me")
	mk("kill-me-1")
	mk("kill-me-2")

	n, err := s.DeleteUserSessionsExcept(u.ID, "keep-me")
	if err != nil {
		t.Fatalf("DeleteUserSessionsExcept: %v", err)
	}
	if n != 2 {
		t.Errorf("revoked %d, want 2", n)
	}
	if _, err := s.SessionByIDHash("keep-me"); err != nil {
		t.Errorf("kept session removed: %v", err)
	}
	if _, err := s.SessionByIDHash("kill-me-1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("kill-me-1 survived: %v", err)
	}

	// Empty keep-hash revokes everything.
	mk("kill-me-3")
	n, err = s.DeleteUserSessionsExcept(u.ID, "")
	if err != nil {
		t.Fatalf("revoke all: %v", err)
	}
	if n != 2 {
		t.Errorf("revoke-all got %d, want 2", n)
	}
	if _, err := s.SessionByIDHash("keep-me"); !errors.Is(err, ErrNotFound) {
		t.Error("keep-me survived revoke-all")
	}
}

package store

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"
)

// openTest opens a store against a fresh temp-dir sqlite — the house
// pattern: the DB is never mocked, migrations run for real.
func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestCreateAndFetchDemo(t *testing.T) {
	s := openTest(t)
	d, err := s.CreateDemo("acme", "pm@example.com", false, "", 1725864000)
	if err != nil {
		t.Fatalf("CreateDemo: %v", err)
	}
	if d.ID == 0 {
		t.Fatal("no rowid assigned")
	}
	byName, err := s.DemoByName("acme")
	if err != nil {
		t.Fatalf("DemoByName: %v", err)
	}
	if byName.ID != d.ID || byName.CreatedBy != "pm@example.com" {
		t.Errorf("byName = %+v", byName)
	}
	byID, err := s.DemoByID(d.ID)
	if err != nil {
		t.Fatalf("DemoByID: %v", err)
	}
	if byID.Name != "acme" {
		t.Errorf("byID = %+v", byID)
	}
}

func TestCreateDemoNameTaken(t *testing.T) {
	s := openTest(t)
	if _, err := s.CreateDemo("acme", "a@b.c", false, "", 1); err != nil {
		t.Fatalf("first CreateDemo: %v", err)
	}
	if _, err := s.CreateDemo("acme", "x@y.z", false, "", 2); !errors.Is(err, ErrNameTaken) {
		t.Errorf("second CreateDemo err = %v, want ErrNameTaken", err)
	}
}

func TestRenameDemo(t *testing.T) {
	s := openTest(t)
	d, _ := s.CreateDemo("acme", "a@b.c", false, "", 1)
	if err := s.RenameDemo(d.ID, "acme2", 5); err != nil {
		t.Fatalf("RenameDemo: %v", err)
	}
	if _, err := s.DemoByName("acme"); !errors.Is(err, ErrNotFound) {
		t.Errorf("old name err = %v, want ErrNotFound", err)
	}
	if _, err := s.DemoByName("acme2"); err != nil {
		t.Errorf("new name: %v", err)
	}
	// Rename onto an existing name conflicts.
	other, _ := s.CreateDemo("bea", "a@b.c", false, "", 2)
	if err := s.RenameDemo(other.ID, "acme2", 6); !errors.Is(err, ErrNameTaken) {
		t.Errorf("colliding rename err = %v, want ErrNameTaken", err)
	}
}

func TestDeleteDemoCascadesReleases(t *testing.T) {
	s := openTest(t)
	d, _ := s.CreateDemo("acme", "a@b.c", false, "", 1)
	if _, err := s.AddRelease(d.ID, "1-100", "a@b.c", 10, 2, 100); err != nil {
		t.Fatalf("AddRelease: %v", err)
	}
	if err := s.DeleteDemo(d.ID); err != nil {
		t.Fatalf("DeleteDemo: %v", err)
	}
	rels, err := s.ReleasesFor(d.ID)
	if err != nil {
		t.Fatalf("ReleasesFor: %v", err)
	}
	if len(rels) != 0 {
		t.Errorf("releases survived cascade: %+v", rels)
	}
	if err := s.DeleteDemo(d.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("double delete err = %v, want ErrNotFound", err)
	}
}

func TestListDemosWithLatestRelease(t *testing.T) {
	s := openTest(t)
	d1, _ := s.CreateDemo("acme", "a@b.c", false, "", 1)
	d2, _ := s.CreateDemo("bea", "x@y.z", false, "", 2)

	if _, err := s.ListDemos(); err != nil {
		t.Fatalf("ListDemos (no releases): %v", err)
	}
	if _, err := s.AddRelease(d1.ID, "1-100", "a@b.c", 10, 2, 100); err != nil {
		t.Fatalf("AddRelease 1: %v", err)
	}
	if _, err := s.AddRelease(d1.ID, "1-200", "a@b.c", 20, 4, 200); err != nil {
		t.Fatalf("AddRelease 2: %v", err)
	}

	list, err := s.ListDemos()
	if err != nil {
		t.Fatalf("ListDemos: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("len = %d, want 2", len(list))
	}
	if list[0].Name != "acme" || list[1].Name != "bea" {
		t.Errorf("order = %s,%s, want acme,bea", list[0].Name, list[1].Name)
	}
	if list[0].LastRelease == nil || list[0].LastRelease.Dir != "1-200" {
		t.Errorf("acme latest = %+v, want dir 1-200", list[0].LastRelease)
	}
	if list[0].ReleaseCount != 2 {
		t.Errorf("acme release count = %d, want 2", list[0].ReleaseCount)
	}
	if list[1].LastRelease != nil {
		t.Errorf("bea latest = %+v, want nil", list[1].LastRelease)
	}
	if list[1].ID != d2.ID {
		t.Errorf("bea id = %d, want %d", list[1].ID, d2.ID)
	}
}

func TestPruneReleasesKeepsNewest(t *testing.T) {
	s := openTest(t)
	d, _ := s.CreateDemo("acme", "a@b.c", false, "", 1)
	for i, ts := range []int64{100, 200, 300} {
		dir := fmt.Sprintf("%d-%d", i+1, ts)
		if _, err := s.AddRelease(d.ID, dir, "a@b.c", int64(i), 1, ts); err != nil {
			t.Fatalf("AddRelease %d: %v", ts, err)
		}
	}
	pruned, err := s.PruneReleases(d.ID, 2)
	if err != nil {
		t.Fatalf("PruneReleases: %v", err)
	}
	if len(pruned) != 1 || pruned[0].Dir != "1-100" {
		t.Errorf("pruned = %+v, want only 1-100", pruned)
	}
	rels, _ := s.ReleasesFor(d.ID)
	if len(rels) != 2 || rels[0].Dir != "3-300" || rels[1].Dir != "2-200" {
		t.Errorf("remaining = %+v", rels)
	}
}

func TestCreateDemoPrivateRoundTrip(t *testing.T) {
	s := openTest(t)
	priv, err := s.CreateDemo("secret", "pm@example.com", true, "hash-of-key", 1)
	if err != nil {
		t.Fatalf("CreateDemo private: %v", err)
	}
	if !priv.Private || priv.AccessKeyHash != "hash-of-key" {
		t.Errorf("created = %+v, want private with hash", priv)
	}
	byName, err := s.DemoByName("secret")
	if err != nil {
		t.Fatalf("DemoByName: %v", err)
	}
	if !byName.Private || byName.AccessKeyHash != "hash-of-key" {
		t.Errorf("byName = %+v, want privacy columns", byName)
	}

	pub, _ := s.CreateDemo("open", "pm@example.com", false, "", 2)
	if pub.Private || pub.AccessKeyHash != "" {
		t.Errorf("public created = %+v, want public and keyless", pub)
	}

	list, err := s.ListDemos()
	if err != nil {
		t.Fatalf("ListDemos: %v", err)
	}
	for _, row := range list {
		switch row.Name {
		case "secret":
			if !row.Private || row.AccessKeyHash != "hash-of-key" {
				t.Errorf("list secret = %+v", row)
			}
		case "open":
			if row.Private || row.AccessKeyHash != "" {
				t.Errorf("list open = %+v", row)
			}
		}
	}
}

func TestCreateDemoPrivateRequiresKey(t *testing.T) {
	s := openTest(t)
	if _, err := s.CreateDemo("secret", "a@b.c", true, "", 1); !errors.Is(err, ErrPrivateKeyNeedsKey) {
		t.Errorf("private without key err = %v, want ErrPrivateKeyNeedsKey", err)
	}
	// A public demo must not keep a key even if one is passed.
	d, err := s.CreateDemo("open", "a@b.c", false, "stray-hash", 1)
	if err != nil {
		t.Fatalf("public CreateDemo: %v", err)
	}
	if d.AccessKeyHash != "" {
		t.Errorf("public demo kept a key hash: %+v", d)
	}
}

func TestSetDemoPrivacy(t *testing.T) {
	s := openTest(t)
	d, _ := s.CreateDemo("acme", "a@b.c", false, "", 1)

	if err := s.SetDemoPrivacy(d.ID, true, "hash-1", 5); err != nil {
		t.Fatalf("enable: %v", err)
	}
	got, _ := s.DemoByID(d.ID)
	if !got.Private || got.AccessKeyHash != "hash-1" || got.UpdatedAt != 5 {
		t.Errorf("after enable = %+v", got)
	}

	// Disable clears the hash; repeating the disable is fine.
	if err := s.SetDemoPrivacy(d.ID, false, "", 6); err != nil {
		t.Fatalf("disable: %v", err)
	}
	got, _ = s.DemoByID(d.ID)
	if got.Private || got.AccessKeyHash != "" {
		t.Errorf("after disable = %+v, want key forgotten", got)
	}
	if err := s.SetDemoPrivacy(d.ID, false, "ignored", 7); err != nil {
		t.Fatalf("re-disable: %v", err)
	}
	got, _ = s.DemoByID(d.ID)
	if got.Private || got.AccessKeyHash != "" {
		t.Errorf("after re-disable = %+v, key must stay forgotten", got)
	}

	if err := s.SetDemoPrivacy(d.ID, true, "", 8); !errors.Is(err, ErrPrivateKeyNeedsKey) {
		t.Errorf("enable without key err = %v, want ErrPrivateKeyNeedsKey", err)
	}
	if err := s.SetDemoPrivacy(999999, false, "", 9); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown id err = %v, want ErrNotFound", err)
	}
}

func TestTouchDemo(t *testing.T) {
	s := openTest(t)
	d, _ := s.CreateDemo("acme", "a@b.c", false, "", 1)
	if err := s.TouchDemo(d.ID, 99); err != nil {
		t.Fatalf("TouchDemo: %v", err)
	}
	got, _ := s.DemoByID(d.ID)
	if got.UpdatedAt != 99 {
		t.Errorf("UpdatedAt = %d, want 99", got.UpdatedAt)
	}
}

func TestSessionRoundTrip(t *testing.T) {
	s := openTest(t)
	in := Session{
		IDHash: "abc123", CSRFToken: "csrf456", GoogleSub: "sub-1",
		GoogleEmail: "pm@example.com", CreatedAt: 1, ExpiresAt: 100,
	}
	if err := s.CreateSession(in); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	got, err := s.SessionByIDHash("abc123")
	if err != nil {
		t.Fatalf("SessionByIDHash: %v", err)
	}
	if got != in {
		t.Errorf("got %+v, want %+v", got, in)
	}
	if err := s.DeleteSession("abc123"); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, err := s.SessionByIDHash("abc123"); !errors.Is(err, ErrNotFound) {
		t.Errorf("after delete err = %v, want ErrNotFound", err)
	}
}

func TestDeleteExpiredSessions(t *testing.T) {
	s := openTest(t)
	s.CreateSession(Session{IDHash: "old", CSRFToken: "c", GoogleSub: "s", GoogleEmail: "e", CreatedAt: 1, ExpiresAt: 50})
	s.CreateSession(Session{IDHash: "new", CSRFToken: "c", GoogleSub: "s", GoogleEmail: "e", CreatedAt: 1, ExpiresAt: 500})
	n, err := s.DeleteExpiredSessions(100)
	if err != nil {
		t.Fatalf("DeleteExpiredSessions: %v", err)
	}
	if n != 1 {
		t.Errorf("deleted %d, want 1", n)
	}
	if _, err := s.SessionByIDHash("new"); err != nil {
		t.Errorf("unexpired session deleted: %v", err)
	}
}

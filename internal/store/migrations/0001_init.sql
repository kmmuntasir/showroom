-- 0001_init.sql — democtl schema (docs/demos.md §democtl service).
-- Timestamps are unix seconds UTC; ids are rowid integers.

CREATE TABLE demos (
  id         INTEGER PRIMARY KEY,
  name       TEXT    NOT NULL UNIQUE,   -- subdomain label, validated by demonames before insert
  created_by TEXT    NOT NULL,          -- Google email of the creator
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE releases (
  id          INTEGER PRIMARY KEY,
  demo_id     INTEGER NOT NULL REFERENCES demos(id) ON DELETE CASCADE,
  dir         TEXT    NOT NULL,          -- basename under DataDir/releases/, e.g. 42-1725864000
  uploaded_by TEXT    NOT NULL,
  uploaded_at INTEGER NOT NULL,
  size_bytes  INTEGER NOT NULL,
  file_count  INTEGER NOT NULL
);
CREATE INDEX idx_releases_demo ON releases(demo_id, uploaded_at DESC);

CREATE TABLE sessions (
  id_hash     TEXT    PRIMARY KEY,       -- sha256 hex of the cookie value; the raw id is never stored
  csrf_token  TEXT    NOT NULL,
  google_sub  TEXT    NOT NULL,
  google_email TEXT   NOT NULL,
  created_at  INTEGER NOT NULL,
  expires_at  INTEGER NOT NULL
);
CREATE INDEX idx_sessions_expiry ON sessions(expires_at);

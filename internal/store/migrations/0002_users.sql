-- 0002_users.sql — local email/password auth (password auth mode).
-- Users back the credentials login; sessions gain an optional owner link.
-- Google sessions predate this table and keep user_id NULL.

CREATE TABLE users (
  id            INTEGER PRIMARY KEY,
  email         TEXT    NOT NULL UNIQUE,   -- lowercased before insert/lookup
  password_hash TEXT    NOT NULL,          -- bcrypt hash, never the password
  role          TEXT    NOT NULL DEFAULT 'user' CHECK (role IN ('superadmin', 'user')),
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL
);

-- Owner of a password-mode session; NULL for Google OAuth sessions.
-- ON DELETE CASCADE revokes every session the moment its user is removed.
ALTER TABLE sessions ADD COLUMN user_id INTEGER REFERENCES users(id) ON DELETE CASCADE;
CREATE INDEX idx_sessions_user ON sessions(user_id);

-- 0003_privacy.sql — per-demo privacy: a private demo is served behind a
-- shared access-key gate on its own host. The key is stored hashed only
-- (house rule: tokens hashed at rest); a public row must carry no key.

ALTER TABLE demos ADD COLUMN private INTEGER NOT NULL DEFAULT 0 CHECK (private IN (0, 1));
ALTER TABLE demos ADD COLUMN access_key_hash TEXT NOT NULL DEFAULT '';

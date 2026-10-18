-- Admin sign-ins survive a restart of Milo. Only the SHA-256 of the cookie token is stored, so a
-- copy of the database (backups include it) cannot be used to sign in.
CREATE TABLE sessions (
    token_hash  TEXT PRIMARY KEY,
    user_id     INTEGER NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
    fingerprint TEXT NOT NULL,
    expires_at  TEXT NOT NULL
);
CREATE INDEX sessions_expires ON sessions (expires_at);

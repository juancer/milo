-- Admin accounts for the web UI: several people (mom, dad...) can manage the
-- device, each with their own password. The first one (the owner) is generated
-- at install time. Not to be confused with `users`, the Telegram family members.

CREATE TABLE admin_users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT NOT NULL UNIQUE CHECK (length(username) BETWEEN 2 AND 24),
    password_hash TEXT NOT NULL,
    is_owner      INTEGER NOT NULL DEFAULT 0 CHECK (is_owner IN (0, 1)),
    must_change   INTEGER NOT NULL DEFAULT 0 CHECK (must_change IN (0, 1)),
    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    last_login    TEXT
);

-- Databases from earlier versions had a single shared admin password.
INSERT INTO admin_users (username, password_hash, is_owner)
    SELECT 'admin', value, 1 FROM system_settings WHERE key = 'admin.password_hash';
DELETE FROM system_settings WHERE key = 'admin.password_hash';

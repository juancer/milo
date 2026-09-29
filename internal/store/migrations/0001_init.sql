-- Phase 1 foundation: system, secrets, audit, identity and permissions.
-- Later phases add their tables (chats, messages, buttons, ...) in new migrations.

CREATE TABLE device (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    name       TEXT NOT NULL DEFAULT 'Nexo',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
INSERT INTO device (id) VALUES (1);

CREATE TABLE system_settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE features (
    name       TEXT PRIMARY KEY,
    enabled    INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

-- Only encrypted values are stored here. See internal/secrets.
CREATE TABLE secrets (
    name       TEXT PRIMARY KEY,
    ciphertext BLOB NOT NULL,
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE audit_log (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    actor   TEXT NOT NULL,
    action  TEXT NOT NULL,
    target  TEXT NOT NULL DEFAULT '',
    details TEXT NOT NULL DEFAULT ''
);
CREATE INDEX audit_log_at ON audit_log (at);

CREATE TABLE users (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    display_name TEXT NOT NULL,
    role         TEXT NOT NULL CHECK (role IN ('OWNER', 'PARENT', 'FAMILY', 'CHILD')),
    created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

-- Explicit per-user overrides on top of the role defaults.
CREATE TABLE permissions (
    user_id    INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    permission TEXT NOT NULL,
    granted    INTEGER NOT NULL CHECK (granted IN (0, 1)),
    PRIMARY KEY (user_id, permission)
);

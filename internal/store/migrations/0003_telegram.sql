-- Phase 3: Telegram identity, invitations, chats and messages.
-- Note: there is intentionally no separate "notifications" or "pending_messages" table.
-- A pending message is a row in `messages` with status 'pending', and whether it has
-- been announced is `notified_at`.

ALTER TABLE users ADD COLUMN telegram_id INTEGER;
ALTER TABLE users ADD COLUMN telegram_username TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN active INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0, 1));
CREATE UNIQUE INDEX users_telegram_id ON users (telegram_id) WHERE telegram_id IS NOT NULL;

-- Only the SHA-256 of the token is stored; the raw token exists once, in the
-- deep link shown to the admin at creation time.
CREATE TABLE invitations (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    token_hash TEXT NOT NULL UNIQUE,
    role       TEXT NOT NULL CHECK (role IN ('PARENT', 'FAMILY', 'CHILD')),
    note       TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    expires_at TEXT NOT NULL,
    used_at    TEXT,
    revoked_at TEXT
);

CREATE TABLE pending_authorizations (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    telegram_id   INTEGER NOT NULL,
    username      TEXT NOT NULL DEFAULT '',
    first_name    TEXT NOT NULL DEFAULT '',
    role          TEXT NOT NULL CHECK (role IN ('PARENT', 'FAMILY', 'CHILD')),
    invitation_id INTEGER REFERENCES invitations (id),
    status        TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected')),
    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    decided_at    TEXT
);
CREATE UNIQUE INDEX pending_one_per_person ON pending_authorizations (telegram_id) WHERE status = 'pending';

-- A destination the child can talk to. kind 'user' is a private chat with an
-- authorized user; 'group' is a Telegram group the bot was added to.
CREATE TABLE chats (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    name             TEXT NOT NULL UNIQUE,
    kind             TEXT NOT NULL CHECK (kind IN ('user', 'group')),
    user_id          INTEGER REFERENCES users (id) ON DELETE CASCADE,
    telegram_chat_id INTEGER NOT NULL,
    favorite         INTEGER NOT NULL DEFAULT 1 CHECK (favorite IN (0, 1)),
    position         INTEGER NOT NULL DEFAULT 0,
    enabled          INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1))
);

CREATE TABLE messages (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    direction       TEXT NOT NULL CHECK (direction IN ('in', 'out')),
    kind            TEXT NOT NULL CHECK (kind IN ('text', 'voice')),
    type            TEXT NOT NULL DEFAULT 'normal' CHECK (type IN ('normal', 'emergency')),
    priority        TEXT NOT NULL DEFAULT 'normal' CHECK (priority IN ('normal', 'critical')),
    -- in:  pending -> played | expired      out: queued -> sent | failed
    status          TEXT NOT NULL CHECK (status IN ('pending', 'played', 'expired', 'queued', 'sent', 'failed')),
    user_id         INTEGER REFERENCES users (id) ON DELETE SET NULL,
    chat_id         INTEGER REFERENCES chats (id) ON DELETE SET NULL,
    body            TEXT NOT NULL DEFAULT '',
    audio           BLOB,
    created_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    notified_at     TEXT,
    played_at       TEXT,
    attempts        INTEGER NOT NULL DEFAULT 0,
    last_error      TEXT NOT NULL DEFAULT '',
    next_attempt_at TEXT
);
CREATE INDEX messages_inbox ON messages (direction, status, priority, id);

-- Phase 8: timers and reminders (music, weather and calendar keep their
-- configuration in system_settings and secrets).

CREATE TABLE timers (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    label      TEXT NOT NULL DEFAULT '',
    fires_at   TEXT NOT NULL,
    status     TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'fired', 'cancelled')),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX timers_due ON timers (status, fires_at);

CREATE TABLE reminders (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    text       TEXT NOT NULL CHECK (length(text) BETWEEN 1 AND 200),
    due_at     TEXT NOT NULL,
    status     TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'done', 'cancelled')),
    source     TEXT NOT NULL DEFAULT 'assistant' CHECK (source IN ('assistant', 'admin', 'button')),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX reminders_due ON reminders (status, due_at);

-- Phase 7: recordings of the wake phrase, kept as speech features (numbers),
-- not as audio. Settings (engine, sensitivity, phrase label) live in system_settings.

CREATE TABLE wake_templates (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    features   BLOB NOT NULL,
    frames     INTEGER NOT NULL CHECK (frames > 0),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

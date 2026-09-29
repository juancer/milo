-- Phase 2: configurable physical buttons.
-- The four rows below are only DEFAULTS. Colours are metadata for humans and the
-- admin UI; nothing in the code depends on them. Parents can change everything.

CREATE TABLE button_configs (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT NOT NULL UNIQUE,
    color      TEXT NOT NULL DEFAULT '',
    gpio       INTEGER NOT NULL UNIQUE CHECK (gpio BETWEEN 0 AND 57),
    active_low INTEGER NOT NULL DEFAULT 1 CHECK (active_low IN (0, 1)),
    enabled    INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1))
);

CREATE TABLE button_actions (
    button_id  INTEGER NOT NULL REFERENCES button_configs (id) ON DELETE CASCADE,
    event_type TEXT NOT NULL CHECK (event_type IN ('short_press', 'long_press', 'double_press')),
    action     TEXT NOT NULL,
    parameters TEXT NOT NULL DEFAULT '{}',
    PRIMARY KEY (button_id, event_type)
);

INSERT INTO button_configs (name, color, gpio) VALUES
    ('Red', 'red', 17),
    ('Blue', 'blue', 22),
    ('Green', 'green', 23),
    ('Yellow', 'yellow', 24);

INSERT INTO button_actions (button_id, event_type, action)
    SELECT id, 'short_press', CASE color
        WHEN 'red' THEN 'talk'
        WHEN 'blue' THEN 'listen'
        WHEN 'green' THEN 'gpt'
        ELSE 'chat_select'
    END FROM button_configs;

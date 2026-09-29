-- Phase 4: usage schedules.
-- A feature with its schedule switched on may only be used inside its rules
-- (an enabled schedule with no rules blocks the feature entirely). A feature
-- without a schedule row, or with the schedule off, is always available.

CREATE TABLE usage_schedules (
    feature TEXT PRIMARY KEY,
    enabled INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1))
);

-- day_of_week: 0 = Sunday ... 6 = Saturday (Go's time.Weekday).
-- Minutes count from midnight. A rule with start > end continues past midnight
-- into the next day (for example 22:00-06:00).
CREATE TABLE usage_schedule_rules (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    feature      TEXT NOT NULL,
    day_of_week  INTEGER NOT NULL CHECK (day_of_week BETWEEN 0 AND 6),
    start_minute INTEGER NOT NULL CHECK (start_minute BETWEEN 0 AND 1439),
    end_minute   INTEGER NOT NULL CHECK (end_minute BETWEEN 1 AND 1440),
    enabled      INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    CHECK (start_minute <> end_minute)
);
CREATE INDEX usage_schedule_rules_feature ON usage_schedule_rules (feature);

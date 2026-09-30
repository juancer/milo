-- Identical audit events repeated within a short window are coalesced into
-- one row that counts the repeats, so one actor's traffic cannot bury the log.
ALTER TABLE audit_log ADD COLUMN repeats INTEGER NOT NULL DEFAULT 1;

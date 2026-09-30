-- Chat names must differ by more than case: send_message matches them without
-- regard to case. Rename later duplicates first (the oldest keeps its name),
-- then let the database enforce it, so two concurrent saves cannot race.
UPDATE chats SET name = name || ' (' || id || ')'
WHERE EXISTS (SELECT 1 FROM chats c2 WHERE lower(c2.name) = lower(chats.name) AND c2.id < chats.id);

CREATE UNIQUE INDEX chats_name_lower ON chats (lower(name));

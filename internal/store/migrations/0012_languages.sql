-- Languages: the assistant language becomes a locale code (or 'auto'), text-to-speech gains
-- language, speed and volume, and each person can have a preferred language.

ALTER TABLE gpt_settings ADD COLUMN tts_language TEXT NOT NULL DEFAULT '';   -- '' = follow the assistant language
ALTER TABLE gpt_settings ADD COLUMN tts_speed    REAL NOT NULL DEFAULT 1.0 CHECK (tts_speed BETWEEN 0.5 AND 2.0);
ALTER TABLE gpt_settings ADD COLUMN tts_volume   INTEGER NOT NULL DEFAULT 100 CHECK (tts_volume BETWEEN 0 AND 100);

-- Older installs stored free text ("" meant "same language as the child"). Keep what maps to a
-- supported language, everything else becomes 'auto'. A database with no admin yet is brand new:
-- it starts in English, the project's source language.
UPDATE gpt_settings SET language = CASE
    WHEN NOT EXISTS (SELECT 1 FROM admin_users) THEN 'en'
    WHEN lower(trim(language)) IN ('english', 'en', 'inglés', 'ingles') THEN 'en'
    WHEN lower(trim(language)) IN ('spanish', 'es', 'español', 'espanol', 'castellano') THEN 'es'
    ELSE 'auto' END;

-- '' = follow the notification language.
ALTER TABLE users ADD COLUMN language TEXT NOT NULL DEFAULT '';

-- Phase 6: more speech providers and Wi-Fi networks.
-- SQLite cannot change a CHECK constraint in place, so gpt_settings is rebuilt.

CREATE TABLE gpt_settings_new (
    id                 INTEGER PRIMARY KEY CHECK (id = 1),
    child_name         TEXT NOT NULL DEFAULT '',
    age                INTEGER NOT NULL DEFAULT 7 CHECK (age BETWEEN 3 AND 17),
    personality        TEXT NOT NULL DEFAULT 'a warm, patient and playful friend',
    language           TEXT NOT NULL DEFAULT '',
    max_response_words INTEGER NOT NULL DEFAULT 60 CHECK (max_response_words BETWEEN 10 AND 300),
    extra_instructions TEXT NOT NULL DEFAULT '',
    retention_days     INTEGER NOT NULL DEFAULT 30 CHECK (retention_days IN (0, 7, 30, 90)),
    input_moderation   INTEGER NOT NULL DEFAULT 1 CHECK (input_moderation IN (0, 1)),
    output_moderation  INTEGER NOT NULL DEFAULT 1 CHECK (output_moderation IN (0, 1)),
    alert_parents      INTEGER NOT NULL DEFAULT 1 CHECK (alert_parents IN (0, 1)),
    follow_up          INTEGER NOT NULL DEFAULT 1 CHECK (follow_up IN (0, 1)),
    max_turns_per_hour INTEGER NOT NULL DEFAULT 60 CHECK (max_turns_per_hour BETWEEN 5 AND 600),
    llm_base_url       TEXT NOT NULL DEFAULT 'https://openrouter.ai/api/v1',
    llm_model          TEXT NOT NULL DEFAULT 'openai/gpt-oss-20b:free',
    -- Speech-to-text: 'cloud' = OpenAI-compatible API (Groq...), 'whispercpp' = on the device.
    stt_engine         TEXT NOT NULL DEFAULT 'cloud' CHECK (stt_engine IN ('cloud', 'whispercpp')),
    stt_base_url       TEXT NOT NULL DEFAULT 'https://api.groq.com/openai/v1',
    stt_model          TEXT NOT NULL DEFAULT 'whisper-large-v3-turbo',
    stt_model_path     TEXT NOT NULL DEFAULT '',
    -- Text-to-speech: 'piper' on the device, 'cloud' = OpenAI-compatible /audio/speech, or 'none'.
    tts_engine         TEXT NOT NULL DEFAULT 'piper' CHECK (tts_engine IN ('piper', 'cloud', 'none')),
    tts_model_path     TEXT NOT NULL DEFAULT '',
    tts_base_url       TEXT NOT NULL DEFAULT 'https://api.groq.com/openai/v1',
    tts_model          TEXT NOT NULL DEFAULT '',
    tts_voice          TEXT NOT NULL DEFAULT ''
);

INSERT INTO gpt_settings_new (id, child_name, age, personality, language, max_response_words, extra_instructions,
    retention_days, input_moderation, output_moderation, alert_parents, follow_up, max_turns_per_hour,
    llm_base_url, llm_model, stt_base_url, stt_model, tts_engine, tts_model_path)
SELECT id, child_name, age, personality, language, max_response_words, extra_instructions,
    retention_days, input_moderation, output_moderation, alert_parents, follow_up, max_turns_per_hour,
    llm_base_url, llm_model, stt_base_url, stt_model, tts_engine, tts_model_path
FROM gpt_settings;

DROP TABLE gpt_settings;
ALTER TABLE gpt_settings_new RENAME TO gpt_settings;

-- Wi-Fi networks the device may join, highest priority first. The password is
-- stored encrypted in `secrets` under secret_name ('' = open network).
CREATE TABLE wifi_networks (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    ssid           TEXT NOT NULL UNIQUE CHECK (length(ssid) BETWEEN 1 AND 32),
    secret_name    TEXT NOT NULL DEFAULT '',
    priority       INTEGER NOT NULL DEFAULT 0 CHECK (priority BETWEEN 0 AND 100),
    enabled        INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    last_connected TEXT
);

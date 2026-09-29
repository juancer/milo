-- Phase 5: assistant settings, moderation rules and conversation history.
-- API keys are NOT here: they live encrypted in `secrets`.

CREATE TABLE gpt_settings (
    id                 INTEGER PRIMARY KEY CHECK (id = 1),
    child_name         TEXT NOT NULL DEFAULT '',
    age                INTEGER NOT NULL DEFAULT 7 CHECK (age BETWEEN 3 AND 17),
    personality        TEXT NOT NULL DEFAULT 'a warm, patient and playful friend',
    language           TEXT NOT NULL DEFAULT '',
    max_response_words INTEGER NOT NULL DEFAULT 60 CHECK (max_response_words BETWEEN 10 AND 300),
    extra_instructions TEXT NOT NULL DEFAULT '',
    -- 0 keeps nothing on disk; otherwise days of conversation history to keep.
    retention_days     INTEGER NOT NULL DEFAULT 30 CHECK (retention_days IN (0, 7, 30, 90)),
    input_moderation   INTEGER NOT NULL DEFAULT 1 CHECK (input_moderation IN (0, 1)),
    output_moderation  INTEGER NOT NULL DEFAULT 1 CHECK (output_moderation IN (0, 1)),
    alert_parents      INTEGER NOT NULL DEFAULT 1 CHECK (alert_parents IN (0, 1)),
    follow_up          INTEGER NOT NULL DEFAULT 1 CHECK (follow_up IN (0, 1)),
    max_turns_per_hour INTEGER NOT NULL DEFAULT 60 CHECK (max_turns_per_hour BETWEEN 5 AND 600),
    -- LLM: any OpenAI-compatible chat API (OpenRouter by default; Groq etc. work too).
    llm_base_url       TEXT NOT NULL DEFAULT 'https://openrouter.ai/api/v1',
    llm_model          TEXT NOT NULL DEFAULT 'openai/gpt-oss-20b:free',
    -- Speech-to-text: any OpenAI-compatible transcription API (Groq by default).
    stt_base_url       TEXT NOT NULL DEFAULT 'https://api.groq.com/openai/v1',
    stt_model          TEXT NOT NULL DEFAULT 'whisper-large-v3-turbo',
    tts_engine         TEXT NOT NULL DEFAULT 'piper' CHECK (tts_engine IN ('piper', 'none')),
    tts_model_path     TEXT NOT NULL DEFAULT ''
);
INSERT INTO gpt_settings (id) VALUES (1);

-- Parent-defined words/phrases the assistant must not hear or say.
CREATE TABLE moderation_rules (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    pattern    TEXT NOT NULL CHECK (length(pattern) BETWEEN 2 AND 80),
    applies_to TEXT NOT NULL DEFAULT 'both' CHECK (applies_to IN ('input', 'output', 'both')),
    enabled    INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE conversations (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    started_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    source     TEXT NOT NULL DEFAULT 'button'
);

CREATE TABLE conversation_messages (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    conversation_id INTEGER NOT NULL REFERENCES conversations (id) ON DELETE CASCADE,
    role            TEXT NOT NULL CHECK (role IN ('user', 'assistant', 'tool')),
    content         TEXT NOT NULL,
    tool_name       TEXT NOT NULL DEFAULT '',
    at              TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX conversation_messages_conv ON conversation_messages (conversation_id);

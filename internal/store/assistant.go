package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// GPTSettings are the parent-editable assistant settings (one row).
type GPTSettings struct {
	ChildName         string
	Age               int
	Personality       string
	Language          string
	MaxResponseWords  int
	ExtraInstructions string
	RetentionDays     int
	InputModeration   bool
	OutputModeration  bool
	AlertParents      bool
	FollowUp          bool
	MaxTurnsPerHour   int
	LLMBaseURL        string
	LLMModel          string
	STTBaseURL        string
	STTModel          string
	TTSEngine         string
	TTSModelPath      string
}

// GetGPTSettings returns the assistant settings.
func (s *Store) GetGPTSettings(ctx context.Context) (GPTSettings, error) {
	var g GPTSettings
	err := s.db.QueryRowContext(ctx, `SELECT child_name, age, personality, language, max_response_words, extra_instructions,
		retention_days, input_moderation, output_moderation, alert_parents, follow_up, max_turns_per_hour,
		llm_base_url, llm_model, stt_base_url, stt_model, tts_engine, tts_model_path FROM gpt_settings WHERE id = 1`).
		Scan(&g.ChildName, &g.Age, &g.Personality, &g.Language, &g.MaxResponseWords, &g.ExtraInstructions,
			&g.RetentionDays, &g.InputModeration, &g.OutputModeration, &g.AlertParents, &g.FollowUp, &g.MaxTurnsPerHour,
			&g.LLMBaseURL, &g.LLMModel, &g.STTBaseURL, &g.STTModel, &g.TTSEngine, &g.TTSModelPath)
	if err != nil {
		return GPTSettings{}, fmt.Errorf("store: get gpt settings: %w", err)
	}
	return g, nil
}

// UpdateGPTSettings replaces the assistant settings.
func (s *Store) UpdateGPTSettings(ctx context.Context, g GPTSettings) error {
	_, err := s.db.ExecContext(ctx, `UPDATE gpt_settings SET child_name = ?, age = ?, personality = ?, language = ?,
		max_response_words = ?, extra_instructions = ?, retention_days = ?, input_moderation = ?, output_moderation = ?,
		alert_parents = ?, follow_up = ?, max_turns_per_hour = ?, llm_base_url = ?, llm_model = ?, stt_base_url = ?,
		stt_model = ?, tts_engine = ?, tts_model_path = ? WHERE id = 1`,
		g.ChildName, g.Age, g.Personality, g.Language, g.MaxResponseWords, g.ExtraInstructions, g.RetentionDays,
		g.InputModeration, g.OutputModeration, g.AlertParents, g.FollowUp, g.MaxTurnsPerHour,
		g.LLMBaseURL, g.LLMModel, g.STTBaseURL, g.STTModel, g.TTSEngine, g.TTSModelPath)
	if err != nil {
		return fmt.Errorf("store: update gpt settings: %w", err)
	}
	return nil
}

// ModerationRule is a parent-defined blocked phrase.
type ModerationRule struct {
	ID        int64
	Pattern   string
	AppliesTo string // input, output or both
	Enabled   bool
}

// ListModerationRules returns all rules.
func (s *Store) ListModerationRules(ctx context.Context) ([]ModerationRule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, pattern, applies_to, enabled FROM moderation_rules ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store: list moderation rules: %w", err)
	}
	defer rows.Close()
	var out []ModerationRule
	for rows.Next() {
		var r ModerationRule
		if err := rows.Scan(&r.ID, &r.Pattern, &r.AppliesTo, &r.Enabled); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CreateModerationRule adds a rule.
func (s *Store) CreateModerationRule(ctx context.Context, r ModerationRule) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO moderation_rules (pattern, applies_to, enabled) VALUES (?, ?, ?)`,
		r.Pattern, r.AppliesTo, r.Enabled)
	if err != nil {
		return 0, fmt.Errorf("store: create moderation rule: %w", err)
	}
	return res.LastInsertId()
}

// DeleteModerationRule removes a rule.
func (s *Store) DeleteModerationRule(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM moderation_rules WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ConversationMessage is one line of a stored conversation.
type ConversationMessage struct {
	ID       int64
	Role     string
	Content  string
	ToolName string
	At       time.Time
}

// Conversation is a stored conversation with its messages.
type Conversation struct {
	ID        int64
	StartedAt time.Time
	Source    string
	Messages  []ConversationMessage
}

// CreateConversation starts a stored conversation.
func (s *Store) CreateConversation(ctx context.Context, source string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO conversations (source) VALUES (?)`, source)
	if err != nil {
		return 0, fmt.Errorf("store: create conversation: %w", err)
	}
	return res.LastInsertId()
}

// AddConversationMessage appends a line to a conversation.
func (s *Store) AddConversationMessage(ctx context.Context, conversationID int64, role, content, toolName string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO conversation_messages (conversation_id, role, content, tool_name) VALUES (?, ?, ?, ?)`,
		conversationID, role, content, toolName)
	if err != nil {
		return fmt.Errorf("store: add conversation message: %w", err)
	}
	return nil
}

// ListConversations returns the most recent conversations with their messages.
func (s *Store) ListConversations(ctx context.Context, limit int) ([]Conversation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, started_at, source FROM conversations ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list conversations: %w", err)
	}
	var out []Conversation
	for rows.Next() {
		var c Conversation
		var at string
		if err := rows.Scan(&c.ID, &at, &c.Source); err != nil {
			rows.Close()
			return nil, err
		}
		c.StartedAt = parseTime(at)
		out = append(out, c)
	}
	rows.Close()
	for i := range out {
		mrows, err := s.db.QueryContext(ctx, `SELECT id, role, content, tool_name, at FROM conversation_messages
			WHERE conversation_id = ? ORDER BY id`, out[i].ID)
		if err != nil {
			return nil, err
		}
		for mrows.Next() {
			var m ConversationMessage
			var at string
			if err := mrows.Scan(&m.ID, &m.Role, &m.Content, &m.ToolName, &at); err != nil {
				mrows.Close()
				return nil, err
			}
			m.At = parseTime(at)
			out[i].Messages = append(out[i].Messages, m)
		}
		mrows.Close()
	}
	return out, nil
}

// PruneConversations deletes conversations that started before cutoff.
func (s *Store) PruneConversations(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM conversations WHERE started_at < ?`, ts(cutoff))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteAllConversations removes every stored conversation.
func (s *Store) DeleteAllConversations(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM conversations`)
	return err
}

// ConversationExists is used by tests and callers that resume a conversation.
func (s *Store) ConversationExists(ctx context.Context, id int64) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM conversations WHERE id = ?`, id).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return n > 0, err
}

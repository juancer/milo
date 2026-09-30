package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Message is a stored voice or text message, incoming or outgoing.
type Message struct {
	ID            int64
	Direction     string // "in" or "out"
	Kind          string // "text" or "voice"
	Type          string // "normal" or "emergency"
	Priority      string // "normal" or "critical"
	Status        string
	UserID        int64 // sender (incoming)
	ChatID        int64 // destination (outgoing)
	Body          string
	Audio         []byte
	CreatedAt     time.Time
	NotifiedAt    *time.Time
	PlayedAt      *time.Time
	Attempts      int
	LastError     string
	NextAttemptAt *time.Time
}

const msgCols = `id, direction, kind, type, priority, status, COALESCE(user_id, 0), COALESCE(chat_id, 0), body, audio,
	created_at, notified_at, played_at, attempts, last_error, next_attempt_at`

func scanMessage(sc interface{ Scan(...any) error }) (Message, error) {
	var m Message
	var created string
	var notified, played, next sql.NullString
	err := sc.Scan(&m.ID, &m.Direction, &m.Kind, &m.Type, &m.Priority, &m.Status, &m.UserID, &m.ChatID, &m.Body, &m.Audio,
		&created, &notified, &played, &m.Attempts, &m.LastError, &next)
	if err != nil {
		return Message{}, err
	}
	m.CreatedAt = parseTime(created)
	if notified.Valid {
		t := parseTime(notified.String)
		m.NotifiedAt = &t
	}
	if played.Valid {
		t := parseTime(played.String)
		m.PlayedAt = &t
	}
	if next.Valid {
		t := parseTime(next.String)
		m.NextAttemptAt = &t
	}
	return m, nil
}

// InsertMessage stores a message and returns its id. Status defaults to
// "pending" for incoming and "queued" for outgoing messages.
func (s *Store) InsertMessage(ctx context.Context, m Message) (int64, error) {
	if m.Status == "" {
		m.Status = "pending"
		if m.Direction == "out" {
			m.Status = "queued"
		}
	}
	if m.Type == "" {
		m.Type = "normal"
	}
	if m.Priority == "" {
		m.Priority = "normal"
	}
	var user, chat any
	if m.UserID != 0 {
		user = m.UserID
	}
	if m.ChatID != 0 {
		chat = m.ChatID
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO messages (direction, kind, type, priority, status, user_id, chat_id, body, audio)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.Direction, m.Kind, m.Type, m.Priority, m.Status, user, chat, m.Body, m.Audio)
	if err != nil {
		return 0, fmt.Errorf("store: insert message: %w", err)
	}
	return res.LastInsertId()
}

// NextPending returns the next unplayed incoming message: critical first, then
// oldest first. If emergencyOnly is set, only emergency messages qualify.
func (s *Store) NextPending(ctx context.Context, emergencyOnly bool) (Message, error) {
	q := `SELECT ` + msgCols + ` FROM messages WHERE direction = 'in' AND status = 'pending'`
	if emergencyOnly {
		q += ` AND type = 'emergency'`
	}
	q += ` ORDER BY (priority = 'critical') DESC, id ASC LIMIT 1`
	m, err := scanMessage(s.db.QueryRowContext(ctx, q))
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, ErrNotFound
	}
	return m, err
}

// PendingFrom counts the unplayed incoming messages from one sender.
func (s *Store) PendingFrom(ctx context.Context, userID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE direction = 'in' AND status = 'pending' AND user_id = ?`, userID).Scan(&n)
	return n, err
}

// QueuedOut counts the outgoing messages still waiting to be sent: those for
// one chat and in total.
func (s *Store) QueuedOut(ctx context.Context, chatID int64) (forChat, total int, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(chat_id = ?), 0), COUNT(*) FROM messages
		WHERE direction = 'out' AND status = 'queued'`, chatID).Scan(&forChat, &total)
	return
}

// ExpireMessage marks a pending incoming message as expired so it is never played.
func (s *Store) ExpireMessage(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE messages SET status = 'expired' WHERE id = ? AND direction = 'in' AND status = 'pending'`, id)
	return err
}

// ExpirePendingFrom marks every pending incoming message from one sender as expired.
func (s *Store) ExpirePendingFrom(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE messages SET status = 'expired' WHERE direction = 'in' AND status = 'pending' AND user_id = ?`, userID)
	return err
}

// MarkPlayed records that a pending message was played.
func (s *Store) MarkPlayed(ctx context.Context, id int64, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE messages SET status = 'played', played_at = ? WHERE id = ? AND status = 'pending'`, ts(now), id)
	return err
}

// MarkNotified records that a message was announced with a sound.
func (s *Store) MarkNotified(ctx context.Context, id int64, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE messages SET notified_at = ? WHERE id = ?`, ts(now), id)
	return err
}

// UnnotifiedPending returns pending incoming messages that were stored silently.
func (s *Store) UnnotifiedPending(ctx context.Context) ([]Message, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+msgCols+` FROM messages
		WHERE direction = 'in' AND status = 'pending' AND notified_at IS NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// PendingCounts returns the number of unplayed incoming messages: total and emergencies.
func (s *Store) PendingCounts(ctx context.Context) (total, emergency int, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(type = 'emergency'), 0) FROM messages
		WHERE direction = 'in' AND status = 'pending'`).Scan(&total, &emergency)
	return
}

// NextQueued returns the oldest outgoing message that is due for a send attempt.
func (s *Store) NextQueued(ctx context.Context, now time.Time) (Message, error) {
	m, err := scanMessage(s.db.QueryRowContext(ctx, `SELECT `+msgCols+` FROM messages
		WHERE direction = 'out' AND status = 'queued' AND (next_attempt_at IS NULL OR next_attempt_at <= ?)
		ORDER BY id ASC LIMIT 1`, ts(now)))
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, ErrNotFound
	}
	return m, err
}

// MarkSent records a successful delivery to Telegram.
func (s *Store) MarkSent(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE messages SET status = 'sent', last_error = '', next_attempt_at = NULL WHERE id = ?`, id)
	return err
}

// MarkAttemptFailed records a failed send. If retryAt is nil the message is
// given up on (status "failed"); otherwise it stays queued until retryAt.
func (s *Store) MarkAttemptFailed(ctx context.Context, id int64, reason string, retryAt *time.Time) error {
	if retryAt == nil {
		_, err := s.db.ExecContext(ctx, `UPDATE messages SET status = 'failed', attempts = attempts + 1, last_error = ?, next_attempt_at = NULL WHERE id = ?`, reason, id)
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE messages SET attempts = attempts + 1, last_error = ?, next_attempt_at = ? WHERE id = ?`, reason, ts(*retryAt), id)
	return err
}

// MessageSummary is a content-free view of a message for the admin UI.
type MessageSummary struct {
	ID        int64
	Direction string
	Kind      string
	Type      string
	Status    string
	Person    string // sender or destination name
	CreatedAt time.Time
	Attempts  int
	LastError string
}

// RecentMessages lists recent messages without their content.
func (s *Store) RecentMessages(ctx context.Context, limit int) ([]MessageSummary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT m.id, m.direction, m.kind, m.type, m.status,
		COALESCE(u.display_name, c.name, ''), m.created_at, m.attempts, m.last_error
		FROM messages m LEFT JOIN users u ON u.id = m.user_id LEFT JOIN chats c ON c.id = m.chat_id
		ORDER BY m.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MessageSummary
	for rows.Next() {
		var m MessageSummary
		var created string
		if err := rows.Scan(&m.ID, &m.Direction, &m.Kind, &m.Type, &m.Status, &m.Person, &created, &m.Attempts, &m.LastError); err != nil {
			return nil, err
		}
		m.CreatedAt = parseTime(created)
		out = append(out, m)
	}
	return out, rows.Err()
}

// PruneMessages deletes finished messages (played, sent, failed, expired)
// created before cutoff and returns how many were removed. Pending and queued
// messages are never pruned.
func (s *Store) PruneMessages(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM messages
		WHERE status IN ('played', 'sent', 'failed', 'expired') AND created_at < ?`, ts(cutoff))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

func ts(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// ---------------------------------------------------------------- users

// TelegramUser is a user identified by Telegram.
type TelegramUser struct {
	User
	TelegramID       int64
	TelegramUsername string
}

// CreateTelegramUser inserts an active user linked to a Telegram account.
func (s *Store) CreateTelegramUser(ctx context.Context, displayName, role string, telegramID int64, username string) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO users (display_name, role, telegram_id, telegram_username) VALUES (?, ?, ?, ?)`,
		displayName, role, telegramID, username)
	if err != nil {
		return 0, fmt.Errorf("store: create telegram user: %w", err)
	}
	return res.LastInsertId()
}

// GetUserByTelegramID returns the user for a Telegram account, or ErrNotFound.
func (s *Store) GetUserByTelegramID(ctx context.Context, telegramID int64) (User, error) {
	var u User
	err := s.db.QueryRowContext(ctx,
		`SELECT id, display_name, role, active FROM users WHERE telegram_id = ?`, telegramID).
		Scan(&u.ID, &u.DisplayName, &u.Role, &u.Active)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("store: user by telegram id: %w", err)
	}
	return u, nil
}

// ListTelegramUsers returns all users with their Telegram identity.
func (s *Store) ListTelegramUsers(ctx context.Context) ([]TelegramUser, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, display_name, role, active, COALESCE(telegram_id, 0), telegram_username
		FROM users ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store: list users: %w", err)
	}
	defer rows.Close()
	var out []TelegramUser
	for rows.Next() {
		var u TelegramUser
		if err := rows.Scan(&u.ID, &u.DisplayName, &u.Role, &u.Active, &u.TelegramID, &u.TelegramUsername); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// UpdateUser changes a user's name, role and active flag.
func (s *Store) UpdateUser(ctx context.Context, id int64, displayName, role string, active bool) error {
	res, err := s.db.ExecContext(ctx, `UPDATE users SET display_name = ?, role = ?, active = ? WHERE id = ?`,
		displayName, role, active, id)
	if err != nil {
		return fmt.Errorf("store: update user: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ClearPermission removes an explicit override so the role default applies again.
func (s *Store) ClearPermission(ctx context.Context, userID int64, permission string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM permissions WHERE user_id = ? AND permission = ?`, userID, permission)
	return err
}

// ---------------------------------------------------------------- invitations

// Invitation is a single-use, expiring Telegram invitation.
type Invitation struct {
	ID        int64
	Role      string
	Note      string
	CreatedAt time.Time
	ExpiresAt time.Time
	UsedAt    *time.Time
	RevokedAt *time.Time
}

// Status is "active", "used", "revoked" or "expired".
func (i Invitation) Status(now time.Time) string {
	switch {
	case i.UsedAt != nil:
		return "used"
	case i.RevokedAt != nil:
		return "revoked"
	case !now.Before(i.ExpiresAt):
		return "expired"
	}
	return "active"
}

// CreateInvitation stores an invitation by the hash of its token.
func (s *Store) CreateInvitation(ctx context.Context, tokenHash, role, note string, expiresAt time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO invitations (token_hash, role, note, expires_at) VALUES (?, ?, ?, ?)`,
		tokenHash, role, note, ts(expiresAt))
	if err != nil {
		return 0, fmt.Errorf("store: create invitation: %w", err)
	}
	return res.LastInsertId()
}

// ConsumeInvitation atomically marks a valid invitation as used and returns it.
// It returns ErrNotFound if the token is unknown, used, revoked or expired, so
// a token can never be redeemed twice even under concurrent requests.
func (s *Store) ConsumeInvitation(ctx context.Context, tokenHash string, now time.Time) (Invitation, error) {
	var inv Invitation
	var created, expires string
	err := s.db.QueryRowContext(ctx, `UPDATE invitations SET used_at = ?
		WHERE token_hash = ? AND used_at IS NULL AND revoked_at IS NULL AND expires_at > ?
		RETURNING id, role, note, created_at, expires_at`, ts(now), tokenHash, ts(now)).
		Scan(&inv.ID, &inv.Role, &inv.Note, &created, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return Invitation{}, ErrNotFound
	}
	if err != nil {
		return Invitation{}, fmt.Errorf("store: consume invitation: %w", err)
	}
	inv.CreatedAt, inv.ExpiresAt = parseTime(created), parseTime(expires)
	used := now
	inv.UsedAt = &used
	return inv, nil
}

// RevokeInvitation revokes an unused invitation.
func (s *Store) RevokeInvitation(ctx context.Context, id int64, now time.Time) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE invitations SET revoked_at = ? WHERE id = ? AND used_at IS NULL AND revoked_at IS NULL`, ts(now), id)
	if err != nil {
		return fmt.Errorf("store: revoke invitation: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListInvitations returns recent invitations, newest first.
func (s *Store) ListInvitations(ctx context.Context, limit int) ([]Invitation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, role, note, created_at, expires_at, used_at, revoked_at
		FROM invitations ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list invitations: %w", err)
	}
	defer rows.Close()
	var out []Invitation
	for rows.Next() {
		var i Invitation
		var created, expires string
		var used, revoked sql.NullString
		if err := rows.Scan(&i.ID, &i.Role, &i.Note, &created, &expires, &used, &revoked); err != nil {
			return nil, err
		}
		i.CreatedAt, i.ExpiresAt = parseTime(created), parseTime(expires)
		if used.Valid {
			t := parseTime(used.String)
			i.UsedAt = &t
		}
		if revoked.Valid {
			t := parseTime(revoked.String)
			i.RevokedAt = &t
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------- pending authorizations

// PendingAuthorization is a Telegram user waiting for admin approval.
type PendingAuthorization struct {
	ID           int64
	TelegramID   int64
	Username     string
	FirstName    string
	Role         string
	InvitationID int64
	Status       string
	CreatedAt    time.Time
}

// CreatePending records an authorization request. ErrConflict-like failure
// (already pending) surfaces as an error from the unique index.
func (s *Store) CreatePending(ctx context.Context, p PendingAuthorization) (int64, error) {
	var inv any
	if p.InvitationID != 0 {
		inv = p.InvitationID
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO pending_authorizations
		(telegram_id, username, first_name, role, invitation_id) VALUES (?, ?, ?, ?, ?)`,
		p.TelegramID, p.Username, p.FirstName, p.Role, inv)
	if err != nil {
		return 0, fmt.Errorf("store: create pending authorization: %w", err)
	}
	return res.LastInsertId()
}

// HasPending reports whether the Telegram account already has a pending request.
func (s *Store) HasPending(ctx context.Context, telegramID int64) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pending_authorizations WHERE telegram_id = ? AND status = 'pending'`, telegramID).Scan(&n)
	return n > 0, err
}

// ListPending returns requests awaiting a decision, oldest first.
func (s *Store) ListPending(ctx context.Context) ([]PendingAuthorization, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, telegram_id, username, first_name, role, COALESCE(invitation_id, 0), status, created_at
		FROM pending_authorizations WHERE status = 'pending' ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store: list pending: %w", err)
	}
	defer rows.Close()
	var out []PendingAuthorization
	for rows.Next() {
		var p PendingAuthorization
		var created string
		if err := rows.Scan(&p.ID, &p.TelegramID, &p.Username, &p.FirstName, &p.Role, &p.InvitationID, &p.Status, &created); err != nil {
			return nil, err
		}
		p.CreatedAt = parseTime(created)
		out = append(out, p)
	}
	return out, rows.Err()
}

// ApprovePending turns a pending request into an active user, atomically.
func (s *Store) ApprovePending(ctx context.Context, id int64, displayName string, now time.Time) (TelegramUser, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TelegramUser{}, err
	}
	defer tx.Rollback() //nolint:errcheck
	var p PendingAuthorization
	err = tx.QueryRowContext(ctx, `UPDATE pending_authorizations SET status = 'approved', decided_at = ?
		WHERE id = ? AND status = 'pending' RETURNING telegram_id, username, first_name, role`, ts(now), id).
		Scan(&p.TelegramID, &p.Username, &p.FirstName, &p.Role)
	if errors.Is(err, sql.ErrNoRows) {
		return TelegramUser{}, ErrNotFound
	}
	if err != nil {
		return TelegramUser{}, err
	}
	if displayName == "" {
		displayName = p.FirstName
	}
	if displayName == "" {
		displayName = p.Username
	}
	if displayName == "" {
		displayName = "Family member"
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO users (display_name, role, telegram_id, telegram_username) VALUES (?, ?, ?, ?)`,
		displayName, p.Role, p.TelegramID, p.Username)
	if err != nil {
		return TelegramUser{}, fmt.Errorf("store: create approved user: %w", err)
	}
	uid, _ := res.LastInsertId()
	if err := tx.Commit(); err != nil {
		return TelegramUser{}, err
	}
	return TelegramUser{User: User{ID: uid, DisplayName: displayName, Role: p.Role, Active: true},
		TelegramID: p.TelegramID, TelegramUsername: p.Username}, nil
}

// RejectPending marks a pending request as rejected and returns the Telegram id.
func (s *Store) RejectPending(ctx context.Context, id int64, now time.Time) (int64, error) {
	var tg int64
	err := s.db.QueryRowContext(ctx, `UPDATE pending_authorizations SET status = 'rejected', decided_at = ?
		WHERE id = ? AND status = 'pending' RETURNING telegram_id`, ts(now), id).Scan(&tg)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return tg, err
}

// ---------------------------------------------------------------- chats

// Chat is a destination for the child's messages.
type Chat struct {
	ID             int64
	Name           string
	Kind           string // "user" or "group"
	UserID         int64  // for kind "user"
	TelegramChatID int64
	Favorite       bool
	Position       int
	Enabled        bool
}

const chatCols = `id, name, kind, COALESCE(user_id, 0), telegram_chat_id, favorite, position, enabled`

func scanChat(sc interface{ Scan(...any) error }) (Chat, error) {
	var c Chat
	err := sc.Scan(&c.ID, &c.Name, &c.Kind, &c.UserID, &c.TelegramChatID, &c.Favorite, &c.Position, &c.Enabled)
	return c, err
}

// ListChats returns all chats ordered by position then name.
func (s *Store) ListChats(ctx context.Context) ([]Chat, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+chatCols+` FROM chats ORDER BY position, name`)
	if err != nil {
		return nil, fmt.Errorf("store: list chats: %w", err)
	}
	defer rows.Close()
	var out []Chat
	for rows.Next() {
		c, err := scanChat(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetChat returns a chat or ErrNotFound.
func (s *Store) GetChat(ctx context.Context, id int64) (Chat, error) {
	c, err := scanChat(s.db.QueryRowContext(ctx, `SELECT `+chatCols+` FROM chats WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Chat{}, ErrNotFound
	}
	return c, err
}

// CreateChat inserts a chat.
func (s *Store) CreateChat(ctx context.Context, c Chat) (int64, error) {
	var user any
	if c.UserID != 0 {
		user = c.UserID
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO chats (name, kind, user_id, telegram_chat_id, favorite, position, enabled)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, c.Name, c.Kind, user, c.TelegramChatID, c.Favorite, c.Position, c.Enabled)
	if err != nil {
		return 0, fmt.Errorf("store: create chat: %w", err)
	}
	return res.LastInsertId()
}

// UpdateChat replaces a chat's settings.
func (s *Store) UpdateChat(ctx context.Context, c Chat) error {
	var user any
	if c.UserID != 0 {
		user = c.UserID
	}
	res, err := s.db.ExecContext(ctx, `UPDATE chats SET name = ?, kind = ?, user_id = ?, telegram_chat_id = ?,
		favorite = ?, position = ?, enabled = ? WHERE id = ?`,
		c.Name, c.Kind, user, c.TelegramChatID, c.Favorite, c.Position, c.Enabled, c.ID)
	if err != nil {
		return fmt.Errorf("store: update chat: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteChat removes a chat.
func (s *Store) DeleteChat(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM chats WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

package web

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/juancer/nexo/internal/permissions"
	"github.com/juancer/nexo/internal/store"
	"github.com/juancer/nexo/internal/telegram"
)

var tokenRe = regexp.MustCompile(`^[0-9]{3,12}:[A-Za-z0-9_-]{20,64}$`)

func (s *server) telegramRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/telegram", s.requireAuth(s.telegramStatus))
	mux.Handle("PUT /api/telegram/token", s.requireAuth(s.setTelegramToken))
	mux.Handle("DELETE /api/telegram/token", s.requireAuth(s.deleteTelegramToken))
	mux.Handle("GET /api/invitations", s.requireAuth(s.listInvitations))
	mux.Handle("POST /api/invitations", s.requireAuth(s.createInvitation))
	mux.Handle("DELETE /api/invitations/{id}", s.requireAuth(s.revokeInvitation))
	mux.Handle("GET /api/authorizations", s.requireAuth(s.listAuthorizations))
	mux.Handle("POST /api/authorizations/{id}/approve", s.requireAuth(s.approveAuthorization))
	mux.Handle("POST /api/authorizations/{id}/reject", s.requireAuth(s.rejectAuthorization))
	mux.Handle("GET /api/users", s.requireAuth(s.listUsers))
	mux.Handle("PUT /api/users/{id}", s.requireAuth(s.updateUser))
	mux.Handle("GET /api/chats", s.requireAuth(s.listChats))
	mux.Handle("POST /api/chats", s.requireAuth(s.saveChat))
	mux.Handle("PUT /api/chats/{id}", s.requireAuth(s.saveChat))
	mux.Handle("DELETE /api/chats/{id}", s.requireAuth(s.deleteChat))
	mux.Handle("GET /api/messages", s.requireAuth(s.listMessages))
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid id")
		return 0, false
	}
	return id, true
}

// ---------------------------------------------------------------- status and token

func (s *server) telegramStatus(w http.ResponseWriter, r *http.Request) {
	total, urgent, _ := s.app.Store.PendingCounts(r.Context())
	enabled, _ := s.app.Features.Enabled(r.Context(), telegram.FeatureName)
	st := s.app.Telegram.Status(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": enabled, "configured": st.Configured, "running": st.Running,
		"bot_username": st.BotUsername, "poll_error": st.PollError,
		"pending_messages": total, "urgent_messages": urgent,
	})
}

func (s *server) setTelegramToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	tok := strings.TrimSpace(body.Token)
	if !tokenRe.MatchString(tok) {
		writeError(w, http.StatusBadRequest, "that does not look like a Telegram bot token (from @BotFather)")
		return
	}
	if err := s.app.Vault.Put(r.Context(), telegram.SecretBotToken, []byte(tok)); err != nil {
		s.app.Log.Error("store telegram token", "error", err)
		writeError(w, http.StatusInternalServerError, "could not store the token")
		return
	}
	s.audit(r, "telegram.token_set", "", nil) // the value is never recorded
	s.restartTelegram(r.Context())
	s.telegramStatus(w, r)
}

func (s *server) deleteTelegramToken(w http.ResponseWriter, r *http.Request) {
	if err := s.app.Store.DeleteSecret(r.Context(), telegram.SecretBotToken); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.audit(r, "telegram.token_removed", "", nil)
	s.restartTelegram(r.Context())
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) restartTelegram(ctx context.Context) {
	rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := s.app.Features.Restart(rctx, telegram.FeatureName); err != nil {
		s.app.Log.Warn("restart telegram", "error", err)
	}
}

// ---------------------------------------------------------------- invitations

func (s *server) listInvitations(w http.ResponseWriter, r *http.Request) {
	list, err := s.app.Store.ListInvitations(r.Context(), 50)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	now := time.Now()
	type row struct {
		ID        int64     `json:"id"`
		Role      string    `json:"role"`
		Note      string    `json:"note"`
		Status    string    `json:"status"`
		CreatedAt time.Time `json:"created_at"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	out := make([]row, 0, len(list))
	for _, i := range list {
		out = append(out, row{i.ID, i.Role, i.Note, i.Status(now), i.CreatedAt, i.ExpiresAt})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) createInvitation(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Role     string `json:"role"`
		TTLHours int    `json:"ttl_hours"`
		Note     string `json:"note"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	// Invitations can never grant OWNER.
	if body.Role != "PARENT" && body.Role != "FAMILY" && body.Role != "CHILD" {
		writeError(w, http.StatusBadRequest, "role must be PARENT, FAMILY or CHILD")
		return
	}
	if body.TTLHours == 0 {
		body.TTLHours = 24
	}
	if body.TTLHours < 1 || body.TTLHours > 168 || len([]rune(body.Note)) > 60 {
		writeError(w, http.StatusBadRequest, "ttl_hours must be 1-168 and note at most 60 characters")
		return
	}
	token, hash, err := telegram.NewInvitationToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	expires := time.Now().Add(time.Duration(body.TTLHours) * time.Hour)
	id, err := s.app.Store.CreateInvitation(r.Context(), hash, body.Role, strings.TrimSpace(body.Note), expires)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.audit(r, "invitation.created", strconv.FormatInt(id, 10), map[string]string{"role": body.Role})
	bot := s.app.Telegram.BotUsername(r.Context())
	link := ""
	if bot != "" {
		link = telegram.DeepLink(bot, token)
	}
	// The raw token is returned exactly once; only its hash is stored.
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": id, "token": token, "link": link, "expires_at": expires, "command": "/start " + token,
	})
}

func (s *server) revokeInvitation(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.app.Store.RevokeInvitation(r.Context(), id, time.Now()); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no such active invitation")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.audit(r, "invitation.revoked", strconv.FormatInt(id, 10), nil)
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------- authorization requests

func (s *server) listAuthorizations(w http.ResponseWriter, r *http.Request) {
	list, err := s.app.Store.ListPending(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	type row struct {
		ID         int64     `json:"id"`
		TelegramID int64     `json:"telegram_id"`
		Username   string    `json:"username"`
		FirstName  string    `json:"first_name"`
		Role       string    `json:"role"`
		CreatedAt  time.Time `json:"created_at"`
	}
	out := make([]row, 0, len(list))
	for _, p := range list {
		out = append(out, row{p.ID, p.TelegramID, p.Username, p.FirstName, p.Role, p.CreatedAt})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) approveAuthorization(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var body struct {
		DisplayName string `json:"display_name"`
		CreateChat  bool   `json:"create_chat"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	name := strings.TrimSpace(body.DisplayName)
	if len([]rune(name)) > 40 {
		writeError(w, http.StatusBadRequest, "name must be at most 40 characters")
		return
	}
	user, err := s.app.Store.ApprovePending(r.Context(), id, name, time.Now())
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no such pending request")
		return
	}
	if err != nil {
		s.app.Log.Error("approve authorization", "error", err)
		writeError(w, http.StatusConflict, "this person is already authorized")
		return
	}
	s.audit(r, "authorization.approved", strconv.FormatInt(user.TelegramID, 10), map[string]string{"role": user.Role})
	if body.CreateChat {
		existing, _ := s.app.Store.ListChats(r.Context())
		if _, err := s.app.Store.CreateChat(r.Context(), store.Chat{
			Name: user.DisplayName, Kind: "user", UserID: user.ID, TelegramChatID: user.TelegramID,
			Favorite: true, Position: len(existing) + 1, Enabled: true, // new chats go last
		}); err != nil {
			s.app.Log.Warn("create chat for approved user", "error", err) // name clash: parents can add it by hand
		}
	}
	s.app.Telegram.SendText(user.TelegramID, "You have been approved! Send a voice message or a text and it will be delivered. Use /menu for options.")
	writeJSON(w, http.StatusOK, map[string]any{"user_id": user.ID})
}

func (s *server) rejectAuthorization(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	tg, err := s.app.Store.RejectPending(r.Context(), id, time.Now())
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no such pending request")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.audit(r, "authorization.rejected", strconv.FormatInt(tg, 10), nil)
	s.app.Telegram.SendText(tg, "Your request was declined.")
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------- users

type userDTO struct {
	ID               int64           `json:"id"`
	DisplayName      string          `json:"display_name"`
	Role             string          `json:"role"`
	Active           bool            `json:"active"`
	TelegramID       int64           `json:"telegram_id"`
	TelegramUsername string          `json:"telegram_username"`
	Effective        map[string]bool `json:"effective"`
	Overrides        map[string]bool `json:"overrides"`
	RoleDefaults     []string        `json:"role_defaults"`
}

func (s *server) userDTO(ctx context.Context, u store.TelegramUser) (userDTO, error) {
	eff, err := s.app.Perms.Effective(ctx, u.ID)
	if err != nil {
		return userDTO{}, err
	}
	over, err := s.app.Store.UserPermissions(ctx, u.ID)
	if err != nil {
		return userDTO{}, err
	}
	d := userDTO{ID: u.ID, DisplayName: u.DisplayName, Role: u.Role, Active: u.Active, TelegramID: u.TelegramID,
		TelegramUsername: u.TelegramUsername, Effective: map[string]bool{}, Overrides: over, RoleDefaults: []string{}}
	for p, v := range eff {
		d.Effective[string(p)] = v
	}
	for _, p := range permissions.Defaults(permissions.Role(u.Role)) {
		d.RoleDefaults = append(d.RoleDefaults, string(p))
	}
	return d, nil
}

func (s *server) listUsers(w http.ResponseWriter, r *http.Request) {
	list, err := s.app.Store.ListTelegramUsers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]userDTO, 0, len(list))
	for _, u := range list {
		d, err := s.userDTO(r.Context(), u)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		out = append(out, d)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) updateUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var body struct {
		DisplayName string           `json:"display_name"`
		Role        string           `json:"role"`
		Active      bool             `json:"active"`
		Permissions map[string]*bool `json:"permissions"` // null clears the override
	}
	if !decodeJSONLimit(w, r, &body, 8<<10) {
		return
	}
	name := strings.TrimSpace(body.DisplayName)
	if name == "" || len([]rune(name)) > 40 || !permissions.ValidRole(permissions.Role(body.Role)) {
		writeError(w, http.StatusBadRequest, "a name (1-40 characters) and a valid role are required")
		return
	}
	for p := range body.Permissions {
		if !permissions.ValidPermission(permissions.Permission(p)) {
			writeError(w, http.StatusBadRequest, "unknown permission "+strconv.Quote(p))
			return
		}
	}
	ctx := r.Context()
	before, err := s.app.Store.GetUser(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no such user")
		return
	}
	if err := s.app.Store.UpdateUser(ctx, id, name, body.Role, body.Active); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	for p, v := range body.Permissions {
		if v == nil {
			err = s.app.Store.ClearPermission(ctx, id, p)
		} else {
			err = s.app.Perms.Set(ctx, id, permissions.Permission(p), *v)
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
	}
	changes := map[string]string{}
	if before.Role != body.Role {
		changes["role"] = before.Role + "->" + body.Role
	}
	if before.Active != body.Active {
		changes["active"] = strconv.FormatBool(body.Active)
	}
	for p, v := range body.Permissions {
		if v == nil {
			changes["perm."+p] = "default"
		} else {
			changes["perm."+p] = strconv.FormatBool(*v)
		}
	}
	s.audit(r, "user.updated", strconv.FormatInt(id, 10), changes)
	all, _ := s.app.Store.ListTelegramUsers(ctx)
	for _, u := range all {
		if u.ID == id {
			d, _ := s.userDTO(ctx, u)
			writeJSON(w, http.StatusOK, d)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------- chats

type chatDTO struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	Kind           string `json:"kind"`
	UserID         int64  `json:"user_id"`
	TelegramChatID int64  `json:"telegram_chat_id"`
	Favorite       bool   `json:"favorite"`
	Position       int    `json:"position"`
	Enabled        bool   `json:"enabled"`
}

func (s *server) listChats(w http.ResponseWriter, r *http.Request) {
	list, err := s.app.Store.ListChats(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]chatDTO, 0, len(list))
	for _, c := range list {
		out = append(out, chatDTO{c.ID, c.Name, c.Kind, c.UserID, c.TelegramChatID, c.Favorite, c.Position, c.Enabled})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) saveChat(w http.ResponseWriter, r *http.Request) {
	var d chatDTO
	if !decodeJSON(w, r, &d) {
		return
	}
	create := r.Method == http.MethodPost
	if !create {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		d.ID = id
	}
	d.Name = strings.TrimSpace(d.Name)
	if d.Name == "" || len([]rune(d.Name)) > 40 || d.Position < 0 || d.Position > 1000 {
		writeError(w, http.StatusBadRequest, "name must be 1-40 characters")
		return
	}
	c := store.Chat{ID: d.ID, Name: d.Name, Kind: d.Kind, UserID: d.UserID, TelegramChatID: d.TelegramChatID,
		Favorite: d.Favorite, Position: d.Position, Enabled: d.Enabled}
	switch d.Kind {
	case "user":
		// The destination is always an authorized user's own account.
		users, _ := s.app.Store.ListTelegramUsers(r.Context())
		found := false
		for _, u := range users {
			if u.ID == d.UserID && u.TelegramID != 0 {
				c.TelegramChatID, found = u.TelegramID, true
			}
		}
		if !found {
			writeError(w, http.StatusBadRequest, "choose an authorized user for this chat")
			return
		}
	case "group":
		c.UserID = 0
		if d.TelegramChatID == 0 {
			writeError(w, http.StatusBadRequest, "a Telegram group chat id is required")
			return
		}
	default:
		writeError(w, http.StatusBadRequest, "kind must be user or group")
		return
	}
	var err error
	action := "chat.updated"
	if create {
		action = "chat.created"
		c.ID, err = s.app.Store.CreateChat(r.Context(), c)
	} else {
		err = s.app.Store.UpdateChat(r.Context(), c)
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "no such chat")
	case err != nil:
		writeError(w, http.StatusConflict, "cannot save chat: the name must be unique")
	default:
		s.audit(r, action, c.Name, nil)
		saved, _ := s.app.Store.GetChat(r.Context(), c.ID)
		writeJSON(w, http.StatusOK, chatDTO{saved.ID, saved.Name, saved.Kind, saved.UserID, saved.TelegramChatID, saved.Favorite, saved.Position, saved.Enabled})
	}
}

func (s *server) deleteChat(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.app.Store.DeleteChat(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no such chat")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.audit(r, "chat.deleted", strconv.FormatInt(id, 10), nil)
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------- messages

// listMessages shows message metadata only. Parents see that a message exists,
// who it is from/to and its state, never its content.
func (s *server) listMessages(w http.ResponseWriter, r *http.Request) {
	list, err := s.app.Store.RecentMessages(r.Context(), 30)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	type row struct {
		ID        int64     `json:"id"`
		Direction string    `json:"direction"`
		Kind      string    `json:"kind"`
		Type      string    `json:"type"`
		Status    string    `json:"status"`
		Person    string    `json:"person"`
		CreatedAt time.Time `json:"created_at"`
		Attempts  int       `json:"attempts"`
		LastError string    `json:"last_error,omitempty"`
	}
	out := make([]row, 0, len(list))
	for _, m := range list {
		out = append(out, row{m.ID, m.Direction, m.Kind, m.Type, m.Status, m.Person, m.CreatedAt, m.Attempts, m.LastError})
	}
	writeJSON(w, http.StatusOK, out)
}

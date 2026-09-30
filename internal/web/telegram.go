package web

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/juancer/milo/internal/permissions"
	"github.com/juancer/milo/internal/store"
	"github.com/juancer/milo/internal/telegram"
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
		writeError(w, http.StatusBadRequest, "web.err.invalid_id")
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
		writeError(w, http.StatusBadRequest, "web.err.that_does_not_look_like_a_telegram_bot_token")
		return
	}
	if err := s.app.Vault.Put(r.Context(), telegram.SecretBotToken, []byte(tok)); err != nil {
		s.app.Log.Error("store telegram token", "error", err)
		writeError(w, http.StatusInternalServerError, "web.err.could_not_store_the_token")
		return
	}
	s.audit(r, "telegram.token_set", "", nil) // the value is never recorded
	s.restartTelegram(r.Context())
	s.telegramStatus(w, r)
}

func (s *server) deleteTelegramToken(w http.ResponseWriter, r *http.Request) {
	if err := s.app.Store.DeleteSecret(r.Context(), telegram.SecretBotToken); err != nil {
		writeError(w, http.StatusInternalServerError, "web.err.internal_error")
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
		writeError(w, http.StatusInternalServerError, "web.err.internal_error")
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
		writeError(w, http.StatusBadRequest, "web.err.role_must_be_parent_family_or_child")
		return
	}
	if body.TTLHours == 0 {
		body.TTLHours = 24
	}
	if body.TTLHours < 1 || body.TTLHours > 168 || len([]rune(body.Note)) > 60 {
		writeError(w, http.StatusBadRequest, "web.err.ttl_hours_must_be_1_168_and_note_at_most_60")
		return
	}
	token, hash, err := telegram.NewInvitationToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "web.err.internal_error")
		return
	}
	expires := time.Now().Add(time.Duration(body.TTLHours) * time.Hour)
	id, err := s.app.Store.CreateInvitation(r.Context(), hash, body.Role, strings.TrimSpace(body.Note), expires)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "web.err.internal_error")
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
		writeError(w, http.StatusNotFound, "web.err.no_such_active_invitation")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "web.err.internal_error")
		return
	}
	s.audit(r, "invitation.revoked", strconv.FormatInt(id, 10), nil)
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------- authorization requests

func (s *server) listAuthorizations(w http.ResponseWriter, r *http.Request) {
	list, err := s.app.Store.ListPending(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "web.err.internal_error")
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
		writeError(w, http.StatusBadRequest, "web.err.name_must_be_at_most_40_characters")
		return
	}
	user, err := s.app.Telegram.ApproveRequest(r.Context(), id, name, body.CreateChat)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "web.err.no_such_pending_request")
		return
	}
	if err != nil {
		s.app.Log.Error("approve authorization", "error", err)
		writeError(w, http.StatusConflict, "web.err.this_person_is_already_authorized")
		return
	}
	s.audit(r, "authorization.approved", strconv.FormatInt(user.TelegramID, 10), map[string]string{"role": user.Role})
	writeJSON(w, http.StatusOK, map[string]any{"user_id": user.ID})
}

func (s *server) rejectAuthorization(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	tg, err := s.app.Telegram.RejectRequest(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "web.err.no_such_pending_request")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "web.err.internal_error")
		return
	}
	s.audit(r, "authorization.rejected", strconv.FormatInt(tg, 10), nil)
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
	Language         string          `json:"language"` // "" follows the notification language
	// ForwardIncoming: this person gets a copy of messages others leave for the device (opt-in;
	// only people who can manage users may turn it on). DeviceAlerts is shown, changed from Telegram.
	ForwardIncoming bool `json:"forward_incoming"`
	DeviceAlerts    bool `json:"device_alerts"`
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
		TelegramUsername: u.TelegramUsername, Language: u.Language, ForwardIncoming: u.ForwardIncoming, DeviceAlerts: u.DeviceAlerts, Effective: map[string]bool{}, Overrides: over, RoleDefaults: []string{}}
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
		writeError(w, http.StatusInternalServerError, "web.err.internal_error")
		return
	}
	out := make([]userDTO, 0, len(list))
	for _, u := range list {
		d, err := s.userDTO(r.Context(), u)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "web.err.internal_error")
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
		Language    *string          `json:"language"`         // nil keeps the current one
		Forward     *bool            `json:"forward_incoming"` // nil keeps the current one
		Permissions map[string]*bool `json:"permissions"`      // null clears the override
	}
	if !decodeJSONLimit(w, r, &body, 8<<10) {
		return
	}
	name := strings.TrimSpace(body.DisplayName)
	if name == "" || len([]rune(name)) > 40 || !permissions.ValidRole(permissions.Role(body.Role)) {
		writeError(w, http.StatusBadRequest, "web.err.a_name_1_40_characters_and_a_valid_role_are")
		return
	}
	if body.Language != nil && *body.Language != "" && !validLanguage(*body.Language) {
		writeError(w, http.StatusBadRequest, "web.err.the_language_must_be_a_supported_language")
		return
	}
	for p := range body.Permissions {
		if !permissions.ValidPermission(permissions.Permission(p)) {
			writeError(w, http.StatusBadRequest, "web.err.unknown_permission", "name", strconv.Quote(p))
			return
		}
	}
	ctx := r.Context()
	before, err := s.app.Store.GetUser(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "web.err.no_such_user")
		return
	}
	// Like invitations, this cannot make anyone an OWNER.
	if body.Role == string(permissions.RoleOwner) && before.Role != body.Role {
		writeError(w, http.StatusBadRequest, "web.err.role_must_be_parent_family_or_child")
		return
	}
	if err := s.app.Store.UpdateUser(ctx, id, name, body.Role, body.Active); err != nil {
		writeError(w, http.StatusInternalServerError, "web.err.internal_error")
		return
	}
	if body.Language != nil {
		if err := s.app.Store.SetUserLanguage(ctx, id, *body.Language); err != nil {
			writeError(w, http.StatusInternalServerError, "web.err.internal_error")
			return
		}
	}
	if !body.Active {
		if err := s.app.Store.ExpirePendingFrom(ctx, id); err != nil {
			writeError(w, http.StatusInternalServerError, "web.err.internal_error")
			return
		}
	}
	for p, v := range body.Permissions {
		if v == nil {
			err = s.app.Store.ClearPermission(ctx, id, p)
		} else {
			err = s.app.Perms.Set(ctx, id, permissions.Permission(p), *v)
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "web.err.internal_error")
			return
		}
	}
	if body.Forward != nil && *body.Forward != before.ForwardIncoming {
		if *body.Forward {
			if ok, _ := s.app.Perms.Has(ctx, id, permissions.ManageUsers); !ok {
				writeError(w, http.StatusBadRequest, "web.err.message_copies_need_the_manage_users_permission")
				return
			}
		}
		if err := s.app.Store.SetUserForwardIncoming(ctx, id, *body.Forward); err != nil {
			writeError(w, http.StatusInternalServerError, "web.err.internal_error")
			return
		}
	}
	changes := map[string]string{}
	if body.Forward != nil && *body.Forward != before.ForwardIncoming {
		changes["forward_incoming"] = strconv.FormatBool(*body.Forward)
	}
	if before.Role != body.Role {
		changes["role"] = before.Role + "->" + body.Role
	}
	if before.Active != body.Active {
		changes["active"] = strconv.FormatBool(body.Active)
	}
	if body.Language != nil && *body.Language != before.Language {
		changes["language"] = *body.Language
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
		writeError(w, http.StatusInternalServerError, "web.err.internal_error")
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
		writeError(w, http.StatusBadRequest, "web.err.name_must_be_1_40_characters")
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
			writeError(w, http.StatusBadRequest, "web.err.choose_an_authorized_user_for_this_chat")
			return
		}
	case "group":
		c.UserID = 0
		if d.TelegramChatID == 0 {
			writeError(w, http.StatusBadRequest, "web.err.a_telegram_group_chat_id_is_required")
			return
		}
	default:
		writeError(w, http.StatusBadRequest, "web.err.kind_must_be_user_or_group")
		return
	}
	// send_message matches names without regard to case, so they must differ by more than case.
	existing, _ := s.app.Store.ListChats(r.Context())
	for _, o := range existing {
		if o.ID != c.ID && strings.ToLower(o.Name) == strings.ToLower(c.Name) {
			writeError(w, http.StatusConflict, "web.err.cannot_save_chat_the_name_must_be_unique")
			return
		}
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
		writeError(w, http.StatusNotFound, "web.err.no_such_chat")
	case err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed"):
		writeError(w, http.StatusConflict, "web.err.cannot_save_chat_the_name_must_be_unique")
	case err != nil:
		writeError(w, http.StatusInternalServerError, "web.err.internal_error")
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
		writeError(w, http.StatusNotFound, "web.err.no_such_chat")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "web.err.internal_error")
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
		writeError(w, http.StatusInternalServerError, "web.err.internal_error")
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

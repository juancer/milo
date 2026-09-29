package web

import (
	"errors"
	"net/http"
	"time"

	"github.com/juancer/nexo/internal/auth"
	"github.com/juancer/nexo/internal/store"
)

func (s *server) accountRoutes(mux *http.ServeMux) {
	mux.Handle("POST /api/account/password", s.requireAuth(s.changePassword))
	mux.Handle("GET /api/admins", s.requireAuth(s.listAdmins))
	mux.Handle("POST /api/admins", s.requireOwner(s.createAdmin))
	mux.Handle("POST /api/admins/{id}/reset", s.requireOwner(s.resetAdmin))
	mux.Handle("DELETE /api/admins/{id}", s.requireOwner(s.deleteAdmin))
}

// requireOwner restricts an endpoint to the owner account.
func (s *server) requireOwner(next http.HandlerFunc) http.Handler {
	return s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		if !adminFrom(r).IsOwner {
			writeError(w, http.StatusForbidden, "only the owner can manage administrators")
			return
		}
		next(w, r)
	})
}

// changePassword lets the signed-in administrator choose a new password. It
// signs out every other session of that account and starts a fresh one here.
func (s *server) changePassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Current string `json:"current_password"`
		New     string `json:"new_password"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	user := adminFrom(r)
	if !s.limiter.Allowed(clientIP(r)) {
		writeError(w, http.StatusTooManyRequests, "too many failed attempts, try again later")
		return
	}
	err := s.accounts.ChangePassword(r.Context(), user.ID, body.Current, body.New)
	switch {
	case errors.Is(err, auth.ErrBadCredentials):
		s.limiter.Fail(clientIP(r))
		writeError(w, http.StatusUnauthorized, "the current password is not correct")
		return
	case err != nil:
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.audit(r, "admin.password_changed", user.Username, nil)
	fresh, err := s.app.Store.GetAdmin(r.Context(), user.ID)
	if err != nil || s.startSession(w, fresh) != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "username": fresh.Username, "is_owner": fresh.IsOwner, "must_change_password": false})
}

type adminDTO struct {
	ID         int64      `json:"id"`
	Username   string     `json:"username"`
	IsOwner    bool       `json:"is_owner"`
	MustChange bool       `json:"must_change_password"`
	CreatedAt  time.Time  `json:"created_at"`
	LastLogin  *time.Time `json:"last_login,omitempty"`
	You        bool       `json:"you"`
}

func (s *server) listAdmins(w http.ResponseWriter, r *http.Request) {
	list, err := s.app.Store.ListAdmins(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	me := adminFrom(r)
	out := make([]adminDTO, 0, len(list))
	for _, a := range list {
		out = append(out, adminDTO{a.ID, a.Username, a.IsOwner, a.MustChange, a.CreatedAt, a.LastLogin, a.ID == me.ID})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) createAdmin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"` // optional: generated when empty
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	user, generated, err := s.accounts.Create(r.Context(), body.Username, body.Password)
	switch {
	case errors.Is(err, auth.ErrUsername), errors.Is(err, auth.ErrUsernameTaken):
		writeError(w, http.StatusBadRequest, err.Error())
		return
	case err != nil:
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.audit(r, "admin.created", user.Username, nil) // never the password
	// A generated password is shown exactly once, here.
	writeJSON(w, http.StatusCreated, map[string]any{"id": user.ID, "username": user.Username, "generated_password": generated})
}

func (s *server) resetAdmin(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	target, err := s.app.Store.GetAdmin(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no such administrator")
		return
	}
	pw, err := s.accounts.Reset(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.audit(r, "admin.password_reset", target.Username, nil)
	writeJSON(w, http.StatusOK, map[string]any{"username": target.Username, "generated_password": pw})
}

func (s *server) deleteAdmin(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	target, _ := s.app.Store.GetAdmin(r.Context(), id)
	err := s.accounts.Delete(r.Context(), adminFrom(r).ID, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "no such administrator")
	case errors.Is(err, auth.ErrSelf), errors.Is(err, auth.ErrLastOwner):
		writeError(w, http.StatusConflict, err.Error())
	case err != nil:
		writeError(w, http.StatusInternalServerError, "internal error")
	default:
		s.audit(r, "admin.removed", target.Username, nil)
		w.WriteHeader(http.StatusNoContent)
	}
}

package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"

	"github.com/juancer/milo/internal/actions"
	"github.com/juancer/milo/internal/store"
)

var (
	actionNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)
	eventTypes   = map[string]bool{"short_press": true, "long_press": true, "double_press": true}
)

type buttonActionDTO struct {
	Action     string         `json:"action"`
	Parameters map[string]any `json:"parameters"`
}

type buttonDTO struct {
	ID        int64                      `json:"id"`
	Name      string                     `json:"name"`
	Color     string                     `json:"color"`
	GPIO      int                        `json:"gpio"`
	ActiveLow bool                       `json:"active_low"`
	Enabled   bool                       `json:"enabled"`
	Actions   map[string]buttonActionDTO `json:"actions"`
	// Unknown lists mapped actions that no feature has registered (yet).
	Unknown []string `json:"unknown_actions,omitempty"`
}

func (s *server) buttonRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/buttons", s.requireAuth(s.listButtons))
	mux.Handle("POST /api/buttons", s.requireAuth(s.createButton))
	mux.Handle("PUT /api/buttons/{id}", s.requireAuth(s.updateButton))
	mux.Handle("DELETE /api/buttons/{id}", s.requireAuth(s.deleteButton))
	mux.Handle("GET /api/actions", s.requireAuth(s.listActions))
}

func (s *server) toDTO(b store.Button) buttonDTO {
	d := buttonDTO{ID: b.ID, Name: b.Name, Color: b.Color, GPIO: b.GPIO, ActiveLow: b.ActiveLow,
		Enabled: b.Enabled, Actions: map[string]buttonActionDTO{}}
	for event, a := range b.Actions {
		params := map[string]any{}
		json.Unmarshal([]byte(a.Parameters), &params) //nolint:errcheck // invalid JSON shows as empty
		d.Actions[event] = buttonActionDTO{Action: a.Action, Parameters: params}
		if !s.app.Actions.Has(a.Action) {
			d.Unknown = append(d.Unknown, a.Action)
		}
	}
	return d
}

func (d buttonDTO) toStore() (store.Button, string) {
	b := store.Button{ID: d.ID, Name: d.Name, Color: d.Color, GPIO: d.GPIO, ActiveLow: d.ActiveLow,
		Enabled: d.Enabled, Actions: map[string]store.ButtonAction{}}
	switch {
	case d.Name == "" || len([]rune(d.Name)) > 40:
		return b, "name must be 1-40 characters"
	case len([]rune(d.Color)) > 20:
		return b, "color must be at most 20 characters"
	case d.GPIO < 0 || d.GPIO > 27:
		return b, "gpio must be between 0 and 27 (BCM numbering)"
	}
	for event, a := range d.Actions {
		if !eventTypes[event] {
			return b, "unknown event type " + strconv.Quote(event)
		}
		if !actionNameRe.MatchString(a.Action) {
			return b, "invalid action name for " + event
		}
		params, err := json.Marshal(a.Parameters)
		if err != nil || len(params) > 1024 {
			return b, "parameters for " + event + " are invalid or too large"
		}
		if a.Parameters == nil {
			params = []byte("{}")
		}
		b.Actions[event] = store.ButtonAction{Action: a.Action, Parameters: string(params)}
	}
	return b, ""
}

func (s *server) listButtons(w http.ResponseWriter, r *http.Request) {
	list, err := s.app.Store.ListButtons(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]buttonDTO, 0, len(list))
	for _, b := range list {
		out = append(out, s.toDTO(b))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) saveButton(w http.ResponseWriter, r *http.Request, create bool) {
	var dto buttonDTO
	if !decodeJSONLimit(w, r, &dto, 16<<10) {
		return
	}
	if !create {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid id")
			return
		}
		dto.ID = id
	}
	b, problem := dto.toStore()
	if problem != "" {
		writeError(w, http.StatusBadRequest, problem)
		return
	}
	var err error
	action := "button.updated"
	if create {
		action = "button.created"
		b.ID, err = s.app.Store.CreateButton(r.Context(), b)
	} else {
		err = s.app.Store.UpdateButton(r.Context(), b)
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "no such button")
		return
	case err != nil:
		// Most likely a duplicate name or GPIO pin.
		writeError(w, http.StatusConflict, "cannot save button: name and GPIO pin must be unique")
		return
	}
	s.audit(r, action, b.Name, map[string]string{"gpio": strconv.Itoa(b.GPIO)})
	s.reloadButtons(r)
	saved, _ := s.app.Store.GetButton(r.Context(), b.ID)
	writeJSON(w, http.StatusOK, s.toDTO(saved))
}

func (s *server) createButton(w http.ResponseWriter, r *http.Request) { s.saveButton(w, r, true) }
func (s *server) updateButton(w http.ResponseWriter, r *http.Request) { s.saveButton(w, r, false) }

func (s *server) deleteButton(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.app.Store.DeleteButton(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no such button")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.audit(r, "button.deleted", strconv.FormatInt(id, 10), nil)
	s.reloadButtons(r)
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) reloadButtons(r *http.Request) {
	if s.app.Buttons == nil {
		return
	}
	if err := s.app.Buttons.Reload(r.Context()); err != nil {
		s.app.Log.Warn("reload buttons", "error", err)
	}
}

type actionDTO struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Feature     string         `json:"feature,omitempty"`
	Schema      map[string]any `json:"schema"`
}

// listActions returns the actions a button may trigger, for the mapping editor.
func (s *server) listActions(w http.ResponseWriter, _ *http.Request) {
	out := []actionDTO{}
	for _, a := range s.app.Actions.List() {
		if a.Exposure.Allows(actions.ActorDevice) {
			out = append(out, actionDTO{a.Name, a.Description, a.Feature, actions.JSONSchema(a.Params)})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

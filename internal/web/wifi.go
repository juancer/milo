package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/juancer/milo/internal/store"
	"github.com/juancer/milo/internal/wifi"
)

func (s *server) wifiRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/wifi", s.requireAuth(s.listWifi))
	mux.Handle("POST /api/wifi", s.requireAuth(s.saveWifi))
	mux.Handle("PUT /api/wifi/{id}", s.requireAuth(s.saveWifi))
	mux.Handle("DELETE /api/wifi/{id}", s.requireAuth(s.deleteWifi))
}

type wifiDTO struct {
	ID            int64      `json:"id"`
	SSID          string     `json:"ssid"`
	Password      string     `json:"password,omitempty"` // write-only
	Priority      int        `json:"priority"`
	Enabled       bool       `json:"enabled"`
	Open          bool       `json:"open"`
	LastConnected *time.Time `json:"last_connected,omitempty"`
}

func (s *server) listWifi(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	nets, err := s.app.Store.ListWifiNetworks(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "web.err.internal_error")
		return
	}
	st := s.app.Wifi.Status(ctx)
	if st.Connected {
		s.app.Store.MarkWifiConnected(ctx, st.SSID, time.Now()) //nolint:errcheck
		nets, _ = s.app.Store.ListWifiNetworks(ctx)
	}
	out := make([]wifiDTO, 0, len(nets))
	for _, n := range nets {
		out = append(out, wifiDTO{ID: n.ID, SSID: n.SSID, Priority: n.Priority, Enabled: n.Enabled, Open: n.SecretName == "", LastConnected: n.LastConnected})
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": st, "networks": out})
}

func (s *server) saveWifi(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var d wifiDTO
	if !decodeJSON(w, r, &d) {
		return
	}
	// Held through the sync at the end, so a concurrent request cannot apply a
	// half-saved row or re-add a profile that was just removed.
	s.app.WifiMu.Lock()
	defer s.app.WifiMu.Unlock()
	create := r.Method == http.MethodPost
	var existing store.WifiNetwork
	if !create {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		var err error
		existing, err = s.app.Store.GetWifiNetwork(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "web.err.no_such_network")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "web.err.internal_error")
			return
		}
	}
	if err := wifi.ValidateSSID(d.SSID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := wifi.ValidatePassphrase(d.Password); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if d.Priority < 0 || d.Priority > 100 {
		writeError(w, http.StatusBadRequest, "web.err.priority_must_be_0_100_higher_is_preferred")
		return
	}
	if create && d.Password == "" && !d.Open {
		// A new network without a password is treated as open only if the admin said so.
		writeError(w, http.StatusBadRequest, "web.err.enter_the_wi_fi_password_or_tick_open_networ")
		return
	}

	n := store.WifiNetwork{ID: existing.ID, SSID: d.SSID, SecretName: existing.SecretName, Priority: d.Priority, Enabled: d.Enabled}
	var err error
	if create {
		n.ID, err = s.app.Store.CreateWifiNetwork(ctx, n)
	} else {
		err = s.app.Store.UpdateWifiNetwork(ctx, n)
	}
	if err != nil {
		writeError(w, http.StatusConflict, "web.err.cannot_save_that_network_name_is_already_in")
		return
	}
	// A new row without its secret would be applied as an open network, so a
	// failure below removes it again.
	rollback := func() {
		if create {
			s.app.Store.DeleteWifiNetwork(ctx, n.ID) //nolint:errcheck
		}
	}
	switch {
	case d.Open && d.Password == "":
		if n.SecretName != "" {
			if err := s.app.Store.DeleteSecret(ctx, n.SecretName); err != nil {
				writeError(w, http.StatusInternalServerError, "web.err.could_not_remove_the_old_password")
				return
			}
			n.SecretName = ""
		}
	case d.Password != "":
		n.SecretName = "wifi." + strconv.FormatInt(n.ID, 10)
		if err := s.app.Vault.Put(ctx, n.SecretName, []byte(d.Password)); err != nil {
			rollback()
			writeError(w, http.StatusInternalServerError, "web.err.could_not_store_the_password")
			return
		}
	}
	if err := s.app.Store.UpdateWifiNetwork(ctx, n); err != nil {
		rollback()
		writeError(w, http.StatusInternalServerError, "web.err.internal_error")
		return
	}
	s.audit(r, map[bool]string{true: "wifi.added", false: "wifi.updated"}[create], d.SSID, nil) // never the password
	s.syncWifi(ctx, w)
}

func (s *server) deleteWifi(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	s.app.WifiMu.Lock()
	defer s.app.WifiMu.Unlock()
	n, err := s.app.Store.GetWifiNetwork(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "web.err.no_such_network")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "web.err.internal_error")
		return
	}
	// The password goes first: if the row cannot be removed afterwards the admin
	// can retry (deleting a missing secret is not an error), whereas the other
	// order would leave a stored password with no row pointing at it.
	if n.SecretName != "" {
		if err := s.app.Store.DeleteSecret(r.Context(), n.SecretName); err != nil {
			writeError(w, http.StatusInternalServerError, "web.err.could_not_remove_the_password")
			return
		}
	}
	if err := s.app.Store.DeleteWifiNetwork(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "web.err.internal_error")
		return
	}
	s.audit(r, "wifi.removed", n.SSID, nil)
	s.syncWifi(r.Context(), w)
}

// syncWifi applies the list to the OS and answers with the fresh state.
func (s *server) syncWifi(ctx context.Context, w http.ResponseWriter) {
	sctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := s.app.SyncWifiLocked(sctx); err != nil {
		s.app.Log.Warn("wifi sync", "error", err)
		writeJSON(w, http.StatusOK, map[string]any{"saved": true, "warning": fmt.Sprintf("Saved, but the device could not apply it: %s", strings.TrimPrefix(err.Error(), "nmcli "))})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"saved": true})
}

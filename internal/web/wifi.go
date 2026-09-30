package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/juancer/nexo/internal/store"
	"github.com/juancer/nexo/internal/wifi"
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
		writeError(w, http.StatusInternalServerError, "internal error")
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
			writeError(w, http.StatusNotFound, "no such network")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
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
		writeError(w, http.StatusBadRequest, "priority must be 0-100 (higher is preferred)")
		return
	}
	if create && d.Password == "" && !d.Open {
		// A new network without a password is treated as open only if the admin said so.
		writeError(w, http.StatusBadRequest, "enter the Wi-Fi password, or tick “open network”")
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
		writeError(w, http.StatusConflict, "cannot save: that network name is already in the list")
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
				writeError(w, http.StatusInternalServerError, "could not remove the old password")
				return
			}
			n.SecretName = ""
		}
	case d.Password != "":
		n.SecretName = "wifi." + strconv.FormatInt(n.ID, 10)
		if err := s.app.Vault.Put(ctx, n.SecretName, []byte(d.Password)); err != nil {
			rollback()
			writeError(w, http.StatusInternalServerError, "could not store the password")
			return
		}
	}
	if err := s.app.Store.UpdateWifiNetwork(ctx, n); err != nil {
		rollback()
		writeError(w, http.StatusInternalServerError, "internal error")
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
	n, err := s.app.Store.GetWifiNetwork(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no such network")
		return
	}
	if err := s.app.Store.DeleteWifiNetwork(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if n.SecretName != "" {
		s.app.Store.DeleteSecret(r.Context(), n.SecretName) //nolint:errcheck
	}
	s.audit(r, "wifi.removed", n.SSID, nil)
	s.syncWifi(r.Context(), w)
}

// syncWifi applies the list to the OS and answers with the fresh state.
func (s *server) syncWifi(ctx context.Context, w http.ResponseWriter) {
	sctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := s.app.SyncWifi(sctx); err != nil {
		s.app.Log.Warn("wifi sync", "error", err)
		writeJSON(w, http.StatusOK, map[string]any{"saved": true, "warning": fmt.Sprintf("Saved, but the device could not apply it: %s", strings.TrimPrefix(err.Error(), "nmcli "))})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"saved": true})
}

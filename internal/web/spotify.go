package web

import (
	"errors"
	"net/http"

	"github.com/juancer/milo/internal/skills"
)

func (s *server) spotifyRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/spotify", s.requireAuth(s.getSpotify))
	// The binary path is what Milo runs, so only the owner may change it.
	mux.Handle("PUT /api/spotify", s.requireOwner(s.putSpotify))
	mux.Handle("POST /api/spotify/signout", s.requireAuth(s.signOutSpotify))
	// The Client ID and Secret are credentials, so only the owner may set or remove them.
	mux.Handle("PUT /api/spotify/search", s.requireOwner(s.putSpotifySearch))
	mux.Handle("DELETE /api/spotify/search", s.requireOwner(s.deleteSpotifySearch))
	// What the family listens to most, kept on the device: shown to every admin, forgotten by the owner.
	mux.Handle("GET /api/spotify/listening", s.requireAuth(s.getSpotifyListening))
	mux.Handle("DELETE /api/spotify/listening", s.requireOwner(s.deleteSpotifyListening))
}

type listenRow struct {
	Name  string `json:"name"`
	Plays int    `json:"plays"`
}

func (s *server) getSpotifyListening(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	artists, err1 := s.app.Store.TopArtists(ctx, 10, 1)
	contexts, err2 := s.app.Store.TopContexts(ctx, 10, 1)
	if err1 != nil || err2 != nil {
		writeError(w, http.StatusInternalServerError, "web.err.internal_error")
		return
	}
	out := map[string][]listenRow{"artists": {}, "contexts": {}}
	for _, a := range artists {
		out["artists"] = append(out["artists"], listenRow{a.Name, a.Plays})
	}
	for _, c := range contexts {
		out["contexts"] = append(out["contexts"], listenRow{c.Name, c.Plays})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) deleteSpotifyListening(w http.ResponseWriter, r *http.Request) {
	if err := s.app.Store.ClearListening(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "web.err.internal_error")
		return
	}
	s.audit(r, "spotify.listening_cleared", "", nil)
	w.WriteHeader(http.StatusNoContent)
}

// putSpotifySearch stores the credentials of a Spotify developer app, after Spotify accepts them.
func (s *server) putSpotifySearch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := s.app.Spotify.SetSearchCredentials(r.Context(), body.ClientID, body.ClientSecret); err != nil {
		if errors.Is(err, skills.ErrSpotifyCredentials) {
			writeError(w, http.StatusUnprocessableEntity, "web.err.spotify_search_credentials_refused")
			return
		}
		writeError(w, http.StatusBadGateway, "web.err.spotify_search_unreachable")
		return
	}
	s.audit(r, "spotify.search_credentials_set", "", nil) // never the values
	writeJSON(w, http.StatusOK, s.app.Spotify.Info(r.Context()))
}

func (s *server) deleteSpotifySearch(w http.ResponseWriter, r *http.Request) {
	if err := s.app.Spotify.ClearSearchCredentials(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "web.err.internal_error")
		return
	}
	s.audit(r, "spotify.search_credentials_removed", "", nil)
	writeJSON(w, http.StatusOK, s.app.Spotify.Info(r.Context()))
}

func (s *server) getSpotify(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.app.Spotify.Info(r.Context()))
}

func (s *server) putSpotify(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Binary       *string `json:"binary"`
		VoiceControl *bool   `json:"voice_control"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	ctx := r.Context()
	if body.Binary != nil {
		if *body.Binary != "" && !dirRe.MatchString(*body.Binary) {
			writeError(w, http.StatusBadRequest, "web.err.spotify_binary_path")
			return
		}
		if err := s.app.Store.SetSetting(ctx, skills.SettingSpotifyBinary, *body.Binary); err != nil {
			writeError(w, http.StatusInternalServerError, "web.err.internal_error")
			return
		}
	}
	if body.VoiceControl != nil {
		v := "0"
		if *body.VoiceControl {
			v = "1"
		}
		if err := s.app.Store.SetSetting(ctx, skills.SettingSpotifyVoice, v); err != nil {
			writeError(w, http.StatusInternalServerError, "web.err.internal_error")
			return
		}
	}
	s.audit(r, "spotify.settings_updated", "", nil)
	if body.Binary != nil {
		if on, err := s.app.Features.Enabled(ctx, "spotify"); err == nil && on {
			s.restartFeature(ctx, "spotify")
		}
	}
	writeJSON(w, http.StatusOK, s.app.Spotify.Info(ctx))
}

// signOutSpotify forgets the paired Spotify account; the receiver then shows a new pairing code.
func (s *server) signOutSpotify(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := s.app.Spotify.SignOut(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "web.err.internal_error")
		return
	}
	s.audit(r, "spotify.signed_out", "", nil)
	if on, err := s.app.Features.Enabled(ctx, "spotify"); err == nil && on {
		s.restartFeature(ctx, "spotify")
	}
	writeJSON(w, http.StatusOK, s.app.Spotify.Info(ctx))
}

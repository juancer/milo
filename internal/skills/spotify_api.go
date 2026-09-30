package skills

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// spotifyClient talks to the local REST API of go-librespot (github.com/devgianlu/go-librespot),
// which listens on 127.0.0.1 only. It is the receiver's own API: no Spotify account, developer
// app or Spotify Web API is involved.
type spotifyClient struct {
	base string
	http *http.Client
}

func newSpotifyClient(base string) *spotifyClient {
	return &spotifyClient{base: strings.TrimRight(base, "/"), http: &http.Client{Timeout: 5 * time.Second}}
}

// errSpotifyNoSession is returned when go-librespot answers 204: it is running but nobody has
// signed in yet (or the session ended).
var errSpotifyNoSession = errors.New("spotify: not signed in")

// SpotifyStatus is the subset of GET /status that Milo uses.
type SpotifyStatus struct {
	Username   string `json:"username"`
	DeviceName string `json:"device_name"`
	Stopped    bool   `json:"stopped"`
	Paused     bool   `json:"paused"`
	Buffering  bool   `json:"buffering"`
	Track      *struct {
		Name        string   `json:"name"`
		ArtistNames []string `json:"artist_names"`
	} `json:"track"`
}

// playing reports whether audio is (about to be) coming out of the receiver.
func (s SpotifyStatus) playing() bool { return !s.Stopped && !s.Paused }

// SpotifyPairing is a pending device_auth sign-in: the person opens URL on any device and types Code.
type SpotifyPairing struct {
	URL       string    `json:"url"`
	Code      string    `json:"code"`
	ExpiresAt time.Time `json:"expires_at"`
}

// spotifyPlaylist is one entry of GET /library/playlists.
type spotifyPlaylist struct {
	URI  string `json:"uri"`
	Name string `json:"name"`
}

func (c *spotifyClient) do(ctx context.Context, method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNoContent && out != nil:
		return errSpotifyNoSession
	case resp.StatusCode/100 != 2:
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10)) //nolint:errcheck
		return fmt.Errorf("spotify: %s %s answered %d", method, path, resp.StatusCode)
	case out == nil:
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10)) //nolint:errcheck
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

func (c *spotifyClient) Status(ctx context.Context) (SpotifyStatus, error) {
	var s SpotifyStatus
	err := c.do(ctx, http.MethodGet, "/status", nil, &s)
	return s, err
}

// AuthCode returns the pending pairing code, or errSpotifyNoSession when no sign-in is waiting.
func (c *spotifyClient) AuthCode(ctx context.Context) (SpotifyPairing, error) {
	var p SpotifyPairing
	err := c.do(ctx, http.MethodGet, "/auth/code", nil, &p)
	return p, err
}

func (c *spotifyClient) Pause(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/player/pause", nil, nil)
}
func (c *spotifyClient) Resume(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/player/resume", nil, nil)
}
func (c *spotifyClient) Next(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/player/next", map[string]any{}, nil)
}
func (c *spotifyClient) Prev(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/player/prev", nil, nil)
}
func (c *spotifyClient) Play(ctx context.Context, uri string) error {
	return c.do(ctx, http.MethodPost, "/player/play", map[string]any{"uri": uri}, nil)
}

// Playlists lists the signed-in account's playlists (the first 500 are enough for a family).
func (c *spotifyClient) Playlists(ctx context.Context) ([]spotifyPlaylist, error) {
	var out struct {
		Items []spotifyPlaylist `json:"items"`
	}
	if err := c.do(ctx, http.MethodGet, "/library/playlists?limit=500", nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

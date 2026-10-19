package skills

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/juancer/milo/internal/actions"
)

// What is heard is remembered (see store.ListenItem): an artist or a playlist counts as one listen when
// a song has been playing for listenAfter. That memory serves "pon lo que más escucho", gives "pon algo
// de música" something of your own to draw on, and lets a name asked for by voice be matched without
// searching Spotify at all, which matters because the search is rate-limited.

const listenAfter = 30 * time.Second

// listenTracker follows the song that is playing and reports it once it has been heard for a while.
type listenTracker struct {
	uri     string
	heard   time.Duration
	last    time.Time
	counted bool
}

// observe is called with every status answer. It returns the song to count, or nil.
func (t *listenTracker) observe(st SpotifyStatus, playing bool, now time.Time) (counted bool) {
	if st.Track == nil || st.Track.URI == "" || !playing {
		t.last = time.Time{}
		return false
	}
	if st.Track.URI != t.uri {
		*t = listenTracker{uri: st.Track.URI}
	}
	if !t.last.IsZero() {
		t.heard += min(now.Sub(t.last), 5*time.Second) // a stalled poll must not count as listening
	}
	t.last = now
	if !t.counted && t.heard >= listenAfter {
		t.counted = true
		return true
	}
	return false
}

// recordListen stores one listen: the main artist of the song and the context it comes from.
func (s *Spotify) recordListen(st SpotifyStatus) {
	if s.Store == nil || st.Track == nil {
		return
	}
	track, ctxURI, ctxName := *st.Track, st.ContextURI, st.ContextName
	go func() {
		bg, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		now := time.Now()
		if len(track.ArtistURIs) > 0 && len(track.ArtistNames) > 0 && strings.HasPrefix(track.ArtistURIs[0], "spotify:artist:") {
			if err := s.Store.CountArtistPlay(bg, track.ArtistURIs[0], track.ArtistNames[0], now); err != nil {
				s.Log.Warn("spotify: cannot remember an artist", "error", err)
			}
		}
		if strings.HasPrefix(ctxURI, "spotify:playlist:") || strings.HasPrefix(ctxURI, "spotify:album:") || strings.HasPrefix(ctxURI, "spotify:artist:") {
			name := ctxName
			if name == "" {
				name = track.AlbumName
			}
			if err := s.Store.CountContextPlay(bg, ctxURI, name, now); err != nil {
				s.Log.Warn("spotify: cannot remember a playlist", "error", err)
			}
		}
	}()
}

// knownArtist finds, among the artists remembered, the one a spoken name refers to (the most
// listened one when several fit).
func (s *Spotify) knownArtist(ctx context.Context, query string) (name, uri string, ok bool) {
	if s.Store == nil {
		return "", "", false
	}
	artists, err := s.Store.KnownArtists(ctx, 1000)
	if err != nil {
		return "", "", false
	}
	for _, a := range artists { // most listened first
		if artistMatches(query, a.Name) {
			return a.Name, a.URI, true
		}
	}
	return "", "", false
}

// favorite is something that can be played again: an artist, or a playlist or album.
type favorite struct {
	name, uri string
	plays     int
	artist    bool
}

// favorites returns what is listened to most, artists and contexts together, most first.
func (s *Spotify) favorites(ctx context.Context) []favorite {
	if s.Store == nil {
		return nil
	}
	var out []favorite
	if artists, err := s.Store.TopArtists(ctx, 12, 2); err == nil {
		for _, a := range artists {
			out = append(out, favorite{a.Name, a.URI, a.Plays, true})
		}
	}
	if contexts, err := s.Store.TopContexts(ctx, 12, 2); err == nil {
		for _, c := range contexts {
			out = append(out, favorite{c.Name, c.URI, c.Plays, false})
		}
	}
	return out
}

// pickFavorite chooses one at random, in proportion to how often it was listened to.
func pickFavorite(favs []favorite) favorite {
	total := 0
	for _, f := range favs {
		total += f.plays
	}
	n := pickRandom(total)
	for _, f := range favs {
		if n < f.plays {
			return f
		}
		n -= f.plays
	}
	return favs[0]
}

// PlayFavorites starts something from what is listened to most.
func (s *Spotify) PlayFavorites(ctx context.Context, actor actions.Actor) (actions.Result, bool, error) {
	if _, ok := s.preferred(ctx); !ok {
		return actions.Result{}, false, nil
	}
	if msg := s.blocked(ctx, actor); msg != "" {
		return actions.Result{Message: msg, Refused: true, Data: map[string]any{"reason": "voice_control_off"}}, true, nil
	}
	c, err := s.api()
	if err != nil {
		return actions.Result{Message: err.Error(), Refused: true, Data: map[string]any{"reason": "not_connected"}}, true, nil
	}
	favs := s.favorites(ctx)
	if len(favs) == 0 {
		return actions.Result{Message: "I do not know yet what you listen to most.", Refused: true, Data: map[string]any{"reason": "no_history"}}, true, nil
	}
	f := pickFavorite(favs)
	if s.OnPlay != nil {
		s.OnPlay()
	}
	c.ShuffleContext(ctx, true) //nolint:errcheck // playing matters more than the order
	if err := c.Play(ctx, f.uri); err != nil {
		return actions.Result{}, true, err
	}
	return actions.Result{Message: "Playing " + f.name + ".", Data: map[string]any{"favorite": f.name}}, true, nil
}

// ---------------------------------------------------------------- searching with patience

// searches are the artist searches waiting for Spotify to allow another try, one per spoken name.
type searches struct {
	mu   sync.Mutex
	jobs map[string]context.CancelFunc
}

// CancelSearches stops every search that is waiting ("para", "apaga").
func (s *Spotify) CancelSearches() {
	s.searches.mu.Lock()
	jobs := s.searches.jobs
	s.searches.jobs = nil
	s.searches.mu.Unlock()
	for _, cancel := range jobs {
		cancel()
	}
}

const (
	searchTries  = 3
	searchGiveUp = 3 * time.Minute
)

// searchMinWait and searchExtra pad Spotify's Retry-After (tests shorten them).
var (
	searchMinWait = 2 * time.Second
	searchExtra   = time.Second
)

// searchLater looks for an artist again after Spotify's wait, up to searchTries times, and plays them
// when found. It reports false when a search for the same name is already waiting.
func (s *Spotify) searchLater(c *spotifyClient, query string, wait time.Duration) (started bool) {
	key := fold(strings.Join(words(query), " "))
	ctx, cancel := context.WithTimeout(context.Background(), searchGiveUp)
	s.searches.mu.Lock()
	if _, busy := s.searches.jobs[key]; busy {
		s.searches.mu.Unlock()
		cancel()
		return false
	}
	if s.searches.jobs == nil {
		s.searches.jobs = map[string]context.CancelFunc{}
	}
	s.searches.jobs[key] = cancel
	s.searches.mu.Unlock()
	go func() {
		defer func() {
			cancel()
			s.searches.mu.Lock()
			delete(s.searches.jobs, key)
			s.searches.mu.Unlock()
		}()
		for try := 0; try < searchTries; try++ {
			if wait < searchMinWait {
				wait = searchMinWait
			}
			if !sleepCtx(ctx, wait+searchExtra) {
				return
			}
			name, uri, err := s.searchOnce(ctx, c, query)
			var busy busyError
			switch {
			case err == nil:
				if s.OnPlay != nil {
					s.OnPlay()
				}
				c.ShuffleContext(ctx, false) //nolint:errcheck
				if err := c.Play(ctx, uri); err != nil {
					s.Log.Warn("spotify: found the artist but could not play them", "artist", name, "error", err)
				}
				return
			case errors.As(err, &busy):
				wait = busy.after
			default:
				s.tellLater(ctx, fmt.Sprintf("No he encontrado a %s en Spotify.", query))
				return
			}
		}
		s.tellLater(ctx, fmt.Sprintf("No he podido buscar a %s en Spotify.", query))
	}()
	return true
}

func (s *Spotify) tellLater(ctx context.Context, text string) {
	if s.Announce != nil && ctx.Err() == nil {
		s.Announce.Announce(ctx, text, false)
	}
}

// searchOnce looks the artist up on Spotify and remembers what it finds.
func (s *Spotify) searchOnce(ctx context.Context, c *spotifyClient, query string) (name, uri string, err error) {
	base := s.WebAPIBase
	if base == "" {
		base = "https://api.spotify.com"
	}
	sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	token, err := s.searchToken(sctx, c)
	if err != nil {
		return "", "", err
	}
	name, uri, err = c.searchArtist(sctx, base, token, strings.Join(words(query), " "))
	if err != nil {
		return "", "", err
	}
	if !artistMatches(query, name) {
		return "", "", errSpotifyNoArtist
	}
	if s.Store != nil {
		s.Store.RememberArtist(ctx, uri, name) //nolint:errcheck // the artist plays either way
	}
	return name, uri, nil
}

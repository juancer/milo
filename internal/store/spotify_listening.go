package store

import (
	"context"
	"fmt"
	"time"
)

// ListenItem is an artist or a playlist/album the family has listened to.
type ListenItem struct {
	URI        string
	Name       string
	Plays      int
	LastPlayed *time.Time
}

const maxRememberedArtists = 2000

// RememberArtist stores an artist found by searching, without counting a listen, so the next
// request for them needs no search.
func (s *Store) RememberArtist(ctx context.Context, uri, name string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO spotify_artists (uri, name) VALUES (?, ?)
		ON CONFLICT(uri) DO UPDATE SET name = excluded.name`, uri, name)
	if err != nil {
		return fmt.Errorf("store: remember artist: %w", err)
	}
	// Artists that were only searched for and never heard do not pile up for ever.
	_, err = s.db.ExecContext(ctx, `DELETE FROM spotify_artists WHERE uri IN (
		SELECT uri FROM spotify_artists WHERE plays = 0 ORDER BY rowid DESC LIMIT -1 OFFSET ?)`, maxRememberedArtists)
	return err
}

// CountArtistPlay records that a song of this artist was heard for a while.
func (s *Store) CountArtistPlay(ctx context.Context, uri, name string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO spotify_artists (uri, name, plays, last_played) VALUES (?, ?, 1, ?)
		ON CONFLICT(uri) DO UPDATE SET plays = plays + 1, name = excluded.name, last_played = excluded.last_played`, uri, name, ts(at))
	if err != nil {
		return fmt.Errorf("store: count artist play: %w", err)
	}
	return nil
}

// CountContextPlay records that a playlist, album or artist page was played for a while.
func (s *Store) CountContextPlay(ctx context.Context, uri, name string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO spotify_contexts (uri, name, plays, last_played) VALUES (?, ?, 1, ?)
		ON CONFLICT(uri) DO UPDATE SET plays = plays + 1, name = CASE WHEN excluded.name <> '' THEN excluded.name ELSE name END, last_played = excluded.last_played`, uri, name, ts(at))
	if err != nil {
		return fmt.Errorf("store: count context play: %w", err)
	}
	return nil
}

func (s *Store) listen(ctx context.Context, table string, limit int, minPlays int) ([]ListenItem, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT uri, name, plays, last_played FROM `+table+` WHERE plays >= ? ORDER BY plays DESC, last_played DESC LIMIT ?`, minPlays, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list %s: %w", table, err)
	}
	defer rows.Close()
	var out []ListenItem
	for rows.Next() {
		var it ListenItem
		var last *string
		if err := rows.Scan(&it.URI, &it.Name, &it.Plays, &last); err != nil {
			return nil, err
		}
		if last != nil {
			t := parseTime(*last)
			it.LastPlayed = &t
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// TopArtists returns the most listened artists (at least minPlays listens), most first.
func (s *Store) TopArtists(ctx context.Context, limit, minPlays int) ([]ListenItem, error) {
	return s.listen(ctx, "spotify_artists", limit, minPlays)
}

// TopContexts returns the most listened playlists and albums, most first.
func (s *Store) TopContexts(ctx context.Context, limit, minPlays int) ([]ListenItem, error) {
	return s.listen(ctx, "spotify_contexts", limit, minPlays)
}

// KnownArtists returns every remembered artist (heard or searched for), most listened first, so a name
// asked for by voice can be matched against them.
func (s *Store) KnownArtists(ctx context.Context, limit int) ([]ListenItem, error) {
	return s.listen(ctx, "spotify_artists", limit, 0)
}

// ClearListening forgets what was listened to and the artists remembered.
func (s *Store) ClearListening(ctx context.Context) error {
	for _, t := range []string{"spotify_artists", "spotify_contexts"} {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM `+t); err != nil {
			return fmt.Errorf("store: clear listening: %w", err)
		}
	}
	return nil
}

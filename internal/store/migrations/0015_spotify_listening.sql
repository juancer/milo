-- What the family listens to on Spotify, kept on the device: the artists and playlists/albums that were
-- heard for a while, with how many times. "Pon lo que más escucho" and "pon algo de música" draw on it, and
-- an artist found by searching is remembered so it is never searched for twice. Nothing leaves the device.
CREATE TABLE spotify_artists (
    uri         TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    plays       INTEGER NOT NULL DEFAULT 0,
    last_played TEXT
);
CREATE INDEX spotify_artists_plays ON spotify_artists (plays DESC);

CREATE TABLE spotify_contexts (
    uri         TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    plays       INTEGER NOT NULL DEFAULT 0,
    last_played TEXT
);
CREATE INDEX spotify_contexts_plays ON spotify_contexts (plays DESC);

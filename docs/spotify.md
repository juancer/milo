# Spotify

Milo can be a **Spotify Connect speaker**: it shows up in the Spotify app as a device called
like your Milo (the device name), and the music plays through Milo's speaker. It is optional,
off by default, and works only with **Spotify Premium** (Spotify's own rule).

Milo does this by running [go-librespot](https://github.com/devgianlu/go-librespot), a separate
program. It is GPL-3.0 software, so Milo does not embed it: `milo install` downloads a pinned
upstream release, checks its SHA-256 and puts it at `/usr/local/bin/go-librespot`. It talks to no
Spotify developer API and needs no developer app.

## Read this first: the risks

- **go-librespot is an unofficial Spotify client.** Spotify tolerates such clients but does not
  endorse them and can break them at any time. A newly created account may hit audio errors
  that an older one does not. There is no promise it keeps working.
- **Spotify's developer policy restricts voice control of Spotify.** Milo's voice verbs are
  therefore **off by default** (see [Voice](#voice-off-by-default)) and are used at your own risk.
- Spotify's policy also says not to build products aimed at children and not to feed Spotify
  content to an AI model. Milo sends Spotify content to no AI model except the **name of a
  playlist** when someone asks for it, but you should judge whether this fits your use.

## go-librespot on the Pi

`sudo milo install` (and every automatic update) installs go-librespot if it is missing. It needs
Internet once. If the download fails, the install still succeeds and prints a warning; run
`sudo milo install` again later. A copy you placed yourself is never replaced. Another path works:
set it in the Spotify sheet.

Then turn the **Spotify** switch on (Home → Spotify). Until go-librespot is present the tile
says *Needs setup*.

Milo starts go-librespot as a child of its own service: it runs as the same unprivileged user,
inside the same systemd sandbox, with a clean environment. You do not write a systemd unit for
it.

## Pair your account

Open **Home → Spotify**. Milo shows a pairing **code** and the address `spotify.com/pair`.
On any phone or computer open that address, sign in to the Spotify account that has Premium
and type the code. Milo never sees your password. The sheet then shows *Signed in as …*.

In the Spotify app on a phone, tap the devices button and pick Milo. The device only appears for
the account that paired it. To use a different account, **Sign out of Spotify** in the sheet (it
deletes the saved sign-in) and pair again.

## How it behaves

- Milo keeps the sound card: go-librespot writes the audio to a private named pipe
  (`<data dir>/spotify/audio.pipe`) and Milo plays it like local music.
- **Notifications, spoken answers and conversations come first.** Spotify is *paused* (not mixed
  underneath) and carries on from the same spot once the device has been quiet for a moment.
  If you paused it from the phone, it stays paused.
- Starting a local song pauses Spotify, and starting Spotify stops the local music.
- **Volume** is Milo's volume (the Sound sheet, buttons, or "volume 7"): the Spotify app's volume
  slider does not change how loud Milo is.
- "Stop" and "pause" always work, by voice, buttons or Telegram (`stop_audio`, `pause_music`,
  `spotify_pause`), and usage schedules do not block them.

## Voice (off by default)

In **Home → Spotify → Settings**, *Let the assistant control Spotify by voice* enables these
assistant actions: pause (always available), resume, next, previous and **play a playlist by
name**. "Play my bedtime playlist" picks the playlist of the paired account whose name best
matches the words. **Searching for a single song is not possible.** Physical buttons are not voice control and always work.

The audio source is chosen by what is playing: "pause", "stop" and "next song" act on local music
when it plays and on Spotify otherwise.

## "Play some music" means Spotify

When Spotify is connected and running, a request to play music (by voice, through the assistant) plays
Spotify, not the local music folder:

- **"Pon algo de música"** (nothing in particular) starts something **new**, shuffled and aimed at your
  taste: one of the playlists Spotify makes for you from what you play ("Daily Mix", "Descubrimiento semanal",
  "Radar de novedades", "On Repeat"...) if the account has any, otherwise one of your playlists at random. It
  never just carries on what was playing before.
- **"Sigue" / "continúa"** carries on what was paused.
- **"Pon la playlist de dormir"** plays the playlist whose name matches the words.
- **"Pon música de Belinda"**, **"pon Camilo"** play that artist (see below).

It needs **voice control** switched on, and says so when it is off. Set `music.source` to `local` to keep
it on the local folder. When Spotify is not connected, the local music plays; with an empty folder, Milo says
there is no music yet instead of pretending.

## Commands that skip the model, and artists

Plain commands are recognised by rules and carried out at once, without a trip to the language model:
"pon algo de música", "play something", "pon música de Belinda", "pon la playlist de dormir", "pausa",
"para", "siguiente", "sube el volumen", "volumen 7" (Spanish and English). They run as the assistant,
so Spotify voice control, the switches and the schedules apply as for the model. A command the rules do
not recognise, or that is refused ("Spotify is not connected"), goes to the model as usual, which
explains it in the child's language.

For "música de <artist>" and a bare name ("pon Camilo", only for names that cannot be anything else: "pon la
mesa" is not one) Milo first looks for a playlist of yours with those words, then searches Spotify for the
artist and plays it, but only if what Spotify found really is that artist (Spotify always returns something).
Without setup the search uses the receiver's own access, which Spotify rate-limits for everyone who uses it
(HTTP 429): it often works and sometimes fails, and Milo then says it could not look the artist up instead of
playing something else. For a reliable search, give Milo the **Client ID and Client secret of a free Spotify
developer app** (the Spotify sheet has the steps): the app gets its own limit and can only search the public
catalogue. Both values are stored encrypted, and Spotify checks them when you save.

## What is stored and sent

- go-librespot keeps its sign-in (`state.json`) in `<data dir>/spotify`, mode 0700. **It is not
  part of Milo's backups** (a backup holds only the database), so after a restore you pair again.
- Its control interface listens on `127.0.0.1` only, on a random port, with no password. Other
  programs on the Pi running as the same user could use it.
- Nothing Spotify-related goes to the assistant's AI except playlist names when asked for.
- Audit entries: `spotify.settings_updated`, `spotify.signed_out`.

## Troubleshooting

| Symptom | Try |
|---|---|
| Tile says *Install go-librespot* | The file is missing or not executable at the path shown in the sheet. Run `sudo milo install`. |
| No pairing code | Wait a few seconds; check `journalctl -u milo` (the receiver's output is at debug level: `MILO_LOG_LEVEL=debug`). |
| Milo does not appear in the Spotify app | Only the paired account sees it; both must be online. Try signing out and pairing again. |
| Errors like "audio key" | A known go-librespot/librespot problem with some accounts; update go-librespot. |
| Music cuts after every notification | Expected: it is paused for the sound and resumes when the device is quiet. |

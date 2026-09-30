# Spotify

Milo can be a **Spotify Connect speaker**: it shows up in the Spotify app as a device called
like your Milo (the device name), and the music plays through Milo's speaker. It is optional,
off by default, and works only with **Spotify Premium** (Spotify's own rule).

Milo does this by running [go-librespot](https://github.com/devgianlu/go-librespot), a separate
program you install yourself. Milo does not ship it (it is GPL-3.0 software), and it talks to no
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

## Install go-librespot on the Pi

1. Download the latest `go-librespot_linux_arm64.tar.gz` (64-bit Raspberry Pi OS; the
   `armv6` build is for 32-bit) from
   <https://github.com/devgianlu/go-librespot/releases>.
2. Unpack it and copy the `go-librespot` file to `/usr/local/bin/`, then
   `sudo chmod 755 /usr/local/bin/go-librespot`. Another path works: set it in the Spotify sheet.
3. Turn the **Spotify** switch on (Home → Spotify). Until step 2 is done the tile says *Needs
   setup*.

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
matches the words. **Searching for a song or artist is not possible** (that needs Spotify's
Web API, which Milo does not use). Physical buttons are not voice control and always work.

The audio source is chosen by what is playing: "pause", "stop" and "next song" act on local music
when it plays and on Spotify otherwise.

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
| Tile says *Install go-librespot* | The file is missing or not executable at the path shown in the sheet. |
| No pairing code | Wait a few seconds; check `journalctl -u milo` (the receiver's output is at debug level: `MILO_LOG_LEVEL=debug`). |
| Milo does not appear in the Spotify app | Only the paired account sees it; both must be online. Try signing out and pairing again. |
| Errors like "audio key" | A known go-librespot/librespot problem with some accounts; update go-librespot. |
| Music cuts after every notification | Expected: it is paused for the sound and resumes when the device is quiet. |

# Skills: timers, reminders, music, weather, calendar

Each skill is a feature that is **off until a parent turns it on** (Dashboard). The child (by
voice, through the assistant) and the buttons use them through the same registered actions as
everything else, so feature switches and usage schedules apply. Timer and reminder announcements
are spoken when text-to-speech is set up; otherwise the child hears an alarm or message tone.

| Skill | Actions | Notes |
|---|---|---|
| Timer | `set_timer`, `cancel_timer`, `list_timers` | Up to 10 at once; survive a reboot. A timer that ran out more than 10 minutes ago while the device was off is dropped instead of ringing late. |
| Reminders | `create_reminder`, `list_reminders`, `cancel_reminder` | "in 10 minutes" or "at 17:30" (next time the clock shows it). Up to 50 pending at once. Parents can add reminders in the admin too. Text is checked against the blocked phrases. |
| Music | `play_music`, `pause_music`, `stop_music`, `next_song` | Plays `.mp3` and 16-bit `.wav` files from a folder. |
| Weather | `get_weather` | Free Open-Meteo service; only your town's coordinates are sent. |
| Calendar | `get_calendar` | Read-only, through the calendar's secret iCalendar (`.ics`) address. |

## Music

Copy files into the music folder (default `/var/lib/milo/music`, changeable under Skills → Music;
the service can only write below `/var/lib/milo` unless you add a `ReadWritePaths=` drop-in for a
USB stick). Sub-folders are scanned (four levels); symbolic links and hidden files are ignored,
so a request can never reach a file outside the folder.

"Play the Frozen song" plays the file whose path best matches the words (filler such as *play*
and *song* is ignored). With no name it plays a surprise. Afterwards the rest of the folder plays
in shuffled order until it is stopped. Saying *stop* (or the stop action) ends it for good.

Music gives way to anything more important: a notification, a spoken answer or a conversation
pauses it, and it carries on from the same spot once the device has been quiet for a moment.
Starting music through the assistant ends that conversation so the microphone is not left open
over the song. Pausing keeps the position; playing again with no name resumes.

## Weather

Skills → Weather: type your town. Milo looks it up once and remembers the coordinates. Units
can be Celsius or Fahrenheit. Nothing about the family is sent, only latitude and longitude.

## Calendar

Skills → Calendar: paste the secret iCalendar address (Google Calendar: *Settings → your
calendar → Secret address in iCal format*; Nextcloud and iCloud have equivalents). It works like a
password, so it is stored encrypted and never shown again. Milo reads it (cached for 10 minutes),
expands repeating events and honours cancellations and exceptions. Unusual repeat rules may be
missed. Event titles are spoken and, when the assistant reads them, sent to the AI service. Titles are
written by other people, so in the turn where the assistant reads the calendar it can only use a short
list of harmless tools (volume, stop, listing and cancelling, time and weather); it cannot send a Telegram
message, create a reminder or timer, or start music. Recurring events are expanded with limits (500
occurrences per event, 2000 per read, a 2 second deadline; repeat rules more frequent than hourly are
ignored).

Usage schedules apply when a reminder or timer is due, when the next track of a music queue starts and to
the wake word, not only when the action is requested; the child can always stop music.

## Ideas that are deliberately not here

Streaming services (Spotify...) need accounts and licences and are not included. Music files
must be supplied by the family.

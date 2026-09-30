# Roadmap

Each phase leaves the repository compiling, tested and documented.

- [x] **Phase 1** - Foundation: store, migrations, secrets, audit, permissions, features,
  auth, web admin skeleton, CLI, systemd, Docker tooling.
- [x] **Phase 2** - GPIO and audio abstractions (real + mock), configurable buttons,
  WM8960, `test-audio`, `test-buttons`.
- [x] **Phase 3** - Telegram bot, invitations, authorization, chats, messages, notifications.
- [x] **Phase 4** - Schedules, parental controls, emergency channel.
- [x] **Phase 5** - OpenRouter, Action Engine, conversation history, cloud STT, local TTS.
- [x] **Phase 6** - More STT/TTS providers, assistant UX, Wi-Fi, system pages.
- [x] **Phase 7** - Local wake word (built-in template engine + external helper plug).
- [x] **Phase 9** - Multiple administrators with generated first credentials; encrypted backup and restore.
- [x] **Phase 8** - Optional skills: timers, reminders, music, weather, calendar.
- [x] **Phase 10** - Multilingual from the start (English source, Spanish, separate interface /
  notification / assistant / voice languages, per-person language); a shared Astro design system
  (global CSS, shared TypeScript, components) and a tile-based Home; first-run setup in the browser
  instead of a generated install-time password; "volume 7" on a 0-10 scale.
- [x] **Phase 11** - Remote administration from Telegram (status and volume, access requests,
  features, usage hours, alerts) and opt-in copies of incoming messages to administrators.
- [x] **Phase 12** - Optional Spotify Connect speaker through a user-installed go-librespot, with
  pairing in the admin and opt-in voice verbs ([spotify.md](spotify.md)).

## Not done yet

- More languages beyond English and Spanish (adding one is a catalog folder plus one UI file, see
  [i18n.md](i18n.md)).
- Real-hardware validation: GPIO, audio, Wi-Fi and wake-word accuracy were only tested with mocks
  and synthetic data ([hardware.md](hardware.md)).
- Setup protection: first-run setup is refused from outside the home network, but until the owner
  exists anyone on that network can still claim the device. There is no setup token yet.
- Spotify: not yet verified on a real Pi with a Premium account (go-librespot's `device_auth` login,
  the `speaker` device type and saved-credentials behaviour are taken from its documentation);
  searching for arbitrary songs is out of scope (needs Spotify's Web API).
- Translating messages that come from lower layers (for example backup passphrase rules), the CLI,
  logs and audit entries.

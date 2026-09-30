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

## Not done yet

- More languages beyond English and Spanish (adding one is a catalog folder plus one UI file, see
  [i18n.md](i18n.md)).
- Real-hardware validation: GPIO, audio, Wi-Fi and wake-word accuracy were only tested with mocks
  and synthetic data ([hardware.md](hardware.md)).
- Setup protection: until the owner exists, the first visitor becomes the owner. There is no setup
  token yet.
- Translating messages that come from lower layers (for example backup passphrase rules), the CLI,
  logs and audit entries.
- The Astro twins of `Select`, `Toggle`, `Table`, `Badge`, `EmptyState` and `PageHeader` exist in
  `web/admin/src/components` but the views use the TypeScript builders.

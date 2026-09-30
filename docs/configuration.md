# Configuration

Only bootstrap settings are environment variables. Everything else is stored in SQLite and
edited from the admin UI.

| Variable | Default | Meaning |
|---|---|---|
| `MILO_DATA_DIR` | `/var/lib/milo` | Directory containing `milo.db` |
| `MILO_LISTEN` | `:8080` | Admin UI listen address |
| `MILO_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `MILO_LOG_FORMAT` | `text` | `text` or `json` |
| `MILO_MASTER_KEY_FILE` | see below | Master key path |
| `MILO_HARDWARE` | `real` | `real` or `mock` |
| `MILO_TELEGRAM_API_URL` | empty | Development and tests only: Bot API base URL. Empty uses the real Telegram API (the `make dev` stack points it at the simulator) |

Master key lookup: `MILO_MASTER_KEY_FILE`, then `$CREDENTIALS_DIRECTORY/master.key`
(systemd), then `/etc/milo/master.key`.

Never put tokens, keys or passwords in the environment or in the repository. The first
administrator is not configured here either: it is created in the browser on first run
([installation.md](installation.md)).

## Language settings

Languages are settings, not environment variables: interface, notification, assistant and
text-to-speech languages are separate, each defaulting to English, and each person can have their
own. See [i18n.md](i18n.md). Other settings the admin UI edits live in `system_settings`, for
example `i18n.interface_language`, `i18n.notification_language`, `audio.volume`,
`telegram.announce_sender` and `messages.retention_days`.

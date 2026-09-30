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

Master key lookup: `MILO_MASTER_KEY_FILE`, then `$CREDENTIALS_DIRECTORY/master.key`
(systemd), then `/etc/milo/master.key`.

Never put tokens, keys or passwords in the environment or in the repository.

# Configuration

Only bootstrap settings are environment variables. Everything else is stored in SQLite and
edited from the admin UI.

| Variable | Default | Meaning |
|---|---|---|
| `NEXO_DATA_DIR` | `/var/lib/nexo` | Directory containing `nexo.db` |
| `NEXO_LISTEN` | `:8080` | Admin UI listen address |
| `NEXO_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `NEXO_LOG_FORMAT` | `text` | `text` or `json` |
| `NEXO_MASTER_KEY_FILE` | see below | Master key path |
| `NEXO_HARDWARE` | `real` | `real` or `mock` |

Master key lookup: `NEXO_MASTER_KEY_FILE`, then `$CREDENTIALS_DIRECTORY/master.key`
(systemd), then `/etc/nexo/master.key`.

Never put tokens, keys or passwords in the environment or in the repository.

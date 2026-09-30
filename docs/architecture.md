# Architecture

```
Hardware ─▶ Events ─▶ Actions ─▶ Features ─▶ SQLite (source of truth)
```

Keep these concerns independent: hardware, transport, AI provider, actions, features,
configuration, persistence, UI.

## Packages

| Package | Responsibility | Phase |
|---|---|---|
| `cmd/milo` | CLI entry point: `run`, `install`, `keygen`, `admin-password`, `admin-reset`, `backup`, `restore`, service control | 1 |
| `internal/store` | **The only package that talks to SQLite.** Embedded, transactional migrations | 1 |
| `internal/secrets` | XChaCha20-Poly1305 vault over the `secrets` table; master key handling | 1 |
| `internal/permissions` | Roles + explicit permissions, shared by web, Telegram and actions | 1 |
| `internal/audit` | Audit trail of administrative actions, with secret redaction | 1 |
| `internal/features` | Feature lifecycle: only enabled features start | 1 |
| `internal/auth` | Admin accounts (owner created in the browser at first run, several administrators), Argon2id, sessions, login throttling | 1, 9 |
| `internal/backup` | Encrypted backup/restore (Argon2id + XChaCha20-Poly1305), local snapshots | 9 |
| `internal/system` | Bootstrap config (`MILO_*`), structured logging, `install` | 1 |
| `internal/web` | Admin API (incl. `/api/setup`, `/api/locale`, `/api/settings/language`) + embedded UI | 1 |
| `internal/i18n` | Translation catalogs (`locales/<code>/messages.json`, en + es), English fallback, per-setting language lookup (`settings.go`) | 10 |
| `web/admin` | Astro + TypeScript UI, built to static files and embedded with `go:embed`: a tile Home, sheets, shared CSS/TS, its own i18n catalogs (see [development.md](development.md)) | 1, 10 |
| `systemd` | Embedded `milo.service` unit | 1 |
| `gpio`, `audio` | Hardware interfaces, each with a real and a mock implementation | 2 |
| `buttons` | Press-pattern engine; resolves a press to a configured action | 2 |
| `actions` | Action Engine: the security boundary for buttons, Telegram, voice and the LLM | 2 |
| `app` | Assembles everything; owns lifecycles | 2 |
| `telegram`, `schedules`, `notifications` | Transport and rules | 3-4 |
| `schedules` | When each feature may be used (timezone-aware windows) | 4 |
| `telegram` | Bot API client, authorization, invitations, inbox/outbox, Telegram actions; `admin.go` (the Administration menu: status, volume, features, hours, alerts), `remote.go` (alerts and opt-in message copies to administrators), `requests.go` (approve/reject shared with the web admin) | 3, 11 |
| `notifications` | Notification sounds honouring parent settings | 3 |
| `assistant` | Conversations, tool loop, voice session; states the configured language explicitly in the prompt; talks to an `AIProvider` interface | 5, 10 |
| `openrouter` | OpenAI-compatible chat client (OpenRouter, Groq...) | 5 |
| `speech` | Speech-to-text and text-to-speech providers; TTS receives language, voice, speed and volume | 5, 10 |
| `skills` | Timers, reminders, music, weather, calendar (each a feature with actions) | 8 |
| `wakeword` | Local wake-word detection (template matcher, external helper plug) | 7 |
| `wifi` | Wi-Fi networks applied to NetworkManager (`nmcli`) | 6 |
| `moderation` | Input/output safety net and parent-defined blocked phrases | 5 |

Packages are created when the phase that needs them starts, not before. Phases 10 and 11 are
the multilingual/first-run work and the Telegram remote administration ([roadmap.md](roadmap.md)).

## Schema changes worth knowing

- `0012_languages`: `gpt_settings.tts_language` / `tts_speed` / `tts_volume`; the assistant
  `language` becomes a code or `auto` (free text is mapped: English → `en`, Spanish → `es`,
  anything else → `auto`); `users.language` (empty = follow the notification language).
- `0013_remote_admin`: `users.forward_incoming` and `users.device_alerts`, both off by default.

## Key decisions

- **Pure Go, no CGO** (`modernc.org/sqlite`, later `go-gpiocdev`, `arecord`/`aplay` with fixed
  arguments) so the binary cross-compiles from x86 in Docker with no emulation.
- **One SQLite connection** serialises writes; WAL + `synchronous=NORMAL`. WAL improves
  resilience but does not make an unprotected SD card immune to power-loss corruption.
- **Migrations** are `internal/store/migrations/NNNN_name.sql`, applied in order, each in a
  transaction. A database newer than the binary is refused, so a downgrade cannot damage data.
- **The first administrator is created in the browser.** `install` prints only the address, so no
  credential ever appears in a terminal or log and no default password exists. The price is that
  whoever opens the page first owns the device: `POST /api/setup` works once, atomically (a single
  `INSERT … WHERE NOT EXISTS`), then answers 409 forever ([security.md](security.md)).
- **Everything user-facing is a translation key.** Go uses `internal/i18n`, the UI uses
  `web/admin/src/i18n`; both fall back to English. Interface, notification, assistant and voice
  languages are separate settings ([i18n.md](i18n.md)).
- **The admin UI is a grid of tiles** (`sections.ts`). Each opens an existing view in a sheet
  whose URL hash (`#telegram`) can be linked.
- **Hardware in development** is selected with `MILO_HARDWARE=mock`: GPIO and audio become
  in-memory mocks and the web admin gains a *Simulator* tile (virtual buttons, microphone and a
  speaker log). The mocks are also what the tests use.
- **Buttons hold no logic.** A press becomes `(action, parameters)` from SQLite and goes through
  the Action Engine, the same path Telegram commands and LLM tool calls will use.

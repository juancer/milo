# Architecture

```
Hardware ─▶ Events ─▶ Actions ─▶ Features ─▶ SQLite (source of truth)
```

Keep these concerns independent: hardware, transport, AI provider, actions, features,
configuration, persistence, UI.

## Packages

| Package | Responsibility | Phase |
|---|---|---|
| `cmd/nexo` | CLI entry point: `run`, `install`, `keygen`, `admin-password`, service control | 1 |
| `internal/store` | **The only package that talks to SQLite.** Embedded, transactional migrations | 1 |
| `internal/secrets` | XChaCha20-Poly1305 vault over the `secrets` table; master key handling | 1 |
| `internal/permissions` | Roles + explicit permissions, shared by web, Telegram and actions | 1 |
| `internal/audit` | Audit trail of administrative actions, with secret redaction | 1 |
| `internal/features` | Feature lifecycle: only enabled features start | 1 |
| `internal/auth` | Argon2id passwords, sessions, login throttling | 1 |
| `internal/system` | Bootstrap config (`NEXO_*`), structured logging, `install` | 1 |
| `internal/web` | Admin API + embedded UI | 1 |
| `web/admin` | Astro + TypeScript UI, built to static files and embedded with `go:embed` | 1 |
| `systemd` | Embedded `nexo.service` unit | 1 |
| `gpio`, `buttons`, `audio` | Hardware interfaces, real + mock | 2 |
| `telegram`, `schedules`, `notifications` | Transport and rules | 3-4 |
| `actions`, `assistant`, `openrouter` | Action Engine and LLM | 5 |

Packages are created when the phase that needs them starts, not before.

## Key decisions

- **Pure Go, no CGO** (`modernc.org/sqlite`, later `go-gpiocdev`, `arecord`/`aplay` with fixed
  arguments) so the binary cross-compiles from x86 in Docker with no emulation.
- **One SQLite connection** serialises writes; WAL + `synchronous=NORMAL`. WAL improves
  resilience but does not make an unprotected SD card immune to power-loss corruption.
- **Migrations** are `internal/store/migrations/NNNN_name.sql`, applied in order, each in a
  transaction. A database newer than the binary is refused, so a downgrade cannot damage data.
- **Admin password is set on the device** (`nexo admin-password`), not in the browser, so
  nobody on the LAN can claim an unconfigured admin.
- **Hardware in development** is selected with `NEXO_HARDWARE=mock`. The GPIO and audio
  interfaces (and their mocks) arrive in Phase 2; in Phase 1 the setting is only reported.

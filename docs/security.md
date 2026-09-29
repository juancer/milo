# Security model

Trust boundaries: child, parent, Telegram user, LLM, local OS, external APIs.
The LLM is not trusted. Telegram users are not trusted until authorized. The child cannot
change parental configuration. The Action Engine (Phase 5) is the central boundary.

## Secrets

Telegram tokens, API keys and Wi-Fi passwords are encrypted with XChaCha20-Poly1305 before
they reach SQLite. The secret's name is bound to the ciphertext, so rows cannot be swapped.

The master key is `/etc/nexo/master.key` (root, mode 0600), handed to the service with
systemd `LoadCredential`. It is **not** stored next to the database.

This protects against someone who obtains only the database (a backup, a copied SD card).
It does **not** protect against an attacker with root on the running device, because the
service must be able to read the key.

At startup a canary secret detects a key that does not match the database and refuses to run.

## Admin UI

- Local network only; do not expose it to the Internet. It is plain HTTP, so the session
  cookie is `HttpOnly` + `SameSite=Strict` but not `Secure`.
- Password hashed with Argon2id; minimum 12 characters; set on the device with
  `nexo admin-password`. There is no default password.
- Sessions are in memory (12 h) and end when the password changes. Login failures are limited
  to 5 per 15 minutes per client address.
- State-changing requests require the `X-Requested-With: nexo-admin` header and a matching
  `Origin`. Strict CSP, `X-Frame-Options: DENY`, no external assets.

## Logging and audit

Log and audit fields whose names look like credentials (`token`, `password`, `key`, ...) are
always redacted. Admin actions are recorded in `audit_log`.

## Commands

The application never builds shell commands from external or LLM input. Fixed commands
(`systemctl`, `useradd`, later `arecord`/`aplay`) use constant arguments.

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

## Admin accounts

- Local network only; do not expose it to the Internet. It is plain HTTP, so the session
  cookie is `HttpOnly` + `SameSite=Strict` but not `Secure`.
- **Several people can manage the device**, each with their own username and password
  (`mom`, `dad`...). The first account, `admin`, is the **owner**: created by `nexo install` with a
  **generated password shown once** on the terminal (it is never logged or stored in clear text).
  Whoever signs in with a generated password must choose their own before anything else works.
- The owner adds, resets and removes administrators, and is the only one who can download or
  restore a backup (a backup carries every account and, with the key, every secret); everyone else
  has the same management powers except that. Administrators cannot remove themselves or the owner.
- Passwords are hashed with Argon2id (minimum 12 characters). Sign-in attempts are limited per client
  address (5 per 15 minutes, counted before the password is checked). There is no per-account limit,
  so nobody can lock the owner out by naming `admin`; at most two Argon2 checks run at once. Unknown
  usernames cost the same time as wrong passwords. Changing a password, or removing an account, ends its sessions immediately.
- Every audit entry names the administrator who acted.
- Lost owner access: on the device, `sudo -u nexo nexo admin-reset` (needs shell access).
- State-changing requests require the `X-Requested-With: nexo-admin` header and a matching
  `Origin`. Strict CSP, `X-Frame-Options: DENY`, no external assets.

## Backups

Downloadable backups are encrypted with a passphrase (Argon2id + XChaCha20-Poly1305, authenticated
chunks). The passphrase is never stored or logged. Restores are verified and staged; a crafted
file cannot write outside its own folder or install a database from a newer Nexo. See
[backup.md](backup.md).

## Logging and audit

Log and audit fields whose names look like credentials (`token`, `password`, `key`, ...) are
always redacted. Admin actions are recorded in `audit_log`.

## Commands

The application never builds shell commands from external or LLM input. Fixed commands
(`systemctl`, `useradd`, later `arecord`/`aplay`) use constant arguments.

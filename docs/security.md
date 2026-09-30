# Security model

Trust boundaries: child, parent, Telegram user, LLM, local OS, external APIs.
The LLM is not trusted. Telegram users are not trusted until authorized. The child cannot
change parental configuration. The Action Engine is the central boundary.

## Secrets

Telegram tokens, API keys and Wi-Fi passwords are encrypted with XChaCha20-Poly1305 before
they reach SQLite. The secret's name is bound to the ciphertext, so rows cannot be swapped.

The master key is `/etc/milo/master.key` (root, mode 0600), handed to the service with
systemd `LoadCredential`. It is **not** stored next to the database.

This protects against someone who obtains only the database (a backup, a copied SD card).
It does **not** protect against an attacker with root on the running device, because the
service must be able to read the key.

At startup a canary secret detects a key that does not match the database and refuses to run.

## Admin accounts

- Local network only; do not expose it to the Internet. It is plain HTTP, so the session
  cookie is `HttpOnly` + `SameSite=Strict` but not `Secure`, and the login password and cookie
  cross the network unencrypted. Run it only on an isolated home network you trust (no
  untrusted devices on the same LAN or Wi-Fi).
- The audit log keeps 90 days of entries (at most 50000 rows; if that is exceeded the oldest rows of the
  actor with the most rows go first, so one admin's volume cannot push out another's history). Identical
  events from the same actor within a minute are counted on one row instead of adding rows.
- **Several people can manage the device**, each with their own username and password
  (`mom`, `dad`...). The first account is the **owner**. It is created in the browser, on first
  run, with a username and password chosen by the person setting up: nothing is generated at
  install time, so no credential ever appears in a terminal or a log. **Until that account exists,
  whoever on the home network reaches the address first becomes the owner**, so finish setup right
  after installing, on a trusted network. `POST /api/setup` is refused (403) unless the TCP peer is
  loopback, private (RFC 1918 or IPv6 ULA) or link-local; `X-Forwarded-For` is never consulted, so a
  public address cannot pass as local (behind a reverse proxy the peer is the proxy, so set up
  before exposing Milo). It is refused (409) forever after the first admin exists, and is
  rate-limited like sign-in. While setup is pending, Milo logs `waiting for first-run setup` with the
  address to open. Accounts created by the owner, or reset from the command line, get a
  generated password shown once and must choose their own before anything else works.
- The owner adds, resets and removes administrators, and is the only one who can download or
  restore a backup (a backup carries every account and, with the key, every secret); everyone else
  has the same management powers except that. Administrators cannot remove themselves or the owner.
- Passwords are hashed with Argon2id (minimum 12 characters). Sign-in attempts are limited per client
  address (5 per 15 minutes, counted before the password is checked; IPv6 clients share one bucket per /64,
  and a successful sign-in gives back only its own attempt, so earlier failures still count). There is no per-account limit,
  so nobody can lock the owner out by naming `admin`; at most two Argon2 checks run at once. Unknown
  usernames cost the same time as wrong passwords. Changing a password, or removing an account, ends its sessions immediately.
- Every audit entry names the administrator who acted (Telegram administration appears as
  `telegram:<name>`). The **Activity** tile shows them in the admin's language.
- Lost owner access: on the device, `sudo -u milo milo admin-reset` (needs shell access).
- State-changing requests require the `X-Requested-With: milo-admin` header and, when the browser
  sends one, an `Origin` that matches the request's `Host`. The Astro dev proxy rewrites `Origin`
  for that reason; production has no proxy. Strict CSP, `X-Frame-Options: DENY`, no external assets.

## Remote administration through Telegram

The Administration menu and message copies (see [telegram.md](telegram.md)) follow the same
permission model as the web admin: each section needs its own `manage_*` permission, re-checked
on the device at every tap, and nothing works for deactivated people or strangers. Secrets (bot
token, API keys, passwords, backup passphrases) are never sent or accepted over Telegram.

Message copies deliberately let an administrator read or hear what relatives send to the device.
They are **opt-in per person, off by default, limited to `manage_users`**, and the sender is not
told per message: only enable them for adults who already supervise the device, and tell the
family. Telegram messages are not end-to-end encrypted with bots, so a copy passes through
Telegram's servers like every other bot message. Device alerts carry a feature name only.

## Backups

Downloadable backups are encrypted with a passphrase (Argon2id + XChaCha20-Poly1305, authenticated
chunks). The passphrase is never stored or logged. Restores are verified and staged; a crafted
file cannot write outside its own folder or install a database from a newer Milo. See
[backup.md](backup.md).

## What leaves the device: weather location

The forecast sends only coordinates to Open-Meteo. **Detect my location** (Skills → Weather, or
the prompt when turning weather on) makes one HTTPS request to a keyless geolocation service,
which therefore sees the device's public IP address. It never runs automatically, stores only the
resulting place (`weather.place`, latitude, longitude), and is audited as
`weather.location_detected`. The location is approximate; typing a town overrides it. The
skills' HTTP client refuses plain http and loopback/link-local targets like the other skills.

## Logging and audit

Log and audit fields whose names look like credentials (`token`, `password`, `key`, ...) are
always redacted. Admin actions are recorded in `audit_log`.

## Commands

The application never builds shell commands from external or LLM input. Fixed commands
(`systemctl`, `useradd`, `arecord`/`aplay`, `nmcli`) use constant arguments.

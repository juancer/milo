# Development

Everything runs in Docker. The host needs only Docker and Docker Compose.
There is no buildx or ARM emulation requirement: the Go binary is cross-compiled natively
(`GOOS=linux GOARCH=arm64 CGO_ENABLED=0`).

| Command | What it does |
|---|---|
| `make test` | `go vet` + `go test` in the `go` service |
| `make build-arm64` | Astro build (pnpm, via corepack) in a Node container, then the ARM64 binary in `dist/` |
| `make dev` | Builds the UI and runs Milo (`:8080`) with mocked hardware, plus the simulator (`:8090`) |
| `make web-dev` | Astro dev server (`:4321`) proxying `/api` to the `dev` service |
| `make shell` | Bash in the Go container |

Files live in `deploy/` (`Dockerfile.dev`, `docker-compose.yml`). Go module and build caches
are kept in the `gocache` volume; dev data (database, master key) in `devdata`.

## Tests

Tests use real SQLite in temporary directories and mocks for anything hardware related.
`web/admin/dist` only needs to contain `.gitkeep` for Go to compile, so `make test` does not
need Node.

## Frontend rules

The server sends `Content-Security-Policy: default-src 'self'`. The Astro build therefore
never inlines scripts or styles (`astro.config.mjs`). Do not add inline `<script>`/`style=`
or third-party assets. Render dynamic text with `textContent`, never `innerHTML`.

## Frontend structure (`web/admin`)

Everything runs in Docker (`make web-deps`, `make web-build`, `make web-dev`); the host needs
no Node. Extra checks, also in Docker:

```sh
docker compose -f deploy/docker-compose.yml run --rm node corepack pnpm run check        # tsc
docker compose -f deploy/docker-compose.yml run --rm node corepack pnpm run check:i18n   # catalogs
```

```
src/
├── pages/index.astro      one page: <AdminLayout />
├── layouts/AdminLayout    shell: header, section sheet, toasts, confirm/prompt dialogs, <template> screens
├── components/            Astro components (static shell) + ui.ts (the same widgets as TS builders)
├── styles/                global.css imports tokens, base, layout, components/*, responsive
├── scripts/
│   ├── app.ts             the single entry point
│   ├── shell.ts           sign-in, routing (Home + sheet from the URL hash), locale bootstrap
│   ├── sections.ts        the tiles: id, group, icon, colour, view, feature switches
│   ├── home.ts sheet.ts   Home tiles + setup checklist; the modal sheet (focus, scroll lock)
│   ├── featureState.ts setupPrompt.ts   feature switches, friendly "needs setup", ask-for-missing-keys prompt
│   ├── api.ts dom.ts ui.ts locales.ts   HTTP client, element builder, toasts/confirm, language options
│   └── views/*.ts         one module per section; compose components/ui.ts
└── i18n/                  index.ts (t, setLocale, fallback) + locales/<code>.json
```

Design lives in `styles/` only: colours and sizes in `tokens.css`, widgets in `components/*.css`.
Pages and views carry no CSS. Views render in the browser, so they use the builders in
`components/ui.ts` (`card`, `formCard`, `field`, `toggle`, `select`, `table`, `badge`, `alert`,
`emptyState`, …), which emit the same class names as the `.astro` components. Behaviour shared
by several views (`toast`, `guard`, `confirmTwice`, `confirmDialog`, `changeLocale`) is in
`scripts/ui.ts`; do not copy it into a view.

**Add a component:** put markup in `components/ui.ts` (and an `.astro` twin if the shell needs
it), its CSS in `styles/components/<name>.css`, and import that from `global.css`.
**Add a section (a tile):** create `scripts/views/<id>.ts` exporting `render(root)`, add an entry
to `SECTIONS` in `sections.ts` (group, icon from `components/icons.ts`, colour, optional
`features`), add `nav.<id>` to the locale files and a summary in `infoFor()` in `home.ts`. Home is
a grid of tiles; clicking one opens its view in a sheet and puts `#<id>` in the URL (Back closes it).
A feature that needs configuration (Telegram token, AI key) is asked for in a prompt when it is
switched on: add the case to `missing()` in `setupPrompt.ts`, and its "not set up" error to
`KNOWN_ERRORS` in `featureState.ts`, so the UI never shows the raw error.

### Translations

User-facing text is never written in a view: use `t("section.key", { name: value })`
(`{name}` placeholders). `en.json` is the source language; a missing key or locale falls back to
English, then to the key itself. Static shell text uses `data-i18n="key"` (also
`data-i18n-placeholder`, `-aria-label`, `-title`).

**Add a language:** copy `src/i18n/locales/en.json` to `<code>.json`, set `"meta.name"` to the
language's own name, translate the values, and run `check:i18n` (it fails on unknown keys or
changed `{placeholders}`, and lists untranslated keys). No code changes: the file is picked up
automatically. The device must also list the code in `GET /api/locales` for it to be offered.

## Adding a migration

Add `internal/store/migrations/NNNN_description.sql` with the next number. Never edit a
migration that has shipped; add a new one.

## The demo environment (`make dev`)

`make dev` starts two containers:

- **dev**: Milo with `MILO_HARDWARE=mock`. On first start `milo dev-seed` creates a demo
  installation: administrators `admin` (owner) and `mom` with a random password printed once (or the one in `MILO_DEV_PASSWORD`), the simulator's fake bot token, a demo
  family (Dad and Mum as PARENT, Grandma as FAMILY) and enabled features. It refuses to run
  unless hardware is mocked, so it can never touch a real device.
- **sim** (`cmd/milo-sim`): fake family phones and a fake Telegram Bot API on
  <http://localhost:8090>. Chat as Dad, Mum, Grandma or a Stranger, paste invitation links,
  tap inline buttons and simulate a Telegram outage.

Open <http://localhost:8080> (sign in as `admin` or `mom`, password printed when the demo was seeded) and use the **Simulator** tile
for virtual buttons, a microphone ("what the child says next") and a speaker log.

The assistant runs against the simulator's rule-based "brain" (`/v1/chat/completions` and
`/v1/audio/transcriptions`), which speaks the same protocol as OpenRouter and Groq. In the
Simulator tile type what the child says, then press the green GPT button.

A quick tour: as *Stranger*, send `/start` (refused). In the admin UI create an invitation,
paste its link into the Stranger chat, approve the request, and message the device as Dad.
Press the blue LISTEN button in the Simulator tile.

The simulator's "voice notes" are silent WAV files that carry their words in a hidden chunk,
so the development speech-to-text can "hear" them. Real recordings never contain it.

# Development

Everything runs in Docker. The host needs only Docker and Docker Compose.
There is no buildx or ARM emulation requirement: the Go binary is cross-compiled natively
(`GOOS=linux GOARCH=arm64 CGO_ENABLED=0`).

| Command | What it does |
|---|---|
| `make test` | `go vet` + `go test` in the `go` service |
| `make build-arm64` | Astro build (pnpm, via corepack) in a Node container, then the ARM64 binary in `dist/` |
| `make dev` | Builds the UI and runs Nexo (`:8080`) with mocked hardware, plus the simulator (`:8090`) |
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

## Adding a migration

Add `internal/store/migrations/NNNN_description.sql` with the next number. Never edit a
migration that has shipped; add a new one.

## The demo environment (`make dev`)

`make dev` starts two containers:

- **dev**: Nexo with `NEXO_HARDWARE=mock`. On first start `nexo dev-seed` creates a demo
  installation: admin password `nexo-demo-password`, the simulator's fake bot token, a demo
  family (Dad and Mum as PARENT, Grandma as FAMILY) and enabled features. It refuses to run
  unless hardware is mocked, so it can never touch a real device.
- **sim** (`cmd/nexo-sim`): fake family phones and a fake Telegram Bot API on
  <http://localhost:8090>. Chat as Dad, Mum, Grandma or a Stranger, paste invitation links,
  tap inline buttons and simulate a Telegram outage.

Open <http://localhost:8080> (sign in with the demo password) and use the **Simulator** tab
for virtual buttons, a microphone ("what the child says next") and a speaker log.

The assistant runs against the simulator's rule-based "brain" (`/v1/chat/completions` and
`/v1/audio/transcriptions`), which speaks the same protocol as OpenRouter and Groq. In the
Simulator tab type what the child says, then press the green GPT button.

A quick tour: as *Stranger*, send `/start` (refused). In the admin UI create an invitation,
paste its link into the Stranger chat, approve the request, and message the device as Dad.
Press the blue LISTEN button in the Simulator tab.

The simulator's "voice notes" are silent WAV files that carry their words in a hidden chunk,
so the development speech-to-text can "hear" them. Real recordings never contain it.

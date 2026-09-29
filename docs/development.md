# Development

Everything runs in Docker. The host needs only Docker and Docker Compose.
There is no buildx or ARM emulation requirement: the Go binary is cross-compiled natively
(`GOOS=linux GOARCH=arm64 CGO_ENABLED=0`).

| Command | What it does |
|---|---|
| `make test` | `go vet` + `go test` in the `go` service |
| `make build-arm64` | Astro build (pnpm, via corepack) in a Node container, then the ARM64 binary in `dist/` |
| `make dev` | Builds the UI and runs Nexo on `:8080` with mocked hardware |
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

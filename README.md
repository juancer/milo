# Nexo

Nexo is an open-source, screenless, child-friendly communication and AI assistant for the
Raspberry Pi. A child uses physical buttons and voice; authorized family members reach the
device through their own Telegram bot; parents configure everything from a local web admin.

Nexo is the name of the software, not of any child. Nothing about a specific family,
Telegram account or bot is hardcoded: each installation is configured on its own.

> **Status: Phase 4 of 8.** Foundation, configurable buttons, audio, the Action Engine, the
> Telegram family channel, usage schedules and the emergency channel work.

## Design in one minute

- One static Go binary (`linux/arm64`, no CGO) + SQLite + an embedded Astro admin UI.
- **SQLite is the source of truth** for configuration and state; code holds behavior.
- Hardware, transports, AI/STT/TTS providers are behind interfaces, so most of it runs and
  is tested with no Raspberry Pi.
- Every input (button, Telegram, voice, LLM) will go through one **Action Engine**; an LLM
  can only call registered, validated actions and never runs shell commands.
- Docker is a **development and build** tool only. The Pi runs the binary under systemd.

More: [architecture](docs/architecture.md) · [security](docs/security.md) ·
[configuration](docs/configuration.md)

## Develop (Docker only)

You need Docker and Docker Compose. No Go, Node or Python on the host.

```sh
make test         # go vet + go test, inside Docker
make build-arm64  # builds the UI, then dist/nexo-linux-arm64 (CGO_ENABLED=0)
make dev          # Nexo (http://localhost:8080) + simulator of family phones (http://localhost:8090)
make web-dev      # Astro dev server with hot reload on http://localhost:4321
make help
```

Set the admin password for the dev instance (the password is chosen on the device, never in
the browser):

```sh
echo 'choose-a-long-password' | docker compose -f deploy/docker-compose.yml run --rm -T \
  -e NEXO_DATA_DIR=/data -e NEXO_MASTER_KEY_FILE=/data/master.key \
  dev go run ./cmd/nexo admin-password --password-stdin
```

Details: [docs/development.md](docs/development.md).

## Install on a Raspberry Pi

Target: Raspberry Pi 3B, Raspberry Pi OS 64-bit.

```sh
make build-arm64
scp dist/nexo-linux-arm64 pi@raspberrypi:nexo
ssh pi@raspberrypi 'sudo ./nexo install && sudo -u nexo nexo admin-password'
```

`install` is idempotent and never deletes data. Details: [docs/installation.md](docs/installation.md).

## License

Apache-2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).

Nexo is open source: improve it, fork it. Please let the project know by opening a pull request
or issue (see [CONTRIBUTING.md](CONTRIBUTING.md)).

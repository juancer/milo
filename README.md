# Milo

Milo is an open-source, screenless, child-friendly communication and AI assistant for the
Raspberry Pi. A child uses physical buttons and voice; authorized family members reach the
device through their own Telegram bot; parents configure everything from a local web admin.

Milo is the name of the software, not of any child. Nothing about a specific family,
Telegram account or bot is hardcoded: each installation is configured on its own.

> **Status: all planned phases done.** Foundation, configurable buttons, audio, the Action Engine, the
> Telegram family channel, usage schedules, the emergency channel, the voice assistant, Wi-Fi,
> the system pages, a local wake word, optional skills (timers, reminders, music, weather,
> calendar), multiple administrators and encrypted backups work.

## Design in one minute

- One static Go binary (`linux/arm64`, no CGO) + SQLite + an embedded Astro admin UI.
- **SQLite is the source of truth** for configuration and state; code holds behavior.
- Hardware, transports, AI/STT/TTS providers are behind interfaces, so most of it runs and
  is tested with no Raspberry Pi.
- Every input (button, Telegram, voice, LLM) will go through one **Action Engine**; an LLM
  can only call registered, validated actions and never runs shell commands.
- Docker is a **development and build** tool only. The Pi runs the binary under systemd.

More: [backup](docs/backup.md) · [skills](docs/skills.md) · [assistant](docs/assistant.md) · [wake word](docs/wakeword.md) · [telegram](docs/telegram.md) · [architecture](docs/architecture.md) · [security](docs/security.md) ·
[configuration](docs/configuration.md) · [languages](docs/i18n.md)

## Develop (Docker only)

You need Docker and Docker Compose. No Go, Node or Python on the host.

```sh
make test         # go vet + go test, inside Docker
make build-arm64  # builds the UI, then dist/milo-linux-arm64 (CGO_ENABLED=0)
make dev          # Milo (http://localhost:8080) + simulator of family phones (http://localhost:8090)
make web-dev      # Astro dev server with hot reload on http://localhost:4321
make help
```

`make dev` seeds a demo installation: sign in as `admin` (or `mom`) with the password printed once
when the demo is seeded (set `MILO_DEV_PASSWORD` to choose it). On a real device the first person to
open Milo in the browser creates the owner account; nothing is generated at install time.

Details: [docs/development.md](docs/development.md).

## Install on a Raspberry Pi

Target: Raspberry Pi 3B, Raspberry Pi OS 64-bit.

```sh
make build-arm64
scp dist/milo-linux-arm64 pi@raspberrypi:milo
ssh pi@raspberrypi 'sudo ./milo install'   # prints the address to open; the owner account is created in the browser
```

`install` is idempotent and never deletes data. Details: [docs/installation.md](docs/installation.md).

## License

Apache-2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).

Milo is open source: improve it, fork it. Please let the project know by opening a pull request
or issue (see [CONTRIBUTING.md](CONTRIBUTING.md)).

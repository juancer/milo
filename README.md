# Milo

**The parent-controlled alternative to Alexa, Google Assistant and Siri, for kids.**
A voice assistant and family messenger with **no screen**, built for a child's room and for
grandparents who live far away. Open it up, build it on a Raspberry Pi, and you decide what it says,
who can talk to it and when.

> *"Hey Milo, ¿quién me ha escrito?"* — *"Hay un mensaje nuevo de la abuela. ¿Quieres que lo lea?"*

## Why Milo

- **Family in, strangers out.** Only people you approve can send messages. A child hears Grandma's
  voice note and answers with a button or their voice. No phone, no tablet, no feed to scroll.
- **You own it.** It runs on a $35 Raspberry Pi in your home. Settings, messages and history live in
  one SQLite file on that Pi, not in a company's account. Nothing is hardcoded to any family or service.
- **Built for kids.** Allowed hours (nothing is announced at 2 am), an emergency channel, an audit
  log and a language model that can only call a short list of actions you can see. It never runs commands.
- **Fast on cheap hardware.** The wake word runs on the device, and time, date, sums, timers and
  volume are answered locally in about a second. Only open questions go to the AI model you pick.
- **You choose the AI.** Groq's free plan, or run speech recognition on the Pi itself. No
  subscription, no lock-in.
- **Speaks your language.** English and Spanish today; every language is a separate setting for the
  interface, notifications, assistant and voice. Adding another is a JSON file.
- **Hackable and open to read.** One Go binary, no CGO, full test suite, every action in one place.

It is not affiliated with Amazon, Google or Apple, and does not use their services.

## What it does

| | |
|---|---|
| **Voice assistant** | Wake word on the device ("Hey Milo", recorded with your own voice). Simple questions (time, date, sums, timers, dice, volume, "what is playing") are answered locally in about a second; the rest goes to a language model you choose (Groq has a free plan). |
| **Family messages** | Authorized relatives write or send voice notes to Milo's Telegram bot. Milo announces new messages ("Hay un mensaje nuevo de Abuela, ¿quieres que lo lea?"), plays voice notes, and offers unheard ones after your next question. |
| **Buttons** | Physical buttons for talk, volume, reply to a contact, emergency. Each one is configurable. |
| **Music** | Spotify Connect speaker (Premium required) and your own audio files. "Pon música", "pon Camilo". |
| **Safety** | Allowed hours (nothing is announced at 2 am), who may write to the device, an emergency channel, an audit log. The language model can only call a fixed list of validated actions and never runs commands. |
| **Languages** | English and Spanish for the interface, notifications, assistant and voice; each is a separate setting. |

## What you need

- A **Raspberry Pi 3B** or newer, with Raspberry Pi OS (32 or 64-bit) and a **good 5 V / 3 A power
  supply**. A weak supply makes the speaker quiet and the Pi unstable.
- A microphone and speaker. Tested with the Waveshare **WM8960 HAT**; see [hardware](docs/hardware.md).
- Optional: push buttons wired to the GPIO pins ([hardware](docs/hardware.md)).
- A Telegram account to create your bot (free, two minutes).
- For the voice assistant: a [Groq](https://console.groq.com) API key (free plan), or run speech
  recognition on the Pi with whisper.cpp.

## Install (about 15 minutes)

You build on a computer with **Docker**, then copy one file to the Pi. The Pi needs no Go, Node or Docker.

1. On the Pi, install the audio tools: `sudo apt install alsa-utils opus-tools`.
2. On your computer:
   ```sh
   git clone https://github.com/juancer/milo.git && cd milo
   make build-arm64      # 64-bit Pi OS. On 32-bit use: make build-armv7
   ```
3. Copy it and install (replace `raspberrypi` with your Pi's name or address):
   ```sh
   scp dist/milo-linux-arm64 pi@raspberrypi:milo
   ssh pi@raspberrypi 'sudo ./milo install'
   ```
4. The install prints `http://<pi-ip>:8080`. **Open it right away** from a device on your home
   network: the first person to open it creates the owner account (language, name, password).
5. Follow the **Finish setting up** list on the Home page: connect your Telegram bot, invite the
   first relative, add the Groq key, record your wake word.

`install` can be run again at any time to upgrade; it never deletes your data. Ready-made binaries
are also attached to the [latest release](https://github.com/juancer/milo/releases/tag/latest).
Details: [installation](docs/installation.md).

## First things to try

- Press the talk button (or say "Hey Milo") and ask "¿qué hora es?" or "what is 6 times 7".
- In Telegram, send your bot a message. Milo announces it and asks whether to read it.
- Say "pon algo de música" once Spotify is connected ([Spotify](docs/spotify.md)).
- Say "apaga" to stop whatever Milo is doing.

## When something does not work

- **Logs:** `journalctl -u milo -f` on the Pi (also on the admin **System** tile).
- **It does not hear "Hey Milo":** record 5 samples on the **Wake word** tile, quiet room, normal
  distance. Loud music hides your voice above about 55% volume: [wake word](docs/wakeword.md).
- **Speaker too quiet or the Pi restarts:** check the power supply (`vcgencmd get_throttled` must
  say `0x0`) and see [hardware](docs/hardware.md).
- **Forgot the admin password:** `sudo -u milo milo admin-reset`.

## Documentation

[Installation](docs/installation.md) · [Hardware](docs/hardware.md) · [Configuration](docs/configuration.md) ·
[Languages](docs/i18n.md) · [Telegram](docs/telegram.md) · [Schedules](docs/schedules.md) ·
[Emergency](docs/emergency.md) · [Assistant](docs/assistant.md) · [Wake word](docs/wakeword.md) ·
[Skills](docs/skills.md) · [Spotify](docs/spotify.md) · [Wi-Fi](docs/wifi.md) · [Backup](docs/backup.md) ·
[Security](docs/security.md) · [Architecture](docs/architecture.md) · [Development](docs/development.md) ·
[Roadmap](docs/roadmap.md)

## How it is built

One static Go binary (no CGO) with SQLite and an embedded Astro admin UI. Hardware, Telegram and
the AI, speech-to-text and text-to-speech providers sit behind interfaces, so almost everything
runs and is tested without a Pi. Every input (button, Telegram, voice, language model) goes
through one Action Engine. Docker is only a build and development tool. See [architecture](docs/architecture.md).

## Contributing

Pull requests are welcome; `main` accepts changes only through reviewed pull requests, and every
one must pass `make test`. Start with [CONTRIBUTING.md](CONTRIBUTING.md). Develop with Docker only:

```sh
make test      # go vet + go test
make dev       # Milo on http://localhost:8080 with demo data and a Telegram phone simulator
make help
```

Report vulnerabilities privately: [SECURITY.md](SECURITY.md).

## License

[PolyForm Noncommercial 1.0.0](LICENSE). The source is public and you may use, change and share it
for **noncommercial** purposes: your home, your family, hobby projects, education, research.
**Commercial use (selling Milo, devices that include it, paid services built on it) needs the
author's permission:** open an issue at https://github.com/juancer/milo/issues first.

This is source-available, not an OSI "open source" license, because it excludes commercial use.

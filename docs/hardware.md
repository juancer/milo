# Hardware: WM8960 HAT, buttons and wiring

Target: Raspberry Pi 3B (a Zero 2 W should work; nothing is Pi-3-specific) with the
Waveshare **WM8960 Hi-Fi Sound Card HAT** and momentary push buttons.

> Everything on this page needs real hardware and could not be tested in CI. If you find a
> difference on your board, please open an issue or a pull request.

## Packages

```sh
sudo apt install alsa-utils opus-tools
```

`alsa-utils` provides `arecord`, `aplay` and `amixer`, which Milo calls with fixed arguments so
the binary stays CGO-free. `opus-tools` is used from Phase 3 for Telegram voice messages.

## WM8960 sound card

1. Attach the HAT to the 40-pin header.
2. Enable the driver. On current Raspberry Pi OS the kernel ships an overlay; add to
   `/boot/firmware/config.txt`:

   ```ini
   dtparam=i2s=on
   dtoverlay=wm8960-soundcard
   ```

   (On older kernels use the vendor driver from Waveshare instead.) Reboot.
3. Check the card exists: `aplay -l` should list `wm8960soundcard`.
4. Test with Milo: `milo test-audio` plays three sounds, records 3 seconds and plays them back.

Milo's defaults match this card. Override them in the `system_settings` table if needed:

| Key | Default |
|---|---|
| `audio.capture_device` | `plughw:wm8960soundcard` |
| `audio.playback_device` | `plughw:wm8960soundcard` |
| `audio.mixer_card` | `wm8960soundcard` |
| `audio.mixer_control` | `Speaker` |

If the volume does not change, list the controls with `amixer -c wm8960soundcard scontrols`
and set `audio.mixer_control` to the right one (for example `Playback`).

The HAT uses I2S (GPIO 18, 19, 20, 21) and I2C (GPIO 2, 3). **Do not wire buttons to those pins.**

## Buttons

Each button connects one GPIO pin to **ground**. Milo enables the pin's internal pull-up, so no
resistor is needed: released reads high, pressed reads low ("active low").

```
   GPIO 17 ──┬── [ RED    ] ──┐
   GPIO 22 ──┼── [ BLUE   ] ──┤
   GPIO 23 ──┼── [ GREEN  ] ──┼── GND
   GPIO 24 ──┴── [ YELLOW ] ──┘
```

The pins above (BCM numbering) are only the **defaults**. Colours are labels for humans;
nothing depends on them. Change pins, names and what each press does in the admin UI
(Buttons). Supported patterns: short press, long press (held ~0.8 s) and double press.

Pins free on this HAT: 4, 5, 6, 12, 13, 16, 17, 22, 23, 24, 25, 26, 27. Check your HAT's pinout
before using others.

Run `milo test-buttons` on the Pi to see presses as Milo recognises them.

## GPIO access

Milo uses the Linux GPIO character device (`/dev/gpiochip0`), not `/dev/mem`. The `milo`
service user must be able to open it: `sudo usermod -aG gpio,audio milo` (the installer does this).

## Volume

Milo sets the volume through the ALSA mixer named in **System → Sound** (default: card
`wm8960soundcard`, control `Speaker`). If that fails (another card, Ubuntu's different control
names, PipeWire or PulseAudio owning the card), it looks for an ALSA card with a `Speaker`, `Master`,
`PCM`, `Headphone` or similar control, then tries PipeWire (`wpctl`) and PulseAudio (`pactl`), and
remembers the first one that works. The unit lists `DeviceAllow=char-alsa` and `char-gpiochip`
because `ProtectClock=true` otherwise hides the sound card and the buttons from the service.

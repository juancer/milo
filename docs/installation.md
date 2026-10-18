# Installation (Raspberry Pi)

Target: Raspberry Pi 3B with Raspberry Pi OS **64-bit**. The Pi needs no Go, Node, Python or
Docker. It needs `alsa-utils` (and `opus-tools` from Phase 3): `sudo apt install alsa-utils opus-tools`.
See [hardware.md](hardware.md) for the sound card and button wiring.

1. Build on your development machine: `make build-arm64`.
2. Copy `dist/milo-linux-arm64` to the Pi (for example with `scp`).
3. On the Pi: `sudo ./milo-linux-arm64 install`.
4. The install ends by printing only an address, `http://<pi-ip>:8080` (the Pi's LAN address; Milo
   listens on all interfaces). No password is generated or shown.
5. Open that address from any device on your network (Milo listens on all interfaces). The welcome
   screen asks for a **language**, **who you are** (Mum, Dad, Grandma... or another name; it fills
   in the username, which you can edit) and a **password** (at least 12 characters, typed twice).
   It then shows your username once more: that is what you sign in with from now on. You land on
   the admin Home straight away. Do this right after installing, on a network you trust: until it
   is done, the first person on your home network to reach the page becomes the owner. Setup is
   refused from any address outside the home network (public Internet addresses get a 403).
6. The Home shows a **Finish setting up** checklist: connect your Telegram bot and invite the first
   person ([telegram.md](telegram.md)); add an AI key and Wi-Fi if you want them. Add the other
   people who will manage the device under the **Admins** tile.

`install` creates the `milo` system user, `/var/lib/milo` (database, and `music/` for your audio files), `/etc/milo/master.key`
(root-only), `/usr/local/bin/milo`, and the `milo.service` unit, then enables and (re)starts it.

It is idempotent. Re-running it upgrades the binary and unit and never deletes the database,
the master key, contacts, messages or credentials.

The unit is sandboxed with a conservative set of systemd options (no capabilities, restricted
address families and namespaces, no kernel module/log/clock access). `PrivateDevices`,
`SystemCallFilter` and `MemoryMax` were left out on purpose: the service needs `/dev/snd` and
`/dev/gpiochip*`, and a syscall filter or memory cap that is too tight can break audio, GPIO or
`nmcli` in ways that cannot be tested without the hardware.

Wi-Fi is managed in the admin UI ([wifi.md](wifi.md)); it needs NetworkManager. The language chosen at setup becomes the interface and notification language; change either later under **System → Languages** ([i18n.md](i18n.md)).

Logs: `journalctl -u milo -f` (the admin **System** tile shows the latest lines too). Service control: `milo start|stop|restart|status`.

## Automatic updates

Every push to `main` runs `.github/workflows/release.yml`, which tests, builds `arm64` and `armv7`
binaries and republishes the rolling `latest` release. Devices pull it; nothing connects to them.
On 32-bit Raspberry Pi OS the binary is `milo-linux-armv7` (`make build-armv7`).

On the Pi (as root), once:

```sh
install -m 755 deploy/pi/milo-update.sh /usr/local/sbin/milo-update
install -m 644 deploy/pi/milo-update.service deploy/pi/milo-update.timer /etc/systemd/system/
install -m 600 /dev/null /etc/milo/update.env
printf 'REPO=owner/milo\nGH_TOKEN=<fine-grained token, Contents: read-only on this repo>\n' > /etc/milo/update.env
systemctl daemon-reload && systemctl enable --now milo-update.timer
```

It checks every 10 minutes, verifies the SHA-256, runs `milo install` (an idempotent upgrade that
keeps the database and master key) and restarts the service. Logs: `journalctl -u milo-update`.

## Back up

Use the **Backup** tile (or `milo backup FILE`): one encrypted file with the database and the master
key, see [backup.md](backup.md). Shut the Pi down cleanly (`sudo shutdown now`) before pulling power.

## Lost the password, or forgot the username?

The username is the name chosen at setup (for example `mom` or `dad`); the sign-in page reminds you
of that, and the admin header shows who is signed in. On the Pi, `sudo -u milo milo admin-reset`
prints a new generated password for the owner (add `--user mom` for another administrator); they
must choose their own at the next sign-in. `milo admin-password [--user name]` sets one you choose.
If nobody has finished the browser setup yet, both commands tell you to do that instead.

## Coming from Nexo?

The project was renamed from Nexo to Milo, and so were the binary, the service and system user, the
folders (`/var/lib/milo`, `/etc/milo`), the `MILO_*` environment variables, the session cookie and the
backup file extension. An existing device is not renamed in place; carry its data over with a backup,
which Milo can read (backups made under the old name still open):

```sh
# 1. On the old device, with the old program: make a backup (it includes the master key by default).
sudo -u nexo nexo backup ~/old-device.nexo                    # asks for a passphrase

# 2. Install Milo (see above), stop it, restore, start it.
sudo systemctl stop milo
sudo milo restore ~/old-device.nexo --replace-key
sudo systemctl start milo

# 3. Remove the old service once Milo works.
sudo systemctl disable --now nexo
sudo rm /etc/systemd/system/nexo.service /usr/local/bin/nexo
```

Administrators sign in again (the session cookie has a new name). Keep the backup file until you have
checked that everything works, then delete it.

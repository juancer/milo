# Installation (Raspberry Pi)

Target: Raspberry Pi 3B with Raspberry Pi OS **64-bit**. The Pi needs no Go, Node, Python or
Docker. It needs `alsa-utils` (and `opus-tools` from Phase 3): `sudo apt install alsa-utils opus-tools`.
See [hardware.md](hardware.md) for the sound card and button wiring.

1. Build on your development machine: `make build-arm64`.
2. Copy `dist/milo-linux-arm64` to the Pi (for example with `scp`).
3. On the Pi: `sudo ./milo-linux-arm64 install`.
4. Read the **admin account** printed at the end of the install (username `admin` and a generated
   password, shown once). Write it down.
5. Open the printed address (`http://<pi-ip>:8080`), sign in and choose your own password when asked.
6. Add the other people who will manage the device (mom, dad...) under **Admins**.

`install` creates the `milo` system user, `/var/lib/milo` (database), `/etc/milo/master.key`
(root-only), `/usr/local/bin/milo`, and the `milo.service` unit, then enables and (re)starts it.

It is idempotent. Re-running it upgrades the binary and unit and never deletes the database,
the master key, contacts, messages or credentials.

The unit is sandboxed with a conservative set of systemd options (no capabilities, restricted
address families and namespaces, no kernel module/log/clock access). `PrivateDevices`,
`SystemCallFilter` and `MemoryMax` were left out on purpose: the service needs `/dev/snd` and
`/dev/gpiochip*`, and a syscall filter or memory cap that is too tight can break audio, GPIO or
`nmcli` in ways that cannot be tested without the hardware.

Wi-Fi is managed in the admin UI ([wifi.md](wifi.md)); it needs NetworkManager.

Logs: `journalctl -u milo -f` (the admin **System** tab shows the latest lines too). Service control: `milo start|stop|restart|status`.

## Back up

Use the **Backup** page (or `milo backup FILE`): one encrypted file with the database and the master
key, see [backup.md](backup.md). Shut the Pi down cleanly (`sudo shutdown now`) before pulling power.

## Lost the admin password?

On the Pi: `sudo -u milo milo admin-reset` prints a new generated password for the owner (add
`--user mom` for another administrator); they must choose their own at the next sign-in.

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

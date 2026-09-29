# Installation (Raspberry Pi)

Target: Raspberry Pi 3B with Raspberry Pi OS **64-bit**. The Pi needs no Go, Node, Python or
Docker. It needs `alsa-utils` (and `opus-tools` from Phase 3): `sudo apt install alsa-utils opus-tools`.
See [hardware.md](hardware.md) for the sound card and button wiring.

1. Build on your development machine: `make build-arm64`.
2. Copy `dist/nexo-linux-arm64` to the Pi (for example with `scp`).
3. On the Pi: `sudo ./nexo-linux-arm64 install`.
4. Read the **admin account** printed at the end of the install (username `admin` and a generated
   password, shown once). Write it down.
5. Open the printed address (`http://<pi-ip>:8080`), sign in and choose your own password when asked.
6. Add the other people who will manage the device (mom, dad...) under **Admins**.

`install` creates the `nexo` system user, `/var/lib/nexo` (database), `/etc/nexo/master.key`
(root-only), `/usr/local/bin/nexo`, and the `nexo.service` unit, then enables and (re)starts it.

It is idempotent. Re-running it upgrades the binary and unit and never deletes the database,
the master key, contacts, messages or credentials.

Wi-Fi is managed in the admin UI ([wifi.md](wifi.md)); it needs NetworkManager.

Logs: `journalctl -u nexo -f` (the admin **System** tab shows the latest lines too). Service control: `nexo start|stop|restart|status`.

## Back up

Use the **Backup** page (or `nexo backup FILE`): one encrypted file with the database and the master
key, see [backup.md](backup.md). Shut the Pi down cleanly (`sudo shutdown now`) before pulling power.

## Lost the admin password?

On the Pi: `sudo -u nexo nexo admin-reset` prints a new generated password for the owner (add
`--user mom` for another administrator); they must choose their own at the next sign-in.

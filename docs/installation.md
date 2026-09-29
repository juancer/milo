# Installation (Raspberry Pi)

Target: Raspberry Pi 3B with Raspberry Pi OS **64-bit**. The Pi needs no Go, Node, Python or
Docker.

1. Build on your development machine: `make build-arm64`.
2. Copy `dist/nexo-linux-arm64` to the Pi (for example with `scp`).
3. On the Pi: `sudo ./nexo-linux-arm64 install`.
4. Set the admin password: `sudo -u nexo nexo admin-password`.
5. Open the printed address (`http://<pi-ip>:8080`) and sign in.

`install` creates the `nexo` system user, `/var/lib/nexo` (database), `/etc/nexo/master.key`
(root-only), `/usr/local/bin/nexo`, and the `nexo.service` unit, then enables and (re)starts it.

It is idempotent. Re-running it upgrades the binary and unit and never deletes the database,
the master key, contacts, messages or credentials.

Logs: `journalctl -u nexo -f`. Service control: `nexo start|stop|restart|status`.

## Back up

Back up **both** `/var/lib/nexo/nexo.db` and `/etc/nexo/master.key`. Stored secrets cannot be
decrypted without the key. Shut the Pi down cleanly (`sudo shutdown now`) before pulling power.

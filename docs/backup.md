# Backups

A memory card can fail or corrupt without warning, so make a backup after setting the device
up and again after big changes.

## The backup file

**Backup** → *Download a backup* gives you one file, `nexo-backup-YYYY-MM-DD.nexo`, holding:

- the whole database: settings, family members, chats, messages, conversation history,
  schedules, administrator accounts;
- the **encrypted** secrets (Telegram token, API keys, Wi-Fi passwords, calendar address);
- optionally the **master key** that decrypts those secrets.

The file is encrypted with a **passphrase you choose** (at least 12 characters): Argon2id turns it
into a key and the data is sealed with XChaCha20-Poly1305 in chunks that cannot be reordered,
dropped or altered without detection. Without the passphrase the file is unreadable, so it is safe
to keep on a USB stick or in cloud storage. **If you lose the passphrase the backup cannot be
opened; Nexo cannot recover it.**

Include the master key if you want the file to be enough to rebuild the device on a new memory card.
Leave it out only if you keep the key (`/etc/nexo/master.key`) safe separately.

The same file can be made from a terminal (also while Nexo is running, for example from cron):

```sh
sudo -u nexo nexo backup /media/usb/nexo.nexo          # asks for the passphrase
echo "my long passphrase" | sudo -u nexo nexo backup FILE --passphrase-stdin
```

## Restoring

**Same device (admin page, owner only).** *Restore from a backup*: choose the file and enter the passphrase.
Nexo decrypts and checks it, and changes nothing until you press *Restart now*. On restart the
backup replaces the database; the previous one is kept beside it as
`nexo.db.before-restore-<time>`. This works when the backup was made with this device's master
key. It replaces everything, including administrator accounts.

**New device or new card (terminal).** Install Nexo, stop it, restore, start it:

```sh
sudo systemctl stop nexo
sudo nexo restore /media/usb/nexo.nexo --replace-key       # asks for the passphrase
sudo systemctl start nexo
```

`--replace-key` installs the backup's master key. Without it, a backup made with a different key is
refused, because the restored secrets could not be decrypted. Everything is verified (integrity
check, format, no unexpected files) before anything on the device is touched.

## Automatic copies on the device

Nexo also keeps recent copies of its database (default: daily, the last 7) in
`/var/lib/nexo/backups`, taking the first one right after the first start. They protect against a
damaged database file, not against a dead card, because they live on the same card. They are **not
encrypted** (they hold what the database holds) and never include the master key. Change the
schedule, count or folder on the Backup page. Note that Nexo can only write below `/var/lib/nexo`
unless you widen the service's `ReadWritePaths` (for example to a USB stick).

To restore one, stop Nexo and copy it over `nexo.db` (delete `nexo.db-wal` and `nexo.db-shm`).

## What is not backed up

- Music files (they are your own files; copy the music folder separately).
- The Piper voice, whisper.cpp model and other programs you installed.
- Operating-system settings (Wi-Fi networks are saved inside Nexo and re-applied after a restore).

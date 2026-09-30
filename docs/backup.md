# Backups

A memory card can fail or corrupt without warning, so make a backup after setting the device
up and again after big changes.

## The backup file

**Backup** → *Download a backup* gives you one file, `milo-backup-YYYY-MM-DD.milo`, holding:

- the whole database: settings, family members, chats, messages, conversation history,
  schedules, administrator accounts;
- the **encrypted** secrets (Telegram token, API keys, Wi-Fi passwords, calendar address);
- optionally the **master key** that decrypts those secrets.

The file is encrypted with a **passphrase you choose** (at least 12 characters): Argon2id turns it
into a key and the data is sealed with XChaCha20-Poly1305 in chunks that cannot be reordered,
dropped or altered without detection. Without the passphrase the file is unreadable, so it is safe
to keep on a USB stick or in cloud storage. **If you lose the passphrase the backup cannot be
opened; Milo cannot recover it.**

Include the master key if you want the file to be enough to rebuild the device on a new memory card.
Leave it out only if you keep the key (`/etc/milo/master.key`) safe separately.

The same file can be made from a terminal (also while Milo is running, for example from cron):

```sh
sudo -u milo milo backup /media/usb/milo.milo          # asks for the passphrase
echo "my long passphrase" | sudo -u milo milo backup FILE --passphrase-stdin
```

## Restoring

**Same device (**Backup** tile, owner only).** *Restore from a backup*: choose the file and enter the passphrase.
Milo decrypts and checks it, and changes nothing until you press *Restart now*. On restart the
backup replaces the database; the previous one is kept beside it as
`milo.db.before-restore-<time>`. This works when the backup was made with this device's master
key (a backup made without the key is checked against the device's key too, and refused if its
secrets could not be decrypted). It replaces everything, including administrator accounts. Only
one restore can be checked at a time, and the database inside must be at most 1 GiB.

**New device or new card (terminal).** Install Milo, stop it, restore, start it:

```sh
sudo systemctl stop milo
sudo milo restore /media/usb/milo.milo --replace-key       # asks for the passphrase
sudo systemctl start milo
```

`--replace-key` installs the backup's master key. Without it, a backup made with a different key is
refused, because the restored secrets could not be decrypted. Everything is verified (integrity
check, database structure, no unexpected files) before anything on the device is touched.

Each restore keeps the database it replaced as `milo.db.before-restore-<time>`; Milo keeps the newest
three and deletes older ones after a successful restore. They hold the old administrator accounts and
messages unencrypted, so delete them yourself once you no longer need them.

## Automatic copies on the device

Milo also keeps recent copies of its database (default: daily, the last 7) in
`/var/lib/milo/backups`, taking the first one right after the first start. They protect against a
damaged database file, not against a dead card, because they live on the same card. They are **not
encrypted**: each one is a plain copy of the database (file mode 0600), so it holds the administrator
password hashes, messages and the still-encrypted secrets, and it never includes the master key.
Anyone who can read `/var/lib/milo/backups` can read them. Change the
schedule, count or folder in the Backup tile. Note that Milo can only write below `/var/lib/milo`
unless you widen the service's `ReadWritePaths` (for example to a USB stick).

To restore one, stop Milo and copy it over `milo.db` (delete `milo.db-wal` and `milo.db-shm`).

## What is not backed up

- Music files (they are your own files; copy the music folder separately).
- The Piper voice, whisper.cpp model and other programs you installed.
- Operating-system settings (Wi-Fi networks are saved inside Milo and re-applied after a restore).

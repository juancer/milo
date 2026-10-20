#!/bin/sh
# Pulls the rolling "latest" release of the Milo repo and upgrades this device when it changed.
# Runs as root from milo-update.timer. Config in /etc/milo/update.env: REPO=owner/name, GH_TOKEN=...
set -eu
# An explicit PATH: root must not run whatever the environment points at. MILO_UPDATE_PATH, MILO_UPDATE_ENV
# and MILO_UPDATE_STATE_DIR exist so milo-update_test.sh can run this script without root and network;
# systemd never sets them.
PATH=${MILO_UPDATE_PATH:-/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin}
export PATH
. "${MILO_UPDATE_ENV:-/etc/milo/update.env}"

case "$(uname -m)" in
  aarch64) BIN=milo-linux-arm64 ;;
  armv7l|armv6l) BIN=milo-linux-armv7 ;;
  *) echo "unsupported architecture $(uname -m)" >&2; exit 1 ;;
esac

API="https://api.github.com/repos/$REPO/releases"
# The state lives in a directory only root can write. It used to be /var/lib/milo/update-commit, a
# directory the unprivileged milo service owns, so a compromised service could swap the file for a
# symlink and have root overwrite another file. That old file is never read: the first run after this
# change simply installs the current release once more.
STATE_DIR=${MILO_UPDATE_STATE_DIR:-/var/lib/milo-update}
STATE=$STATE_DIR/commit
if [ -L "$STATE_DIR" ]; then echo "$STATE_DIR is a symlink; refusing" >&2; exit 1; fi
install -d -m 0700 "$STATE_DIR"
if [ "$(stat -c %u "$STATE_DIR")" != "$(id -u)" ]; then echo "$STATE_DIR is not owned by the user running the update; refusing" >&2; exit 1; fi
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

gh() { curl -fsSL -H "Authorization: Bearer $GH_TOKEN" "$@"; }
asset_id() { python3 -c 'import json,sys; print(next(a["id"] for a in json.load(sys.stdin)["assets"] if a["name"]==sys.argv[1]))' "$1"; }
fetch() { gh -H "Accept: application/octet-stream" -o "$2" "$API/assets/$(asset_id "$1" < "$TMP/release.json")"; }

gh "$API/tags/latest" -o "$TMP/release.json"
fetch commit.txt "$TMP/commit.txt"
NEW=$(cat "$TMP/commit.txt")
[ "$NEW" = "$(cat "$STATE" 2>/dev/null || true)" ] && exit 0

fetch "$BIN" "$TMP/$BIN"
fetch SHA256SUMS "$TMP/SHA256SUMS"
(cd "$TMP" && grep " $BIN\$" SHA256SUMS | sha256sum -c -)
chmod 755 "$TMP/$BIN"
"$TMP/$BIN" install
# Written to a temp file in the same directory and renamed, so a crash never leaves half a state and a
# link at $STATE is replaced rather than followed.
NEWSTATE=$(mktemp "$STATE_DIR/commit.XXXXXX")
echo "$NEW" > "$NEWSTATE"
mv -f "$NEWSTATE" "$STATE"
echo "updated to $NEW"

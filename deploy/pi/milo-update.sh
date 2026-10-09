#!/bin/sh
# Pulls the rolling "latest" release of the Milo repo and upgrades this device when it changed.
# Runs as root from milo-update.timer. Config in /etc/milo/update.env: REPO=owner/name, GH_TOKEN=...
set -eu
. /etc/milo/update.env

case "$(uname -m)" in
  aarch64) BIN=milo-linux-arm64 ;;
  armv7l|armv6l) BIN=milo-linux-armv7 ;;
  *) echo "unsupported architecture $(uname -m)" >&2; exit 1 ;;
esac

API="https://api.github.com/repos/$REPO/releases"
STATE=/var/lib/milo/update-commit
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
echo "$NEW" > "$STATE"
echo "updated to $NEW"

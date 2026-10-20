#!/bin/sh
# Tests milo-update.sh without root and without network: curl, python3 and the downloaded "milo" binary
# are stubs in a temp directory. Run: sh deploy/pi/milo-update_test.sh
set -eu
HERE=$(cd "$(dirname "$0")" && pwd)
W=$(mktemp -d)
trap 'rm -rf "$W"' EXIT
FAILED=0
fail() { echo "FAIL: $1" >&2; FAILED=1; }
ok() { echo "ok: $1"; }

mkdir "$W/bin" "$W/state-parent"
printf 'REPO=owner/milo\nGH_TOKEN=test\n' > "$W/update.env"
LOG=$W/install.log
: > "$LOG"

# The "release": the commit and a binary that only logs that it was asked to install.
cat > "$W/bin/curl" <<'STUB'
#!/bin/sh
out=; url=
while [ $# -gt 0 ]; do
  case "$1" in -o) out=$2; shift ;; http*) url=$1 ;; esac
  shift
done
name=${url##*/}
case "$url" in
  */tags/latest) echo '{"assets":[]}' > "$out" ;;
  */commit.txt) echo "$FAKE_COMMIT" > "$out" ;;
  */SHA256SUMS) printf '%s  %s\n' "$(printf '#!/bin/sh\necho "$@" >> "%s"\n' "$FAKE_LOG" | sha256sum | cut -d' ' -f1)" "$FAKE_BIN" > "$out" ;;
  */"$FAKE_BIN") printf '#!/bin/sh\necho "$@" >> "%s"\n' "$FAKE_LOG" > "$out" ;;
  *) echo "unexpected url $url" >&2; exit 22 ;;
esac
STUB
cat > "$W/bin/python3" <<'STUB'
#!/bin/sh
# asset_id: python3 -c SCRIPT NAME, with the release json on stdin. The stub answers with the name,
# which the curl stub reads back from the URL.
cat > /dev/null
echo "$3"
STUB
cat > "$W/bin/uname" <<'STUB'
#!/bin/sh
echo aarch64
STUB
chmod 755 "$W/bin/curl" "$W/bin/python3" "$W/bin/uname"

run() {
  PATH="$W/bin:$PATH" MILO_UPDATE_PATH="$W/bin:/usr/bin:/bin" MILO_UPDATE_ENV="$W/update.env" \
    MILO_UPDATE_STATE_DIR="$1" FAKE_COMMIT="$2" FAKE_LOG="$LOG" FAKE_BIN=milo-linux-arm64 \
    sh "$HERE/milo-update.sh" 2>&1
}

STATE_DIR=$W/state-parent/milo-update

# 1. First run installs and writes the state atomically in a private directory.
run "$STATE_DIR" c1 > "$W/out1" || { cat "$W/out1"; fail "first run failed"; }
[ "$(cat "$LOG")" = "install" ] && ok "first run installed once" || fail "install calls: $(cat "$LOG")"
[ "$(cat "$STATE_DIR/commit")" = "c1" ] && ok "state holds the commit" || fail "state content"
[ "$(stat -c %a "$STATE_DIR")" = "700" ] && ok "state directory is 0700" || fail "directory mode $(stat -c %a "$STATE_DIR")"
[ "$(ls "$STATE_DIR")" = "commit" ] && ok "no temp file left behind" || fail "leftovers: $(ls "$STATE_DIR")"

# 2. The same commit again does nothing.
: > "$LOG"
run "$STATE_DIR" c1 > "$W/out2" || fail "second run failed"
[ ! -s "$LOG" ] && ok "same commit: no install" || fail "reinstalled the same commit"

# 3. A link at the state path is replaced, never followed.
printf 'precious\n' > "$W/victim"
rm -f "$STATE_DIR/commit"
ln -s "$W/victim" "$STATE_DIR/commit"
run "$STATE_DIR" c2 > "$W/out3" || { cat "$W/out3"; fail "run with a planted link failed"; }
[ "$(cat "$W/victim")" = "precious" ] && ok "the file behind a planted link is untouched" || fail "victim overwritten: $(cat "$W/victim")"
[ ! -L "$STATE_DIR/commit" ] && [ "$(cat "$STATE_DIR/commit")" = "c2" ] && ok "link replaced by a regular state file" || fail "state is still a link"

# 4. A state directory that is a symlink is refused before anything is downloaded or installed.
: > "$LOG"
mkdir "$W/elsewhere"
ln -s "$W/elsewhere" "$W/state-parent/linked"
if run "$W/state-parent/linked" c3 > "$W/out4"; then fail "a symlinked state directory was accepted"; else ok "symlinked state directory refused"; fi
[ ! -s "$LOG" ] && [ -z "$(ls "$W/elsewhere")" ] && ok "nothing installed or written through the link" || fail "work done through the link"

[ "$FAILED" = 0 ] && echo "all milo-update tests passed" || { echo "milo-update tests FAILED" >&2; exit 1; }

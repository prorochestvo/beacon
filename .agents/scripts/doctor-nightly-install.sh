#!/usr/bin/env bash
#
# doctor-nightly-install.sh moves the nightly doctor audit from hive-warden-01 to
# tomiris-pi. Run it from mac-m1: the two owner-managed secret files are piped from
# warden to the Pi over ssh and never touch the Mac's disk or the terminal.
#
# On tomiris-pi, as buddy, it:
#   1. installs Go into ~/.local/go when no go is on the launcher's PATH;
#   2. links openrouter-cli from the starterkit plugin into ~/.local/bin;
#   3. copies ~/.config/beacon-doctor/mattermost.env and ~/.config/cli-openrouter/.env
#      from warden when the Pi does not have them yet;
#   4. clones the dev branch into the state directory and installs the launcher copy;
#   5. runs the launcher once without the start delay; it posts to Mattermost;
#   6. installs the cron line at the local time that corresponds to 22:00 UTC.
# Then it removes the beacon-doctor cron line from buddy's crontab on warden.
#
# The Pi's clock is not necessarily UTC and Ubuntu's cron has no CRON_TZ, so the cron
# hour and minute are computed on the Pi from its own zone. A zone with daylight
# saving time would need the line recomputed at each switch; Asia/Almaty has none.
#
# Steps 5 and 6 and the warden change run only when every earlier step succeeded, so
# a failed install leaves warden's job running. The script is safe to rerun: existing
# Go, links, secrets and clone are kept, and the cron line is replaced, not duplicated.
#
# Usage: doctor-nightly-install.sh

# SSH command strings below expand $path and the arguments on purpose, on this side.
# shellcheck disable=SC2029
set -euo pipefail

PI_HOST="tomiris-pi"
WARDEN_HOST="hive-warden-01"
GO_VERSION="1.27.3"

log() {
  printf '%s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*"
}

# pi_buddy: runs the stdin script on the Pi as buddy.
pi_buddy() {
  ssh "$PI_HOST" 'sudo -n -u buddy -H bash -s' "$@"
}

# copy_secret PATH: pipes $HOME/PATH from warden's buddy to the Pi's buddy, mode 600,
# unless the Pi already has a non-empty file there.
copy_secret() {
  local path="$1"
  if ssh "$PI_HOST" "sudo -n -u buddy -H bash -c 'test -s ~/$path'"; then
    log "secret $path: present on the Pi, kept"
    return 0
  fi
  ssh "$WARDEN_HOST" "sudo -n -u buddy cat ~buddy/$path" |
    ssh "$PI_HOST" "sudo -n -u buddy -H bash -c 'umask 077; mkdir -p \"\$(dirname ~/$path)\"; cat > ~/$path'"
  ssh "$PI_HOST" "sudo -n -u buddy -H bash -c 'test -s ~/$path'" || {
    log "secret $path: copy came out empty"
    return 1
  }
  log "secret $path: copied"
}

log "tooling on $PI_HOST"
pi_buddy -- "$GO_VERSION" <<'EOF'
set -euo pipefail
go_version="$1"
export PATH="$HOME/.local/bin:/usr/local/bin:/usr/bin:/bin"
mkdir -p "$HOME/.local/bin"

if command -v go >/dev/null 2>&1; then
  echo "go: kept $(go version)"
else
  case "$(uname -m)" in
    aarch64) arch=arm64 ;;
    x86_64) arch=amd64 ;;
    *) echo "go: unsupported machine $(uname -m)" >&2; exit 1 ;;
  esac
  file="go${go_version}.linux-${arch}.tar.gz"
  want=$(curl -fsSL 'https://go.dev/dl/?mode=json&include=all' | python3 -c '
import json, sys
name = sys.argv[1]
for release in json.load(sys.stdin):
    for f in release["files"]:
        if f["filename"] == name:
            print(f["sha256"])
            sys.exit(0)
sys.exit(1)
' "$file")
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  curl -fsSL -o "$tmp/$file" "https://go.dev/dl/$file"
  got=$(gnusha256sum "$tmp/$file" | cut -d ' ' -f 1)
  if [[ "$got" != "$want" ]]; then
    echo "go: checksum mismatch for $file" >&2
    exit 1
  fi
  rm -rf "$HOME/.local/go"
  tar -xzf "$tmp/$file" -C "$HOME/.local"
  ln -sfn "$HOME/.local/go/bin/go" "$HOME/.local/bin/go"
  echo "go: installed $(go version)"
fi

or_target="$HOME/.claude/plugins/marketplaces/starterkit/plugins/core/bin/openrouter-cli"
if [[ ! -x "$or_target" ]]; then
  echo "openrouter-cli: $or_target is missing" >&2
  exit 1
fi
ln -sfn "$or_target" "$HOME/.local/bin/openrouter-cli"
echo "openrouter-cli: linked"

for tool in git curl python3 flock claude gnudate gnutimeout gnusha256sum; do
  command -v "$tool" >/dev/null 2>&1 || { echo "missing tool: $tool" >&2; exit 1; }
done
echo "tools: ok"
EOF

log "secrets from $WARDEN_HOST"
copy_secret ".config/beacon-doctor/mattermost.env"
copy_secret ".config/cli-openrouter/.env"

log "state, test run and cron on $PI_HOST"
pi_buddy <<'EOF'
set -euo pipefail
export PATH="$HOME/.local/bin:/usr/local/bin:/usr/bin:/bin"
state="$HOME/.local/state/beacon-doctor"
launcher="$state/doctor-nightly.sh"

mkdir -p -m 700 "$state"
if [[ -d "$state/repo/.git" ]]; then
  echo "clone: kept"
else
  git clone -q -b dev https://github.com/prorochestvo/beacon.git "$state/repo"
  echo "clone: created"
fi
git -C "$state/repo" fetch -q origin dev
git -C "$state/repo" reset -q --hard FETCH_HEAD
install -m 700 "$state/repo/.agents/scripts/doctor-nightly.sh" "$launcher"
echo "launcher: installed from $(git -C "$state/repo" rev-parse --short HEAD)"

echo "test run: started; it posts to Mattermost"
if ! "$launcher" --max-delay 0 >>"$state/cron.log" 2>&1; then
  echo "test run: failed; see $state/cron.log" >&2
  tail -n 20 "$state/cron.log" >&2
  exit 1
fi
tail -n 3 "$state/cron.log"

# The local wall-clock time of 22:00 UTC today, in the Pi's own zone.
hour=$(gnudate -d '22:00 UTC' +%-H)
minute=$(gnudate -d '22:00 UTC' +%-M)
line="$minute $hour * * * $launcher >>$state/cron.log 2>&1"
{ crontab -l 2>/dev/null | grep -vF "$launcher" || true; printf '%s\n' "$line"; } | crontab -
echo "cron: $line ($(gnudate +%Z) local time)"
EOF

log "removing the job from $WARDEN_HOST"
ssh "$WARDEN_HOST" 'sudo -n -iu buddy bash -s' <<'EOF'
set -euo pipefail
crontab -l 2>/dev/null | grep -v 'beacon-doctor/doctor-nightly.sh' | crontab - || true
if crontab -l 2>/dev/null | grep -q 'beacon-doctor'; then
  echo "warden: beacon-doctor line still present" >&2
  exit 1
fi
echo "warden: cron line removed; state directory kept"
EOF

log "done"

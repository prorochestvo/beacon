#!/usr/bin/env bash
#
# sqlite_dump.sh — snapshot the Beacon SQLite database into /opt/beacon/backups, mirror
# it to Google Drive via rclone, prune both stores, and report the result to the admin
# over Telegram. Runs on the deploy host (be-happy.kz) as root, not locally.
#
# Install: scp this file to /opt/beacon/backups/sqlite_dump.sh, chmod +x, then add a
# daily 00:00 root crontab entry:
#
#   0 0 * * * /opt/beacon/backups/sqlite_dump.sh > /opt/beacon/logs/backup.log 2>&1
#
# Failure model: the LOCAL snapshot and LOCAL retention prune are the critical path and
# ALWAYS run. A Google Drive failure is reported but never aborts local hygiene. The
# script exits non-zero if any step failed.
#
# Telegram reporting: a clean run sends a "#BEACON backup OK" summary (new snapshot size,
# retained inventory, pruned files); any failure sends "#BEACON backup FAILED" with the
# error list. Silence the success summary with NOTIFY_ON_SUCCESS=0. Verify the wiring on
# the host without leaking the token:
#   /opt/beacon/backups/sqlite_dump.sh selftest
#
# Credentials resolve as: (1) ALERT_TELEGRAM_TOKEN + ALERT_TELEGRAM_CHAT_ID from the
# adjacent .env; else (2) BEACON_TELEGRAMBOT_DSN from $MAIN_ENV (default /opt/beacon/.env);
# else (3) reporting disabled (logged once, never fatal).
#
# Optional overrides in the adjacent .env (/opt/beacon/backups/.env), sourced first:
# GDRIVE_REMOTE, LOCAL_RETENTION_DAYS, REMOTE_RETENTION_DAYS, ALERT_TELEGRAM_*, MAIN_ENV,
# NOTIFY_ON_SUCCESS.
#
# NOTE: no `set -e` — a remote (rclone) failure must not skip the local retention prune.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [ -f "${SCRIPT_DIR}/.env" ]; then
    set -a
    # shellcheck disable=SC1091
    . "${SCRIPT_DIR}/.env"
    set +a
fi

BEACON_DIR="/opt/beacon"
BACKUP_DIR="${BEACON_DIR}/backups"
MAIN_ENV="${MAIN_ENV:-${BEACON_DIR}/.env}"
GDRIVE_REMOTE="${GDRIVE_REMOTE:-gdrive:backups/beacon}"
# Local retention is deliberately shorter than remote: the host disk is the scarce
# resource (94% full at the time of writing), while Google Drive is not. The local
# copies exist to make a same-week restore fast, not to be the archive of record —
# anything older is pulled from the remote mirror, which the sync above always
# completes before the local prune below runs.
LOCAL_RETENTION_DAYS="${LOCAL_RETENTION_DAYS:-3}"
REMOTE_RETENTION_DAYS="${REMOTE_RETENTION_DAYS:-14}"
NOTIFY_ON_SUCCESS="${NOTIFY_ON_SUCCESS:-1}"
SNAPSHOT_GLOB='beacon.*.sqlite*'
STAMP="$(date -u +%Y%m%d)"

failures=0
fail_summary=""

log() { printf '%s %s\n' "$(date -u +%FT%TZ)" "$*"; }

fail() {
    failures=$((failures + 1))
    fail_summary="${fail_summary}"$'\n'"- $(html_escape "$*")"
    log "ERROR: $*"
}

# human_size renders a byte count as an IEC unit (e.g. 4.6MiB). Prefers numfmt (GNU),
# falls back to awk so the script also runs where numfmt is absent.
human_size() {
    local b="${1:-0}"
    if command -v numfmt >/dev/null 2>&1; then
        numfmt --to=iec-i --suffix=B "${b}" 2>/dev/null || printf '%sB' "${b}"
    else
        awk -v b="${b}" 'BEGIN{split("B KiB MiB GiB TiB",u," ");i=1;while(b>=1024&&i<5){b/=1024;i++}printf (i==1?"%d%s":"%.1f%s"),b,u[i]}'
    fi
}

# file_size prints the size of $1 in bytes, portable across GNU and BSD stat.
file_size() { stat -c %s "$1" 2>/dev/null || stat -f %z "$1" 2>/dev/null || echo 0; }

# html_escape escapes the three characters that matter for Telegram HTML parse mode, so
# dynamic values (paths, filenames, error text) can never break the message markup.
html_escape() {
    local s="$1"
    s="${s//&/&amp;}"; s="${s//</&lt;}"; s="${s//>/&gt;}"
    printf '%s' "${s}"
}

# resolve_tg_creds populates TG_TOKEN and TG_CHAT (empty when unavailable). The DSN is
# <adminChatID>[:<pw>]@<botToken>: the chat id is the userinfo (Login), the token is the
# host (Addr). Other layouts carry the token in the userinfo password, so validate both
# fields and keep the one that looks like a bot token (<9+ digits>:<35+ url-safe chars>).
resolve_tg_creds() {
    TG_TOKEN=""
    TG_CHAT=""
    if [ -n "${ALERT_TELEGRAM_TOKEN:-}" ] && [ -n "${ALERT_TELEGRAM_CHAT_ID:-}" ]; then
        TG_TOKEN="${ALERT_TELEGRAM_TOKEN}"
        TG_CHAT="${ALERT_TELEGRAM_CHAT_ID}"
        return 0
    fi
    if [ -r "${MAIN_ENV}" ]; then
        local line dsn left right cand
        line="$(grep -E '^BEACON_TELEGRAMBOT_DSN=' "${MAIN_ENV}" | head -n1)"
        dsn="${line#BEACON_TELEGRAMBOT_DSN=}"
        dsn="${dsn%\"}"; dsn="${dsn#\"}"
        dsn="${dsn%\'}"; dsn="${dsn#\'}"
        case "${dsn}" in *"://"*) dsn="${dsn#*://}";; esac
        if [ -n "${dsn}" ]; then
            right="${dsn##*@}"
            left="${dsn%@*}"
            TG_CHAT="${left%%:*}"
            for cand in "${right}" "${left#*:}"; do
                if printf '%s' "${cand}" | grep -qE '^[0-9]{9,}:[A-Za-z0-9_-]{35,}$'; then
                    TG_TOKEN="${cand}"
                    break
                fi
            done
        fi
    fi
}

# tg_send delivers one Telegram message to the admin. Never fatal: any problem here is
# logged and swallowed so reporting can't break the backup.
tg_send() {
    local text="$1"
    resolve_tg_creds
    if [ -z "${TG_TOKEN}" ] || [ -z "${TG_CHAT}" ]; then
        log "WARNING: telegram creds unavailable — message not sent"
        return 0
    fi
    if ! command -v curl >/dev/null 2>&1; then
        log "WARNING: curl absent — message not sent"
        return 0
    fi
    curl -s -m 15 -o /dev/null \
        -X POST "https://api.telegram.org/bot${TG_TOKEN}/sendMessage" \
        --data-urlencode "chat_id=${TG_CHAT}" \
        --data-urlencode "parse_mode=HTML" \
        --data-urlencode "text=${text}" \
        || log "WARNING: telegram sendMessage failed"
}

if [ "${1:-}" = "selftest" ]; then
    resolve_tg_creds
    log "selftest: chat=${TG_CHAT:-<none>} token_len=${#TG_TOKEN}"
    tg_send "#BEACON backup selftest"$'\n'"reporting wired on $(html_escape "$(hostname)") at $(date -u +%FT%TZ)"
    log "selftest: message dispatched (check the admin chat)"
    exit 0
fi

mkdir -p "${BACKUP_DIR}" || fail "cannot create ${BACKUP_DIR}"

src="${BEACON_DIR}/beacon.sqlite"
dst="${BACKUP_DIR}/beacon.${STAMP}.sqlite"
snapshot_made=0

if [ ! -f "${src}" ]; then
    log "skip: ${src} not present"
elif command -v sqlite3 >/dev/null 2>&1; then
    # Online backup: a consistent snapshot even while the services write (WAL mode).
    if sqlite3 "${src}" ".backup '${dst}'"; then
        # Compress the finished snapshot. A SQLite file is mostly sparse pages and
        # repeated text keys and gzips ~4x, which is the difference between the
        # retained set fitting on this host's disk and not: seven dailies of a
        # 200 MB database is 1.4 GB uncompressed against ~1.2 GB of headroom.
        # Compression happens after .backup rather than through a pipe so the
        # snapshot is proven complete on disk before it is rewritten, and so a
        # gzip failure leaves a usable uncompressed snapshot behind.
        # -f overwrites a same-day snapshot, matching what `sqlite3 .backup` already
        # does to the uncompressed file. Without it a second run on the same date —
        # which is exactly what an on-demand snapshot after a deploy is — fails on
        # the existing .gz and leaves two disagreeing snapshots for one day.
        if command -v gzip >/dev/null 2>&1; then
            if gzip -6 -f "${dst}"; then
                dst="${dst}.gz"
                log "backup: ${dst}"
            else
                fail "gzip failed for ${dst} — keeping the uncompressed snapshot"
                log "backup: ${dst} (uncompressed)"
            fi
        else
            log "WARNING: gzip not found — keeping the uncompressed snapshot"
            log "backup: ${dst}"
        fi
        snapshot_made=1
    else
        fail "sqlite3 .backup failed for ${src}"
    fi
else
    # Fallback when the sqlite3 CLI is absent: copy the main file plus its WAL/SHM
    # sidecars so the snapshot can be replayed consistently. Deliberately left
    # uncompressed: the three files only restore as a set, and compressing them
    # individually invites a partial restore where the db is unpacked and its WAL
    # is not — silently replaying nothing, or worse, the wrong pages.
    if cp "${src}" "${dst}" \
        && { [ ! -f "${src}-wal" ] || cp "${src}-wal" "${dst}-wal"; } \
        && { [ ! -f "${src}-shm" ] || cp "${src}-shm" "${dst}-shm"; }; then
        log "backup (cp fallback): ${dst}"
        snapshot_made=1
    else
        fail "cp fallback backup failed for ${src}"
    fi
fi

# Mirror snapshots to Google Drive. `copy` is additive; a remote failure is recorded but
# does NOT skip the local retention prune below.
remote_status="local only (rclone absent)"
if command -v rclone >/dev/null 2>&1; then
    if rclone copy "${BACKUP_DIR}" "${GDRIVE_REMOTE}" --include "${SNAPSHOT_GLOB}"; then
        log "synced to ${GDRIVE_REMOTE}"
        remote_status="synced to ${GDRIVE_REMOTE}"
    else
        fail "rclone copy to ${GDRIVE_REMOTE} failed"
    fi
    if rclone delete "${GDRIVE_REMOTE}" --include "${SNAPSHOT_GLOB}" --min-age "${REMOTE_RETENTION_DAYS}d"; then
        log "remote pruned (retention: ${REMOTE_RETENTION_DAYS}d)"
    else
        fail "rclone remote prune failed"
    fi
else
    log "WARNING: rclone not found — keeping local snapshots only, no Google Drive copy"
fi

# Local retention: keep only the last LOCAL_RETENTION_DAYS days. Record each pruned
# file's name and size BEFORE deleting so the report can list what went. Critical
# hygiene — runs regardless of any remote failure above.
pruned_list=""
pruned_count=0
prune_candidates="$(find "${BACKUP_DIR}" -maxdepth 1 -type f -name "${SNAPSHOT_GLOB}" -mtime "+${LOCAL_RETENTION_DAYS}" 2>/dev/null)"
if [ $? -ne 0 ]; then
    fail "local retention scan failed"
fi
if [ -n "${prune_candidates}" ]; then
    while IFS= read -r f; do
        [ -n "${f}" ] || continue
        sz="$(file_size "${f}")"
        if rm -f "${f}"; then
            pruned_count=$((pruned_count + 1))
            pruned_list="${pruned_list}"$'\n'"- $(html_escape "${f##*/}") $(human_size "${sz}")"
            log "pruned: ${f##*/}"
        else
            fail "failed to remove ${f}"
        fi
    done <<< "${prune_candidates}"
fi

if [ "${failures}" -gt 0 ]; then
    tg_send "#BEACON backup FAILED"$'\n'"${failures} error(s):${fail_summary}"
    log "completed with ${failures} error(s)"
    exit 1
fi

if [ "${NOTIFY_ON_SUCCESS}" = "1" ]; then
    kept_list=""
    kept_count=0
    kept_total=0
    # Both extensions: snapshots taken before compression landed, and the cp
    # fallback's uncompressed set, still count towards the retained inventory.
    for f in "${BACKUP_DIR}"/beacon.*.sqlite "${BACKUP_DIR}"/beacon.*.sqlite.gz; do
        [ -f "${f}" ] || continue
        sz="$(file_size "${f}")"
        kept_count=$((kept_count + 1))
        kept_total=$((kept_total + sz))
        kept_list="${kept_list}"$'\n'"- $(html_escape "${f##*/}") $(human_size "${sz}")"
    done

    if [ "${snapshot_made}" = "1" ]; then
        new_line="new: $(html_escape "${dst##*/}") ($(human_size "$(file_size "${dst}")"))"
    else
        new_line="new: none (source db absent)"
    fi

    if [ "${pruned_count}" -gt 0 ]; then
        pruned_block="pruned ${pruned_count}:${pruned_list}"
    else
        pruned_block="pruned: none"
    fi

    tg_send "#BEACON backup OK"$'\n'"<i>$(html_escape "${remote_status}")</i>"$'\n'"${new_line}"$'\n'"kept ${kept_count} snapshot(s), $(human_size "${kept_total}") total:${kept_list}"$'\n'"${pruned_block}"
fi

log "ok"
exit 0

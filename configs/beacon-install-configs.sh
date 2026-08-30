#!/usr/bin/env bash
#
# Install Beacon's configuration files from the host staging directory to their
# final locations, then reload whatever needs reloading. Installed by `make init`
# to /usr/local/sbin/beacon-install-configs (0755 root:root) and driven from a
# workstation by `make deploy-configs`.
#
# Why a script rather than a NOPASSWD `install`: a passwordless `install` whose
# source and destination come from the command line is root-equivalent by
# construction — the caller picks the destination. Here the destinations are a
# table inside a root-owned file the operator account cannot edit, so the set of
# reachable paths is fixed at provisioning time and the grant is narrow in fact
# rather than only in wording.
#
# Two files are deliberately absent from that table:
#
#   /usr/local/sbin/beacon-install-configs   this script
#   /etc/sudoers.d/beacon-configs            the grant that reaches it
#
# Either one in the table would let the caller rewrite the table, or the grant,
# without a password and then run it as root — the exact escalation the table
# exists to prevent. Both stay with `make init`, which asks for a password. The
# consequence is intended: changing this script means re-running `make init`, and
# `make config-drift` reports the host as behind until that happens.
#
# The staging directory is not under /tmp for the same reason. This runs as root
# and installs whatever it finds there; a world-writable staging path would let
# any local user swap a file between the upload and the install.
#
# Usage:
#   beacon-install-configs [--dry-run]
#
# Exit codes:
#   0  every file is in place (installed now, or already matching)
#   1  refused to run, or a step failed — no file is left half-applied
#   2  bad invocation

set -euo pipefail

STAGING_DIR=/opt/beacon/config-staging
OPERATOR=seil

# staged basename | destination | mode
CONFIG_TABLE=(
    "sqlite_dump.sh|/opt/beacon/backups/sqlite_dump.sh|0755"
    "beacon.service|/etc/systemd/system/beacon.service|0644"
    "beacon-migrate.service|/etc/systemd/system/beacon-migrate.service|0644"
    "nginx.beacon_common_settings.conf|/etc/nginx/snippets/beacon.common_settings.conf|0644"
    "nginx.beacon_gzip.conf|/etc/nginx/snippets/beacon.gzip.conf|0644"
    "nginx.beacon.conf|/etc/nginx/sites-available/dev.seilbekskindirov.beacon|0644"
)

# What has to be reloaded is decided by where a file lands, rather than by a
# fourth column, so the table above stays one readable line per file.
SYSTEMD_UNIT_DIR=/etc/systemd/system
NGINX_CONF_DIR=/etc/nginx

dry_run=""

log() { printf '%s\n' "$*"; }

fail() { printf 'beacon-install-configs: %s\n' "$*" >&2; exit 1; }

usage() {
    cat <<'USAGE'
Usage: beacon-install-configs [--dry-run]

Installs Beacon's staged configuration files to their final locations and reloads
systemd and nginx when the files they own have changed. Takes no path arguments:
the source, destination and mode of every file are fixed inside this script.

  --dry-run   report what would change and exit without writing anything
  --help      this text
USAGE
}

while [ $# -gt 0 ]; do
    case "$1" in
        --dry-run) dry_run="yes" ;;
        --help|-h) usage; exit 0 ;;
        *)
            printf 'beacon-install-configs: unknown argument %s\n' "$1" >&2
            usage >&2
            exit 2
            ;;
    esac
    shift
done

if [ "$(id -u)" -ne 0 ]; then
    fail "must run as root — from a workstation this is 'make deploy-configs', on the host 'sudo beacon-install-configs'"
fi

# The staging directory must be a real directory provisioned by init and writable
# by nobody but its owner. Anything else and its contents are not trustworthy
# input for a process running as root.
if [ -L "${STAGING_DIR}" ]; then
    fail "${STAGING_DIR} is a symlink; expected the directory 'make init' provisions"
fi
if [ ! -d "${STAGING_DIR}" ]; then
    fail "${STAGING_DIR} does not exist — run 'make init' once to bootstrap this host"
fi

staging_owner="$(stat -c '%U' "${STAGING_DIR}")"
staging_mode="$(stat -c '%a' "${STAGING_DIR}")"
if [ "${staging_owner}" != "root" ] && [ "${staging_owner}" != "${OPERATOR}" ]; then
    fail "${STAGING_DIR} is owned by ${staging_owner}; expected root or ${OPERATOR}"
fi
if [ $((8#${staging_mode} & 8#22)) -ne 0 ]; then
    fail "${STAGING_DIR} is mode ${staging_mode}, writable by group or other; expected 0700"
fi

# Validate every source before touching a single destination, so a half-finished
# upload cannot leave the host half-configured.
for entry in "${CONFIG_TABLE[@]}"; do
    IFS='|' read -r name dest _ <<<"${entry}"
    src="${STAGING_DIR}/${name}"

    if [ ! -e "${src}" ]; then
        fail "staged file ${src} is missing — nothing was installed; re-run 'make deploy-configs'"
    fi
    if [ -L "${src}" ]; then
        fail "staged file ${src} is a symlink; refusing to install what it points at"
    fi
    if [ ! -f "${src}" ]; then
        fail "staged file ${src} is not a regular file"
    fi
    if [ ! -s "${src}" ]; then
        fail "staged file ${src} is empty; refusing to install it over ${dest}"
    fi

    dest_dir="$(dirname "${dest}")"
    if [ ! -d "${dest_dir}" ]; then
        fail "${dest_dir} does not exist — run 'make init' once to bootstrap this host"
    fi
done

# A file needs installing when its bytes, its mode or its ownership differ. Mode
# and ownership are compared too because a file that drifted only in permissions
# is still drift, and a re-run with unchanged content must repair it.
needs_install() {
    local src="$1" dest="$2" mode="$3"

    [ -f "${dest}" ] || return 0
    cmp -s "${src}" "${dest}" || return 0
    [ "$(stat -c '%a' "${dest}")" = "${mode#0}" ] || return 0
    [ "$(stat -c '%U:%G' "${dest}")" = "root:root" ] || return 0
    return 1
}

changed=()
changed_systemd=""
changed_nginx=""

for entry in "${CONFIG_TABLE[@]}"; do
    IFS='|' read -r name dest mode <<<"${entry}"
    src="${STAGING_DIR}/${name}"

    if needs_install "${src}" "${dest}" "${mode}"; then
        changed+=("${entry}")
        case "${dest}" in
            "${SYSTEMD_UNIT_DIR}"/*) changed_systemd="yes" ;;
            "${NGINX_CONF_DIR}"/*)   changed_nginx="yes" ;;
        esac
    else
        log "same      ${dest}"
    fi
done

# Always, success or failure: the repository is the source of truth and
# `make deploy-configs` re-uploads on every run, so leaving root-installable
# content lying about on the host buys nothing.
clear_staging() {
    find "${STAGING_DIR}" -mindepth 1 -maxdepth 1 -exec rm -rf {} +
}

if [ "${#changed[@]}" -eq 0 ]; then
    log ""
    log "beacon-install-configs: nothing to do; every file already matches."
    if [ -z "${dry_run}" ]; then
        clear_staging
    fi
    exit 0
fi

if [ -n "${dry_run}" ]; then
    for entry in "${changed[@]}"; do
        IFS='|' read -r _ dest _ <<<"${entry}"
        log "would update ${dest}"
    done
    log ""
    log "beacon-install-configs: dry run; nothing was written and the staging directory was kept."
    exit 0
fi

# Rollback set. mktemp gives a 0700 root-owned directory, so copies of whatever is
# being replaced are no more readable than the originals were.
backup_dir="$(mktemp -d)"
restore_paths=()
restore_sources=()
committed=""

restore_previous() {
    local i
    for i in "${!restore_paths[@]}"; do
        if [ -n "${restore_sources[$i]}" ]; then
            cp -p -T "${restore_sources[$i]}" "${restore_paths[$i]}"
        else
            rm -f "${restore_paths[$i]}"
        fi
    done
    if [ -n "${changed_systemd}" ]; then
        systemctl daemon-reload
    fi
    # nginx is deliberately not reloaded here: this path is only reachable before a
    # successful reload, so the running configuration is already the previous one.
}

# Any exit between the first install and the final commit puts the previous files
# back. Without this, a failure partway through the table would leave the host
# running a mixture of two revisions — the silent half-state this whole mechanism
# exists to make impossible.
on_exit() {
    local status=$?
    if [ "${#restore_paths[@]}" -gt 0 ] && [ -z "${committed}" ]; then
        printf 'beacon-install-configs: rolling back to the previous configuration.\n' >&2
        restore_previous
    fi
    rm -rf "${backup_dir}"
    exit "${status}"
}
trap on_exit EXIT

for entry in "${changed[@]}"; do
    IFS='|' read -r name dest mode <<<"${entry}"
    src="${STAGING_DIR}/${name}"

    if [ -f "${dest}" ]; then
        backup="${backup_dir}/$(printf '%s' "${dest}" | tr '/' '_')"
        cp -p -T "${dest}" "${backup}"
        restore_sources+=("${backup}")
    else
        restore_sources+=("")
    fi
    restore_paths+=("${dest}")

    install -o root -g root -m "${mode}" "${src}" "${dest}"
    log "updated   ${dest}"
done

if [ -n "${changed_systemd}" ]; then
    systemctl daemon-reload
    log "reloaded  systemd unit files"
fi

if [ -n "${changed_nginx}" ]; then
    # Validate before reloading, and let the rollback trap put the previous files
    # back if the new set does not parse. Leaving a broken config on disk would be
    # its own outage on a host serving five other sites: the next `nginx -t`
    # anyone runs, for any reason, would fail on Beacon's file.
    if ! nginx -t; then
        fail "nginx rejected the new configuration; nothing was reloaded"
    fi
    nginx -s reload
    log "reloaded  nginx"
fi

committed="yes"
clear_staging

log ""
log "beacon-install-configs: ${#changed[@]} file(s) updated."
if [ -n "${changed_systemd}" ]; then
    log "beacon-install-configs: a unit file changed — daemon-reload has run, but the running"
    log "beacon-install-configs: service keeps its old unit until 'systemctl restart beacon'."
fi

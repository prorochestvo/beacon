#!/usr/bin/env bash
#
# Report which host configuration files no longer match the repository.
#
# The release pipeline uploads binaries and flips a symlink; it never touches these
# files. So a change to any of them ships in the repository, passes CI, appears in a
# release, and silently does not take effect — which is exactly what happened to
# snapshot compression, live in git for three weeks while the host kept writing
# uncompressed backups.
#
# `make deploy-configs` now delivers most of them without a password, and ends by
# running this check as its own proof. Three still reach the host only through
# `make init`: the two sudoers grants and the installer script `deploy-configs`
# drives. Those are the privilege boundary — an installer that could rewrite its own
# destination table, or the grant that reaches it, would be passwordless root — so
# they are deliberately outside what a passwordless deploy can touch, and this check
# is what reports them as behind.
#
# This turns that silence into a line of output. It changes nothing on the host and needs
# no privileges beyond reading.
#
# The file list is parsed out of the Makefile's init recipe rather than kept in a manifest
# beside it. A manifest would be a second place to update, and the failure mode of
# forgetting is precisely the one being fixed here: a config nobody checks. Reading the
# recipe means the check covers whatever init installs, by construction.
#
# Usage:
#   configs/config_drift.sh [makefile]
#
# Environment:
#   SSH_CMD   how to reach the host (default: "ssh be-happy.kz"). The release job passes
#             its own key and port.
#
# Exit codes:
#   0  every checked file matches
#   1  at least one file differs or was never installed
#   2  the check could not run (recipe unparseable, host unreachable)

set -uo pipefail

MAKEFILE="${1:-Makefile}"
SSH_CMD="${SSH_CMD:-ssh be-happy.kz}"

# Files installed by init that are NOT expected to match.
#
# backups/.env is a template: init copies sqlite_dump.env.example into place only when the
# file is absent, and the operator then edits it with the real Google Drive remote and
# Telegram credentials. It is supposed to diverge, and comparing it would report drift on
# every run forever — the kind of permanent false positive that trains people to ignore the
# whole check.
EXCLUDED_HOST_PATHS=(
    "/opt/beacon/backups/.env"
)

# A recipe that stops matching would otherwise report a clean bill of health for zero
# files, which is worse than not checking at all: it would look like proof.
#
# This is the count the install table currently yields, not a loose floor: adding a
# file leaves the guard passing, while dropping one forces whoever dropped it to say
# so here. `make deploy-configs` runs this check as its final gate, so a partial parse
# would otherwise let a deploy claim success over files it never compared.
MIN_EXPECTED_FILES=9

if [ ! -f "${MAKEFILE}" ]; then
    echo "config-drift: ${MAKEFILE} not found" >&2
    exit 2
fi

# Parse `sudo install -m MODE /tmp/BASENAME HOSTPATH` out of the init recipe. The basename
# is the repo file's, because init scps ./configs/* into /tmp unchanged.
declare -a REPO_FILES=()
declare -a HOST_FILES=()

while read -r basename hostpath; do
    [ -n "${basename}" ] || continue

    skip=""
    for excluded in "${EXCLUDED_HOST_PATHS[@]}"; do
        if [ "${hostpath}" = "${excluded}" ]; then
            skip="yes"
            break
        fi
    done
    [ -z "${skip}" ] || continue

    repo="configs/${basename}"
    if [ ! -f "${repo}" ]; then
        echo "config-drift: init installs ${basename} but ${repo} does not exist" >&2
        exit 2
    fi
    REPO_FILES+=("${repo}")
    HOST_FILES+=("${hostpath}")
done < <(grep -oE 'install -m [0-7]{4} /tmp/[^ ]+ [^ ;]+' "${MAKEFILE}" \
    | awk '{ n = $4; sub(/^\/tmp\//, "", n); print n, $5 }')

if [ "${#REPO_FILES[@]}" -lt "${MIN_EXPECTED_FILES}" ]; then
    echo "config-drift: parsed only ${#REPO_FILES[@]} file(s) from ${MAKEFILE}, expected at least ${MIN_EXPECTED_FILES}" >&2
    echo "config-drift: the init recipe's install lines probably changed shape — fix this script rather than trusting it" >&2
    exit 2
fi

# One round trip: send every path, get back "path<TAB>state<TAB>checksum".
#
# The three failure states are kept apart deliberately. MISSING means the file was never
# provisioned; DIFFERS means it was and has since fallen behind; UNREADABLE means the check
# has no opinion, which is not the same as a clean result and must not be reported as one.
#
# The single quotes are the point: $p is the remote shell's loop variable, and expanding
# it here would send a script with the paths already substituted away.
# shellcheck disable=SC2016
remote_output="$(printf '%s\n' "${HOST_FILES[@]}" | ${SSH_CMD} 'while IFS= read -r p; do
    if [ ! -e "$p" ]; then
        printf "%s\tMISSING\t-\n" "$p"
    elif [ ! -r "$p" ]; then
        printf "%s\tUNREADABLE\t-\n" "$p"
    else
        printf "%s\tOK\t%s\n" "$p" "$(sha256sum "$p" | cut -d" " -f1)"
    fi
done')"

if [ -z "${remote_output}" ]; then
    echo "config-drift: no response from the host (${SSH_CMD})" >&2
    exit 2
fi

drift=0
unknown=0
printf '%-10s %s\n' "STATE" "PATH"

for i in "${!HOST_FILES[@]}"; do
    hostpath="${HOST_FILES[$i]}"
    repo="${REPO_FILES[$i]}"

    line="$(printf '%s\n' "${remote_output}" | grep -F "${hostpath}"$'\t' | head -n1)"
    state="$(printf '%s' "${line}" | cut -f2)"
    remote_sum="$(printf '%s' "${line}" | cut -f3)"
    local_sum="$(sha256sum "${repo}" | cut -d' ' -f1)"

    case "${state}" in
        MISSING)
            printf '%-10s %s\n' "MISSING" "${hostpath}"
            drift=1
            ;;
        UNREADABLE)
            # Root-only files (the sudoers grant) land here. Reporting them as drift would
            # be a false alarm; reporting them as fine would be a false negative.
            printf '%-10s %s (root-only; cannot compare)\n' "UNKNOWN" "${hostpath}"
            unknown=1
            ;;
        OK)
            if [ "${remote_sum}" = "${local_sum}" ]; then
                printf '%-10s %s\n' "same" "${hostpath}"
            else
                printf '%-10s %s  <- %s\n' "DIFFERS" "${hostpath}" "${repo}"
                drift=1
            fi
            ;;
        *)
            printf '%-10s %s (no answer from host)\n' "UNKNOWN" "${hostpath}"
            unknown=1
            ;;
    esac
done

echo
if [ "${drift}" -ne 0 ]; then
    cat <<'FIX'
config-drift: the host is running configuration older than this repository.

    make deploy-configs

ships every file above except these three, which are the privilege boundary and reach
the host only through the one privileged bootstrap:

    /etc/sudoers.d/beacon-deploy
    /etc/sudoers.d/beacon-configs
    /usr/local/sbin/beacon-install-configs

If one of those is what drifted, run `make init` — it asks for a password, and that is
the point.

FIX
    exit 1
fi

if [ "${unknown}" -ne 0 ]; then
    echo "config-drift: everything comparable matches; the entries above could not be read."
    exit 0
fi

echo "config-drift: every checked file matches the repository."
exit 0

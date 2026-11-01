#!/usr/bin/env bash
#
# Assert the two cache-header invariants that the hashed-asset location in
# configs/nginx.beacon_common_settings.conf encodes:
#
#   live hashed asset      exactly one Cache-Control, carrying immutable, no Expires
#   retired hashed asset   no Cache-Control at all
#
# Both are argued at length in that file, in comments. A comment does not fail a
# build. Reintroducing `always` on the add_header would pin a 404 in every client
# cache for a week — a state no page reload recovers from — and adding `expires`
# beside it would emit a second max-age that a strict cache may treat as
# unparseable, discarding the `immutable` the whole content-hashing scheme buys.
# Neither shows up in `nginx -t`; both show up here.
#
# The retired URL is a random hash rather than a fixed sentinel, so it necessarily
# matches the location's regex while missing the origin's asset registry. That is
# the only way to observe the `always` regression: a URL that failed the regex
# would fall through to the catch-all and carry no Cache-Control for reasons that
# have nothing to do with this policy.
#
# Why it retries. nginx's graceful reload keeps old workers alive until their
# connections drain, and Cloudflare holds keepalive connections to the origin: on
# 2026-08-13 the previous headers were still being served roughly ninety seconds
# after a successful reload. A single assertion would fail on a correct deploy.
#
# The check reads only. It runs through Cloudflare because the origin refuses
# connections without the edge's client certificate.
#
# Usage:
#   configs/verify_edge.sh [origin]
#
# Environment:
#   BEACON_ORIGIN            default origin, if no argument is given
#   VERIFY_EDGE_WINDOW       seconds to keep retrying (default 180)
#   VERIFY_EDGE_INTERVAL     seconds between attempts (default 10)
#
# Exit codes:
#   0  both invariants hold
#   1  an invariant is broken
#   2  the check could not run (origin unreachable, no hashed asset to test)

set -uo pipefail

ORIGIN="${1:-${BEACON_ORIGIN:-https://beacon.seilbekskindirov.dev}}"
ORIGIN="${ORIGIN%/}"
WINDOW="${VERIFY_EDGE_WINDOW:-180}"
INTERVAL="${VERIFY_EDGE_INTERVAL:-10}"

CURL_TIMEOUT=20

fatal() { printf 'verify-edge: %s\n' "$*" >&2; exit 2; }

# headers_of prints the response headers of a HEAD request, lowercased and with the
# CR stripped, so every match below is a plain lowercase comparison regardless of
# HTTP version.
headers_of() {
    curl -sSI --max-time "${CURL_TIMEOUT}" "$1" 2>/dev/null | tr -d '\r' | tr '[:upper:]' '[:lower:]'
}

count_lines() { printf '%s\n' "$1" | grep -c "$2"; }

count_occurrences() { printf '%s\n' "$1" | grep -o "$2" | grep -c .; }

# The status comes off the last status line rather than a substring search over the
# whole response: a 404 whose body happens to be 200 bytes carries "content-length:
# 200", which a naive match reads as success.
status_of() { printf '%s\n' "$1" | grep -E '^http/' | tail -n1 | awk '{print $2}'; }

# max-age and immutable are counted inside the Cache-Control lines only. Any HSTS
# header the edge adds carries its own max-age, and counting it would fail a
# correct origin.
cache_control_of() { printf '%s\n' "$1" | grep '^cache-control:'; }

random_hash() { printf '%04x%04x' "$((RANDOM % 65536))" "$((RANDOM % 65536))"; }

# live_asset_path extracts one content-hashed asset URL from the dispatcher page.
# It is read from the live page rather than computed here because the hash is the
# build's, and a stale guess would test a URL that is itself retired.
live_asset_path() {
    curl -fsS --max-time "${CURL_TIMEOUT}" "${ORIGIN}/" 2>/dev/null \
        | grep -oE '/(app|wasm_exec)\.[a-f0-9]{8}\.(wasm|js)' \
        | head -n1
}

# attempt returns 0 when both invariants hold, 1 when one is broken, 2 when the
# check could not run. It writes the reason of a non-zero result to $reason.
reason=""
attempt() {
    reason=""

    local live_path
    live_path="$(live_asset_path)"
    if [ -z "${live_path}" ]; then
        reason="no content-hashed asset found on ${ORIGIN}/ — is the origin up, and does the page still reference /app.<hash>.wasm?"
        return 2
    fi

    local live_hash retired_hash
    live_hash="$(printf '%s' "${live_path}" | grep -oE '[a-f0-9]{8}')"
    retired_hash="$(random_hash)"
    while [ "${retired_hash}" = "${live_hash}" ]; do
        retired_hash="$(random_hash)"
    done

    local live_headers retired_headers
    live_headers="$(headers_of "${ORIGIN}${live_path}")"
    retired_headers="$(headers_of "${ORIGIN}/app.${retired_hash}.wasm")"

    if [ -z "${live_headers}" ] || [ -z "${retired_headers}" ]; then
        reason="no response headers from ${ORIGIN}"
        return 2
    fi

    local live_status retired_status
    live_status="$(status_of "${live_headers}")"
    retired_status="$(status_of "${retired_headers}")"

    if [ "${live_status}" != "200" ]; then
        reason="${live_path} answered ${live_status:-nothing}, not 200; the page advertises an asset the origin does not serve"
        return 2
    fi
    if [ "${retired_status}" != "404" ]; then
        reason="/app.${retired_hash}.wasm answered ${retired_status:-nothing}, not 404; the retired-asset invariant cannot be tested"
        return 2
    fi

    # Live asset: exactly one Cache-Control header, exactly one max-age inside it,
    # carrying immutable, and no Expires beside it. The max-age count is separate
    # from the header count because a cache that merges duplicate fields into one
    # comma-joined value would otherwise hide the `expires` regression.
    local live_cc cc_lines max_ages expires_lines
    live_cc="$(cache_control_of "${live_headers}")"
    cc_lines="$(count_lines "${live_headers}" '^cache-control:')"
    max_ages="$(count_occurrences "${live_cc}" 'max-age=')"
    expires_lines="$(count_lines "${live_headers}" '^expires:')"

    if [ "${cc_lines}" -ne 1 ]; then
        reason="${live_path} carries ${cc_lines} Cache-Control header(s), expected exactly 1 — 'expires' has probably returned alongside the add_header"
        return 1
    fi
    if [ "${max_ages}" -ne 1 ]; then
        reason="${live_path} carries ${max_ages} max-age directives, expected exactly 1 — 'expires' has probably returned alongside the add_header"
        return 1
    fi
    if [ "${expires_lines}" -ne 0 ]; then
        reason="${live_path} carries an Expires header; the add_header must be the sole cache policy on these paths"
        return 1
    fi
    case "${live_cc}" in
        *immutable*) ;;
        *)
            reason="${live_path} has a Cache-Control without immutable; the seven-day pin the content hashing buys is gone"
            return 1
            ;;
    esac

    # Retired asset: no cache policy at all. A Cache-Control here means `always` is
    # back on the add_header and clients are being told to cache a 404.
    local retired_cc retired_expires
    retired_cc="$(count_lines "${retired_headers}" '^cache-control:')"
    retired_expires="$(count_lines "${retired_headers}" '^expires:')"

    if [ "${retired_cc}" -ne 0 ]; then
        reason="the retired asset /app.${retired_hash}.wasm 404s with a Cache-Control header — 'always' is back on the add_header, and clients will cache that 404 for a week"
        return 1
    fi
    if [ "${retired_expires}" -ne 0 ]; then
        reason="the retired asset /app.${retired_hash}.wasm 404s with an Expires header"
        return 1
    fi

    printf 'verify-edge: %s carries one immutable Cache-Control and no Expires\n' "${live_path}"
    printf 'verify-edge: a retired hashed asset 404s with no cache policy\n'
    return 0
}

deadline=$(($(date +%s) + WINDOW))
status=0

while :; do
    attempt
    status=$?
    [ "${status}" -eq 0 ] && break

    now=$(date +%s)
    if [ "${now}" -ge "${deadline}" ]; then
        break
    fi

    printf 'verify-edge: %s\n' "${reason}" >&2
    printf 'verify-edge: retrying in %ss (nginx drains old workers and Cloudflare holds keepalives; %ss of the window left)\n' \
        "${INTERVAL}" "$((deadline - now))" >&2
    sleep "${INTERVAL}"
done

if [ "${status}" -eq 0 ]; then
    printf 'verify-edge: edge cache invariants hold.\n'
    exit 0
fi

printf 'verify-edge: FAILED after %ss — %s\n' "${WINDOW}" "${reason}" >&2
if [ "${status}" -eq 2 ]; then
    fatal "the check could not run; nothing was proved either way"
fi
exit 1

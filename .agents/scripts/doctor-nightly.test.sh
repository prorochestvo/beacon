#!/usr/bin/env bash
#
# Fixture tests for doctor-nightly.sh.
#
# Linux only: needs GNU coreutils (gnudate, gnutimeout, gnusha256sum; on hosts
# without the gnu-prefixed names the plain GNU tools are used) and util-linux
# flock. Every case runs a copy of the launcher under a temporary HOME. The
# external commands the launcher calls (git, go, claude, openrouter-cli, curl,
# jev-cli and the built doctor and migrator) are stubs that record their
# arguments and replay the fixture of the case, testdata/doctor-nightly/<case>/.
# The webhook URL in the fake mattermost.env is a secret: no recorded file may
# contain it, and assert_no_secret checks that after every case.
#
# Usage: doctor-nightly.test.sh [CASE...]

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="$HERE/doctor-nightly.sh"
FIXTURES="$HERE/testdata/doctor-nightly"
TOKEN="s3cr3thookid0123456789abcd"
SENTINEL="https://mm.invalid/hooks/inheritedsentinel0123456789"

PASS=0
FAIL=0
CASE=""
HOME_DIR=""
LAST_RC=""
ROOT_TMP=""
GNU_DATE=""
GNU_TIMEOUT=""
GNU_SHA256SUM=""

die() {
  printf 'doctor-nightly.test: %s\n' "$*" >&2
  exit 1
}

# find_gnu NAME FALLBACK: prints the path of a GNU coreutils tool.
find_gnu() {
  local candidate="" version=""
  if [[ -x "/usr/bin/gnu$1" ]]; then
    printf '%s\n' "/usr/bin/gnu$1"
    return 0
  fi
  candidate=$(command -v "$2" || true)
  if [[ -n "$candidate" ]]; then
    version=$("$candidate" --version 2>/dev/null || true)
  fi
  if [[ "${version%%$'\n'*}" == *'GNU coreutils'* ]]; then
    printf '%s\n' "$candidate"
    return 0
  fi
  return 1
}

require_tools() {
  GNU_DATE=$(find_gnu date date) || die "GNU coreutils date not found (gnudate); the test runs on Linux only"
  GNU_TIMEOUT=$(find_gnu timeout timeout) || die "GNU coreutils timeout not found (gnutimeout)"
  GNU_SHA256SUM=$(find_gnu sha256sum sha256sum) || die "GNU coreutils sha256sum not found (gnusha256sum)"
  if ! command -v flock >/dev/null 2>&1; then
    die "flock not found (util-linux)"
  fi
}

# check LABEL COMMAND...: records one assertion.
check() {
  local label="$1"
  shift
  if "$@"; then
    PASS=$((PASS + 1))
  else
    FAIL=$((FAIL + 1))
    printf 'FAIL %s: %s (stdout: %s)\n' "$CASE" "$label" "$HOME_DIR/stdout.txt" >&2
  fi
}

# stub PATH NAME: writes an executable that records its arguments in calls/NAME.txt,
# then runs the body read from stdin.
stub() {
  {
    printf '#!/usr/bin/env bash\n'
    cat <<EOF
printf '%s\n' "\$*" >>"\$HOME/calls/$2.txt"
EOF
    cat
  } >"$1"
  chmod +x "$1"
}

# install_stubs BIN_DIR: GNU links and the external command stubs.
install_stubs() {
  local bin="$1"
  ln -s "$GNU_DATE" "$bin/gnudate"
  ln -s "$GNU_TIMEOUT" "$bin/gnutimeout"
  ln -s "$GNU_SHA256SUM" "$bin/gnusha256sum"

  stub "$bin/git" git <<'EOF'
case "$*" in
  *rev-parse*)
    if [[ ! -f "$HOME/fixture/git.norev" ]]; then
      echo 0123456789abcdef0123456789abcdef01234567
    fi
    ;;
esac
exit 0
EOF

  stub "$bin/go" go <<'EOF'
if [[ -f "$HOME/fixture/go.fail" ]]; then
  echo "go: build failed" >&2
  exit 1
fi
out=""
prev=""
for arg in "$@"; do
  if [[ "$prev" == -o ]]; then
    out="$arg"
  fi
  prev="$arg"
done
case "$out" in
  */doctor|*/migrator) ln -sf "$HOME/.local/bin/replay" "$out" ;;
esac
exit 0
EOF

  stub "$bin/claude" claude <<'EOF'
pwd >>"$HOME/calls/claude-cwd.txt"
env >>"$HOME/calls/claude-env.txt"
cat >"$HOME/calls/claude-stdin.txt"
if [[ -f "$HOME/fixture/claude.json" ]]; then
  cat "$HOME/fixture/claude.json"
fi
if [[ -f "$HOME/fixture/claude.exit" ]]; then
  exit "$(cat "$HOME/fixture/claude.exit")"
fi
exit 0
EOF

  stub "$bin/openrouter-cli" openrouter <<'EOF'
cat >/dev/null
if [[ -f "$HOME/fixture/openrouter.out" ]]; then
  cat "$HOME/fixture/openrouter.out"
fi
if [[ -f "$HOME/fixture/openrouter.exit" ]]; then
  exit "$(cat "$HOME/fixture/openrouter.exit")"
fi
exit 0
EOF

  # curl stands in for the webhook post. It refuses any argv whose first argument is not
  # --disable, compares the URL in its --config descriptor with the fake env file without
  # printing either, and records the decoded post text. It answers with fixture/curl.http.
  stub "$bin/curl" curl <<'EOF'
if [[ "${1:-}" != --disable ]]; then
  printf 'first-arg-not-disable\n' >>"$HOME/calls/curl-violations.txt"
  exit 99
fi
env >>"$HOME/calls/curl-env.txt"
config=""
prev=""
for arg in "$@"; do
  if [[ "$prev" == --config ]]; then
    config="$arg"
  fi
  prev="$arg"
done
url=""
if [[ -n "$config" ]]; then
  url=$(sed -n 's/^url = "\(.*\)"$/\1/p' "$config")
fi
expected=$(sed -n 's#^MM_WEBHOOK_URL=##p' "$HOME/.config/beacon-doctor/mattermost.env" | tr -d '\r')
if [[ -n "$url" && "$url" == "$expected" ]]; then
  printf 'match\n' >"$HOME/calls/curl-config.txt"
else
  printf 'mismatch\n' >"$HOME/calls/curl-config.txt"
fi
payload=$(cat)
printf '%s' "$payload" >>"$HOME/calls/mattermost-payload.txt"
printf '%s' "$payload" | python3 -c 'import json,sys; sys.stdout.write(json.load(sys.stdin)["text"] + "\n---\n")' >>"$HOME/calls/mattermost-messages.txt"
if [[ -f "$HOME/fixture/curl.http" ]]; then
  printf '%s' "$(cat "$HOME/fixture/curl.http")"
else
  printf '200'
fi
if [[ -f "$HOME/fixture/curl.exit" ]]; then
  exit "$(cat "$HOME/fixture/curl.exit")"
fi
exit 0
EOF

  # replay is the doctor and migrator binary: go links the -o path to it.
  cat >"$bin/replay" <<'EOF'
#!/usr/bin/env bash
name="$(basename -- "$0")"
printf '%s %s\n' "$name" "$*" >>"$HOME/calls/binaries.txt"
if [[ "$name" == doctor ]]; then
  if [[ -f "$HOME/fixture/audit.out" ]]; then
    cat "$HOME/fixture/audit.out"
  fi
  if [[ -f "$HOME/fixture/audit.err" ]]; then
    cat "$HOME/fixture/audit.err" >&2
  fi
  exit "$(cat "$HOME/fixture/audit.exit" 2>/dev/null || echo 0)"
fi
exit 0
EOF
  chmod +x "$bin/replay"
}

# install_cli_stubs: the CLI the launcher calls by absolute path under HOME.
install_cli_stubs() {
  stub "$HOME_DIR/.claude/skills/cli-jev/jev-cli" jev <<'EOF'
cat >/dev/null
if [[ -f "$HOME/fixture/jev.out" ]]; then
  cat "$HOME/fixture/jev.out"
fi
if [[ -f "$HOME/fixture/jev.exit" ]]; then
  exit "$(cat "$HOME/fixture/jev.exit")"
fi
exit 0
EOF
}

# place_transient STATE_DIR: maps fixture/transient/{today,minus1,minus2}.txt to real UTC dates.
place_transient() {
  local state="$1" f base when="" date_value=""
  if [[ ! -d "$HOME_DIR/fixture/transient" ]]; then
    return 0
  fi
  for f in "$HOME_DIR/fixture/transient"/*.txt; do
    base=$(basename "$f" .txt)
    case "$base" in
      today) when="today" ;;
      minus1) when="-1 day" ;;
      minus2) when="-2 days" ;;
      *) die "unknown transient fixture $base" ;;
    esac
    date_value=$("$GNU_DATE" -u -d "$when" +%F)
    cp "$f" "$state/transient/$date_value.txt"
  done
  rm -rf "$HOME_DIR/fixture/transient"
}

# write_env LINE...: writes the fake mattermost.env of the current HOME, mode 600.
write_env() {
  local env_file="$HOME_DIR/.config/beacon-doctor/mattermost.env"
  printf '%s\n' "$@" >"$env_file"
  chmod 600 "$env_file"
}

# setup_home FIXTURE: a fresh HOME with stubs, the launcher copies and the fixture.
setup_home() {
  local fixture="$1" state bin
  HOME_DIR=$(mktemp -d "$ROOT_TMP/home.XXXXXX")
  bin="$HOME_DIR/.local/bin"
  state="$HOME_DIR/.local/state/beacon-doctor"
  mkdir -p "$bin" "$HOME_DIR/calls" "$HOME_DIR/fixture" \
    "$HOME_DIR/.claude/skills/cli-jev" "$HOME_DIR/.config/beacon-doctor" \
    "$state/repo/.agents/scripts" "$state/bin" "$state/runs" "$state/transient"
  install_stubs "$bin"
  install_cli_stubs
  write_env "MM_WEBHOOK_URL=https://mm.invalid/hooks/$TOKEN"
  cp "$SCRIPT" "$state/doctor-nightly.sh"
  cp "$SCRIPT" "$state/repo/.agents/scripts/doctor-nightly.sh"
  if [[ -d "$FIXTURES/$fixture" ]]; then
    cp -R "$FIXTURES/$fixture/." "$HOME_DIR/fixture/"
  fi
  if [[ ! -f "$HOME_DIR/fixture/claude.json" ]]; then
    printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"structured_output":{"verdict":"ok","message":"No rule problems found."}}' >"$HOME_DIR/fixture/claude.json"
  fi
  mkdir -p "$state/repo/.git"
  place_transient "$state"
}

# break_gnudate: replaces the GNU date link with a failing command.
break_gnudate() {
  rm -f "$HOME_DIR/.local/bin/gnudate"
  printf '#!/usr/bin/env bash\nexit 127\n' >"$HOME_DIR/.local/bin/gnudate"
  chmod +x "$HOME_DIR/.local/bin/gnudate"
}

# run_launcher [VAR=VALUE...]: runs the installed launcher copy with --max-delay 0 in a
# cleared environment plus the given variables; prints its exit code.
run_launcher() {
  local rc=0
  env -i HOME="$HOME_DIR" PATH=/usr/bin:/bin "$@" \
    bash "$HOME_DIR/.local/state/beacon-doctor/doctor-nightly.sh" --max-delay 0 \
    >"$HOME_DIR/stdout.txt" 2>&1 || rc=$?
  printf '%s' "$rc"
}

state_dir() {
  printf '%s/.local/state/beacon-doctor' "$HOME_DIR"
}

today_transient() {
  printf '%s/transient/%s.txt' "$(state_dir)" "$("$GNU_DATE" -u +%F)"
}

message_count() {
  local f="$HOME_DIR/calls/mattermost-messages.txt" n=0
  if [[ -f "$f" ]]; then
    n=$(grep -c '^---$' "$f" || true)
  fi
  printf '%s' "$n"
}

# message_chars: Unicode code points (Mattermost's measure, not bytes) of the message in the
# calls file, without the stub's separator. It is the only message of a case that posts once.
message_chars() {
  python3 -c '
import sys
data = open(sys.argv[1], "rb").read().decode("utf-8")
end = "\n---\n"
if data.endswith(end):
    data = data[: -len(end)]
print(len(data))
' "$HOME_DIR/calls/mattermost-messages.txt"
}

# message_bytes: bytes of the last message, without the stub's separator.
message_bytes() {
  python3 -c '
import sys
data = open(sys.argv[1], "rb").read()
end = b"\n---\n"
if data.endswith(end):
    data = data[: -len(end)]
print(len(data))
' "$HOME_DIR/calls/mattermost-messages.txt"
}

# message_utf16: UTF-16 code units of the message in the calls file, without the stub's separator.
message_utf16() {
  python3 -c '
import sys
data = open(sys.argv[1], "rb").read().decode("utf-8")
end = "\n---\n"
if data.endswith(end):
    data = data[: -len(end)]
print(len(data.encode("utf-16-le")) // 2)
' "$HOME_DIR/calls/mattermost-messages.txt"
}

# assert_no_secret: the webhook token and the inherited sentinel appear in no file under the test
# HOME (state, stdout, recorded calls, stubs) except the fake env file itself.
assert_no_secret() {
  local hits=""
  hits=$(grep -rlF -e "$TOKEN" -e "$SENTINEL" --exclude=mattermost.env "$HOME_DIR" 2>/dev/null || true)
  check "webhook URL and inherited sentinel absent from every file under the test HOME" test -z "$hits"
}

# current_run_log: run.log of the newest run directory, which is the run under test.
current_run_log() {
  local dirs=("$(state_dir)"/runs/*/)
  printf '%s/run.log' "${dirs[-1]%/}"
}

# current_run_id: the name of the newest run directory, as the OK message carries it.
current_run_id() {
  local dirs=("$(state_dir)"/runs/*/)
  dirs=("${dirs[-1]%/}")
  printf '%s' "${dirs[0]##*/}"
}

# called NAME: succeeds when the stub NAME recorded at least one call.
called() {
  [[ -s "$HOME_DIR/calls/$1.txt" ]]
}

check_static() {
  local hits=""
  CASE="static"
  hits=$(grep -nE '(^|[^[:alnum:]_./-])(date|timeout|sha256sum)([[:space:]]|$)' "$SCRIPT" | grep -vE '^[0-9]+:[[:space:]]*#' || true)
  check "no plain date, timeout or sha256sum call" test -z "$hits"
  check "script is valid bash" bash -n "$SCRIPT"
  hits=$(grep -nE '(^|[^[:alnum:]_./-])tbot' "$SCRIPT" | grep -vE '^[0-9]+:[[:space:]]*#' || true)
  check "no tbot reference" test -z "$hits"
  hits=$(grep -nE 'set -x|set -o xtrace|export MM_WEBHOOK_URL' "$SCRIPT" || true)
  check "no xtrace and no exported webhook URL" test -z "$hits"
  hits=$(grep -nE '(^|[^[:alnum:]_./-])curl([[:space:]]|$)' "$SCRIPT" | grep -vE '^[0-9]+:[[:space:]]*#' | grep -vE 'curl --disable([[:space:]]|$)' || true)
  check "every curl invocation starts with --disable" test -z "$hits"
  # shellcheck disable=SC2016
  check "the env file path is under the user config directory" grep -Eq '^MM_ENV="\$HOME/\.config/[^"]+"$' "$SCRIPT"
}

preflight() {
  CASE="preflight"
  setup_home all-ok
  if ! (
    # HOME is set in this subshell on purpose.
    # shellcheck disable=SC2030
    export HOME="$HOME_DIR"
    # shellcheck source=/dev/null
    source "$SCRIPT"
    setup_path
    for tool in git go claude openrouter-cli curl gnudate gnutimeout gnusha256sum; do
      case "$(command -v "$tool" || true)" in
        "$HOME_DIR/.local/bin/"*) ;;
        *)
          printf 'preflight: %s does not resolve under the temporary HOME\n' "$tool" >&2
          exit 1
          ;;
      esac
    done
    case "$MM_ENV" in
      "$HOME_DIR"/*) ;;
      *) printf 'preflight: MM_ENV is outside the temporary HOME\n' >&2; exit 1 ;;
    esac
    case "$JEV_CLI" in
      "$HOME_DIR"/*) ;;
      *) printf 'preflight: JEV_CLI is outside the temporary HOME\n' >&2; exit 1 ;;
    esac
  ); then
    FAIL=$((FAIL + 1))
    printf 'FAIL preflight: launcher PATH or CLI paths\n' >&2
  else
    PASS=$((PASS + 1))
  fi
}

# Each case_<name> sets up its HOME, runs the launcher and asserts on the result.

case_all_ok() {
  setup_home all-ok
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "exactly one post, the OK message" test "$(message_count)" = 1
  check "the OK message has the OK header" grep -Fq '#DOCTOR nightly: OK' "$HOME_DIR/calls/mattermost-messages.txt"
  check "the OK message counts the audited sources and names the run" grep -Fq "3 sources checked, run $(current_run_id)" "$HOME_DIR/calls/mattermost-messages.txt"
  check "run.log ends with the OK report then finish failures=0" test "$(tail -n 2 "$(current_run_log)" | head -n 1 | grep -c 'report ok-sent$')" = 1
  check "the last run.log line is finish failures=0" test "$(tail -n 1 "$(current_run_log)" | grep -c ' finish failures=0$')" = 1
  check "today transient file exists" test -f "$(today_transient)"
  check "today transient file is empty" test ! -s "$(today_transient)"
  check "claude is called once" test "$(grep -c '' "$HOME_DIR/calls/claude.txt")" = 1
  check "claude runs from the run directory" grep -q '/runs/' "$HOME_DIR/calls/claude-cwd.txt"
  check "claude runs restricted in safe mode" grep -Fq -- "--restricted --safe-mode" "$HOME_DIR/calls/claude.txt"
  check "claude tools are limited to read-only file tools" grep -Fq -- "--tools Read,Grep,Glob --allowedTools Read,Grep,Glob" "$HOME_DIR/calls/claude.txt"
  check "claude runs in permission mode dontAsk" grep -Fq -- "--permission-mode dontAsk" "$HOME_DIR/calls/claude.txt"
  check "claude has no MCP servers" grep -Fq -- "--strict-mcp-config" "$HOME_DIR/calls/claude.txt"
  check "claude keeps no session" grep -Fq -- "--no-session-persistence" "$HOME_DIR/calls/claude.txt"
  check "claude output is JSON with a schema" grep -Fq -- "--output-format json --json-schema" "$HOME_DIR/calls/claude.txt"
  check "claude does not load setting sources" test -z "$(grep -F -- '--setting-sources' "$HOME_DIR/calls/claude.txt" || true)"
  check "claude gets the untrusted audit block" grep -q 'UNTRUSTED_AUDIT_OUTPUT' "$HOME_DIR/calls/claude-stdin.txt"
  check "jev is not called" test ! -s "$HOME_DIR/calls/jev.txt"
  check "openrouter is not called" test ! -s "$HOME_DIR/calls/openrouter.txt"
  check "classification records infra=0" grep -qx 'infra=0' "$(state_dir)/runs/"*/classification.txt
}

case_regex_fix() {
  setup_home regex-fix
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "one message" test "$(message_count)" = 1
  check "header is FIX" grep -Fq ': FIX' "$HOME_DIR/calls/mattermost-messages.txt"
  local run="" report_date=""
  run=$(basename "$(state_dir)/runs/"*)
  report_date=$(TZ=Asia/Almaty "$GNU_DATE" -d "${run:0:10} ${run:11:2}:${run:13:2}:${run:15:2} UTC" +%F)
  check "header carries the GMT+5 date of the run" grep -Fq "#DOCTOR audit $report_date: FIX" "$HOME_DIR/calls/mattermost-messages.txt"
  check "names the source" grep -Fq 'KZ_X_USD_KZT REGEX_NO_MATCH' "$HOME_DIR/calls/mattermost-messages.txt"
  check "carries the model next step" grep -Fq 'doctor rulegen KZ_X_USD_KZT' "$HOME_DIR/calls/mattermost-messages.txt"
  check "model text is marked unverified" grep -Fq 'model (unverified): KZ_X_USD_KZT' "$HOME_DIR/calls/mattermost-messages.txt"
  check "curl reads the URL from a process-substitution config" grep -Fq -- '--config /dev/fd/' "$HOME_DIR/calls/curl.txt"
  check "curl posts the payload on stdin" grep -Fq -- '--data-binary @-' "$HOME_DIR/calls/curl.txt"
  check "curl config carries the configured URL" grep -qx match "$HOME_DIR/calls/curl-config.txt"
  check "payload is a JSON object whose only key is text" python3 -c '
import json, sys
sys.exit(0 if list(json.load(open(sys.argv[1]))) == ["text"] else 1)
' "$HOME_DIR/calls/mattermost-payload.txt"
}

case_regex_disagree() {
  setup_home regex-disagree
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "one message" test "$(message_count)" = 1
  check "header is DISAGREEMENT" grep -Fq ': DISAGREEMENT' "$HOME_DIR/calls/mattermost-messages.txt"
}

case_fetch_first() {
  setup_home fetch-first
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "a silent run posts the OK message" test "$(message_count)" = 1
  check "the silent run posts the OK header" grep -Fq '#DOCTOR nightly: OK' "$HOME_DIR/calls/mattermost-messages.txt"
  check "the OK message counts the audited sources and names the run" grep -Fq "2 sources checked, run $(current_run_id)" "$HOME_DIR/calls/mattermost-messages.txt"
  check "source recorded as transient today" grep -Fqx KZ_A_USD_KZT "$(today_transient)"
}

case_fetch_repeated() {
  setup_home fetch-repeated
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "one message" test "$(message_count)" = 1
  check "source listed as repeated" grep -Fq 'repeated (1):' "$HOME_DIR/calls/mattermost-messages.txt"
}

case_fetch_gap() {
  setup_home fetch-gap
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "a silent run posts the OK message" test "$(message_count)" = 1
  check "the silent run posts the OK header" grep -Fq '#DOCTOR nightly: OK' "$HOME_DIR/calls/mattermost-messages.txt"
  check "source recorded as transient today" grep -Fqx KZ_A_USD_KZT "$(today_transient)"
}

case_unparsed() {
  setup_home unparsed
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "one message" test "$(message_count)" = 1
  check "malformed MISS line is reported as unparsed" grep -Fq '<unparsed> UNPARSED' "$HOME_DIR/calls/mattermost-messages.txt"
  check "transient file is not written" test ! -e "$(today_transient)"
}

case_max_turns() {
  setup_home max-turns
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "one message" test "$(message_count)" = 1
  check "header is MODEL FAILURE" grep -Fq ': MODEL FAILURE' "$HOME_DIR/calls/mattermost-messages.txt"
  check "reason is max turns" grep -Fq 'max turns reached' "$HOME_DIR/calls/mattermost-messages.txt"
  check "jev is not called" test ! -s "$HOME_DIR/calls/jev.txt"
  check "openrouter is not called" test ! -s "$HOME_DIR/calls/openrouter.txt"
}

case_limit_regex() {
  setup_home limit-regex
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "a silent run posts the OK message" test "$(message_count)" = 1
  check "the silent run posts the OK header" grep -Fq '#DOCTOR nightly: OK' "$HOME_DIR/calls/mattermost-messages.txt"
  check "skip-until written with a UTC timestamp" grep -Eq '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$' "$(state_dir)/skip-until"
  check "jev is not called" test ! -s "$HOME_DIR/calls/jev.txt"
  check "openrouter is not called" test ! -s "$HOME_DIR/calls/openrouter.txt"
  local builds_before
  builds_before=$(grep -c '' "$HOME_DIR/calls/go.txt")
  LAST_RC=$(run_launcher)
  check "second run exits 0" test "$LAST_RC" = 0
  check "second run still builds" test "$(grep -c '' "$HOME_DIR/calls/go.txt")" -gt "$builds_before"
  check "second run does not call claude" test "$(grep -c '' "$HOME_DIR/calls/claude.txt")" = 1
}

case_limit_jev_openrouter() {
  local reset_day=""
  setup_home limit-jev-openrouter
  reset_day=$("$GNU_DATE" -u -d '+2 days' +%Y-%m-%d)
  printf '%sT09:00:00+00:00\n' "$reset_day" >"$HOME_DIR/fixture/openrouter.out"
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "a silent run posts the OK message" test "$(message_count)" = 1
  check "the silent run posts the OK header" grep -Fq '#DOCTOR nightly: OK' "$HOME_DIR/calls/mattermost-messages.txt"
  check "skip-until is the OpenRouter answer" grep -qx "${reset_day}T09:00:00Z" "$(state_dir)/skip-until"
  check "jev is called once" test "$(grep -c '' "$HOME_DIR/calls/jev.txt")" = 1
  check "openrouter is called once" test "$(grep -c '' "$HOME_DIR/calls/openrouter.txt")" = 1
}

case_limit_none() {
  setup_home limit-none
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "a silent run posts the OK message" test "$(message_count)" = 1
  check "the silent run posts the OK header" grep -Fq '#DOCTOR nightly: OK' "$HOME_DIR/calls/mattermost-messages.txt"
  check "no skip-until written" test ! -e "$(state_dir)/skip-until"
  check "reset time logged as not determined" grep -rq 'reset-time not determined' "$(state_dir)/runs"
}

case_jev_low() {
  setup_home jev-low
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "one message" test "$(message_count)" = 1
  check "header is MODEL FAILURE" grep -Fq ': MODEL FAILURE' "$HOME_DIR/calls/mattermost-messages.txt"
  check "reason is claude failed" grep -Fq 'model unavailable: claude failed' "$HOME_DIR/calls/mattermost-messages.txt"
  check "the Claude tail is in the message" grep -Fq 'Something odd happened in the request.' "$HOME_DIR/calls/mattermost-messages.txt"
  check "no skip-until written" test ! -e "$(state_dir)/skip-until"
  check "openrouter is not called" test ! -s "$HOME_DIR/calls/openrouter.txt"
}

case_jev_fail() {
  setup_home jev-fail
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "one message" test "$(message_count)" = 1
  check "reason is jev failed" grep -Fq 'model unavailable: jev failed' "$HOME_DIR/calls/mattermost-messages.txt"
  check "no skip-until written" test ! -e "$(state_dir)/skip-until"
}

case_openrouter_fail() {
  setup_home openrouter-fail
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "one message" test "$(message_count)" = 1
  check "reason is openrouter failed" grep -Fq 'model unavailable: openrouter failed' "$HOME_DIR/calls/mattermost-messages.txt"
  check "no skip-until written" test ! -e "$(state_dir)/skip-until"
}

case_invalid_verdict() {
  setup_home invalid-verdict
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "one message" test "$(message_count)" = 1
  check "header is MODEL FAILURE" grep -Fq ': MODEL FAILURE' "$HOME_DIR/calls/mattermost-messages.txt"
  check "jev is called for the unknown failure" called jev
}

case_infra_audit3() {
  setup_home infra-audit3
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "one message" test "$(message_count)" = 1
  check "header is INFRA" grep -Fq ': INFRA' "$HOME_DIR/calls/mattermost-messages.txt"
  check "names the audit exit" grep -Fq 'infra: audit exit 3' "$HOME_DIR/calls/mattermost-messages.txt"
  check "says the model failed" grep -Fq 'model unavailable: claude failed' "$HOME_DIR/calls/mattermost-messages.txt"
}

case_go_fail() {
  setup_home go-fail
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "one message" test "$(message_count)" = 1
  check "header is INFRA" grep -Fq ': INFRA' "$HOME_DIR/calls/mattermost-messages.txt"
  check "names the failed build step" grep -Fq 'infra: step build-doctor exit 1' "$HOME_DIR/calls/mattermost-messages.txt"
  check "claude is still invoked" test -s "$HOME_DIR/calls/claude-stdin.txt"
  check "audit is not run after the failed build" test ! -s "$HOME_DIR/calls/binaries.txt"
}

case_audit_exit1_no_miss() {
  setup_home audit-exit1-no-miss
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "one message" test "$(message_count)" = 1
  check "header is INFRA" grep -Fq ': INFRA' "$HOME_DIR/calls/mattermost-messages.txt"
  check "names the missing MISS lines" grep -Fq 'audit exit 1 without parsable MISS lines' "$HOME_DIR/calls/mattermost-messages.txt"
}

case_limit_with_alertable() {
  setup_home limit-with-alertable
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "one message" test "$(message_count)" = 1
  check "header is FIX" grep -Fq ': FIX' "$HOME_DIR/calls/mattermost-messages.txt"
  check "says the model was skipped until a time" grep -Fq 'model skipped until' "$HOME_DIR/calls/mattermost-messages.txt"
  check "skip-until written" test -s "$(state_dir)/skip-until"
}

case_budget_56() {
  setup_home budget-56
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "one message" test "$(message_count)" = 1
  check "message is at most 16000 code points" test "$(message_chars)" -le 16000
  check "lists all 56 names" grep -Fq 'immediate (56):' "$HOME_DIR/calls/mattermost-messages.txt"
  check "lists the fifteenth name" grep -Fq 'KZ_BULK_15_USD_KZT REGEX_NO_MATCH' "$HOME_DIR/calls/mattermost-messages.txt"
  check "lists the last name" grep -Fq 'KZ_BULK_56_USD_KZT REGEX_NO_MATCH' "$HOME_DIR/calls/mattermost-messages.txt"
  check "counts nothing as not listed" test -z "$(grep -F ' more' "$HOME_DIR/calls/mattermost-messages.txt" || true)"
  check "keeps the log line" grep -Fq "log: $(hostname -s 2>/dev/null || echo unknown):" "$HOME_DIR/calls/mattermost-messages.txt"
}

case_audit_100k() {
  setup_home audit-100k
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "prompt data is capped at 32 KB" test "$(wc -c <"$HOME_DIR/calls/claude-stdin.txt")" -le 32768
  check "prompt data keeps the open delimiter" grep -Fq '<<<UNTRUSTED_AUDIT_OUTPUT' "$HOME_DIR/calls/claude-stdin.txt"
  check "prompt data keeps the close delimiter" grep -Fq 'UNTRUSTED_AUDIT_OUTPUT>>>' "$HOME_DIR/calls/claude-stdin.txt"
}

case_control_chars() {
  setup_home control-chars
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "one message" test "$(message_count)" = 1
  check "control characters are stripped from the model text" test -z "$(tr -cd '\001' <"$HOME_DIR/calls/mattermost-messages.txt")"
  check "zero-width space is stripped" test "$(LC_ALL=C grep -c "$(printf '\342\200\213')" "$HOME_DIR/calls/mattermost-messages.txt" || true)" = 0
  check "model text is still delivered" grep -Fq 'Line one with control char' "$HOME_DIR/calls/mattermost-messages.txt"
}

case_lock_held() {
  setup_home all-ok
  local holder=""
  flock "$(state_dir)/lock" sleep 30 &
  holder=$!
  sleep 1
  LAST_RC=$(run_launcher)
  kill "$holder" 2>/dev/null || true
  wait "$holder" 2>/dev/null || true
  check "exit 0" test "$LAST_RC" = 0
  check "no go call while another run holds the lock" test ! -e "$HOME_DIR/calls/go.txt"
  check "no claude call while another run holds the lock" test ! -e "$HOME_DIR/calls/claude.txt"
}

case_lock_error() {
  setup_home all-ok
  mkdir -p "$(state_dir)/lock"
  LAST_RC=$(run_launcher)
  check "exit 1" test "$LAST_RC" = 1
  check "one message sent" test "$(message_count)" = 1
  check "message names the lock error" grep -Fq 'lock error' "$HOME_DIR/calls/mattermost-messages.txt"
  check "no go call without the lock" test ! -e "$HOME_DIR/calls/go.txt"
}

case_runs_pruned() {
  local i name
  setup_home all-ok
  for i in $(seq 1 35); do
    name=$(printf '2025-12-01T0000%02dZ' "$i")
    mkdir -p "$(state_dir)/runs/$name"
  done
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "30 run directories remain" test "$(find "$(state_dir)/runs" -mindepth 1 -maxdepth 1 -type d | grep -c '')" = 30
  check "oldest run directory is removed" test ! -e "$(state_dir)/runs/2025-12-01T000001Z"
}

case_drift() {
  setup_home all-ok
  printf '# local change\n' >>"$(state_dir)/repo/.agents/scripts/doctor-nightly.sh"
  LAST_RC=$(run_launcher)
  check "first run exits 0" test "$LAST_RC" = 0
  check "first run posts the OK message and one drift message" test "$(message_count)" = 2
  check "drift message names the reinstall" grep -Fq 'launcher copy is out of date' "$HOME_DIR/calls/mattermost-messages.txt"
  LAST_RC=$(run_launcher)
  check "second run exits 0" test "$LAST_RC" = 0
  check "second run posts only the OK message" test "$(message_count)" = 3
  check "second run does not repeat the drift message" test "$(grep -c 'launcher copy is out of date' "$HOME_DIR/calls/mattermost-messages.txt")" = 1
}

case_skip_future() {
  setup_home all-ok
  "$GNU_DATE" -u -d '+2 days' +%Y-%m-%dT%H:%M:%SZ >"$(state_dir)/skip-until"
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "the audit still builds during the skip window" test -s "$HOME_DIR/calls/go.txt"
  check "no claude call during the skip window" test ! -e "$HOME_DIR/calls/claude.txt"
  check "skip-until is kept" test -s "$(state_dir)/skip-until"
  check "the model skip is logged" grep -rq 'model paused until' "$(state_dir)/runs"
}

case_skip_garbage() {
  setup_home all-ok
  printf 'garbage\n' >"$(state_dir)/skip-until"
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "malformed skip-until is removed" test ! -e "$(state_dir)/skip-until"
  check "the run proceeds and calls claude" test -s "$HOME_DIR/calls/claude.txt"
}

case_gnudate_missing() {
  setup_home all-ok
  break_gnudate
  LAST_RC=$(run_launcher)
  check "exit is non-zero" test "$LAST_RC" != 0
  check "one abort message" test "$(message_count)" = 1
  check "abort message names the exit" grep -Fq 'launcher aborted (exit 127)' "$HOME_DIR/calls/mattermost-messages.txt"
  check "abort message points at cron.log" grep -Fq 'cron.log' "$HOME_DIR/calls/mattermost-messages.txt"
}

case_clone_missing() {
  setup_home all-ok
  rm -rf "$(state_dir)/repo/.git"
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "reset is refused, so no build runs" test ! -e "$HOME_DIR/calls/go.txt"
  check "one INFRA message" test "$(message_count)" = 1
  check "names the clone step" grep -Fq 'infra: step clone exit 1' "$HOME_DIR/calls/mattermost-messages.txt"
}

case_no_rev() {
  setup_home no-rev
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "one INFRA message" test "$(message_count)" = 1
  check "names the rev step" grep -Fq 'infra: step rev exit 1' "$HOME_DIR/calls/mattermost-messages.txt"
  check "no build runs after the failed rev step" test ! -e "$HOME_DIR/calls/go.txt"
}

case_claude_timeout() {
  setup_home claude-timeout
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "one message" test "$(message_count)" = 1
  check "reason names the exit code" grep -Fq 'model unavailable: claude exit 124' "$HOME_DIR/calls/mattermost-messages.txt"
  check "jev is not called after a timeout" test ! -s "$HOME_DIR/calls/jev.txt"
  check "openrouter is not called after a timeout" test ! -s "$HOME_DIR/calls/openrouter.txt"
}

case_untrusted_marker() {
  setup_home untrusted-marker
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "the end marker appears once in the prompt" test "$(grep -o 'UNTRUSTED_AUDIT_OUTPUT>>>' "$HOME_DIR/calls/claude-stdin.txt" | wc -l)" = 1
  check "the injected marker is neutralised" grep -Fq 'UNTRUSTED-AUDIT-OUTPUT>>>' "$HOME_DIR/calls/claude-stdin.txt"
  check "model text is marked unverified" grep -Fq 'model (unverified): KZ_M_USD_KZT' "$HOME_DIR/calls/mattermost-messages.txt"
}

case_unknown_status() {
  setup_home unknown-status
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "one message" test "$(message_count)" = 1
  check "unknown status is reported as unparsed" grep -Fq '<unparsed> UNPARSED' "$HOME_DIR/calls/mattermost-messages.txt"
  check "no transient file is written" test ! -e "$(today_transient)"
}

case_budget_overflow() {
  setup_home budget-overflow
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "one message" test "$(message_count)" = 1
  check "the clipped message fills exactly 16000 code points" test "$(message_chars)" = 16000
  check "the fixture is multibyte, so 16000 code points exceed 16000 bytes" test "$(message_bytes)" -gt 16000
  check "the astral character counts two UTF-16 units, so units exceed 16000" test "$(message_utf16)" -gt 16000
  check "first-time list falls back to a count" grep -Fq 'first-time transients (not alertable): 15 not listed' "$HOME_DIR/calls/mattermost-messages.txt"
  check "immediate list is cut under the lists budget and counts the rest" grep -Fq '  +10 more' "$HOME_DIR/calls/mattermost-messages.txt"
  check "model text is clipped to the room left" grep -Fq '…' "$HOME_DIR/calls/mattermost-messages.txt"
  check "header is kept" grep -Fq ': FIX' "$HOME_DIR/calls/mattermost-messages.txt"
  check "both fence lines are kept" test "$(grep -cx -F -- '```' "$HOME_DIR/calls/mattermost-messages.txt")" = 2
  check "log line is kept" grep -Fq 'log: ' "$HOME_DIR/calls/mattermost-messages.txt"
}

case_model_fix_silent() {
  setup_home model-fix-silent
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "exactly one post, the OK message" test "$(message_count)" = 1
  check "the post has the OK header" grep -Fq '#DOCTOR nightly: OK' "$HOME_DIR/calls/mattermost-messages.txt"
  check "run.log records the model fix verdict" grep -rFq 'model status=fix' "$(state_dir)/runs"
}

case_markdown_inert() {
  local text=""
  setup_home markdown-inert
  text="$HOME_DIR/calls/mattermost-messages.txt"
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "one message" test "$(message_count)" = 1
  check "exactly two fence lines" test "$(grep -cx -F -- '```' "$text")" = 2
  check "no backtick outside the fence lines" test "$(tr -cd '`' <"$text" | wc -c)" = 6
  check "no mention or image markup before the opening fence" \
    test -z "$(sed -n '1,/^```$/p' "$text" | sed '$d' | grep -F -e '@channel' -e '![' || true)"
  # shellcheck disable=SC2016
  check "mention and image markup sit inside the fence" \
    test "$(sed -n '/^```$/,/^```$/p' "$text" | grep -c -F -e '@channel' -e '![x]' || true)" = 1
}

case_skip_expired() {
  setup_home all-ok
  printf '2001-01-01T00:00:00Z\n' >"$(state_dir)/skip-until"
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "expired skip-until is removed" test ! -e "$(state_dir)/skip-until"
  check "the run proceeds and calls claude" test -s "$HOME_DIR/calls/claude.txt"
}

case_mm_env_missing() {
  setup_home all-ok
  rm -f "$HOME_DIR/.config/beacon-doctor/mattermost.env"
  LAST_RC=$(run_launcher)
  check "exit 1" test "$LAST_RC" = 1
  check "stdout names the env file path" grep -Fq "$HOME_DIR/.config/beacon-doctor/mattermost.env" "$HOME_DIR/stdout.txt"
  check "no git call before the refusal" test ! -e "$HOME_DIR/calls/git.txt"
  check "no go call before the refusal" test ! -e "$HOME_DIR/calls/go.txt"
  check "no curl call before the refusal" test ! -e "$HOME_DIR/calls/curl.txt"
  check "no lock file is created before the refusal" test ! -e "$(state_dir)/lock"
}

# mm_env_refused LINE: a launcher given a malformed env file exits 1 before any build or post.
mm_env_refused() {
  setup_home regex-fix
  write_env "$1"
  LAST_RC=$(run_launcher)
  check "exit 1" test "$LAST_RC" = 1
  check "stdout reports the missing or malformed file" grep -Fq 'mattermost webhook not configured' "$HOME_DIR/stdout.txt"
  check "no curl call" test ! -e "$HOME_DIR/calls/curl.txt"
  check "no go call" test ! -e "$HOME_DIR/calls/go.txt"
  check "no lock file is created before the refusal" test ! -e "$(state_dir)/lock"
}

case_mm_env_malformed() {
  mm_env_refused "MM_WEBHOOK_URL=\"https://mm.invalid/hooks/$TOKEN\""
}

case_mm_env_quote() {
  mm_env_refused "MM_WEBHOOK_URL=https://mm.invalid/a\"b/hooks/$TOKEN"
}

case_mm_env_space() {
  mm_env_refused "MM_WEBHOOK_URL=https://mm.invalid/a b/hooks/$TOKEN"
}

case_mm_env_backslash() {
  mm_env_refused "MM_WEBHOOK_URL=https://mm.invalid/a\\b/hooks/$TOKEN"
}

case_mm_env_bom() {
  mm_env_refused "$(printf '\357\273\277')MM_WEBHOOK_URL=https://mm.invalid/hooks/$TOKEN"
}

case_mm_env_crlf() {
  setup_home regex-fix
  write_env "MM_WEBHOOK_URL=https://mm.invalid/hooks/$TOKEN"$'\r'
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "one post" test "$(message_count)" = 1
  check "curl config carries the URL without a carriage return" grep -qx match "$HOME_DIR/calls/curl-config.txt"
}

case_mm_env_inherited() {
  setup_home regex-fix
  LAST_RC=$(run_launcher "MM_WEBHOOK_URL=$SENTINEL")
  check "exit 0" test "$LAST_RC" = 0
  check "one post" test "$(message_count)" = 1
  check "claude environment carries neither URL" test -z "$(grep -F -e "$SENTINEL" -e "$TOKEN" "$HOME_DIR/calls/claude-env.txt" || true)"
  check "curl environment carries neither URL" test -z "$(grep -F -e "$SENTINEL" -e "$TOKEN" "$HOME_DIR/calls/curl-env.txt" || true)"
  check "curl config carries the file URL" grep -qx match "$HOME_DIR/calls/curl-config.txt"
}

case_ok_send_fail() {
  setup_home all-ok
  printf '22\n' >"$HOME_DIR/fixture/curl.exit"
  printf '500\n' >"$HOME_DIR/fixture/curl.http"
  stub "$HOME_DIR/.local/bin/sleep" sleep <<'EOF'
exit 0
EOF
  LAST_RC=$(run_launcher)
  check "exit 1" test "$LAST_RC" = 1
  check "the failed OK send is logged" grep -rFq 'report ok send failed' "$(state_dir)/runs"
  check "two failed attempts are logged" test "$(cat "$(state_dir)"/runs/*/run.log | grep -cF 'mattermost failed exit=22 http=500' || true)" = 2
  check "run.log ends with finish failures=1" test "$(tail -n 1 "$(current_run_log)" | grep -c ' finish failures=1$')" = 1
}

case_mm_send_fail_4xx() {
  setup_home regex-fix
  printf '22\n' >"$HOME_DIR/fixture/curl.exit"
  printf '404\n' >"$HOME_DIR/fixture/curl.http"
  stub "$HOME_DIR/.local/bin/sleep" sleep <<'EOF'
exit 0
EOF
  LAST_RC=$(run_launcher)
  check "exit 1" test "$LAST_RC" = 1
  check "one curl attempt, a 4xx answer is not retried" test "$(grep -c '' "$HOME_DIR/calls/curl.txt")" = 1
  check "the failed attempt is logged once with its HTTP status" test "$(cat "$(state_dir)"/runs/*/run.log | grep -cF 'mattermost failed exit=22 http=404' || true)" = 1
  check "no sleep before a 4xx retry decision" test ! -e "$HOME_DIR/calls/sleep.txt"
}

case_mm_send_fail() {
  setup_home regex-fix
  printf '22\n' >"$HOME_DIR/fixture/curl.exit"
  printf '500\n' >"$HOME_DIR/fixture/curl.http"
  stub "$HOME_DIR/.local/bin/sleep" sleep <<'EOF'
exit 0
EOF
  LAST_RC=$(run_launcher)
  check "exit 1" test "$LAST_RC" = 1
  check "two curl attempts" test "$(grep -c '' "$HOME_DIR/calls/curl.txt")" = 2
  check "both failed attempts are logged with exit and HTTP status" test "$(cat "$(state_dir)"/runs/*/run.log | grep -cF 'mattermost failed exit=22 http=500' || true)" = 2
  check "one report send failure is logged" test "$(cat "$(state_dir)"/runs/*/run.log | grep -cF 'report send failed' || true)" = 1
}

# txt_unit ARGS: the launcher's txt helper in a subshell. It needs no GNU date, so it runs without UNIT_HOME.
txt_unit() {
  (
    # shellcheck source=/dev/null
    source "$SCRIPT"
    txt "$@"
  )
}

# unit_call FUNC ARGS...: runs a launcher function in a subshell with the unit HOME, which holds a GNU date link.
unit_call() {
  (
    # HOME is set in this subshell on purpose.
    # shellcheck disable=SC2031
    export HOME="$UNIT_HOME"
    # shellcheck source=/dev/null
    source "$SCRIPT"
    setup_path
    "$@"
  )
}

# limit_case LABEL NOW_ISO TEXT WANT_ISO: parse_limit_message with a fixed now; WANT_ISO empty means no reset.
limit_case() {
  local label="$1" now_iso="$2" text="$3" want_iso="$4" file now got="" want=""
  CASE="unit"
  file=$(mktemp "$ROOT_TMP/limit.XXXXXX")
  printf '%s' "$text" >"$file"
  now=$("$GNU_DATE" -u -d "$now_iso" +%s)
  got=$(unit_call parse_limit_message "$file" "$now")
  if [[ -n "$want_iso" ]]; then
    want=$("$GNU_DATE" -u -d "$want_iso" +%s)
  fi
  check "$label" test "$got" = "$want"
}

# epoch_case LABEL NOW_ISO LINE WANT_ISO: reset_epoch_from_line with a fixed now.
epoch_case() {
  local label="$1" now_iso="$2" line="$3" want_iso="$4" now got="" want=""
  CASE="unit"
  now=$("$GNU_DATE" -u -d "$now_iso" +%s)
  got=$(unit_call reset_epoch_from_line "$line" "$now")
  if [[ -n "$want_iso" ]]; then
    want=$("$GNU_DATE" -u -d "$want_iso" +%s)
  fi
  check "$label" test "$got" = "$want"
}

# unit_txt_checks: txt measures and clips in Unicode code points, so an emoji counts as one
# and a clipped text keeps the ellipsis inside the limit.
unit_txt_checks() {
  CASE="unit"
  local emoji ellipsis sharp_s
  emoji=$(printf '\360\237\230\200')
  ellipsis=$(printf '\342\200\246')
  sharp_s=$(printf '\303\237')
  check "an emoji plus a letter measures two code points" test "$(printf '%sa' "$emoji" | txt_unit len)" = 2
  check "head 2 of three letters keeps one letter and the ellipsis" test "$(printf '%s%s%s' "$sharp_s" "$sharp_s" "$sharp_s" | txt_unit head 2)" = "$sharp_s$ellipsis"
  check "head keeps an emoji whole" test "$(printf '%s%s%s' "$emoji" "$emoji" "$emoji" | txt_unit head 2)" = "$emoji$ellipsis"
}

# unit_limit_checks: fixed-clock checks of the usage-limit parsers.
unit_limit_checks() {
  CASE="unit"
  UNIT_HOME=$(mktemp -d "$ROOT_TMP/unit.XXXXXX")
  mkdir -p "$UNIT_HOME/.local/bin"
  ln -s "$GNU_DATE" "$UNIT_HOME/.local/bin/gnudate"

  limit_case "3:50pm UTC before the reset" "2026-11-01T14:21:00Z" "You've hit your session limit · resets 3:50pm (UTC)" "2026-11-01T15:50:00Z"
  limit_case "3:50pm UTC after the reset rolls to tomorrow" "2026-11-01T16:00:00Z" "You've hit your session limit · resets 3:50pm (UTC)" "2026-11-02T15:50:00Z"
  limit_case "6pm Asia/Almaty" "2026-11-01T11:19:00Z" "You've hit your session limit · resets 6pm (Asia/Almaty)" "2026-11-01T13:00:00Z"
  limit_case "weekly 3am Asia/Almaty is the next occurrence" "2026-11-01T11:35:00Z" "You've hit your weekly limit · resets 3am (Asia/Almaty)" "2026-11-01T22:00:00Z"
  limit_case "zone-less 3pm defaults to UTC" "2026-11-01T14:00:00Z" "You've hit your limit · resets 3pm" "2026-11-01T15:00:00Z"
  limit_case "rejects hour 25" "2026-11-01T14:00:00Z" "You've hit your session limit · resets 25pm (UTC)" ""
  limit_case "rejects an unknown zone" "2026-11-01T14:00:00Z" "You've hit your session limit · resets 3pm (Not/AZone)" ""
  limit_case "rejects a dated form" "2026-11-01T14:00:00Z" "You've hit your session limit · resets Nov 5, 9am (UTC)" ""
  limit_case "rejects an empty file" "2026-11-01T14:00:00Z" "" ""
  limit_case "rejects a connection error" "2026-11-01T14:00:00Z" "Error: connect ECONNREFUSED" ""

  epoch_case "ISO time without seconds" "2026-11-01T00:00:00Z" "2026-11-03T09:00Z" "2026-11-03T09:00:00Z"
  epoch_case "ISO time with Z" "2026-11-01T00:00:00Z" "2026-11-03T09:00:00Z" "2026-11-03T09:00:00Z"
  epoch_case "ISO time with an offset" "2026-11-01T00:00:00Z" "2026-11-03T09:00:00+05:00" "2026-11-03T04:00:00Z"
  epoch_case "rejects more than 8 days ahead" "2026-11-01T00:00:00Z" "2026-11-10T09:00:00Z" ""
  epoch_case "rejects a past time" "2026-11-01T00:00:00Z" "2026-10-30T09:00:00Z" ""
}

run_case() {
  local name="$1" fn
  CASE="$name"
  fn="case_${name//-/_}"
  if ! declare -F "$fn" >/dev/null; then
    die "unknown case $name"
  fi
  "$fn"
  check "no curl call without --disable" test ! -s "$HOME_DIR/calls/curl-violations.txt"
  assert_no_secret
}

ALL_CASES=(
  all-ok regex-fix regex-disagree fetch-first fetch-repeated fetch-gap unparsed
  max-turns limit-regex limit-jev-openrouter limit-none jev-low jev-fail
  openrouter-fail invalid-verdict infra-audit3 go-fail audit-exit1-no-miss
  limit-with-alertable budget-56 audit-100k control-chars lock-held lock-error runs-pruned
  drift skip-future skip-garbage skip-expired gnudate-missing clone-missing no-rev
  claude-timeout untrusted-marker unknown-status budget-overflow markdown-inert
  mm-env-missing mm-env-malformed mm-env-quote mm-env-space mm-env-inherited mm-send-fail
  ok-send-fail model-fix-silent
  mm-env-backslash mm-env-bom mm-env-crlf mm-send-fail-4xx
)

main() {
  local names=("$@")
  require_tools
  ROOT_TMP=$(mktemp -d)
  trap 'rm -rf "$ROOT_TMP"' EXIT
  check_static
  preflight
  unit_limit_checks
  unit_txt_checks
  if (( $# == 0 )); then
    names=("${ALL_CASES[@]}")
  fi
  local name
  for name in "${names[@]}"; do
    run_case "$name"
  done
  printf '%d passed, %d failed\n' "$PASS" "$FAIL"
  (( FAIL == 0 ))
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi

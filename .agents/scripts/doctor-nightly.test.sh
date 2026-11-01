#!/usr/bin/env bash
#
# Fixture tests for doctor-nightly.sh.
#
# Linux only: needs GNU coreutils (gnudate, gnutimeout, gnusha256sum; on hosts
# without the gnu-prefixed names the plain GNU tools are used) and util-linux
# flock. Every case runs a copy of the launcher under a temporary HOME. The
# external commands the launcher calls (git, go, claude, openrouter-cli,
# tbot-cli, jev-cli and the built doctor and migrator) are stubs that record
# their arguments and replay the fixture of the case, testdata/doctor-nightly/<case>/.
#
# Usage: doctor-nightly.test.sh [CASE...]

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="$HERE/doctor-nightly.sh"
FIXTURES="$HERE/testdata/doctor-nightly"

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
  local candidate=""
  if [[ -x "/usr/bin/gnu$1" ]]; then
    printf '%s\n' "/usr/bin/gnu$1"
    return 0
  fi
  candidate=$(command -v "$2" || true)
  if [[ -n "$candidate" ]] && "$candidate" --version 2>/dev/null | head -n 1 | grep -q 'GNU coreutils'; then
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
  *rev-parse*) echo 0123456789abcdef0123456789abcdef01234567 ;;
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
  exit "$(cat "$HOME/fixture/audit.exit")"
fi
exit 0
EOF
  chmod +x "$bin/replay"
}

# install_cli_stubs: the CLIs the launcher calls by absolute path under HOME.
install_cli_stubs() {
  stub "$HOME_DIR/.claude/skills/cli-telegram/tbot-cli" tbot <<'EOF'
printf '%s\n---\n' "$1" >>"$HOME/calls/tbot-messages.txt"
exit 0
EOF

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

# setup_home FIXTURE: a fresh HOME with stubs, the launcher copies and the fixture.
setup_home() {
  local fixture="$1" state bin
  HOME_DIR=$(mktemp -d "$ROOT_TMP/home.XXXXXX")
  bin="$HOME_DIR/.local/bin"
  state="$HOME_DIR/.local/state/beacon-doctor"
  mkdir -p "$bin" "$HOME_DIR/calls" "$HOME_DIR/fixture" \
    "$HOME_DIR/.claude/skills/cli-telegram" "$HOME_DIR/.claude/skills/cli-jev" \
    "$state/repo/.agents/scripts" "$state/bin" "$state/runs" "$state/transient"
  install_stubs "$bin"
  install_cli_stubs
  cp "$SCRIPT" "$state/doctor-nightly.sh"
  cp "$SCRIPT" "$state/repo/.agents/scripts/doctor-nightly.sh"
  if [[ -d "$FIXTURES/$fixture" ]]; then
    cp -R "$FIXTURES/$fixture/." "$HOME_DIR/fixture/"
  fi
  place_transient "$state"
}

# run_launcher: runs the installed launcher copy with --max-delay 0; prints its exit code.
run_launcher() {
  local rc=0
  env -i HOME="$HOME_DIR" PATH=/usr/bin:/bin \
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
  local f="$HOME_DIR/calls/tbot-messages.txt" n=0
  if [[ -f "$f" ]]; then
    n=$(grep -c '^---$' "$f" || true)
  fi
  printf '%s' "$n"
}

# called NAME: succeeds when the stub NAME recorded at least one call.
called() {
  [[ -s "$HOME_DIR/calls/$1.txt" ]]
}

check_static() {
  local hits=""
  CASE="static"
  hits=$(grep -vE '^[[:space:]]*#' "$SCRIPT" | grep -nE '(^|[^[:alnum:]_./-])(date|timeout|sha256sum)([[:space:]]|$)' || true)
  check "no plain date, timeout or sha256sum call" test -z "$hits"
  check "script is valid bash" bash -n "$SCRIPT"
}

preflight() {
  CASE="preflight"
  setup_home all-ok
  if ! (
    export HOME="$HOME_DIR"
    # shellcheck source=/dev/null
    source "$SCRIPT"
    setup_path
    for tool in git go claude openrouter-cli gnudate gnutimeout gnusha256sum; do
      case "$(command -v "$tool" || true)" in
        "$HOME_DIR/.local/bin/"*) ;;
        *)
          printf 'preflight: %s does not resolve under the temporary HOME\n' "$tool" >&2
          exit 1
          ;;
      esac
    done
    case "$TBOT_CLI" in
      "$HOME_DIR"/*) ;;
      *) printf 'preflight: TBOT_CLI is outside the temporary HOME\n' >&2; exit 1 ;;
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
  check "no message" test "$(message_count)" = 0
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
  check "header is FIX" grep -Fq ': FIX' "$HOME_DIR/calls/tbot-messages.txt"
  check "names the source" grep -Fq 'KZ_X_USD_KZT REGEX_NO_MATCH' "$HOME_DIR/calls/tbot-messages.txt"
  check "carries the model next step" grep -Fq 'doctor rulegen KZ_X_USD_KZT' "$HOME_DIR/calls/tbot-messages.txt"
}

case_regex_disagree() {
  setup_home regex-disagree
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "one message" test "$(message_count)" = 1
  check "header is DISAGREEMENT" grep -Fq ': DISAGREEMENT' "$HOME_DIR/calls/tbot-messages.txt"
}

case_fetch_first() {
  setup_home fetch-first
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "no message for a first-time fetch failure" test "$(message_count)" = 0
  check "source recorded as transient today" grep -Fqx KZ_A_USD_KZT "$(today_transient)"
}

case_fetch_repeated() {
  setup_home fetch-repeated
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "one message" test "$(message_count)" = 1
  check "source listed as repeated" grep -Fq 'repeated (1):' "$HOME_DIR/calls/tbot-messages.txt"
}

case_fetch_gap() {
  setup_home fetch-gap
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "no message when a night was skipped in between" test "$(message_count)" = 0
  check "source recorded as transient today" grep -Fqx KZ_A_USD_KZT "$(today_transient)"
}

case_unparsed() {
  setup_home unparsed
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "one message" test "$(message_count)" = 1
  check "malformed MISS line is reported as unparsed" grep -Fq '<unparsed> UNPARSED' "$HOME_DIR/calls/tbot-messages.txt"
  check "transient file is not written" test ! -e "$(today_transient)"
}

case_max_turns() {
  setup_home max-turns
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "one message" test "$(message_count)" = 1
  check "header is MODEL FAILURE" grep -Fq ': MODEL FAILURE' "$HOME_DIR/calls/tbot-messages.txt"
  check "reason is max turns" grep -Fq 'max turns reached' "$HOME_DIR/calls/tbot-messages.txt"
  check "jev is not called" test ! -s "$HOME_DIR/calls/jev.txt"
  check "openrouter is not called" test ! -s "$HOME_DIR/calls/openrouter.txt"
}

case_limit_regex() {
  setup_home limit-regex
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "no message" test "$(message_count)" = 0
  check "skip-until written with a UTC timestamp" grep -Eq '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$' "$(state_dir)/skip-until"
  check "jev is not called" test ! -s "$HOME_DIR/calls/jev.txt"
  check "openrouter is not called" test ! -s "$HOME_DIR/calls/openrouter.txt"
  local builds_before
  builds_before=$(grep -c '' "$HOME_DIR/calls/go.txt")
  LAST_RC=$(run_launcher)
  check "second run exits 0" test "$LAST_RC" = 0
  check "second run is skipped before claude" test "$(grep -c '' "$HOME_DIR/calls/claude.txt")" = 1
  check "second run does not build" test "$(grep -c '' "$HOME_DIR/calls/go.txt")" = "$builds_before"
}

case_limit_jev_openrouter() {
  local reset_day=""
  setup_home limit-jev-openrouter
  reset_day=$("$GNU_DATE" -u -d '+2 days' +%Y-%m-%d)
  printf '%sT09:00:00+00:00\n' "$reset_day" >"$HOME_DIR/fixture/openrouter.out"
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "no message" test "$(message_count)" = 0
  check "skip-until is the OpenRouter answer" grep -qx "${reset_day}T09:00:00Z" "$(state_dir)/skip-until"
  check "jev is called once" test "$(grep -c '' "$HOME_DIR/calls/jev.txt")" = 1
  check "openrouter is called once" test "$(grep -c '' "$HOME_DIR/calls/openrouter.txt")" = 1
}

case_limit_none() {
  setup_home limit-none
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "no message" test "$(message_count)" = 0
  check "no skip-until written" test ! -e "$(state_dir)/skip-until"
  check "reset time logged as not determined" grep -rq 'reset-time not determined' "$(state_dir)/runs"
}

case_jev_low() {
  setup_home jev-low
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "one message" test "$(message_count)" = 1
  check "header is MODEL FAILURE" grep -Fq ': MODEL FAILURE' "$HOME_DIR/calls/tbot-messages.txt"
  check "reason is claude failed" grep -Fq 'model unavailable: claude failed' "$HOME_DIR/calls/tbot-messages.txt"
  check "no skip-until written" test ! -e "$(state_dir)/skip-until"
  check "openrouter is not called" test ! -s "$HOME_DIR/calls/openrouter.txt"
}

case_jev_fail() {
  setup_home jev-fail
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "one message" test "$(message_count)" = 1
  check "reason is jev failed" grep -Fq 'model unavailable: jev failed' "$HOME_DIR/calls/tbot-messages.txt"
  check "no skip-until written" test ! -e "$(state_dir)/skip-until"
}

case_openrouter_fail() {
  setup_home openrouter-fail
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "one message" test "$(message_count)" = 1
  check "reason is openrouter failed" grep -Fq 'model unavailable: openrouter failed' "$HOME_DIR/calls/tbot-messages.txt"
  check "no skip-until written" test ! -e "$(state_dir)/skip-until"
}

case_invalid_verdict() {
  setup_home invalid-verdict
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "one message" test "$(message_count)" = 1
  check "header is MODEL FAILURE" grep -Fq ': MODEL FAILURE' "$HOME_DIR/calls/tbot-messages.txt"
  check "jev is called for the unknown failure" called jev
}

case_infra_audit3() {
  setup_home infra-audit3
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "one message" test "$(message_count)" = 1
  check "header is INFRA" grep -Fq ': INFRA' "$HOME_DIR/calls/tbot-messages.txt"
  check "names the audit exit" grep -Fq 'infra: audit exit 3' "$HOME_DIR/calls/tbot-messages.txt"
  check "says the model failed" grep -Fq 'model unavailable: claude failed' "$HOME_DIR/calls/tbot-messages.txt"
}

case_go_fail() {
  setup_home go-fail
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "one message" test "$(message_count)" = 1
  check "header is INFRA" grep -Fq ': INFRA' "$HOME_DIR/calls/tbot-messages.txt"
  check "names the failed build step" grep -Fq 'infra: step build-doctor exit 1' "$HOME_DIR/calls/tbot-messages.txt"
  check "claude is still invoked" test -s "$HOME_DIR/calls/claude-stdin.txt"
  check "audit is not run after the failed build" test ! -s "$HOME_DIR/calls/binaries.txt"
}

case_audit_exit1_no_miss() {
  setup_home audit-exit1-no-miss
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "one message" test "$(message_count)" = 1
  check "header is INFRA" grep -Fq ': INFRA' "$HOME_DIR/calls/tbot-messages.txt"
  check "names the missing MISS lines" grep -Fq 'audit exit 1 without parsable MISS lines' "$HOME_DIR/calls/tbot-messages.txt"
}

case_limit_with_alertable() {
  setup_home limit-with-alertable
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "one message" test "$(message_count)" = 1
  check "header is FIX" grep -Fq ': FIX' "$HOME_DIR/calls/tbot-messages.txt"
  check "says the model was skipped until a time" grep -Fq 'model skipped until' "$HOME_DIR/calls/tbot-messages.txt"
  check "skip-until written" test -s "$(state_dir)/skip-until"
}

case_budget_56() {
  setup_home budget-56
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "one message" test "$(message_count)" = 1
  check "message within 4096 characters plus separator" test "$(wc -c <"$HOME_DIR/calls/tbot-messages.txt")" -le 4101
  check "keeps the first 15 names" grep -Fq 'KZ_BULK_15_USD_KZT REGEX_NO_MATCH' "$HOME_DIR/calls/tbot-messages.txt"
  check "does not list the sixteenth name" test -z "$(grep -F 'KZ_BULK_16_USD_KZT' "$HOME_DIR/calls/tbot-messages.txt" || true)"
  check "counts the rest" grep -Fq '+41 more' "$HOME_DIR/calls/tbot-messages.txt"
  check "keeps the log line" grep -Fq 'log: hive-warden-01:' "$HOME_DIR/calls/tbot-messages.txt"
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
  check "control characters are stripped from the model text" test -z "$(tr -cd '\001' <"$HOME_DIR/calls/tbot-messages.txt")"
  check "model text is still delivered" grep -Fq 'Line one with control char' "$HOME_DIR/calls/tbot-messages.txt"
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
  check "first run sends one drift message" test "$(message_count)" = 1
  check "drift message names the reinstall" grep -Fq 'launcher copy is out of date' "$HOME_DIR/calls/tbot-messages.txt"
  LAST_RC=$(run_launcher)
  check "second run exits 0" test "$LAST_RC" = 0
  check "second run does not repeat the drift message" test "$(message_count)" = 1
}

case_skip_future() {
  setup_home all-ok
  "$GNU_DATE" -u -d '+2 days' +%Y-%m-%dT%H:%M:%SZ >"$(state_dir)/skip-until"
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "no go call during the skip window" test ! -e "$HOME_DIR/calls/go.txt"
  check "no claude call during the skip window" test ! -e "$HOME_DIR/calls/claude.txt"
  check "skip-until is kept" test -s "$(state_dir)/skip-until"
}

case_skip_expired() {
  setup_home all-ok
  printf '2001-01-01T00:00:00Z\n' >"$(state_dir)/skip-until"
  LAST_RC=$(run_launcher)
  check "exit 0" test "$LAST_RC" = 0
  check "expired skip-until is removed" test ! -e "$(state_dir)/skip-until"
  check "the run proceeds and calls claude" test -s "$HOME_DIR/calls/claude.txt"
}

run_case() {
  local name="$1" fn
  CASE="$name"
  fn="case_${name//-/_}"
  if ! declare -F "$fn" >/dev/null; then
    die "unknown case $name"
  fi
  "$fn"
}

ALL_CASES=(
  all-ok regex-fix regex-disagree fetch-first fetch-repeated fetch-gap unparsed
  max-turns limit-regex limit-jev-openrouter limit-none jev-low jev-fail
  openrouter-fail invalid-verdict infra-audit3 go-fail audit-exit1-no-miss
  limit-with-alertable budget-56 audit-100k control-chars lock-held runs-pruned
  drift skip-future skip-expired
)

main() {
  local names=("$@")
  require_tools
  ROOT_TMP=$(mktemp -d)
  trap 'rm -rf "$ROOT_TMP"' EXIT
  check_static
  preflight
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

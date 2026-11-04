# Task Breakdown

## Overview

Everything under `configs/` reaches the host through exactly one command: `make init`. That
target is one-time provisioning — it creates directories, chowns `.env` and the database,
installs two systemd units and runs `daemon-reload`, installs a sudoers file and validates
it with `visudo -c`, re-downloads the Cloudflare origin-pull CA, installs three nginx files,
and relinks the vhost. It also needs an interactive password, because it runs through
`ssh -t` and its `install` calls are not covered by any NOPASSWD grant.

So there is no way to ship a configuration change alone. The consequences are documented in
this repository already: `configs/config_drift.sh` exists because snapshot compression sat
in git for three weeks while the host kept writing uncompressed backups. The same gap
recurred on 2026-08-13 — an nginx fix merged to `alpha`, `config-drift` reported one
`DIFFERS` line, and delivering two edited lines took a hand-assembled
`ssh -t ... sudo install ... && nginx -t && nginx -s reload`.

The sibling repository `~/projects/developments/com_lingocrm_api` has a `deploy_environment`
target that solves the ergonomics: `scp` each config straight to its final path under
`/etc/nginx/`, then `ssh "sudo nginx -s reload"`. No sudo on the copy, no password, one
command.

**This plan adopts that goal and rejects that mechanism.** The reason is in the file modes:

```
666 root:root /etc/nginx/snippets/com_lingocrm_api_common_settings.conf
666 root:root /etc/nginx/sites-available/com_lingocrm_api
644 root:root /etc/nginx/snippets/beacon.common_settings.conf
```

A plain `scp` into `/etc/nginx/` only works because those files are world-writable. Combined
with the `nginx -t` / `nginx -s reload` NOPASSWD grant that already exists on this host, that
is a complete local privilege-escalation path: any process on the box rewrites an nginx
config, triggers a reload, and the nginx master runs as root.

The distinction that matters is *who* gains the ability, not *how much*. The operator
account `pi5_aide` already holds `(ALL:ALL) ALL` sudo and membership in `docker`, so a narrow
passwordless grant to that account adds almost nothing to what it can already do. Mode `0666`
is different in kind: it hands write access to every user and every process on a host that
also serves lingocrm, qazaqmap, bookingin and be-happy. Their application workers would gain
the ability to rewrite Beacon's nginx configuration, and Beacon's would gain it over theirs.

This plan therefore delivers a `make deploy-configs` that is as convenient as
`deploy_environment` and does not widen file permissions: a root-owned installer script on
the host with a hardcoded source→destination table, reachable through one narrow NOPASSWD
sudoers entry.

## Assumptions

- **The operator account is `pi5_aide`** — verified by `ssh be-happy.kz whoami`. The CI
  account is `github_aide` and is deliberately left out of scope: the release pipeline
  uploads binaries and flips a symlink, and giving CI the ability to rewrite nginx would
  widen the blast radius of a leaked deploy key, which `configs/beacon-deploy.sudoers`
  exists to keep narrow.
- **`sudo nginx -t` and `sudo nginx -s reload` are already NOPASSWD for `pi5_aide`** —
  verified via `sudo -n -l`. Only the `install` step needs a new grant.
- **Beacon's three nginx files are `644 root:root`** and stay that way. Nothing in this plan
  changes a mode on the host.
- **`config_drift.sh` parses `install -m [0-7]{4} /tmp/<basename> <hostpath>` out of the
  whole Makefile**, not out of the `init` recipe specifically — verified by reading its
  `grep -oE` at line 82. Duplicating those lines into a second target would make every file
  appear twice in the drift report and would inflate the `MIN_EXPECTED_FILES` guard into
  meaninglessness. The install table must therefore exist **once** in the Makefile text.
- **`make init` remains the bootstrap.** The installer script and its sudoers entry can only
  be placed with a password; that is correct and unavoidable. One privileged bootstrap buys
  passwordless config deploys thereafter.

## Tasks

### Task 1: Extract the config install table into a single Make definition

- Description: move the `sudo install -m ... /tmp/<file> <hostpath>` lines out of the `init`
  recipe into a Make `define install_beacon_configs`, and have `init` invoke it with
  `$(call ...)`. The literal text then appears exactly once in the Makefile, which is what
  `config_drift.sh` requires, and the new deploy target in Task 4 reuses it rather than
  restating it.
- Acceptance Criteria:
  - `make config-drift` parses the same file count as before the change (currently 7 host
    paths; the script's `MIN_EXPECTED_FILES` guard is 6).
  - `make init` behaviour is byte-identical: same files, same modes, same destinations.
  - No install line appears twice in the Makefile.

### Task 2: A root-owned installer script on the host

- Description: add `configs/beacon-install-configs.sh`, installed by `init` to
  `/usr/local/sbin/beacon-install-configs` with mode `0755` owned by `root:root`. It takes no
  arguments that select paths. It carries the source-basename → destination → mode table
  internally, copies each staged file from a fixed staging directory, runs `nginx -t`, and
  reloads **only** on success. It refuses to run if any staged file is absent, and it prints
  what it changed and what it left alone.
- Rationale: the sudoers grant then names one script rather than a parameterised `install`.
  A NOPASSWD `install` whose source and destination come from the command line is
  root-equivalent by construction; a script the caller cannot edit is not. `pi5_aide` cannot
  modify a `root:root 0755` file, so the set of reachable destinations is fixed at
  provisioning time.
- Acceptance Criteria:
  - Running it with a missing staged file exits non-zero and installs nothing.
  - Running it with a syntactically invalid config leaves the previous config live: `nginx -t`
    fails, the reload does not run, exit is non-zero.
  - It is idempotent: a second run with unchanged files reports no changes and still exits 0.
  - It never widens a mode; every destination is written `0644 root:root`.
  - Shellcheck-clean, `set -euo pipefail`.

### Task 3: The narrow sudoers grant

- Description: add `configs/beacon-configs.sudoers`, installed by `init` to
  `/etc/sudoers.d/beacon-configs` with mode `0440` and validated by `visudo -c`, modelled on
  the existing `configs/beacon-deploy.sudoers`. It grants `pi5_aide` NOPASSWD for exactly
  `/usr/local/sbin/beacon-install-configs` and nothing else. The file carries a comment
  explaining why this is not an escalation for this account and why the script rather than
  `install` is the grant target.
- Acceptance Criteria:
  - `sudo visudo -c` passes.
  - `sudo -n /usr/local/sbin/beacon-install-configs` succeeds without a password.
  - `sudo -n install ...` still prompts — the grant did not leak a general capability.
  - No wildcard appears in the grant.

### Task 4: `make deploy-configs`

- Description: a target that stages every config into the staging directory over `scp`, then
  invokes the installer through `sudo -n`, then runs the drift check to prove the host now
  matches the repository. It must not require a TTY and must not call `make init`.
- Acceptance Criteria:
  - `make deploy-configs` completes with no password prompt on a provisioned host.
  - It ends by printing a clean `config-drift` report; a non-clean report fails the target.
  - On a host that has not been bootstrapped it fails with a message naming `make init`,
    rather than a raw sudo error.
  - Running it twice in a row is a no-op the second time.
  - The staging directory is cleaned afterwards, as `init` already does for `/tmp`.

### Task 5: Post-deploy verification of the nginx cache contract

- Description: extend the target — or add `make verify-edge` — to assert the two header
  invariants that were verified by hand on 2026-08-13, so a config regression is caught by a
  command rather than by reading headers: a retired hashed asset must carry **no**
  `Cache-Control`, and a live hashed asset must carry **exactly one**, without `Expires`.
- Rationale: this is the pair that `configs/nginx.beacon_common_settings.conf` encodes in
  prose comments. A comment does not fail a build. Note for whoever implements it: nginx's
  graceful reload keeps old workers alive until their connections drain, and Cloudflare holds
  keepalive connections to the origin — during the 2026-08-13 deploy the old headers were
  still served for roughly ninety seconds after a successful reload. The check must retry
  rather than assert once, or it will produce false failures.
- Acceptance Criteria:
  - The check fails if `always` is reintroduced on the `add_header`.
  - The check fails if `expires` returns alongside the `add_header`.
  - It retries for a bounded window before failing, and says which invariant broke.

### Task 6: Documentation

- Description: `deploy/README.md` gains the split — `init` provisions, `deploy-configs`
  ships configuration — and the bootstrap ordering. `CLAUDE.md`'s Deployment paragraph
  currently states that configs reach the host only through `make init`; that sentence
  becomes wrong and needs one clause, not a paragraph (the file has a hard 20k budget).
  `configs/config_drift.sh`'s header comment, which explains the drift check by saying
  nothing but `init` installs these files, needs the same correction.
- Acceptance Criteria:
  - No document still claims `make init` is the only path.
  - `CLAUDE.md` grows by less than 300 characters; measured with `wc -c`, not estimated.

## Risks and rejected alternatives

- **Rejected: `chmod 0666` on the config files, as `com_lingocrm_api` does.** Analysed above.
  It is the only mechanism that makes a bare `scp` work, and it is the reason this plan is
  longer than that target's nine lines. Worth revisiting the *sibling's* configuration for the
  same reason — the same escalation path exists there today, for five other sites.
- **Rejected: NOPASSWD on a parameterised `install`.** Equivalent to passwordless root:
  the caller chooses the destination. Pinning literal paths in sudoers narrows it but leaves
  the staged source under a directory the caller controls, and sudoers path matching is
  subtle enough that a mistake would be silent. A root-owned script moves the decision out of
  the caller's reach.
- **Rejected: running the whole `make init` for a config change.** It is the status quo, and
  the drift report on 2026-08-13 showed why it is wrong: exactly one of seven files had
  drifted, and `init` would have reasserted six identical files, re-run `daemon-reload`,
  re-downloaded the CA and re-validated sudoers to deliver two lines.
- **Risk: the installer script and the Makefile table can disagree.** Task 1 keeps the
  Makefile table single-sourced for the drift check, but Task 2 restates the destinations
  inside the script. A mismatch would mean `deploy-configs` silently skips a file that
  `config-drift` still checks. Mitigation: the drift check at the end of Task 4 turns that
  disagreement into a failed target rather than a silent gap — which is precisely the failure
  mode this whole plan exists to close, so it must be verified deliberately, not assumed.
- **Out of scope: giving CI the same capability.** Deliberate; see Assumptions.

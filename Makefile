-include .env

SHELL := /bin/bash

ROOT_DIRECTORY := $(shell pwd)
PROJECT_NAME := $(shell basename "$(PWD)")
VERSION := $(shell git describe --tags --always)
BRANCH := $(shell git rev-parse --abbrev-ref HEAD)
BUILD := $(shell git rev-parse --short HEAD)
TIME := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

BUILD_OPTIONS := "-s -w -X main.BuildVersion=${BRANCH} -X main.BuildTime=${TIME} -X main.BuildHash=${BUILD}"

GOLANGCI_VERSION := v2.12.2
# Revision the adoption gate compares against: only code newer than this is
# required to be clean while the standing findings are worked off.
#
# origin/alpha, not origin/main: main only ever moves to the latest non-prerelease
# tag, so it trails alpha by a whole alpha series. Comparing against it would
# classify that entire series as "new code" and demand it be clean today, which is
# the opposite of a baseline — measured at 88 findings against main versus 0
# against alpha.
LINT_BASE ?= origin/alpha

.PHONY: all run build build-collector build-notifier build-web build-wasm build-migrator migrate test lint lint-new format audit audit-help doctor-help swagger clean init backups db-inspect config-drift deploy-configs verify-edge

# Host-side paths of the passwordless config-deploy mechanism. The operator account
# owns the staging directory; the installer it feeds is root-owned and unwritable by
# that account, which is what keeps its NOPASSWD grant narrow. Staging is under
# /opt/beacon rather than /tmp so no other local user can swap a file between the
# upload and the install — the installer runs as root.
OPERATOR_ACCOUNT := pi5_aide
CONFIG_STAGING_DIR := /opt/beacon/config-staging
CONFIG_INSTALLER := /usr/local/sbin/beacon-install-configs

# Configs `deploy-configs` ships. The installer on the host carries the matching
# destination table, and `make config-drift` fails the deploy if the two disagree.
DEPLOYABLE_CONFIGS := \
	./configs/beacon.service \
	./configs/beacon-migrate.service \
	./configs/nginx.beacon.conf \
	./configs/nginx.beacon_common_settings.conf \
	./configs/nginx.beacon_gzip.conf \
	./configs/sqlite_dump.sh

# Configs only `init` may place. The two sudoers grants and the installer itself are
# the privilege boundary: if the installer could write them, the operator account
# could rewrite the grant, or the installer's own destination table, without a
# password and then run it as root. sqlite_dump.env.example is a template the
# operator edits in place, so it is copied only when absent.
BOOTSTRAP_ONLY_CONFIGS := \
	./configs/beacon-deploy.sudoers \
	./configs/beacon-configs.sudoers \
	./configs/beacon-install-configs.sh \
	./configs/sqlite_dump.env.example

INIT_CONFIGS := $(DEPLOYABLE_CONFIGS) $(BOOTSTRAP_ONLY_CONFIGS)

# The one definition of what goes where. `configs/config_drift.sh` parses these
# `install -m MODE /tmp/NAME PATH` lines out of the Makefile *text* to decide what
# to compare, scanning the whole file rather than this recipe — so the table must
# appear exactly once and its paths must stay literal. A second copy in another
# target would double every entry and turn the script's file-count guard into noise;
# a $(VARIABLE) in a destination would be compared against the host as that literal
# string.
#
# $(strip) collapses the block onto one shell line so it can sit inside init's single
# ssh invocation. Without it make splits the expansion across recipe lines and hands
# each one its own shell, which would end the ssh command mid-quote.
define beacon_config_installs
sudo install -m 0755 /tmp/sqlite_dump.sh /opt/beacon/backups/sqlite_dump.sh;
[ -f /opt/beacon/backups/.env ] && echo "skip: backups/.env exists" || { sudo install -m 0600 /tmp/sqlite_dump.env.example /opt/beacon/backups/.env; echo "installed backups/.env (edit GDRIVE_REMOTE)"; };
sudo install -m 0644 /tmp/beacon.service /etc/systemd/system/beacon.service;
sudo install -m 0644 /tmp/beacon-migrate.service /etc/systemd/system/beacon-migrate.service;
sudo install -m 0440 /tmp/beacon-deploy.sudoers /etc/sudoers.d/beacon-deploy && sudo visudo -c;
sudo install -m 0440 /tmp/beacon-configs.sudoers /etc/sudoers.d/beacon-configs && sudo visudo -c;
sudo install -m 0755 /tmp/beacon-install-configs.sh /usr/local/sbin/beacon-install-configs;
sudo install -m 0644 /tmp/nginx.beacon_common_settings.conf /etc/nginx/snippets/beacon.common_settings.conf;
sudo install -m 0644 /tmp/nginx.beacon_gzip.conf /etc/nginx/snippets/beacon.gzip.conf;
sudo install -m 0644 /tmp/nginx.beacon.conf /etc/nginx/sites-available/dev.seilbekskindirov.beacon;
endef



## init: provision the host once — release-layout sandbox (artifacts/+bin/ owned by CI), systemd units, narrow sudoers, config installer, backup script, and the Cloudflare nginx vhost
init:
	scp $(INIT_CONFIGS) be-happy.kz:/tmp/
	ssh -t be-happy.kz 'set -e; \
		CI=github_aide; \
		OP=$(OPERATOR_ACCOUNT); \
		sudo install -d -o $$CI -g $$CI -m 0755 /opt/beacon/artifacts /opt/beacon/bin; \
		sudo install -d -m 0755 /opt/beacon/logs /opt/beacon/backups; \
		sudo install -d -o $$OP -g $$OP -m 0700 $(CONFIG_STAGING_DIR); \
		sudo chown root:root /opt/beacon && sudo chmod 0755 /opt/beacon; \
		[ -f /opt/beacon/.env ] && sudo chown root:root /opt/beacon/.env && sudo chmod 0600 /opt/beacon/.env || true; \
		[ -f /opt/beacon/beacon.sqlite ] && sudo chown root:root /opt/beacon/beacon.sqlite && sudo chmod 0600 /opt/beacon/beacon.sqlite || true; \
		for w in collector.sh notifier.sh; do [ -f /opt/beacon/$$w ] && sudo chown root:root /opt/beacon/$$w && sudo chmod 0750 /opt/beacon/$$w || true; done; \
		sudo mkdir -p /etc/nginx/certificates/cloudflare /etc/nginx/snippets /etc/nginx/sites-available /etc/nginx/sites-enabled; \
		sudo curl -fsSL https://developers.cloudflare.com/ssl/static/authenticated_origin_pull_ca.pem -o /etc/nginx/certificates/cloudflare/origin-pull-ca.pem; \
		$(strip $(beacon_config_installs)) \
		sudo systemctl daemon-reload; \
		sudo ln -sfn /etc/nginx/sites-available/dev.seilbekskindirov.beacon /etc/nginx/sites-enabled/dev.seilbekskindirov.beacon.conf; \
		sudo rm -f $(addprefix /tmp/,$(notdir $(INIT_CONFIGS))); \
		if sudo test -s /etc/nginx/certificates/cloudflare/seilbekskindirov.dev.pem && sudo test -s /etc/nginx/certificates/cloudflare/seilbekskindirov.dev.key; then \
			sudo nginx -t && sudo systemctl reload nginx && echo "nginx: beacon edge vhost live"; \
		else \
			echo "WARNING: /etc/nginx/certificates/cloudflare/seilbekskindirov.dev.{pem,key} missing — place the Cloudflare Origin cert, then rerun"; \
		fi'
	echo "init done"

## deploy-configs: ship configs/ to a provisioned host and reload — no password, no re-provisioning
deploy-configs:
	@ssh be-happy.kz 'test -d $(CONFIG_STAGING_DIR) && test -x $(CONFIG_INSTALLER)' || { \
		echo "deploy-configs: $(CONFIG_INSTALLER) or $(CONFIG_STAGING_DIR) is not on the host."; \
		echo "deploy-configs: this host is not bootstrapped for config deploys — run 'make init' once; it needs a password."; \
		exit 1; \
	}
	scp $(DEPLOYABLE_CONFIGS) be-happy.kz:$(CONFIG_STAGING_DIR)/
	@ssh be-happy.kz 'sudo -n $(CONFIG_INSTALLER)' || { \
		status=$$?; \
		echo "deploy-configs: the installer exited $$status."; \
		echo "deploy-configs: if sudo asked for a password, /etc/sudoers.d/beacon-configs is absent or stale — run 'make init'."; \
		exit $$status; \
	}
	@$(MAKE) --no-print-directory config-drift
	@$(MAKE) --no-print-directory verify-edge



## run: apply migrations, then start migrator, collector, notifier and web locally from source
run: migrate
	@set -a; . .env; set +a; CGO_ENABLED=0 go run -ldflags ${BUILD_OPTIONS} ./cmd/migrator/main.go   --logs-dir ./build/logs
	@set -a; . .env; set +a; CGO_ENABLED=0 go run -ldflags ${BUILD_OPTIONS} ./cmd/collector/main.go  --logs-dir ./build/logs
	@set -a; . .env; set +a; CGO_ENABLED=0 go run -ldflags ${BUILD_OPTIONS} ./cmd/notifier/main.go   --logs-dir ./build/logs
	@set -a; . .env; set +a; CGO_ENABLED=0 go run -ldflags ${BUILD_OPTIONS} ./cmd/web                --logs-dir ./build/logs --api-dsn "$${API_DSN:-https://localhost/}"

## migrate: apply pending SQL migrations
migrate: build
	@set -a; . .env; set +a; ./build/migrator

## build-wasm: produce the three embedded static assets — wasm_exec.js, app.wasm, app.wasm.gz
##
## The single definition of how the browser bundle is built. The release workflow calls
## this target rather than restating the commands: it used to restate them and omitted the
## gzip, so production served 4.59 MB where 1.21 MB would do, and no local run could
## reproduce it because `make build` gzipped correctly (#79).
##
## No prerequisites on purpose — CI must not reformat the tree on its way to a build.
build-wasm:
	cp "$$(go env GOROOT)/lib/wasm/wasm_exec.js" ./cmd/web/static/wasm_exec.js
	CGO_ENABLED=0 GOOS=js GOARCH=wasm go build -o ./cmd/web/static/app.wasm ./cmd/wasm
	gzip -kfn -9 ./cmd/web/static/app.wasm

## build: format, build the WASM bundle (+gzip), then compile all service binaries into ./build/
build: format build-wasm
	go vet ./...
	CGO_ENABLED=0 go build -o ./build/collector  -ldflags ${BUILD_OPTIONS} ./cmd/collector/main.go
	CGO_ENABLED=0 go build -o ./build/notifier   -ldflags ${BUILD_OPTIONS} ./cmd/notifier/main.go
	CGO_ENABLED=0 go build -o ./build/migrator   -ldflags ${BUILD_OPTIONS} ./cmd/migrator
	CGO_ENABLED=0 go build -o ./build/web        -ldflags ${BUILD_OPTIONS} ./cmd/web
	CGO_ENABLED=0 go build -o ./build/doctor     -ldflags ${BUILD_OPTIONS} ./cmd/doctor

## doctor-help: print doctor combined usage and subcommand descriptions
doctor-help: build
	./build/doctor --help 2>&1 || true

## audit-help: print doctor audit usage and exit codes
audit-help: build
	./build/doctor audit --help 2>&1 || true



## test: format, go vet, the full race-enabled native suite, and the WASM-runtime tests (WASM skipped with a warning if Node is absent)
test: format
	go clean -cache
	CGO_ENABLED=0 go vet ./...
	# -race requires cgo: macOS bundles the race runtime so CGO_ENABLED=0 works
	# there, but Linux (CI) needs a C toolchain. The pure-Go production build
	# stays CGO_ENABLED=0; this is the documented race-detector exception.
	CGO_ENABLED=1 go test -race ./...
	@if command -v node >/dev/null 2>&1; then \
		echo "running WASM tests..."; \
		CGO_ENABLED=0 GOOS=js GOARCH=wasm go test \
			-exec "$$(go env GOROOT)/lib/wasm/go_js_wasm_exec" \
			./cmd/wasm/dom/... ./cmd/wasm/apiclient/...; \
	else \
		echo "WARNING: 'node' not found — skipping WASM tests (install Node.js 18+ to run them)"; \
	fi

## lint: golangci-lint over the whole tree, then the text-level review checks
lint:
	@command -v golangci-lint >/dev/null 2>&1 || { \
		echo "golangci-lint not found — install $(GOLANGCI_VERSION): https://golangci-lint.run/docs/welcome/install/local/"; \
		exit 1; \
	}
	CGO_ENABLED=0 golangci-lint run ./...
	@scripts/lint-checks.sh

## lint-new: lint only code changed since $(LINT_BASE); this is the mergeable gate
lint-new:
	@command -v golangci-lint >/dev/null 2>&1 || { \
		echo "golangci-lint not found — install $(GOLANGCI_VERSION): https://golangci-lint.run/docs/welcome/install/local/"; \
		exit 1; \
	}
	CGO_ENABLED=0 golangci-lint run --new-from-rev=$(LINT_BASE) ./...
	@scripts/lint-checks.sh $(LINT_BASE)

## audit: probe seeded rate sources; default audits all sources; override with ARGS="--source halyk_usd" (exits non-zero on any MISS)
ARGS ?= --all
audit: build
	./build/doctor audit $(ARGS)

## swagger: regenerate Swagger/OpenAPI documentation
swagger:
	swag init -g cmd/web/main.go -o internal/gateway/swagger



## format: run go fmt across all packages
format:
	go fmt ./...



## clean: remove built binaries and generated WASM assets, then go mod tidy
clean:
	rm -f ./build/collector ./build/notifier ./build/migrator ./build/web ./build/beacon.db
	rm -f ./build/doctor
	rm -f ./cmd/web/static/app.wasm ./cmd/web/static/wasm_exec.js
	rm -rf ./build/static
	go mod tidy


## backups: pull the latest DB snapshot + service logs from the host into one archive (./backups/beacon.<stamp>.tar.gz)
backups:
	@mkdir -p ./backups ./tmp
	@stamp=$$(date -u +%Y%m%d-%H%M%S); \
	tmpdir=./tmp/backups-beacon; \
	rm -rf $$tmpdir; mkdir -p $$tmpdir; \
	latest=$$(ssh be-happy.kz "ls -1t /opt/beacon/backups/beacon.*.sqlite /opt/beacon/backups/beacon.*.sqlite.gz 2>/dev/null | head -n1"); \
	if [ -n "$$latest" ]; then \
		age=$$(ssh be-happy.kz "now=\$$(date -u +%s); mtime=\$$(stat -c %Y '$$latest' 2>/dev/null || stat -f %m '$$latest'); echo \$$(( (now - mtime) / 60 ))"); \
		echo "db:   $$latest ($$age min old)"; \
		scp be-happy.kz:$$latest $$tmpdir/; \
		case "$$latest" in *.gz) gunzip -f $$tmpdir/*.sqlite.gz;; esac; \
	else \
		echo "db:   no snapshot in /opt/beacon/backups (has sqlite_dump.sh run on the host yet?)"; \
	fi; \
	if ssh be-happy.kz "test -d /opt/beacon/logs"; then \
		echo "logs: /opt/beacon/logs"; \
		scp -r be-happy.kz:/opt/beacon/logs $$tmpdir/logs; \
	else \
		echo "logs: /opt/beacon/logs not present"; \
	fi; \
	if [ -n "$$(ls -A $$tmpdir)" ]; then \
		archive=./backups/beacon.$$stamp.tar.gz; \
		tar -czf $$archive -C $$tmpdir .; \
		echo "archive: $$archive"; \
	else \
		echo "nothing pulled; no archive created"; \
	fi; \
	rm -rf $$tmpdir


## config-drift: report which host config files no longer match configs/ (read-only, no changes)
config-drift:
	@SSH_CMD="ssh be-happy.kz" ./configs/config_drift.sh Makefile

## verify-edge: assert the hashed-asset cache-header invariants against the live edge (read-only)
verify-edge:
	@./configs/verify_edge.sh

## db-inspect: pull the newest host snapshot and open it READ-ONLY; one-shot query with ARGS="SELECT ..."
db-inspect:
	@mkdir -p ./tmp/db-inspect
	@latest=$$(ssh be-happy.kz "ls -1t /opt/beacon/backups/beacon.*.sqlite /opt/beacon/backups/beacon.*.sqlite.gz 2>/dev/null | head -n1"); \
	if [ -z "$$latest" ]; then \
		echo "no snapshot in /opt/beacon/backups — the 00:00 cron has not run, or sqlite_dump.sh is not installed"; \
		exit 1; \
	fi; \
	age=$$(ssh be-happy.kz "now=\$$(date -u +%s); mtime=\$$(stat -c %Y '$$latest' 2>/dev/null || stat -f %m '$$latest'); echo \$$(( (now - mtime) / 60 ))"); \
	echo "snapshot: $$latest"; \
	echo "age:      $$age min (snapshots are cut daily at 00:00 UTC; anything older than your last deploy cannot confirm it)"; \
	local=./tmp/db-inspect/inspect.sqlite; \
	case "$$latest" in \
		*.gz) ssh be-happy.kz "cat '$$latest'" | gunzip -c > $$local;; \
		*)    ssh be-happy.kz "cat '$$latest'" > $$local;; \
	esac; \
	if [ -n "$(ARGS)" ]; then \
		sqlite3 -header -column "file:$$local?immutable=1" "$(ARGS)"; \
	else \
		echo "opening $$local — read-only, edits are impossible and local anyway"; \
		sqlite3 -header -column "file:$$local?immutable=1"; \
	fi


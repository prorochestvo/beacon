-include .env

SHELL := /bin/bash

ROOT_DIRECTORY := $(shell pwd)
PROJECT_NAME := $(shell basename "$(PWD)")
VERSION := $(shell git describe --tags --always)
BRANCH := $(shell git rev-parse --abbrev-ref HEAD)
BUILD := $(shell git rev-parse --short HEAD)
TIME := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

BUILD_OPTIONS := "-s -w -X main.BuildVersion=${BRANCH} -X main.BuildTime=${TIME} -X main.BuildHash=${BUILD}"

.PHONY: all claude_task claude_evaluates_project claude_auto_fix_tests run build build-collector build-notifier build-web build-wasm test test-wasm lint format swagger clean deploy_environment


deploy_environment:
	#scp -r ./configs/nginx.kz_behappy.conf be-happy.kz:/etc/nginx/sites-available/kz.be-happy
	#scp -r ./configs/nginx.kz_behappy_balancer_prime.conf be-happy.kz:/etc/nginx/configurations/kz.be-happy.balancer_prime.conf
	#scp -r ./configs/nginx.kz_behappy_balancer_stage.conf be-happy.kz:/etc/nginx/configurations/kz.be-happy.balancer_stage.conf
	#scp -r ./configs/nginx.kz_behappy_services_prime.conf be-happy.kz:/etc/nginx/configurations/kz.be-happy.services_prime.conf
	#scp -r ./configs/nginx.kz_behappy_services_stage.conf be-happy.kz:/etc/nginx/configurations/kz.be-happy.services_stage.conf
	#scp -r ./configs/nginx.kz_behappy_common_settings.conf be-happy.kz:/etc/nginx/snippets/kz.be-happy.common_settings.conf
	#scp -r ./configs/nginx.kz_behappy_certificates.conf be-happy.kz:/etc/nginx/certificates/kz.be-happy.conf
	#scp -r ./configs/certbot.com_lingocrm.sh be-happy.kz:/opt/letsencrypt/kz.be-happy
	scp -r ./configs/srv.prime_monitor.service be-happy.kz:/tmp/service.prime_monitor
	scp -r ./configs/srv.stage_monitor.service be-happy.kz:/tmp/service.stage_monitor
	#ssh -t be-happy.kz "sudo mv /tmp/service.prime_monitor /etc/systemd/system/prime_monitor.service"
	#ssh -t be-happy.kz "sudo mv /tmp/service.stage_monitor /etc/systemd/system/stage_monitor.service"
	#ssh -t be-happy.kz "sudo systemctl daemon-reload"
	#ssh be-happy.kz "sudo nginx -s reload"
	echo "done"



claude_dev_test:
	claude ./.claude/logs/task_20260329232102.s02.plan.md --allowedTools "Edit,Write" --system-prompt "$$(cat ./.claude/prompts/developer.md)" 2>&1 | tee -a ./.claude/logs/task_20260329232102.s03.implementation.md

## claude_implement:
claude_implement:
	$(eval TIME_NUM := $(shell date -u +%Y%m%d%H%M%S))
	vi ./.claude/logs/task_$(TIME_NUM).design.md
	echo "Implement this plan. step by step, following the order of tasks and phases. Do not skip any steps. Do not change the task descriptions or acceptance criteria. Do not add any new tasks. Focus on correctness and completeness for each task before moving to the next one. " | tee ./.claude/plans/task_$(TIME_NUM).md
	claude ./.claude/logs/task_$(TIME_NUM).design.md --allowedTools "Read" --system-prompt "$$(cat ./.claude/prompts/architect.md)" 2>&1 | tee -a ./.claude/plans/task_$(TIME_NUM).md
	claude ./.claude/plans/task_$(TIME_NUM).md --allowedTools "Edit,Write" --system-prompt "$$(cat ./.claude/prompts/developer.md)" 2>&1 | tee -a ./.claude/logs/task_$(TIME_NUM).implementation.md

## claude_implement_plan:
claude_implement_plan:
	$(eval TIME_NUM := $(shell date -u +%Y%m%d%H%M%S))
	vi ./.claude/logs/task_$(TIME_NUM).design.md
	echo "Implement this plan. step by step, following the order of tasks and phases. Do not skip any steps. Do not change the task descriptions or acceptance criteria. Do not add any new tasks. Focus on correctness and completeness for each task before moving to the next one. " | tee ./.claude/plans/task_$(TIME_NUM).md
	claude ./.claude/logs/task_$(TIME_NUM).design.md --allowedTools "Read" --system-prompt "$$(cat ./.claude/prompts/architect.md)" 2>&1 | tee -a ./.claude/plans/task_$(TIME_NUM).md

## claude_implement_task:
claude_implement_task:
	claude ./.claude/plans/task_20260412054316.md --allowedTools "Edit,Write" --system-prompt "$$(cat ./.claude/prompts/developer.md)" 2>&1 | tee -a ./.claude/logs/task_$(TIME_NUM).implementation.md

## claude_evaluate:
claude_evaluate:
	$(eval TIME_NUM := $(shell date -u +%Y%m%d%H%M%S))
	claude "you need to evaluate the project. pros and cons." --allowedTools "Read" --system-prompt "$$(cat ./.claude/prompts/architect.md)" 2>&1 | tee -a ./.claude/logs/task_$(TIME_NUM).evaluation.md

claude_reviewer:
	$(eval TIME_NUM := $(shell date -u +%Y%m%d%H%M%S))
	claude "could you review my changes. detaisl: - design: .claude/logs/task_20260414033233.design.md; - plan: .claude/plans/task_20260414033233.md; - implementation: .claude/logs/task_20260414033233.implementation.md" --allowedTools "Read" --system-prompt "$$(cat ./.claude/prompts/reviewer.md)" 2>&1 | tee -a ./.claude/logs/task_$(TIME_NUM).review.md

## claude_auto_fix_tests:
claude_auto_fix_tests:
	$(eval TIME_NUM := $(shell date -u +%Y%m%d%H%M%S))
	go clean -cache
	CGO_ENABLED=0 go vet ./... 2>&1 | tee ./.claude/logs/issues_$(TIME_NUM).s01.logs.md
	CGO_ENABLED=0 go test -race -timeout 5m ./... 2>&1 | tee -a ./.claude/logs/issues_$(TIME_NUM).s01.logs.md
	claude ./.claude/logs/issues_$(TIME_NUM).s01.logs.md --allowedTools "Edit,Write" --system-prompt "$$(cat ./.claude/prompts/tester.md)" 2>&1 | tee -a ./.claude/logs/issues_$(TIME_NUM).s02.fixes.md



## run:
run: build
	@set -a; . .env; set +a; CGO_ENABLED=0 go run -ldflags ${BUILD_OPTIONS} ./cmd/collector/main.go  --logs-dir ./build/logs
	@set -a; . .env; set +a; CGO_ENABLED=0 go run -ldflags ${BUILD_OPTIONS} ./cmd/notifier/main.go   --logs-dir ./build/logs
	@set -a; . .env; set +a; CGO_ENABLED=0 go run -ldflags ${BUILD_OPTIONS} ./cmd/web                 --logs-dir ./build/logs --api-dsn "$${API_DSN:-https://localhost/}"



## build:
build:
	cp "$$(go env GOROOT)/lib/wasm/wasm_exec.js" ./cmd/web/static/wasm_exec.js
	CGO_ENABLED=0 GOOS=js GOARCH=wasm go build -o ./cmd/web/static/app.wasm ./cmd/wasm/main.go
	CGO_ENABLED=0 go build -o ./build/collector -ldflags ${BUILD_OPTIONS} ./cmd/collector/main.go
	CGO_ENABLED=0 go build -o ./build/notifier  -ldflags ${BUILD_OPTIONS} ./cmd/notifier/main.go
	CGO_ENABLED=0 go build -o ./build/web       -ldflags ${BUILD_OPTIONS} ./cmd/web



## test:
test: format
	go clean -cache
	CGO_ENABLED=0 go vet ./...
	CGO_ENABLED=0 go test -race ./...

## test-wasm: run tests for WASM-only packages under the WASM runtime (requires Node)
test-wasm:
	@if ! command -v node >/dev/null 2>&1; then \
		echo "Error: 'node' is required for test-wasm. Install Node.js 18+ first."; \
		exit 1; \
	fi
	CGO_ENABLED=0 GOOS=js GOARCH=wasm go test \
		-exec "$$(go env GOROOT)/lib/wasm/go_js_wasm_exec" \
		./internal/wasm/dom/... ./internal/wasm/apiclient/...

## lint: run go vet across all packages
lint: format
	CGO_ENABLED=0 go vet ./...

## swagger: regenerate Swagger/OpenAPI documentation
swagger:
	swag init -g cmd/web/main.go -o internal/gateway/swagger



## format:
format:
	go fmt ./...



## clean:
clean:
	rm -f ./build/monitor ./build/collector ./build/notifier ./build/web ./build/monitor.db
	rm -f ./cmd/web/static/app.wasm ./cmd/web/static/wasm_exec.js
	rm -rf ./build/static
	go mod tidy

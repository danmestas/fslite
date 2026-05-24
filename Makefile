.PHONY: build test tidy clean wasm-build wasm-build-js docker-image docker-e2e docker-down demo demo-clean compose-up

BINARY = fslite
COMPOSE = docker compose -f docker/docker-compose.yml

# Container target arch. Defaults to the host's GOARCH so `make docker-image`
# does the right thing on both Apple Silicon (arm64) and Linux amd64 CI
# runners. Override with `make docker-image TARGETARCH=amd64` for explicit
# control.
TARGETARCH ?= $(shell go env GOARCH)

build:
	go build -buildvcs=false -o $(BINARY) ./cmd/fslite

test:
	go test -buildvcs=false ./engine/... ./vfs/...

tidy:
	go mod tidy

# --- Docker e2e ---

bin/fslite-linux-$(TARGETARCH):
	GOOS=linux GOARCH=$(TARGETARCH) CGO_ENABLED=0 go build -ldflags="-s -w" -o $@ ./cmd/fslite

bin/fslite-seed-linux-$(TARGETARCH):
	GOOS=linux GOARCH=$(TARGETARCH) CGO_ENABLED=0 go build -ldflags="-s -w" -o $@ ./cmd/fslite-seed

docker-image: bin/fslite-linux-$(TARGETARCH) bin/fslite-seed-linux-$(TARGETARCH)
	docker build --build-arg TARGETARCH=$(TARGETARCH) -t fslite:e2e -f docker/Dockerfile .

docker-e2e: docker-image
	RUN_DOCKER_E2E=1 go test -v -timeout 5m ./docker/

docker-down:
	$(COMPOSE) down -v

# --- Interactive demos ---

# Single host-process demo: builds the binary, creates a fresh repo in
# ./demo-data/repo.fossil, starts fslite on port 8080. No NATS, no
# second agent — just the simplest path for "mount in Finder and see".
# Ctrl-C to stop; demo-data/ is preserved for inspection.
demo: build
	@mkdir -p demo-data
	@echo "Starting fslite on http://127.0.0.1:8080"
	@echo "Mount in Finder: Cmd+K → http://127.0.0.1:8080"
	@echo "Mount via cadaver: cadaver http://127.0.0.1:8080"
	@echo "Trigger a commit: curl -X POST http://127.0.0.1:8080/_admin/commit -d 'my message'"
	@echo
	AGENT_ID=local \
	REPO_PATH=$(PWD)/demo-data/repo.fossil \
	HTTP_ADDR=127.0.0.1:8080 \
	./$(BINARY)

demo-clean:
	rm -rf demo-data/

# Multi-agent demo via the docker-compose stack; brings everything up
# and leaves it running for interactive exploration. Mount in Finder:
#   Cmd+K → http://127.0.0.1:18081  (agent A)
#   Cmd+K → http://127.0.0.1:18082  (agent B)
# Tear down with `make docker-down`.
compose-up: docker-image
	$(COMPOSE) up -d
	@echo
	@echo "Agent A WebDAV: http://127.0.0.1:18081"
	@echo "Agent B WebDAV: http://127.0.0.1:18082"
	@echo "Trigger a commit on A: curl -X POST http://127.0.0.1:18081/_admin/commit -d 'msg'"
	@echo "Stop:                  make docker-down"

# Cross-compile the production code for WASI Preview 1. Runtime
# validation is blocked on the wasi/SQLite fsync interaction;
# this target verifies the compilation surface stays portable.
wasm-build:
	GOOS=wasip1 GOARCH=wasm go build ./...

# Cross-compile for the browser/JS WASM target (uses the ncruces driver
# + go-sqlite3-opfs when wired up at the caller).
wasm-build-js:
	GOOS=js GOARCH=wasm go build ./...

clean:
	rm -rf $(BINARY) bin/

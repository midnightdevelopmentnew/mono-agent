VERSION := $(shell git describe --tags --always 2>/dev/null || echo "dev")
LDFLAGS := -X main.version=$(VERSION) -X main.buildDate=$(shell date -u +%Y-%m-%dT%H:%M:%SZ)

# Both the CLI and the Wails GUI share internal/ packages — build them together.
.PHONY: build
build: build-cli build-app

.PHONY: build-cli
build-cli:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/monoagentcli ./cmd/monoes

.PHONY: build-app
build-app:
	cd wails-app && wails build -ldflags "$(LDFLAGS)" -o ../bin/MonoAgent

.PHONY: dev
dev:
	cd wails-app && wails dev

.PHONY: build-all
build-all: build-cli
	GOOS=darwin  GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/monoagentcli-darwin-amd64 ./cmd/monoes
	GOOS=darwin  GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/monoagentcli-darwin-arm64 ./cmd/monoes
	GOOS=linux   GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/monoagentcli-linux-amd64 ./cmd/monoes
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/monoagentcli-windows-amd64.exe ./cmd/monoes

.PHONY: test
test:
	go test -race -v ./...

.PHONY: lint
lint:
	golangci-lint run ./...

.PHONY: clean
clean:
	rm -rf bin/

.PHONY: tidy
tidy:
	go mod tidy
	cd wails-app && go mod tidy

# Manual release: bump version, tag, push. Usage: make release [v=minor|major]
# Every push to master auto-creates a patch release via GitHub Actions.
# Use this for intentional minor/major bumps:
#   make release          → v0.1.5 → v0.2.0 (minor)
#   make release v=major  → v0.2.0 → v1.0.0
.PHONY: release
release:
	@LAST=$$(git tag --sort=-v:refname | head -1); \
	if [ -z "$$LAST" ]; then \
		NEXT="v0.1.0"; \
	else \
		MAJOR=$$(echo $$LAST | sed 's/v//' | cut -d. -f1); \
		MINOR=$$(echo $$LAST | sed 's/v//' | cut -d. -f2); \
		PATCH=$$(echo $$LAST | sed 's/v//' | cut -d. -f3); \
		case "$(v)" in \
			major) MAJOR=$$((MAJOR+1)); MINOR=0; PATCH=0;; \
			minor) MINOR=$$((MINOR+1)); PATCH=0;; \
			*)     MINOR=$$((MINOR+1)); PATCH=0;; \
		esac; \
		NEXT="v$$MAJOR.$$MINOR.$$PATCH"; \
	fi; \
	echo "Tagging $$NEXT ..."; \
	git tag -a "$$NEXT" -m "Release $$NEXT"; \
	git push origin master --tags; \
	echo "✓ Pushed $$NEXT — GitHub Actions will create the release"

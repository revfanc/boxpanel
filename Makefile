GO ?= go
PNPM ?= pnpm
VERSION ?= dev

export CGO_ENABLED := 0

.PHONY: build build-web dev-web dev-server lint test tools

build: build-web
	mkdir -p bin
	$(GO) build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/boxpanel ./cmd/boxpanel

build-web:
	$(PNPM) --dir web build

dev-web:
	$(PNPM) --dir web dev

# Build the embedded shell even on a fresh checkout; --dev initializes ./dev.db.
dev-server: build-web
	$(GO) run ./cmd/boxpanel serve --dev

lint:
	$(GO) vet ./...
	@unformatted=$$(gofmt -l cmd web/*.go); \
	if [ -d internal ]; then unformatted="$$unformatted $$(gofmt -l internal)"; fi; \
	if [ -n "$$(printf '%s' "$$unformatted" | tr -d '[:space:]')" ]; then \
		printf 'Run gofmt on:\n%s\n' "$$unformatted"; exit 1; \
	fi
	$(PNPM) --dir web lint

test:
	@if [ -x "$(CURDIR)/.tools/bin/sing-box" ]; then \
		PATH="$(CURDIR)/.tools/bin:$$PATH" $(GO) test ./...; \
	else $(GO) test ./...; fi
	$(PNPM) --dir web typecheck
	$(PNPM) --dir web lint
	$(PNPM) --dir web test

tools:
	$(GO) run ./cmd/tools

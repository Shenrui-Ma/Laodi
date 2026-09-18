GO ?= go

.PHONY: build test check

build:
	$(GO) build -trimpath -o bin/laodi ./cmd/laodi
	sh platform/macos/notifier/build.sh

test:
	$(GO) test -race ./...

check:
	$(GO) vet ./...
	@test -z "$$($(GO) fmt ./...)"

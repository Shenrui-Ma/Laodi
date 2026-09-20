GO ?= go

.PHONY: build test check

ifeq ($(OS),Windows_NT)
build:
	$(GO) build -trimpath -o bin/laodi.exe ./cmd/laodi
	$(GO) build -trimpath -ldflags "-H windowsgui" -o bin/laodi-host.exe ./cmd/laodi
else
build:
	$(GO) build -trimpath -o bin/laodi ./cmd/laodi
	sh platform/macos/notifier/build.sh
endif

test:
	$(GO) test -race ./...

check:
	$(GO) vet ./...
ifeq ($(OS),Windows_NT)
	powershell.exe -NoProfile -NonInteractive -Command "$$changed = & '$(GO)' fmt ./...; if ($$changed) { throw 'Go files required formatting' }"
else
	@test -z "$$($(GO) fmt ./...)"
endif

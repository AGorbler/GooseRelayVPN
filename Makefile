VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
GO ?= go

.PHONY: all build client server test race vet tidy clean release-local

all: build

build: client server

client:
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/goose-client ./cmd/client

server:
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/goose-server ./cmd/server

test:
	$(GO) test -count=1 ./...

race:
	$(GO) test -race -count=1 ./...

vet:
	$(GO) vet ./...

tidy:
	$(GO) mod tidy

clean:
	rm -rf bin dist

# Local cross-compile dry run, mirroring the GitHub release matrix.
release-local:
	@for plat in linux/amd64 linux/arm64 windows/amd64 darwin/amd64 darwin/arm64; do \
	  os=$${plat%/*}; arch=$${plat#*/}; \
	  name=GooseRelayVPN-$(VERSION)-$$os-$$arch; \
	  ext=$$([ "$$os" = "windows" ] && echo ".exe" || echo ""); \
	  mkdir -p dist/$$name; \
	  echo "==> $$os/$$arch"; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o dist/$$name/goose-client$$ext ./cmd/client; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o dist/$$name/goose-server$$ext ./cmd/server; \
	  cp -r apps_script client_config.example.json server_config.example.json README.md dist/$$name/; \
	done
	@echo "==> done. binaries in dist/"

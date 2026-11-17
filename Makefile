.PHONY: build test test-install lint clean install release-snapshot

BINARY_NAME=actr
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "v0.1.0")
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
DATE ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

LDFLAGS=-ldflags "-X main.Version=$(VERSION) -X main.Commit=$(COMMIT) -X main.Date=$(DATE)"
INSTALL_DIR ?= $(HOME)/.local/bin

build:
	CGO_ENABLED=0 go build $(LDFLAGS) -o bin/$(BINARY_NAME) ./cmd/actr

install: build
	mkdir -p $(INSTALL_DIR)
	cp bin/$(BINARY_NAME) $(INSTALL_DIR)/$(BINARY_NAME)
	@if [ -d "$$(brew --prefix 2>/dev/null)/share/zsh/site-functions" ]; then \
		bin/$(BINARY_NAME) completion zsh > "$$(brew --prefix)/share/zsh/site-functions/_$(BINARY_NAME)"; \
		echo "Installed zsh completion to $$(brew --prefix)/share/zsh/site-functions/_$(BINARY_NAME)"; \
	fi

release-snapshot:
	goreleaser release --snapshot --clean --skip=publish

test:
	go test -v -race ./...

test-install:
	./test/install_test.sh

lint:
	golangci-lint run ./...

clean:
	rm -rf bin/ _work/ _temp/ dist/



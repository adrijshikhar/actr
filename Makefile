.PHONY: build test test-install lint clean install release-snapshot web-build

BINARY_NAME=actr
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "v0.1.0")
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
DATE ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

LDFLAGS=-ldflags "-s -w -X main.Version=$(VERSION) -X main.Commit=$(COMMIT) -X main.Date=$(DATE)"
INSTALL_DIR ?= $(HOME)/.local/bin
COMPLETION_SHELL ?= zsh

web-build:
	cd web && npm install && npm run build

build: web-build
	CGO_ENABLED=0 go build -trimpath $(LDFLAGS) -o bin/$(BINARY_NAME) ./cmd/actr

install: build
	mkdir -p $(INSTALL_DIR)
	cp bin/$(BINARY_NAME) $(INSTALL_DIR)/$(BINARY_NAME)
	bin/$(BINARY_NAME) completion install $(COMPLETION_SHELL)

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



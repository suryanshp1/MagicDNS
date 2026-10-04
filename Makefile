.DEFAULT_GOAL := build

.PHONY: build dev test test-race fmt vet check

VERSION ?= 0.0.1-dev
LDFLAGS := -s -w -X main.version=$(VERSION)

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/devmesh ./cmd/devmesh

dev:
	go run ./cmd/devmesh serve --config devmesh.example.yaml

test:
	go test ./...

test-race:
	go test -race ./...

fmt:
	gofmt -w cmd internal

vet:
	go vet ./...

check: fmt vet test

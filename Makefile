.PHONY: build run test fmt vet tidy clean

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.1.0)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildDate=$(DATE)

build:
	go build -ldflags "$(LDFLAGS)" -o bin/argo9s ./cmd/argo9s

run: build
	./bin/argo9s

test:
	go test ./...

fmt:
	go fmt ./...

vet:
	go vet ./...

tidy:
	go mod tidy

clean:
	rm -rf bin/

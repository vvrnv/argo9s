.PHONY: build run test fmt vet tidy clean screenshots

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.2.0)
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

# README screenshots + GIF from offline demo data. Needs vhs + ttyd
# (https://github.com/charmbracelet/vhs) and the JetBrainsMono Nerd Font.
screenshots: build
	vhs docs/screenshots/screenshots.tape
	vhs docs/screenshots/demo.tape

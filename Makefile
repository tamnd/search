# Build into bin/ (gitignored) so the binary never collides with package source.
BINARY  := bin/sx
PKG     := ./cmd/sx
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
	-X main.version=$(VERSION) \
	-X main.commit=$(COMMIT) \
	-X main.date=$(DATE)

.PHONY: build install lib test race vet fmt lint tidy clean run

build:
	@mkdir -p $(dir $(BINARY))
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) $(PKG)

install:
	CGO_ENABLED=0 go install -trimpath -ldflags "$(LDFLAGS)" $(PKG)

# The C ABI shared library. It needs cgo, so it builds separately from the
# default pure-Go binary and stays out of the release pipeline. The header is
# emitted next to the library.
lib:
	@mkdir -p $(dir $(BINARY))
	CGO_ENABLED=1 go build -buildmode=c-shared -o bin/libsearch.so ./cabi

test:
	go test -count=1 ./...

race:
	go test -race -count=1 ./...

vet:
	go vet ./...

fmt:
	gofmt -w -s .

lint:
	golangci-lint run

tidy:
	go mod tidy

clean:
	rm -rf bin dist

run: build
	./$(BINARY) $(ARGS)

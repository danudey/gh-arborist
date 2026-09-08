BINARY  := gh-arborist
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

.PHONY: build test vet fmt check install clean

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) .

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

# What CI should run.
check: vet test
	@test -z "$$(gofmt -l . )" || { echo "gofmt needed:"; gofmt -l .; exit 1; }

# Install the local build as a gh extension.
install: build
	gh extension install .

clean:
	rm -f $(BINARY) $(BINARY).exe

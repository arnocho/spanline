BINARY := spanline
PKG := ./cmd/spanline
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test lint fmt vet fixtures airgap install clean

build:
	go build -trimpath -ldflags '$(LDFLAGS)' -o $(BINARY) $(PKG)

airgap:
	go build -trimpath -tags airgap -ldflags '$(LDFLAGS)' -o $(BINARY) $(PKG)

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

fixtures:
	python3 scripts/make_fixtures.py

install:
	go install -trimpath -ldflags '$(LDFLAGS)' $(PKG)

clean:
	rm -f $(BINARY)

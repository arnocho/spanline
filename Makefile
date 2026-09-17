BINARY := spanline
PKG := ./cmd/spanline
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test lint fmt vet fixtures airgap install clean record

record: build
	./$(BINARY) demo --cast docs/demo.cast --cast-width 108 --cast-height 30
	agg docs/demo.cast docs/demo.gif --font-family Menlo --font-size 15 --fps-cap 20 --last-frame-duration 2 --theme dracula
	ffmpeg -y -hide_banner -loglevel error -i docs/demo.gif -vf "scale=trunc(iw/2)*2:trunc(ih/2)*2,format=yuv420p" -movflags +faststart -r 20 docs/demo.mp4

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

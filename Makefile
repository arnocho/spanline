BINARY := spanline
PKG := ./cmd/spanline
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test lint fmt vet fixtures airgap install clean record stills

record: build
	./$(BINARY) demo --cast docs/demo.cast --cast-width 108 --cast-height 30
	agg docs/demo.cast docs/demo.gif --font-family Menlo --font-size 15 --fps-cap 20 --last-frame-duration 2 --theme dracula
	ffmpeg -y -hide_banner -loglevel error -i docs/demo.gif -vf "scale=trunc(iw/2)*2:trunc(ih/2)*2,format=yuv420p" -movflags +faststart -r 20 docs/demo.mp4

# One PNG per screen, cut from a recording that holds each screen for three seconds. The time
# of each cut is read from the cast itself, so the stills follow the code, not a hand kept list.
stills: build
	./$(BINARY) demo --cast docs/stills.cast --cast-script stills --cast-width 108 --cast-height 30
	@# each still is rendered from a one frame cast of its own, so no earlier frame can bleed into it
	@mkdir -p docs/img
	@# name:marker:seconds after the marker first appears; the reading screen waits for its last line
	@for triple in "reading:reading:3.1" "overview:look at this first:1.6" "incident:separates them:1.6" "impact:what that breaks:1.6" "keys:what the three views answer:1.6" "evidence:scroll:1.6"; do \
	  name=$${triple%%:*}; rest=$${triple#*:}; marker=$${rest%:*}; offset=$${rest##*:}; \
	  t=$$(grep -m1 -F "$$marker" docs/stills.cast | sed -E 's/^\[([0-9.]+),.*/\1/'); \
	  at=$$(echo "$$t + $$offset" | bc); \
	  head -1 docs/stills.cast > docs/still-$$name.cast; \
	  awk -v at="$$at" 'NR>1 { split($$0, a, ","); t=substr(a[1], 2); if (t+0 <= at+0) line=$$0 } END { sub(/^\[[0-9.]+,/, "[0,", line); print line }' docs/stills.cast >> docs/still-$$name.cast; \
	  agg docs/still-$$name.cast docs/still-$$name.gif --font-family Menlo --font-size 15 --last-frame-duration 1 --theme dracula >/dev/null 2>&1; \
	  ffmpeg -y -hide_banner -loglevel error -i docs/still-$$name.gif -frames:v 1 docs/img/$$name.png; \
	  rm -f docs/still-$$name.cast docs/still-$$name.gif; \
	  echo "docs/img/$$name.png from the frame at $${at}s"; \
	done

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

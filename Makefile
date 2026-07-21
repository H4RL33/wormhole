# Wormhole build targets. All compiled binaries land in $(DIST), which is
# gitignored. Use `make build` instead of bare `go build ./cmd/...`, which
# drops binaries in the repo root.

DIST := dist
BINARIES := wormhole wormholed wormhole-server

.PHONY: all build clean test vet $(BINARIES)

all: build

build: $(BINARIES)

$(BINARIES):
	go build -o $(DIST)/$@ ./cmd/$@

test:
	go test ./...

vet:
	go vet ./...

clean:
	rm -rf $(DIST)

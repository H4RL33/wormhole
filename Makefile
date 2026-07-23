# Wormhole build targets. All compiled binaries land in $(DIST), which is
# gitignored. Use `make build` instead of bare `go build ./cmd/...`, which
# drops binaries in the repo root.

DIST := dist
BINARIES := wormhole gatewayd fabric

.PHONY: all build clean test vet check integration coverage race fmt-check naming-check release-rehearsal release-test $(BINARIES)

all: build

build: $(BINARIES)

$(BINARIES):
	go build -o $(DIST)/$@ ./cmd/$@

naming-check:
	@test -x $(DIST)/wormhole
	@test -x $(DIST)/gatewayd
	@test -x $(DIST)/fabric
	@test ! -e $(DIST)/wormholed
	@test ! -e $(DIST)/wormhole-server

test:
	go test ./...

fmt-check:
	@test -z "$$(gofmt -l $$(find . -name '*.go' -not -path './dist/*'))"

race:
	WORMHOLE_INTEGRATION_REQUIRED=1 go test -race ./...

integration:
	WORMHOLE_INTEGRATION_REQUIRED=1 go test ./...

coverage:
	WORMHOLE_INTEGRATION_REQUIRED=1 go test -coverpkg=./... -covermode=atomic -coverprofile=coverage.out ./...
	./.github/scripts/coverage-check.sh coverage.out docs/testing-coverage-exceptions.md

release-test:
	.github/scripts/release_test.sh

release-rehearsal:
	SOURCE_DATE_EPOCH=$$(git show -s --format=%ct HEAD) \
	  .github/scripts/build-release.sh 0.0.0-alpha.rehearsal dist/release
	.github/scripts/verify-release.sh dist/release

check: fmt-check build vet integration race coverage

vet:
	go vet ./...

clean:
	rm -rf $(DIST)

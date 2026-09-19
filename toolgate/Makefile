BINARY := toolgate
PKG    := ./cmd/toolgate
LDFLAGS := -s -w -X github.com/its-aryansingh/toolgate/internal/version.Version=$(shell git describe --tags --always --dirty 2>/dev/null || echo 0.1.0-dev)

.PHONY: all test vet build release clean deps

all: vet test build

test:
	go test ./...

vet:
	gofmt -l cmd internal | tee /dev/stderr | (! read)
	go vet ./...

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) $(PKG)

# deps is a test, not a report: the zero-dependency claim in the README is
# checked here so that adding an import fails the build rather than a review.
deps:
	@test "$$(go list -m all | wc -l)" -eq 1 || (echo "third-party dependency added; see docs/DECISIONS.md T1" && exit 1)
	@echo "zero third-party dependencies"

release:
	@mkdir -p dist
	GOOS=linux   GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/$(BINARY)-linux-amd64   $(PKG)
	GOOS=linux   GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/$(BINARY)-linux-arm64   $(PKG)
	GOOS=darwin  GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/$(BINARY)-darwin-arm64  $(PKG)
	GOOS=darwin  GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/$(BINARY)-darwin-amd64  $(PKG)
	GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/$(BINARY)-windows-amd64.exe $(PKG)

clean:
	rm -rf dist $(BINARY)

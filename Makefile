.PHONY: build test test-race test-cover vet run tidy clean vuln

VERSION_PKG := github.com/renewelches/confkoffer/internal/version
LDFLAGS := -X '$(VERSION_PKG).Version=DevBuild' \
           -X '$(VERSION_PKG).Commit=$(shell git rev-parse --short HEAD)' \
           -X '$(VERSION_PKG).Date=$(shell date -u +%Y-%m-%dT%H:%M:%SZ)'

build:
	go build -ldflags="$(LDFLAGS)" -o bin/confkoffer ./cmd/confkoffer

test:
	go test ./...

vet:
	go vet ./...

test-race:
	go test -race ./...

test-cover:
	go test -coverprofile=cover.out ./... && go tool cover -func=cover.out

run:
	go run ./cmd/confkoffer $(ARGS)

tidy:
	go mod tidy

vuln:
	govulncheck ./...

clean:
	rm -rf bin/ cover.out coverage.html

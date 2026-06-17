.PHONY: build test test-race test-cover run tidy clean

build:
	go build -ldflags="-X 'github.com/renewelches/confkoffer/internal/version.Version=DevBuild' -X 'github.com/renewelches/confkoffer/internal/version.CommitSHA=$(git rev-parse --short HEAD)'" -o bin/confkoffer ./cmd/confkoffer

test:
	go test ./...

test-race:
	go test -race ./...

test-cover:
	go test -coverprofile=cover.out ./... && go tool cover -func=cover.out

run:
	go run ./cmd/confkoffer $(ARGS)

tidy:
	go mod tidy

clean:
	rm -rf bin/ cover.out coverage.html

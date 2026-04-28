.PHONY: build test test-race test-cover run tidy clean

build:
	go build -o bin/confkoffer ./

test:
	go test ./...

test-race:
	go test -race ./...

test-cover:
	go test -coverprofile=cover.out ./... && go tool cover -func=cover.out

run:
	go run ./ $(ARGS)

tidy:
	go mod tidy

clean:
	rm -rf bin/ cover.out coverage.html

.PHONY: build test e2e

build:
	go build -o bin/hashigo ./cmd/hashigo

test:
	go vet ./...
	go test ./...

e2e:
	go test -tags e2e -count=1 -v ./test/e2e/...

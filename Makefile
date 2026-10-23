.PHONY: build fmt fmt-check lint tidy-check test e2e check release-check

# Build the binary into bin/.
build:
	go build -o bin/hashigo ./cmd/hashigo

# Auto-format the code (gofumpt + goimports via golangci-lint).
fmt:
	golangci-lint fmt ./...

# Fail if the code is not formatted (mirrors the CI format gate).
fmt-check:
	golangci-lint fmt --diff ./...

# Run the linters (mirrors the CI lint step).
lint:
	golangci-lint run ./...

# Fail if go.mod/go.sum are not tidy (mirrors the CI module-hygiene gate).
tidy-check:
	go mod tidy
	git diff --exit-code -- go.mod go.sum

# Run tests with the race detector and coverage (mirrors the CI test step).
test:
	gotestsum --format testdox -- -race -coverprofile=coverage.out -covermode=atomic ./...

# Run the Docker E2E: real ssh through bastion -> mid -> ap -> db (mirrors the CI e2e job).
e2e:
	go test -tags e2e -count=1 -timeout 5m -v ./test/e2e/...

# Run everything the CI lint and test jobs check, locally. `make e2e` is separate because it needs Docker.
check: tidy-check lint fmt-check test

# Validate the release configuration and cross-compilation (mirrors release CI).
release-check:
	goreleaser check
	GOOS=windows GOARCH=amd64 go build -o /dev/null ./cmd/hashigo
	GOOS=darwin GOARCH=arm64 go build -o /dev/null ./cmd/hashigo
	GOOS=linux GOARCH=amd64 go build -o /dev/null ./cmd/hashigo

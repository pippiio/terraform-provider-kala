BINARY := terraform-provider-kala
COVER_PROFILE := coverage.out

.PHONY: build test cover cover-html lint fmt fmt-check install-mirror release-check vuln clean help

## build: compile the provider binary
build:
	go build -o $(BINARY) .

## test: run unit tests (no network; acceptance tests are deferred — see spec Non-Goals)
test:
	go test ./... -count=1

## cover: run unit tests with coverage and report the total
cover:
	go test ./... -count=1 -coverprofile=$(COVER_PROFILE) -covermode=atomic
	@go tool cover -func=$(COVER_PROFILE) | tail -1

## cover-html: open an HTML coverage report
cover-html: cover
	go tool cover -html=$(COVER_PROFILE)

## lint: run golangci-lint
lint:
	golangci-lint run

## fmt: format all Go source
fmt:
	gofmt -s -w .

## fmt-check: fail if any file is unformatted (used by CI)
fmt-check:
	@unformatted=$$(gofmt -s -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "Unformatted files:"; echo "$$unformatted"; exit 1; \
	fi
	@echo "All files formatted."

## install-mirror: build and install into Terraform's filesystem mirror for local use
##                 (VERSION defaults to 0.0.0-dev)
install-mirror: build
	@VERSION=$${VERSION:-0.0.0-dev}; \
	PLATFORM=$$(go env GOOS)_$$(go env GOARCH); \
	DEST=$$HOME/.terraform.d/plugins/registry.terraform.io/techchapter/kala/$$VERSION/$$PLATFORM; \
	mkdir -p $$DEST; \
	cp $(BINARY) $$DEST/$(BINARY)_v$$VERSION; \
	echo "installed $$DEST/$(BINARY)_v$$VERSION"

## vuln: scan for vulnerabilities this code can actually reach
vuln:
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

## release-check: validate .goreleaser.yml and dry-run the release build
release-check:
	goreleaser check
	goreleaser build --snapshot --clean --single-target

## clean: remove build and coverage artifacts
clean:
	rm -f $(BINARY) $(COVER_PROFILE) coverage.html

## help: list available targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## //'

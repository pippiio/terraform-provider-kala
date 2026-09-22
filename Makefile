BINARY := terraform-provider-kala
COVER_PROFILE := coverage.out

.PHONY: build test testacc cover cover-html lint fmt fmt-check install-mirror release-check vuln clean help

## build: compile the provider binary
build:
	go build -o $(BINARY) .

## test: run unit tests (hermetic — no network, no credentials)
test:
	go test ./... -count=1

## testacc: run acceptance tests against a real Kala tenant.
##          Requires KALA_API_KEY, KALA_USERNAME, KALA_PASSWORD,
##          KALA_ACC_EMPLOYEE_NUMBER and KALA_ACC_EMAIL. Creates the employee
##          under that number if it does not exist, and CANNOT delete it
##          afterwards — Kala has no delete. Read the Acceptance tests section
##          of README.md before the first run.
testacc:
	TF_ACC=1 go test ./internal/provider/ -run '^TestAcc' -count=1 -v -timeout 30m

## cover: run unit tests with coverage and report the total
cover:
	go test ./... -count=1 -coverprofile=$(COVER_PROFILE) -covermode=atomic
	@go tool cover -func=$(COVER_PROFILE) | tail -1

## cover-html: open an HTML coverage report
cover-html: cover
	go tool cover -html=$(COVER_PROFILE)

## lint: run golangci-lint
##       Uses `go run` so the gate cannot be skipped for want of a local
##       install. It was silently unrunnable on two tracks: the target assumed
##       golangci-lint was on PATH, so `make lint` failed with "command not
##       found" and the gate got asserted rather than executed. `vuln` already
##       worked this way; now so does this.
lint:
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run

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
	DEST=$$HOME/.terraform.d/plugins/registry.terraform.io/pippiio/kala/$$VERSION/$$PLATFORM; \
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

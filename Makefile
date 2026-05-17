.PHONY: test build shell release-changelog-context release-patch release-minor release-major

build:
	go build ./cmd/s46

test:
	go test ./...

shell:
	./scripts/shell

release-changelog-context:
	go run ./scripts/release.go changelog-context

release-patch:
	go run ./scripts/release.go patch

release-minor:
	go run ./scripts/release.go minor

release-major:
	go run ./scripts/release.go major

.PHONY: lint test coverage pre-commit install-hooks build shell release-changelog-context release-patch release-minor release-major

build:
	go build ./cmd/s46

lint:
	@fmt="$$(gofmt -l $$(git ls-files '*.go'))"; \
	if [ -n "$$fmt" ]; then \
		echo "[s46] gofmt needed:"; \
		echo "$$fmt"; \
		exit 1; \
	fi
	go vet ./...

test:
	go test ./...

coverage:
	mkdir -p .coverage
	go test ./... -coverprofile=.coverage/coverage.out
	go tool cover -func=.coverage/coverage.out | tee .coverage/coverage.txt

pre-commit: lint test

install-hooks:
	git config core.hooksPath .githooks
	chmod +x .githooks/pre-commit

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

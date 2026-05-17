.PHONY: test build audit

test:
	go test ./...

build:
	go build ./cmd/s46

audit:
	@echo "checking for JavaScript/npm leftovers"
	@test -z "$$(find . -path ./.git -prune -o \( -name '*.js' -o -name '*.mjs' -o -name '*.cjs' -o -name 'package.json' -o -name 'package-lock.json' -o -name 'node_modules' \) -print)"
	@echo "checking architecture audit"
	@! grep -E '\| .* \| (TODO|Fail|Partial)' docs/spec-audit.md
	@echo "running Go tests"
	@go test ./...

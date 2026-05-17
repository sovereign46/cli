.PHONY: test build shell

build:
	go build ./cmd/s46

test:
	go test ./...

shell:
	./scripts/shell

.PHONY: test build

build:
	go build ./cmd/s46

test:
	@go test ./...

.PHONY: test run fmt

test:
	go test ./...

run:
	go run ./cmd/temporality-runtime

fmt:
	gofmt -w cmd frp

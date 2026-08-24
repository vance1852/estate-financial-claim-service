.PHONY: test race vet build run

test:
	go test ./... -count=1

race:
	go test -race ./... -count=1

vet:
	go vet ./...

build:
	go build ./...

run:
	go run ./cmd/server

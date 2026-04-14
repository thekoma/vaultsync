BINARY := vaultsync
IMAGE  := ghcr.io/thekoma/vaultsync
TAG    ?= latest

.PHONY: build test lint docker run clean

build:
	go build -o $(BINARY) .

test:
	go test -v -race -coverprofile=coverage.out ./...

lint:
	golangci-lint run ./...

docker:
	docker build -t $(IMAGE):$(TAG) .

run: build
	./$(BINARY)

clean:
	rm -f $(BINARY) coverage.out coverage.html

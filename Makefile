VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/CarriedWorldUniverse/agora/internal/version.Version=$(VERSION)

.PHONY: build test vet version clean

build:
	go build -ldflags '$(LDFLAGS)' -o bin/agora ./cmd/agora

test:
	go test -race ./...

vet:
	go vet ./...

version:
	@echo $(VERSION)

clean:
	rm -rf bin/

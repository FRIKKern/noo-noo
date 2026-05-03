.PHONY: build build-cli build-daemon test lint fmt clean install

BINARY := noo-noo
PKG    := ./cmd/noo-noo

build: build-cli build-daemon

build-cli:
	go build -trimpath -ldflags="-s -w" -o bin/noo-noo ./cmd/noo-noo

build-daemon:
	go build -trimpath -ldflags="-s -w" -o bin/noo-nood ./cmd/noo-nood

test:
	go test -race ./...

lint:
	gofmt -l . | tee /dev/stderr | (! read)
	go vet ./...
	golangci-lint run

fmt:
	gofmt -w .

clean:
	rm -rf bin/

install: build
	install -m 0755 bin/$(BINARY) /usr/local/bin/$(BINARY)

.PHONY: build test lint fmt clean install

BINARY := noo-noo
PKG    := ./cmd/noo-noo

build:
	go build -trimpath -ldflags="-s -w" -o bin/$(BINARY) $(PKG)

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

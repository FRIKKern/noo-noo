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

# --- Phase 0.3 menubar app ---
APP_VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo dev)
APP_OUT     := build/Noo-Noo.app

.PHONY: app app-dev app-package app-frontend

app-frontend:
	cd cmd/noo-noo-app/frontend && npm install && npm run build

app: app-frontend
	go build -tags production -o build/noo-noo-app ./cmd/noo-noo-app/

app-dev:
	cd cmd/noo-noo-app/frontend && npm install
	cd cmd/noo-noo-app/frontend && npm run dev &
	go run ./cmd/noo-noo-app/

app-package: app
	rm -rf $(APP_OUT)
	mkdir -p $(APP_OUT)/Contents/MacOS $(APP_OUT)/Contents/Resources
	# Render Info.plist with version substitution. Verify LSUIElement
	# survived the substitution so the bundle is genuinely menubar-only.
	sed "s/{{.Version}}/$(APP_VERSION)/g" cmd/noo-noo-app/Info.plist.tmpl \
		> $(APP_OUT)/Contents/Info.plist
	@grep -q LSUIElement $(APP_OUT)/Contents/Info.plist \
		|| { echo "FATAL: LSUIElement missing from rendered Info.plist"; exit 1; }
	cp build/noo-noo-app $(APP_OUT)/Contents/MacOS/noo-noo-app
	cp cmd/noo-noo-app/build/appicon.png $(APP_OUT)/Contents/Resources/appicon.png
	# Ad-hoc sign so Gatekeeper accepts the bundle on the build machine.
	# This is NOT a notarized signature (Phase 0.4); first-launch will
	# require Right-click > Open to bypass Gatekeeper.
	codesign --force --deep --sign - $(APP_OUT)
	@echo "Built $(APP_OUT) (version: $(APP_VERSION))"

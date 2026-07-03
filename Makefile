# Neocron Classic (NC1) launcher — build helpers.
# The frontend is a prebuilt static site under frontend/dist (no npm step), so a
# plain `go build` produces a working binary via go:embed. `wails build` is also
# supported for packaged, per-platform bundles.

BIN        := nc1-launcher
BUILD_DIR  := build/bin
VERSION    ?= 0.1.0
LDFLAGS    := -X main.Version=$(VERSION) -w -s
# Wails' desktop webview backend is gated behind build tags; without them the
# binary prints "will not build without the correct build tags" and exits.
TAGS       := desktop,production

export GOFLAGS := -mod=mod

.PHONY: all build wails run test vet clean

all: build

## build: compile the launcher with the embedded frontend (current platform)
build:
	mkdir -p $(BUILD_DIR)
	go build -tags "$(TAGS)" -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BIN) .

## wails: package via the Wails toolchain (needs `wails` on PATH)
wails:
	wails build -ldflags "$(LDFLAGS)"

## run: build and run
run: build
	$(BUILD_DIR)/$(BIN)

## test: unit tests for the RE-derived logic (auth/patch/game)
test:
	go test ./pkg/...

## vet: static checks
vet:
	go vet ./...

## clean: remove build artifacts
clean:
	rm -rf $(BUILD_DIR)

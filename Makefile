# danbooru-tag-mcp build configuration
# Usage: make <target>   (requires GNU Make; bundled with Git Bash / MinGW / WSL)

# ---- Variables ----
BINARY   := danbooru-tag-mcp
# Version: prefers git describe (tag or commit); falls back to the default.
# Injected via ldflags into internal/app.Version at build time; no version
# literal is hardcoded in the source.
VERSION  := $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//')
DEFAULT_VERSION := 0.1.0-dev
ifeq ($(VERSION),)
VERSION := $(DEFAULT_VERSION)
endif
DIST     := dist
GO       := go
# Target arch: defaults to the host (go env GOARCH); cross-compile ARM64 with make build GOARCH=arm64.
# Artifacts are stored per-arch (dist/<arch>/danbooru-tag-mcp.exe) so dual-arch builds don't clobber.
GOARCH   ?= $(shell $(GO) env GOARCH)
LDFLAGS  := -s -w -X danbooru-tag-mcp/internal/app.Version=$(VERSION)
# Dev builds additionally inject Bootstrap=off: dist/ dev binaries skip the
# silent bootstrap at startup (no user PATH writes), keeping the environment
# clean; release builds (build-dist) omit this flag.
LDFLAGS_DEV := $(LDFLAGS) -X danbooru-tag-mcp/internal/app.Bootstrap=off
TARGET   := $(DIST)/$(GOARCH)/$(BINARY).exe

# ---- Default target ----
.PHONY: all
all: build

# ---- Build (dev: bootstrap off, no user PATH pollution) ----
# -trimpath strips local paths, -ldflags "-s -w" drops debug symbols for size
# GOOS is pinned to windows (Windows-only project); GOARCH accepts arm64 for cross builds
.PHONY: build
build:
	@echo "[build] $(TARGET) (v$(VERSION), windows/$(GOARCH), dev: no bootstrap)..."
	@mkdir -p $(DIST)/$(GOARCH)
	GOOS=windows GOARCH=$(GOARCH) $(GO) build -trimpath -ldflags "$(LDFLAGS_DEV)" -o $(TARGET) .
	@echo "[ok]   $(TARGET)"

# ---- Build (release flavor: bootstrap on, for release packaging and install.ps1) ----
.PHONY: build-dist
build-dist:
	@echo "[build-dist] $(TARGET) (v$(VERSION), windows/$(GOARCH))..."
	@mkdir -p $(DIST)/$(GOARCH)
	GOOS=windows GOARCH=$(GOARCH) $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(TARGET) .
	@echo "[ok]   $(TARGET)"

# ---- Build and run (args: make run ARGS="version") ----
.PHONY: run
run: build
	./$(TARGET) $(ARGS)

# ---- For releases: portable zip + checksums.txt ----
# Artifacts: dist/danbooru-tag-mcp-windows-$(GOARCH).zip (contains a single exe,
# fetched and matched exactly by `danbooru-tag-mcp upgrade` and install.ps1).
# checksums.txt is generated with sha256sum; file names carry no path prefix
# (matching upgrade's parseChecksum bare-filename convention); all zips under
# dist/ are listed, so building both arches and re-running this target merges
# them into one checksum manifest.
.PHONY: release
release: build-dist
	@powershell -NoProfile -Command "Compress-Archive -Path $(DIST)\$(GOARCH)\$(BINARY).exe -DestinationPath $(DIST)\$(BINARY)-windows-$(GOARCH).zip -Force"
	@cd $(DIST) && sha256sum $$(ls *.zip) > checksums.txt
	@echo "[release] $(DIST)/$(BINARY)-windows-$(GOARCH).zip + checksums.txt"
	@echo "          Upload to the GitHub Release (tag: v$(VERSION)); `upgrade` self-update then works"

# ---- Clean ----
.PHONY: clean
clean:
	@rm -rf $(DIST)
	@echo "[clean] removed $(DIST)/"

# ---- Dependency management ----
.PHONY: tidy
tidy:
	$(GO) mod tidy

# ---- Code checks ----
.PHONY: fmt
fmt:
	$(GO) fmt ./...

.PHONY: vet
vet:
	$(GO) vet ./...

.PHONY: test
test:
	$(GO) test ./...

# ---- Race detection (-race is slower; guards concurrent paths like the api.Client throttle lock) ----
.PHONY: test-race
test-race:
	$(GO) test -race ./...

# ---- Help ----
.PHONY: help
help:
	@echo "danbooru-tag-mcp build targets:"
	@echo "  make build                    dev build (no bootstrap) -> $(DIST)/<arch>/$(BINARY).exe"
	@echo "  make build-dist               release-flavor build (bootstrap on)"
	@echo "  make run ARGS=\"version\"        dev build and run (GOARCH=arm64 for ARM64 cross build)"
	@echo "  make release                  build portable zip + checksums.txt -> $(DIST)/"
	@echo "  make clean                    remove $(DIST)/"
	@echo "  make tidy                     go mod tidy"
	@echo "  make fmt                      format code"
	@echo "  make vet                      static check"
	@echo "  make test                     run unit tests"
	@echo "  make test-race                run unit tests with -race detector"

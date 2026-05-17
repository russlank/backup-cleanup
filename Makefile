# backup-cleanup build configuration
#
# This Makefile is intentionally small and conventional so the same commands can
# be used by a developer laptop, CI, or Ansible build step.

APP_NAME := backup-cleanup
CMD_PATH := ./cmd/backup-cleanup
DIST_DIR := dist

# These defaults can be overridden, for example:
#   make linux-amd64 VERSION=1.2.3
VERSION ?= dev
COMMIT  ?= unknown
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

# CGO_ENABLED=0 gives a self-contained Linux binary for this program.
# -trimpath removes local filesystem paths from the binary.
# -s -w strips symbol/debug tables to reduce binary size.
# -X flags inject version metadata that is printed by --version.
GOFLAGS := -trimpath
LDFLAGS := -s -w \
	-X main.version=$(VERSION) \
	-X main.commit=$(COMMIT) \
	-X main.buildDate=$(DATE)

.PHONY: all clean fmt test build linux-amd64 linux-arm64 windows-amd64 dist

all: fmt test build

fmt:
	go fmt ./...

test:
	go test ./...

build:
	mkdir -p $(DIST_DIR)
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(DIST_DIR)/$(APP_NAME) $(CMD_PATH)

linux-amd64:
	mkdir -p $(DIST_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(DIST_DIR)/$(APP_NAME)-linux-amd64 $(CMD_PATH)

linux-arm64:
	mkdir -p $(DIST_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(DIST_DIR)/$(APP_NAME)-linux-arm64 $(CMD_PATH)

windows-amd64:
	mkdir -p $(DIST_DIR)
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(DIST_DIR)/$(APP_NAME)-windows-amd64.exe $(CMD_PATH)

dist: linux-amd64 linux-arm64 windows-amd64
	cd $(DIST_DIR) && sha256sum $(APP_NAME)-linux-* $(APP_NAME)-windows-* > SHA256SUMS

clean:
	rm -rf $(DIST_DIR)

GO ?= go
VERSION ?= dev
.DEFAULT_GOAL := build

.PHONY: guest build test check release

guest:
	GO="$(GO)" bash scripts/build-guest.sh

build: guest
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "-X main.version=$(VERSION)" -o .build/agent-vm ./cmd/agent-vm

test: guest
	$(GO) test ./...

check: test build
	$(GO) vet ./...
	for script in lima/bootstrap.sh lima/provision.sh lima/dotfiles.sh incus/bootstrap.sh scripts/build-guest.sh scripts/release.sh; do bash -n "$$script" || exit; done

# Example: make release VERSION=v0.1.0 REPOSITORY=OWNER/REPO
release:
	bash scripts/release.sh "$(VERSION)" "$(REPOSITORY)"

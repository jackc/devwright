GO ?= go
VERSION ?= dev

.PHONY: build test check release

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "-X main.version=$(VERSION)" -o .build/agent-vm ./cmd/agent-vm

test:
	$(GO) test ./...

check: test build
	$(GO) vet ./...
	ruby tests/test_verification.rb
	ruby tests/test_terminal.rb
	for script in lima/bootstrap.sh lima/provision.sh lima/dotfiles.sh scripts/release.sh; do bash -n "$$script" || exit; done

# Example: make release VERSION=v0.1.0 REPOSITORY=OWNER/REPO
release:
	bash scripts/release.sh "$(VERSION)" "$(REPOSITORY)"

# axprobe — build, test, and install helpers.
BINARY  := axprobe
LOCALBIN := $(HOME)/.local/bin

.PHONY: build test vet check install dev clean

build: ## compile ./axprobe in this directory
	go build -o $(BINARY) .

test: ## run all tests (includes docker integration tests)
	go test ./...

vet:
	go vet ./...

check: vet ## vet + fast tests (skips docker integration)
	go test -short ./...

install: ## install a static binary to ~/.local/bin (snapshot; rerun after edits)
	go build -o $(LOCALBIN)/$(BINARY) .
	@echo "installed static binary → $(LOCALBIN)/$(BINARY)"

dev: ## install a dev wrapper to ~/.local/bin (rebuilds from source on every call)
	@printf '#!/bin/sh\nsrc=%s\nbin="$${TMPDIR:-/tmp}/axprobe-dev"\n( cd "$$src" && %s build -o "$$bin" . ) >&2 || exit 1\nexec "$$bin" "$$@"\n' "$(CURDIR)" "$(shell command -v go)" > $(LOCALBIN)/$(BINARY)
	@chmod +x $(LOCALBIN)/$(BINARY)
	@echo "installed dev wrapper → $(LOCALBIN)/$(BINARY) (edits are live)"

clean:
	rm -f $(BINARY)

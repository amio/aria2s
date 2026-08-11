VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -s -w -X github.com/amio/aria2s/cmd.Version=$(VERSION)
MACOS_CODESIGN_IDENTITY ?= $(shell security find-identity -v -p codesigning 2>/dev/null | awk '/"Apple Development:/ { print $$2; exit }')

.DEFAULT_GOAL := help

.PHONY: help build test release-patch release-minor release-major

help: ## Show available development commands
	@printf "Usage: make <target>\n\nTargets:\n"
	@awk 'BEGIN {FS = ":.*## "}; /^[a-zA-Z0-9_.-]+:.*## / {printf "  %-14s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build the aria2s binary
	go build -ldflags "$(LDFLAGS)" -o bin/aria2s .
	@if [ "$$(uname -s)" = "Darwin" ] && [ -n "$(MACOS_CODESIGN_IDENTITY)" ]; then \
		codesign --force --sign "$(MACOS_CODESIGN_IDENTITY)" \
			--identifier io.github.amio.aria2s bin/aria2s; \
	fi

test: ## Run the full Go test suite
	go test ./...

# ── version bump (like npm version) ──────────────────────────

LATEST_TAG := $(shell git describe --tags --abbrev=0 2>/dev/null | sed 's/^v//')
CUR_VER    := $(or $(LATEST_TAG),0.0.0)

release-patch: ## Bump patch version and push tag (0.0.x)
	@$(MAKE) _bump KIND=patch

release-minor: ## Bump minor version and push tag (0.x.0)
	@$(MAKE) _bump KIND=minor

release-major: ## Bump major version and push tag (x.0.0)
	@$(MAKE) _bump KIND=major

_bump:
	@case "$(CUR_VER)" in \
	  *.*.*) ;; \
	  *) echo "error: unexpected version format: $(CUR_VER)"; exit 1 ;; \
	esac
	@MAJOR=$$(echo $(CUR_VER) | cut -d. -f1); \
	MINOR=$$(echo $(CUR_VER) | cut -d. -f2); \
	PATCH=$$(echo $(CUR_VER) | cut -d. -f3); \
	case "$(KIND)" in \
	  major) MAJOR=$$((MAJOR + 1)); MINOR=0; PATCH=0 ;; \
	  minor) MINOR=$$((MINOR + 1)); PATCH=0 ;; \
	  patch) PATCH=$$((PATCH + 1)) ;; \
	  *) echo "error: unknown kind $(KIND)"; exit 1 ;; \
	esac; \
	NEW="$${MAJOR}.$${MINOR}.$${PATCH}"; \
	echo "  $(CUR_VER)  →  v$${NEW}"; \
	git tag -a "v$${NEW}" -m "v$${NEW}"; \
	BRANCH=$$(git rev-parse --abbrev-ref HEAD); \
	git push origin "$${BRANCH}" "v$${NEW}"

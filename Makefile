# Self-documented Makefile (https://marmelab.com/blog/2016/02/29/auto-documented-makefile.html)
# Run 'make' or 'make help' to list targets.
#
# Pre-push gate:  make ci
# Quick check:    make images smoke

.DEFAULT_GOAL := help

REGISTRY  ?= ghcr.io/otfabric
VERSION   ?= dev
PLATFORMS ?= linux/amd64,linux/arm64

# Adapters with a published image, in priority order. The self-tests run over
# all of them; override to work on one: make smoke ADAPTERS=lib60870
ADAPTERS ?= lib60870 openmuc

image = $(REGISTRY)/iec104-interop-$(1)

export REGISTRY VERSION

# image-% and smoke-% are pattern rules, which make ignores for .PHONY
# targets: they depend on FORCE instead.
.PHONY: help all ci validate documents lint images smoke interop buildx publish candidate-openmuc candidate-lib60870 \
        run-lib60870 run-openmuc capabilities clean FORCE
FORCE:

help: ## Show this help
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z0-9_%-]+:.*?## / {printf "\033[36m%-22s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

all: validate images ## Validate fixtures and build the local images

ci: validate lint images documents ## Full local CI: validate, lint, build, smoke and cross-stack self-test, document schemas
	@echo ""
	@echo "All local CI checks passed."

# ── Validation ───────────────────────────────────────────────────────────────

validate: ## Validate every fixture against the schema and the cross-field rules; check versions.yaml
	@scripts/check-versions.sh
	@echo "Validating fixtures..."
	@if python3 -c 'import jsonschema' 2>/dev/null; then \
		python3 scripts/validate-fixtures.py --require-schema; \
	else \
		echo "(jsonschema not installed locally: running the validator in a container)"; \
		docker run --rm -v "$(CURDIR):/work:ro" -w /work python:3.12-slim \
			sh -c 'pip install -q --root-user-action=ignore jsonschema && python scripts/validate-fixtures.py --require-schema'; \
	fi

documents: ## Run the self-tests keeping every document, and validate them against schemas/
	@rm -rf reports && mkdir -p reports
	@REPORT_DIR=reports $(MAKE) --no-print-directory smoke interop
	@echo "Validating documents..."
	@if python3 -c 'import jsonschema' 2>/dev/null; then \
		python3 scripts/validate-documents.py reports; \
	else \
		docker run --rm -v "$(CURDIR):/work:ro" -w /work python:3.12-slim \
			sh -c 'pip install -q --root-user-action=ignore jsonschema && python scripts/validate-documents.py reports'; \
	fi

lint: ## Shellcheck the scripts (in a container)
	@echo "Linting scripts..."
	@docker run --rm -v "$(CURDIR):/mnt:ro" koalaman/shellcheck:stable --severity=warning -x -P scripts scripts/*.sh

# ── Local single-arch images (loaded into Docker; used by smoke and interop) ──

images: $(addprefix image-,$(ADAPTERS)) ## Build every adapter image for the host architecture

image-%: FORCE ## Build one adapter image, e.g. image-lib60870
	@echo "Building $* image ($(VERSION))..."
	@docker build \
		--build-arg ADAPTER_VERSION=$(VERSION) \
		--tag $(call image,$*):$(VERSION) \
		--file adapters/$*/Dockerfile \
		.

# ── Self-tests (images must be built) ─────────────────────────────────────────

smoke: $(addprefix smoke-,$(ADAPTERS)) ## Container-contract smoke test of every adapter

smoke-%: FORCE ## Smoke test one adapter, e.g. smoke-openmuc
	@scripts/smoke.sh $*

interop: ## Cross-stack self-test: every client against every server
	@scripts/interop.sh $(ADAPTERS)

# ── Upstream candidates ───────────────────────────────────────────────────────
# Builds an adapter on another upstream version than the release pin and runs
# the self-tests on it. Bugs of that version listed in tests/known-bugs.tsv
# are reported as known, so the run shows whether everything else still works.

J60870_CANDIDATE ?= 1.8.0

candidate-openmuc: image-lib60870 ## Build openmuc on j60870 $(J60870_CANDIDATE) and self-test it (release pin untouched)
	@echo "Building openmuc candidate on j60870 $(J60870_CANDIDATE)..."
	@docker build \
		--build-arg ADAPTER_VERSION=candidate \
		--build-arg J60870_VERSION=$(J60870_CANDIDATE) \
		--tag $(call image,openmuc):candidate \
		--file adapters/openmuc/Dockerfile .
	@docker tag $(call image,lib60870):$(VERSION) $(call image,lib60870):candidate
	@VERSION=candidate scripts/smoke.sh openmuc
	@VERSION=candidate scripts/interop.sh lib60870 openmuc

# A lib60870 candidate is a commit: a newer tag or the tip of the default branch.
LIB60870_CANDIDATE_SHA  ?=
LIB60870_CANDIDATE_NAME ?= candidate

candidate-lib60870: image-openmuc ## Build lib60870 at LIB60870_CANDIDATE_SHA and self-test it (release pin untouched)
	@test -n "$(LIB60870_CANDIDATE_SHA)" || { echo "Error: LIB60870_CANDIDATE_SHA=<commit> is required"; exit 1; }
	@echo "Building lib60870 candidate at $(LIB60870_CANDIDATE_SHA)..."
	@docker build \
		--build-arg ADAPTER_VERSION=candidate \
		--build-arg LIB60870_SHA=$(LIB60870_CANDIDATE_SHA) \
		--build-arg LIB60870_VERSION=$(LIB60870_CANDIDATE_NAME) \
		--tag $(call image,lib60870):candidate \
		--file adapters/lib60870/Dockerfile .
	@docker tag $(call image,openmuc):$(VERSION) $(call image,openmuc):candidate
	@VERSION=candidate scripts/smoke.sh lib60870
	@VERSION=candidate scripts/interop.sh lib60870 openmuc

capabilities: ## Print the capability document of every adapter
	@for a in $(ADAPTERS); do docker run --rm $(call image,$$a):$(VERSION) print-capabilities; done

# ── Run a reference server on the host ────────────────────────────────────────

LIB60870_PORT ?= 2404
OPENMUC_PORT  ?= 2405

run-lib60870: ## Run the lib60870 server on 127.0.0.1:$(LIB60870_PORT)
	docker run --rm -p 127.0.0.1:$(LIB60870_PORT):2404 $(call image,lib60870):$(VERSION) server

run-openmuc: ## Run the OpenMUC server on 127.0.0.1:$(OPENMUC_PORT)
	docker run --rm -p 127.0.0.1:$(OPENMUC_PORT):2404 $(call image,openmuc):$(VERSION) server

# ── Multi-arch build and publish ──────────────────────────────────────────────

buildx: ## Build every image for $(PLATFORMS) without pushing (checks that both architectures build)
	@for a in $(ADAPTERS); do \
		echo "Building $$a for $(PLATFORMS)..."; \
		docker buildx build --platform $(PLATFORMS) \
			--build-arg ADAPTER_VERSION=$(VERSION) \
			--file adapters/$$a/Dockerfile . || exit 1; \
	done

publish: ## Build and push multi-arch images (requires VERSION=vX.Y.Z; normally done by the release workflow)
	@if ! echo "$(VERSION)" | grep -qE '^v[0-9]+\.[0-9]+\.[0-9]+$$'; then \
		echo "Error: VERSION must be in the form v<major>.<minor>.<patch>"; exit 1; fi
	@for a in $(ADAPTERS); do \
		echo "Publishing $$a $(VERSION) for $(PLATFORMS)..."; \
		docker buildx build --platform $(PLATFORMS) --push \
			--build-arg ADAPTER_VERSION=$(VERSION) \
			--tag $(call image,$$a):$(VERSION) \
			--file adapters/$$a/Dockerfile . || exit 1; \
	done

# ── Clean ─────────────────────────────────────────────────────────────────────

clean: ## Remove test containers, networks, local images and reports
	@docker ps -aq --filter "label=iec104-interop.run" | xargs -r docker rm -f >/dev/null 2>&1 || true
	@docker network ls --format '{{.Name}}' | grep '^iec104-interop-' | xargs -r docker network rm >/dev/null 2>&1 || true
	@docker compose down --remove-orphans 2>/dev/null || true
	@for a in $(ADAPTERS); do docker rmi $(call image,$$a):$(VERSION) 2>/dev/null || true; done
	@rm -rf reports

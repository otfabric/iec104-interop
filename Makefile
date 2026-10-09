# Self-documented Makefile (https://marmelab.com/blog/2016/02/29/auto-documented-makefile.html)
# Run 'make' or 'make help' to list targets.
#
# Pre-push gate:  make ci
# Quick check:    make images smoke

.DEFAULT_GOAL := help

REGISTRY  ?= ghcr.io/otfabric
VERSION   ?= dev
PLATFORMS ?= linux/amd64,linux/arm64

# Registry of the base images (debian, maven, eclipse-temurin, golang). They
# are pinned by digest in the Dockerfiles, so any mirror of Docker Hub's
# official images gives the same build. The workflows set
#   BASE_REGISTRY=mirror.gcr.io/library
# because anonymous pulls from Docker Hub are rate limited per runner address.
BASE_REGISTRY ?= docker.io/library

# Registry of the images the build tooling itself runs: BuildKit, the QEMU
# handlers, shellcheck, Python. The workflows set TOOLS_REGISTRY=mirror.gcr.io.
TOOLS_REGISTRY ?= docker.io

# Everything that pulls from or pushes to a registry goes through this: it
# repeats a command that failed on a rate limit, a 5xx or a timeout, and only
# then. See scripts/retry.sh.
RETRY := scripts/retry.sh

# Name of the docker-container builder "make buildx-setup" creates.
BUILDER ?= iec104-interop

# Adapters with a published image, in priority order. The self-tests run over
# all of them; override to work on one: make smoke ADAPTERS=lib60870
ADAPTERS ?= lib60870 openmuc wendy512

image = $(REGISTRY)/iec104-interop-$(1)

export REGISTRY VERSION

# image-% and smoke-% are pattern rules, which make ignores for .PHONY
# targets: they depend on FORCE instead.
.PHONY: help all ci validate documents lint images smoke interop buildx publish candidate-openmuc candidate-lib60870 \
        candidate-wendy512 buildx-setup publish-arch publish-manifest digests run-lib60870 run-openmuc run-wendy512 capabilities clean FORCE
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
		$(RETRY) docker run --rm -v "$(CURDIR):/work:ro" -w /work $(TOOLS_REGISTRY)/library/python:3.12-slim \
			sh -c 'pip install -q --root-user-action=ignore jsonschema && python scripts/validate-fixtures.py --require-schema'; \
	fi

documents: ## Run the self-tests keeping every document, and validate them against schemas/
	@rm -rf reports && mkdir -p reports
	@REPORT_DIR=reports $(MAKE) --no-print-directory smoke interop
	@echo "Validating documents..."
	@if python3 -c 'import jsonschema' 2>/dev/null; then \
		python3 scripts/validate-documents.py reports; \
	else \
		$(RETRY) docker run --rm -v "$(CURDIR):/work:ro" -w /work $(TOOLS_REGISTRY)/library/python:3.12-slim \
			sh -c 'pip install -q --root-user-action=ignore jsonschema && python scripts/validate-documents.py reports'; \
	fi

lint: ## Shellcheck the scripts (the installed shellcheck, or a container when there is none)
	@echo "Linting scripts..."
	@if command -v shellcheck >/dev/null 2>&1; then \
		shellcheck --severity=warning -x -P scripts scripts/*.sh; \
	else \
		$(RETRY) docker run --rm -v "$(CURDIR):/mnt:ro" $(TOOLS_REGISTRY)/koalaman/shellcheck:stable --severity=warning -x -P scripts scripts/*.sh; \
	fi

# ── Local single-arch images (loaded into Docker; used by smoke and interop) ──

images: $(addprefix image-,$(ADAPTERS)) ## Build every adapter image for the host architecture

image-%: FORCE ## Build one adapter image, e.g. image-lib60870
	@echo "Building $* image ($(VERSION))..."
	@$(RETRY) docker build \
		--build-arg BASE_REGISTRY=$(BASE_REGISTRY) --build-arg ADAPTER_VERSION=$(VERSION) \
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
	@$(RETRY) docker build \
		--build-arg BASE_REGISTRY=$(BASE_REGISTRY) --build-arg ADAPTER_VERSION=candidate \
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
	@$(RETRY) docker build \
		--build-arg BASE_REGISTRY=$(BASE_REGISTRY) --build-arg ADAPTER_VERSION=candidate \
		--build-arg LIB60870_SHA=$(LIB60870_CANDIDATE_SHA) \
		--build-arg LIB60870_VERSION=$(LIB60870_CANDIDATE_NAME) \
		--tag $(call image,lib60870):candidate \
		--file adapters/lib60870/Dockerfile .
	@docker tag $(call image,openmuc):$(VERSION) $(call image,openmuc):candidate
	@VERSION=candidate scripts/smoke.sh lib60870
	@VERSION=candidate scripts/interop.sh lib60870 openmuc

# A wendy512 candidate is a version of each of its two modules: a tag, a
# branch or a commit, as "go get" takes them.
IEC104_CANDIDATE   ?= latest
GOIECP5_CANDIDATE  ?= latest

candidate-wendy512: image-lib60870 image-openmuc ## Build wendy512 on IEC104_CANDIDATE and GOIECP5_CANDIDATE and self-test it (release pin untouched)
	@echo "Building wendy512 candidate on iec104 $(IEC104_CANDIDATE), go-iecp5 $(GOIECP5_CANDIDATE)..."
	@$(RETRY) docker build \
		--build-arg BASE_REGISTRY=$(BASE_REGISTRY) --build-arg ADAPTER_VERSION=candidate \
		--build-arg IEC104_VERSION=$(IEC104_CANDIDATE) \
		--build-arg GOIECP5_VERSION=$(GOIECP5_CANDIDATE) \
		--tag $(call image,wendy512):candidate \
		--file adapters/wendy512/Dockerfile .
	@docker tag $(call image,lib60870):$(VERSION) $(call image,lib60870):candidate
	@docker tag $(call image,openmuc):$(VERSION) $(call image,openmuc):candidate
	@VERSION=candidate scripts/smoke.sh wendy512
	@VERSION=candidate scripts/interop.sh lib60870 openmuc wendy512

capabilities: ## Print the capability document of every adapter
	@for a in $(ADAPTERS); do docker run --rm $(call image,$$a):$(VERSION) print-capabilities; done

# ── Run a reference server on the host ────────────────────────────────────────

LIB60870_PORT ?= 2404
OPENMUC_PORT  ?= 2405
WENDY512_PORT ?= 2406

run-lib60870: ## Run the lib60870 server on 127.0.0.1:$(LIB60870_PORT)
	docker run --rm -p 127.0.0.1:$(LIB60870_PORT):2404 $(call image,lib60870):$(VERSION) server

run-openmuc: ## Run the OpenMUC server on 127.0.0.1:$(OPENMUC_PORT)
	docker run --rm -p 127.0.0.1:$(OPENMUC_PORT):2404 $(call image,openmuc):$(VERSION) server

run-wendy512: ## Run the wendy512 server on 127.0.0.1:$(WENDY512_PORT)
	docker run --rm -p 127.0.0.1:$(WENDY512_PORT):2404 $(call image,wendy512):$(VERSION) server

# ── Multi-arch build and publish ──────────────────────────────────────────────

buildx-setup: ## Prepare multi-arch builds: QEMU handlers and a docker-container builder (pulled through TOOLS_REGISTRY)
	@echo "Installing QEMU handlers and creating builder $(BUILDER)..."
	@$(RETRY) docker run --privileged --rm $(TOOLS_REGISTRY)/tonistiigi/binfmt:latest --install arm64,amd64 >/dev/null
	@docker buildx rm $(BUILDER) >/dev/null 2>&1 || true
	@$(RETRY) docker buildx create --name $(BUILDER) --driver docker-container \
		--driver-opt image=$(TOOLS_REGISTRY)/moby/buildkit:buildx-stable-1 --bootstrap >/dev/null
	@docker buildx inspect $(BUILDER) | grep -E '^(Name|Driver|Platforms)' || true

# Uses the builder of buildx-setup when there is one, the current builder otherwise.
builder_flag = $(shell docker buildx inspect $(BUILDER) >/dev/null 2>&1 && echo --builder $(BUILDER))

buildx: ## Build every image for $(PLATFORMS) under emulation, without pushing (local check; CI builds natively on both)
	@for a in $(ADAPTERS); do \
		echo "Building $$a for $(PLATFORMS)..."; \
		$(RETRY) docker buildx build $(builder_flag) --platform $(PLATFORMS) \
			--build-arg BASE_REGISTRY=$(BASE_REGISTRY) --build-arg ADAPTER_VERSION=$(VERSION) \
			--file adapters/$$a/Dockerfile . || exit 1; \
	done

publish: ## Build and push multi-arch images under emulation, from one machine (requires VERSION=vX.Y.Z; the release workflow uses publish-arch and publish-manifest instead)
	@if ! echo "$(VERSION)" | grep -qE '^v[0-9]+\.[0-9]+\.[0-9]+$$'; then \
		echo "Error: VERSION must be in the form v<major>.<minor>.<patch>"; exit 1; fi
	@for a in $(ADAPTERS); do \
		echo "Publishing $$a $(VERSION) for $(PLATFORMS)..."; \
		$(RETRY) docker buildx build $(builder_flag) --platform $(PLATFORMS) --push \
			--build-arg BASE_REGISTRY=$(BASE_REGISTRY) --build-arg ADAPTER_VERSION=$(VERSION) \
			--tag $(call image,$$a):$(VERSION) \
			--file adapters/$$a/Dockerfile . || exit 1; \
	done

# ── Multi-arch publish from native runners ────────────────────────────────────
# The release workflow builds on an amd64 and on an arm64 runner (no
# emulation), self-tests there and pushes what it tested under
# <image>:<version>-<arch>. A last job joins the two into <image>:<version>.

ARCH ?= $(shell uname -m | sed -e 's/^x86_64$$/amd64/' -e 's/^aarch64$$/arm64/')
ARCHS ?= amd64 arm64

publish-arch: ## Push the local images of this machine as <image>:$(VERSION)-$(ARCH) (build and self-test them first)
	@if ! echo "$(VERSION)" | grep -qE '^v[0-9]+\.[0-9]+\.[0-9]+$$'; then \
		echo "Error: VERSION must be in the form v<major>.<minor>.<patch>"; exit 1; fi
	@for a in $(ADAPTERS); do \
		got=$$(docker image inspect --format '{{.Architecture}}' $(call image,$$a):$(VERSION)) || exit 1; \
		if [ "$$got" != "$(ARCH)" ]; then echo "Error: $$a:$(VERSION) is $$got, not $(ARCH)"; exit 1; fi; \
		echo "Pushing $$a $(VERSION) for linux/$(ARCH)..."; \
		docker tag $(call image,$$a):$(VERSION) $(call image,$$a):$(VERSION)-$(ARCH) || exit 1; \
		$(RETRY) docker push --quiet $(call image,$$a):$(VERSION)-$(ARCH) || exit 1; \
	done

publish-manifest: ## Join the per-architecture images of VERSION into the multi-arch <image>:$(VERSION)
	@for a in $(ADAPTERS); do \
		echo "Creating $$a $(VERSION) from: $(ARCHS)"; \
		$(RETRY) docker buildx imagetools create --tag $(call image,$$a):$(VERSION) \
			$(foreach arch,$(ARCHS),$(call image,$$a):$(VERSION)-$(arch)) || exit 1; \
		for arch in $(ARCHS); do \
			$(RETRY) docker buildx imagetools inspect $(call image,$$a):$(VERSION) --format '{{json .Manifest}}' \
				| tr -d ' \n' | grep -q "\"architecture\":\"$$arch\"" || { echo "Error: $$a:$(VERSION) has no linux/$$arch image"; exit 1; }; \
		done; \
	done

digests: ## Print "<adapter> <digest>" of the published multi-arch images of VERSION
	@for a in $(ADAPTERS); do \
		d=$$($(RETRY) docker buildx imagetools inspect $(call image,$$a):$(VERSION) --format '{{.Manifest.Digest}}') || exit 1; \
		echo "$$a $$d"; \
	done

# ── Clean ─────────────────────────────────────────────────────────────────────

clean: ## Remove test containers, networks, local images and reports
	@docker ps -aq --filter "label=iec104-interop.run" | xargs -r docker rm -f >/dev/null 2>&1 || true
	@docker network ls --format '{{.Name}}' | grep '^iec104-interop-' | xargs -r docker network rm >/dev/null 2>&1 || true
	@docker compose down --remove-orphans 2>/dev/null || true
	@for a in $(ADAPTERS); do docker rmi $(call image,$$a):$(VERSION) 2>/dev/null || true; done
	@rm -rf reports

# iec104-interop adapter for pascaldekloe/part5.
#
# Build from the repository root:
#   docker build -f adapters/part5/Dockerfile -t iec104-interop-part5:dev .
#
# pascaldekloe/part5 is dedicated to the public domain (CC0-1.0) and has no
# dependencies. The adapter links it statically and is distributed under
# GPL-3.0-or-later like the other adapters; the CC0 text is copied to
# /licenses. See THIRD_PARTY_NOTICES.md. The upstream pin is below, in go.mod
# (with its checksum in go.sum) and in versions.yaml.

# Where the base images come from. They are pinned by digest, so a mirror of
# Docker Hub serves exactly the same images; CI uses one to stay clear of
# Docker Hub's pull rate limit (see the Makefile).
ARG BASE_REGISTRY=docker.io/library
ARG GOLANG_DIGEST=sha256:e58d6f83b3416618d8bcac2b3dde1b7f7e3c4a77d25e88637f8bbae81536c48d
ARG DEBIAN_DIGEST=sha256:a29215f6a35e51e22adffa17f89e9d2ef06214e64a2bad10d765c46aea49f11f

# ---------------------------------------------------------------------------
# Stage 1: build the adapter (the upstream module linked in)
# ---------------------------------------------------------------------------
FROM ${BASE_REGISTRY}/golang:1.27.2-trixie@${GOLANG_DIGEST} AS build

# The release pin. "make candidate-part5" overrides it to try another
# upstream version (a tag or a commit) without touching the pin.
ARG PART5_VERSION=v0.3.0
ARG ADAPTER_VERSION=dev

ENV CGO_ENABLED=0 GOFLAGS=-mod=mod
WORKDIR /src
COPY adapters/part5/go.mod adapters/part5/go.sum ./

# At the pin the modules must match go.sum; anything else is a candidate.
RUN if [ "$(go list -m -f '{{.Version}}' github.com/pascaldekloe/part5)" = "${PART5_VERSION}" ]; then \
         GOFLAGS=-mod=readonly go mod download && go mod verify; \
       else \
         go get "github.com/pascaldekloe/part5@${PART5_VERSION}"; \
       fi

COPY adapters/part5/*.go ./
RUN test -z "$(gofmt -l .)" \
    && go vet ./... \
    && go build -trimpath -ldflags "-s -w \
         -X main.adapterVersion=${ADAPTER_VERSION} \
         -X main.upstreamVersion=$(go list -m -f '{{.Version}}' github.com/pascaldekloe/part5) \
         -X main.upstreamCommit=github.com/pascaldekloe/part5@$(go list -m -f '{{.Version}}' github.com/pascaldekloe/part5)" \
         -o /out/iec104-interop . \
    && for m in $(go list -deps -f '{{with .Module}}{{if not .Main}}{{.Path}}{{end}}{{end}}' . | sort -u); do \
         dir="$(go list -m -f '{{.Dir}}' "$m")" && mkdir -p "/out/licenses/$m" \
         && cp "$dir"/LICENSE* "$dir"/NOTICE* "/out/licenses/$m/" 2>/dev/null; \
         ls "/out/licenses/$m" | grep -q . || { echo "no licence in $m"; exit 1; }; \
       done

# ---------------------------------------------------------------------------
# Stage 2: runtime
# ---------------------------------------------------------------------------
FROM ${BASE_REGISTRY}/debian:trixie-slim@${DEBIAN_DIGEST} AS runtime

ARG ADAPTER_VERSION=dev

RUN useradd --uid 10001 --no-create-home --shell /usr/sbin/nologin interop \
    && mkdir -p /run/iec104-interop \
    && chown interop /run/iec104-interop

COPY --from=build /out/iec104-interop /usr/local/bin/iec104-interop
COPY --from=build /out/licenses /licenses
COPY licenses/GPL-3.0.txt /licenses/GPL-3.0.txt
COPY THIRD_PARTY_NOTICES.md /licenses/THIRD_PARTY_NOTICES.md
COPY fixtures /fixtures

LABEL org.opencontainers.image.title="iec104-interop-part5" \
      org.opencontainers.image.description="IEC 60870-5-104 reference client and server built on pascaldekloe/part5" \
      org.opencontainers.image.source="https://github.com/otfabric/iec104-interop" \
      org.opencontainers.image.licenses="GPL-3.0-or-later" \
      org.opencontainers.image.version="${ADAPTER_VERSION}"

USER interop
EXPOSE 2404

HEALTHCHECK --interval=2s --timeout=2s --start-period=10s --retries=15 \
  CMD test -f /run/iec104-interop/ready

ENTRYPOINT ["/usr/local/bin/iec104-interop"]
CMD ["server"]

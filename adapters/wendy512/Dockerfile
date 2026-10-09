# iec104-interop adapter for wendy512/iec104 and its engine wendy512/go-iecp5.
#
# Build from the repository root:
#   docker build -f adapters/wendy512/Dockerfile -t iec104-interop-wendy512:dev .
#
# wendy512/iec104 is Apache-2.0 and go-iecp5 is LGPL-3.0. The adapter links
# both statically and is distributed under GPL-3.0-or-later like the other
# adapters; its source is in this repository, so it can be relinked against
# another go-iecp5. See THIRD_PARTY_NOTICES.md. The upstream pins are below,
# in go.mod (with their checksums in go.sum) and in versions.yaml.

# Where the base images come from. They are pinned by digest, so a mirror of
# Docker Hub serves exactly the same images; CI uses one to stay clear of
# Docker Hub's pull rate limit (see the Makefile).
ARG BASE_REGISTRY=docker.io/library
ARG GOLANG_DIGEST=sha256:e58d6f83b3416618d8bcac2b3dde1b7f7e3c4a77d25e88637f8bbae81536c48d
ARG DEBIAN_DIGEST=sha256:a29215f6a35e51e22adffa17f89e9d2ef06214e64a2bad10d765c46aea49f11f

# ---------------------------------------------------------------------------
# Stage 1: build the adapter (the upstream modules linked in)
# ---------------------------------------------------------------------------
FROM ${BASE_REGISTRY}/golang:1.27.2-trixie@${GOLANG_DIGEST} AS build

# The release pins. "make candidate-wendy512" overrides them to try another
# upstream version (a tag or a commit) without touching the pins.
ARG IEC104_VERSION=v1.0.4
ARG GOIECP5_VERSION=v1.2.6
ARG ADAPTER_VERSION=dev

ENV CGO_ENABLED=0 GOFLAGS=-mod=mod
WORKDIR /src
COPY adapters/wendy512/go.mod adapters/wendy512/go.sum ./

# At the pins the modules must match go.sum; anything else is a candidate.
RUN want="github.com/wendy512/iec104 ${IEC104_VERSION} github.com/wendy512/go-iecp5 ${GOIECP5_VERSION} " \
    && have="$(go list -m -f '{{.Path}} {{.Version}}' github.com/wendy512/iec104 github.com/wendy512/go-iecp5 | tr '\n' ' ')" \
    && if [ "${want}" = "${have}" ]; then \
         GOFLAGS=-mod=readonly go mod download && go mod verify; \
       else \
         go get "github.com/wendy512/iec104@${IEC104_VERSION}" "github.com/wendy512/go-iecp5@${GOIECP5_VERSION}"; \
       fi

COPY adapters/wendy512/*.go ./
RUN test -z "$(gofmt -l .)" \
    && go vet ./... \
    && go build -trimpath -ldflags "-s -w \
         -X main.adapterVersion=${ADAPTER_VERSION} \
         -X main.upstreamVersion=$(go list -m -f '{{.Version}}' github.com/wendy512/iec104) \
         -X main.engineVersion=$(go list -m -f '{{.Version}}' github.com/wendy512/go-iecp5)" \
         -o /out/iec104-interop . \
    && mkdir -p /out/licenses/iec104 /out/licenses/go-iecp5 \
    && cp "$(go list -m -f '{{.Dir}}' github.com/wendy512/iec104)/LICENSE" /out/licenses/iec104/LICENSE \
    && cp "$(go list -m -f '{{.Dir}}' github.com/wendy512/go-iecp5)/LICENSE" /out/licenses/go-iecp5/LICENSE

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

LABEL org.opencontainers.image.title="iec104-interop-wendy512" \
      org.opencontainers.image.description="IEC 60870-5-104 reference client and server built on wendy512/iec104 and go-iecp5" \
      org.opencontainers.image.source="https://github.com/otfabric/iec104-interop" \
      org.opencontainers.image.licenses="GPL-3.0-or-later" \
      org.opencontainers.image.version="${ADAPTER_VERSION}"

USER interop
EXPOSE 2404

HEALTHCHECK --interval=2s --timeout=2s --start-period=10s --retries=15 \
  CMD test -f /run/iec104-interop/ready

ENTRYPOINT ["/usr/local/bin/iec104-interop"]
CMD ["server"]

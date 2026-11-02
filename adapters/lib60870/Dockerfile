# iec104-interop adapter for MZ Automation lib60870-C.
#
# Build from the repository root:
#   docker build -f adapters/lib60870/Dockerfile -t iec104-interop-lib60870:dev .
#
# lib60870-C is GPL-3.0. The adapter links it statically, so the adapter binary
# and this image are distributed under GPL-3.0 as well; see
# THIRD_PARTY_NOTICES.md. Upstream pins are mirrored in versions.yaml.
# Mbed TLS is compiled into the library: it is what lib60870-C has for TLS.

# Where the base images come from. They are pinned by digest, so a mirror of
# Docker Hub serves exactly the same images; CI uses one to stay clear of
# Docker Hub's pull rate limit (see the Makefile).
ARG BASE_REGISTRY=docker.io/library
ARG DEBIAN_DIGEST=sha256:a29215f6a35e51e22adffa17f89e9d2ef06214e64a2bad10d765c46aea49f11f

# ---------------------------------------------------------------------------
# Stage 1: lib60870-C from a pinned commit
# ---------------------------------------------------------------------------
FROM ${BASE_REGISTRY}/debian:trixie-slim@${DEBIAN_DIGEST} AS lib60870-build

ARG LIB60870_REPO=https://github.com/mz-automation/lib60870.git
ARG LIB60870_VERSION=v2.4.1
ARG LIB60870_SHA=7a388e3e133999e1ca77ba7521d55d074b7cd2bc
ARG MBEDTLS_VERSION=3.6.7
ARG MBEDTLS_SHA256=a7e8bcbec0e6f761b4af24f25677626b35f762f68eef79c08677a363212d11f6

RUN apt-get update && apt-get install -y --no-install-recommends \
      bzip2 \
      ca-certificates \
      cmake \
      curl \
      g++ \
      gcc \
      git \
      libc6-dev \
      make \
    && rm -rf /var/lib/apt/lists/*

# Fetched by commit, so the pin is the hash and not a tag that could move.
# LIB60870_VERSION is the name of that commit, for print-capabilities.
WORKDIR /src/lib60870
RUN git init -q . \
    && git remote add origin "${LIB60870_REPO}" \
    && git fetch -q --depth 1 origin "${LIB60870_SHA}" \
    && git checkout -q --detach FETCH_HEAD \
    && test "$(git rev-parse HEAD)" = "${LIB60870_SHA}"

# The release tarball, not the tag: it has the generated sources. lib60870-C's
# cmake finds it in dependencies/ by its name and builds it into the library.
# Upstream's src/hal/tls/mbedtls3/mbedtls_config.h is never read, since a quoted
# include finds the one next to Mbed TLS's build_info.h first, so Mbed TLS's own
# default configuration applies. That one has no threading, and a station runs
# one thread per connection over the global state of PSA: switch it on.
WORKDIR /src/lib60870/lib60870-C/dependencies
RUN curl -fsSL --retry 5 --retry-all-errors -o mbedtls.tar.bz2 \
      "https://github.com/Mbed-TLS/mbedtls/releases/download/mbedtls-${MBEDTLS_VERSION}/mbedtls-${MBEDTLS_VERSION}.tar.bz2" \
    && echo "${MBEDTLS_SHA256}  mbedtls.tar.bz2" | sha256sum -c - \
    && tar xjf mbedtls.tar.bz2 \
    && rm mbedtls.tar.bz2 \
    && cp "mbedtls-${MBEDTLS_VERSION}/LICENSE" /src/mbedtls-LICENSE \
    && sed -i -E 's|^//(#define MBEDTLS_THREADING_(C\|PTHREAD))$|\1|' \
      "mbedtls-${MBEDTLS_VERSION}/include/mbedtls/mbedtls_config.h" \
    && test "$(grep -cE '^#define MBEDTLS_THREADING_(C|PTHREAD)$' "mbedtls-${MBEDTLS_VERSION}/include/mbedtls/mbedtls_config.h")" = 2

WORKDIR /src/lib60870/lib60870-C/build
RUN cmake .. \
      -DCMAKE_BUILD_TYPE=Release \
      -DCMAKE_INSTALL_PREFIX=/opt/lib60870 \
      -DBUILD_EXAMPLES=OFF \
      -DBUILD_TESTS=OFF \
    && make -j"$(nproc)" \
    && make install

# ---------------------------------------------------------------------------
# Stage 2: the adapter
# ---------------------------------------------------------------------------
FROM ${BASE_REGISTRY}/debian:trixie-slim@${DEBIAN_DIGEST} AS adapter-build

ARG LIB60870_VERSION=v2.4.1
ARG LIB60870_SHA=7a388e3e133999e1ca77ba7521d55d074b7cd2bc
ARG ADAPTER_VERSION=dev

RUN apt-get update && apt-get install -y --no-install-recommends \
      cmake \
      gcc \
      libc6-dev \
      libcjson-dev \
      make \
      pkg-config \
    && rm -rf /var/lib/apt/lists/*

COPY --from=lib60870-build /opt/lib60870 /opt/lib60870
COPY adapters/lib60870/CMakeLists.txt /src/adapter/CMakeLists.txt
COPY adapters/lib60870/src /src/adapter/src

WORKDIR /src/adapter/build
RUN cmake .. \
      -DCMAKE_BUILD_TYPE=Release \
      -DADAPTER_VERSION="${ADAPTER_VERSION}" \
      -DUPSTREAM_VERSION="${LIB60870_VERSION}" \
      -DUPSTREAM_REVISION="${LIB60870_SHA}" \
    && make -j"$(nproc)"

# ---------------------------------------------------------------------------
# Stage 3: runtime
# ---------------------------------------------------------------------------
FROM ${BASE_REGISTRY}/debian:trixie-slim@${DEBIAN_DIGEST} AS runtime

ARG ADAPTER_VERSION=dev

RUN apt-get update && apt-get install -y --no-install-recommends \
      libcjson1 \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --uid 10001 --no-create-home --shell /usr/sbin/nologin interop \
    && mkdir -p /run/iec104-interop /licenses/lib60870 \
    && chown interop /run/iec104-interop

COPY --from=adapter-build /src/adapter/build/iec104-interop /usr/local/bin/iec104-interop
COPY --from=lib60870-build /src/lib60870/COPYING /licenses/lib60870/COPYING
COPY --from=lib60870-build /src/mbedtls-LICENSE /licenses/mbedtls/LICENSE
COPY THIRD_PARTY_NOTICES.md /licenses/THIRD_PARTY_NOTICES.md
COPY fixtures /fixtures

LABEL org.opencontainers.image.title="iec104-interop-lib60870" \
      org.opencontainers.image.description="IEC 60870-5-104 reference client and server built on MZ Automation lib60870-C" \
      org.opencontainers.image.source="https://github.com/otfabric/iec104-interop" \
      org.opencontainers.image.licenses="GPL-3.0-or-later" \
      org.opencontainers.image.version="${ADAPTER_VERSION}"

USER interop
EXPOSE 2404

HEALTHCHECK --interval=2s --timeout=2s --start-period=10s --retries=15 \
  CMD test -f /run/iec104-interop/ready

ENTRYPOINT ["/usr/local/bin/iec104-interop"]
CMD ["server"]

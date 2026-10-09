# iec104-interop adapter for OpenMUC j60870.
#
# Build from the repository root:
#   docker build -f adapters/openmuc/Dockerfile -t iec104-interop-openmuc:dev .
#
# j60870 is GPL-3.0. The adapter links it, so the adapter jar and this image
# are distributed under GPL-3.0 as well; see THIRD_PARTY_NOTICES.md. The j60870
# version is pinned below (J60870_VERSION) and mirrored in pom.xml and
# versions.yaml.

# Where the base images come from. They are pinned by digest, so a mirror of
# Docker Hub serves exactly the same images; CI uses one to stay clear of
# Docker Hub's pull rate limit (see the Makefile).
ARG BASE_REGISTRY=docker.io/library
ARG MAVEN_DIGEST=sha256:93b8a14ea2f412782e4e842651273b4d903e35cc496284f178fbbe2d67d00976
ARG JRE_DIGEST=sha256:d9a39a23634650173f1e2bbc176227af9728587ecf0f4b62d53e9355cd7a19ab

# ---------------------------------------------------------------------------
# Stage 1: build the adapter jar (j60870 and Gson shaded in)
# ---------------------------------------------------------------------------
FROM ${BASE_REGISTRY}/maven:3.9-eclipse-temurin-25@${MAVEN_DIGEST} AS build

# The release pin. "make candidate-openmuc" overrides it to try another
# upstream version without touching the pin.
ARG J60870_VERSION=1.7.2

WORKDIR /build
COPY adapters/openmuc/pom.xml .
RUN mvn -B -q -Dj60870.version="${J60870_VERSION}" dependency:go-offline

COPY adapters/openmuc/src ./src
RUN mvn -B -q -o -Dj60870.version="${J60870_VERSION}" package -DskipTests

# ---------------------------------------------------------------------------
# Stage 2: runtime
# ---------------------------------------------------------------------------
FROM ${BASE_REGISTRY}/eclipse-temurin:25-jre-noble@${JRE_DIGEST} AS runtime

ARG ADAPTER_VERSION=dev

RUN useradd --uid 10001 --no-create-home --shell /usr/sbin/nologin interop \
    && mkdir -p /run/iec104-interop /app /licenses/j60870 \
    && chown interop /run/iec104-interop \
    && printf '%s\n' '#!/bin/sh' \
       'exec java -XX:+UseSerialGC -XX:TieredStopAtLevel=1 -Xshare:auto -jar /app/iec104-interop-openmuc.jar "$@"' \
       > /usr/local/bin/iec104-interop \
    && chmod +x /usr/local/bin/iec104-interop

COPY --from=build /build/target/iec104-interop-openmuc.jar /app/iec104-interop-openmuc.jar
COPY licenses/GPL-3.0.txt /licenses/j60870/COPYING
COPY THIRD_PARTY_NOTICES.md /licenses/THIRD_PARTY_NOTICES.md
COPY fixtures /fixtures

ENV ADAPTER_VERSION=${ADAPTER_VERSION}

LABEL org.opencontainers.image.title="iec104-interop-openmuc" \
      org.opencontainers.image.description="IEC 60870-5-104 reference client and server built on OpenMUC j60870" \
      org.opencontainers.image.source="https://github.com/otfabric/iec104-interop" \
      org.opencontainers.image.licenses="GPL-3.0-or-later" \
      org.opencontainers.image.version="${ADAPTER_VERSION}"

USER interop
EXPOSE 2404

HEALTHCHECK --interval=2s --timeout=2s --start-period=20s --retries=15 \
  CMD test -f /run/iec104-interop/ready

ENTRYPOINT ["/usr/local/bin/iec104-interop"]
CMD ["server"]

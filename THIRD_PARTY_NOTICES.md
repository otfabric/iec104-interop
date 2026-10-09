# Third-party notices

The images published from this repository contain third-party software.
This file is copied into every image as `/licenses/THIRD_PARTY_NOTICES.md`,
next to the licence texts.

## What is licensed how

| Part | Licence |
|------|---------|
| Repository tooling, fixtures, schemas, scripts, documentation | MIT ([LICENSE](LICENSE)) |
| `adapters/lib60870/` (links lib60870-C) | GPL-3.0-or-later |
| `adapters/openmuc/` (links j60870) | GPL-3.0-or-later |
| The published images | GPL-3.0 (they contain the libraries below) |

The adapter programs are derivative works of GPL-3.0 libraries and carry
`SPDX-License-Identifier: GPL-3.0-or-later`. Nothing in this repository is
linked into a consumer: a library that tests against the images talks to them
over TCP and reads their output, and is not affected by their licence.

## Image `iec104-interop-lib60870`

| Component | Version | Licence | Source |
|-----------|---------|---------|--------|
| lib60870-C (MZ Automation GmbH) | v2.4.1, commit `7a388e3e133999e1ca77ba7521d55d074b7cd2bc` | GPL-3.0 | https://github.com/mz-automation/lib60870 |
| cJSON (Debian `libcjson1`) | Debian trixie | MIT | https://github.com/DaveGamble/cJSON |
| Adapter | this repository, `adapters/lib60870/` | GPL-3.0-or-later | https://github.com/otfabric/iec104-interop |
| Base system | Debian trixie-slim | various; see `/usr/share/doc/*/copyright` in the image | https://www.debian.org |

Licence text in the image: `/licenses/lib60870/COPYING`.

lib60870-C is also available from MZ Automation under a commercial licence.
This repository uses the GPL-3.0 release only.

## Image `iec104-interop-openmuc`

| Component | Version | Licence | Source |
|-----------|---------|---------|--------|
| j60870 (Fraunhofer ISE, OpenMUC) | 1.7.2 | GPL-3.0 | https://www.openmuc.org/iec-60870-5-104/ ; sources on Maven Central: `org.openmuc:j60870:1.7.2:sources` |
| Gson (Google) | 2.14.0 | Apache-2.0 | https://github.com/google/gson |
| Adapter | this repository, `adapters/openmuc/` | GPL-3.0-or-later | https://github.com/otfabric/iec104-interop |
| Eclipse Temurin JRE 25 and base system | Ubuntu noble | GPL-2.0 with Classpath Exception; various | https://adoptium.net |

Licence text in the image: `/licenses/j60870/COPYING`.

## Corresponding source

For every published image the complete corresponding source is:

1. this repository at the release tag named by the image's
   `org.opencontainers.image.version` label and by `print-capabilities`
   (`adapterVersion`), which contains the adapter source and the build
   instructions (the Dockerfile); and
2. the upstream sources at exactly the versions pinned by that Dockerfile and
   `adapters/openmuc/pom.xml`, recorded in [versions.yaml](versions.yaml) and
   in the `manifest.json` of the release.

`docker build -f adapters/<adapter>/Dockerfile .` at the release tag
reproduces the image from those sources.

Each GitHub release of this repository records the upstream revisions and
image digests it was built from.

# iec104-interop Releases

## How a release is made

1. `make ci` on the commit to release.
2. Update `interop_version` in `versions.yaml` and the notes below.
3. Tag `vX.Y.Z` and push the tag. The release workflow validates, builds both images for linux/amd64 and linux/arm64 with `ADAPTER_VERSION` set to the tag, runs the smoke and cross-stack self-tests on the built images, pushes them to GHCR and creates the GitHub release with `manifest.json` (upstream pins and image digests) and `checksums.txt`.
4. Consumers pin the digests from `manifest.json`.

Tags are immutable. A fix is a new version.

| What changed | Version |
|--------------|---------|
| Contract or fixture format changed incompatibly | major |
| New operation, behaviour, fixture, adapter or document field | minor |
| Upstream pin, base image or bug fix with unchanged contract | patch |

---

## Unreleased (planned v0.1.0)

**Date:** 2026-10-09

## Summary

First version: two independent reference implementations, as client and as
server, behind container contract 1.0.

## Changes

### Added

- **Container contract 1.0** — `server`, `client <operation>`, `print-capabilities`, `print-fixture`; JSON Lines server events; one result document per client operation; exit codes 0..4; readiness file and health check.
- **Fixture format 1.0** with JSON Schema, cross-field validation and the **baseline** fixture: twelve points of eight types, seven commands of six types.
- **`lib60870` adapter** — MZ Automation lib60870-C v2.4.1 (commit `7a388e3e`), C.
- **`openmuc` adapter** — OpenMUC j60870 1.7.2, Java 25.
- **Client operations** — `connect`, `interrogate`, `counter-interrogate`, `read`, `clock-sync`, `test-command`, `command` (direct, select, sbo, cancel; with or without time tag), `monitor`.
- **Self-tests** — `make smoke` (29 contract checks per adapter) and `make interop` (249 checks over all four client/server pairings, requiring identical result documents).
- **Schemas** for the capability and client result documents, validated against every document the self-tests produce.
- **Upstream candidates workflow** — weekly and on demand: resolves the latest lib60870 release and tip and the latest j60870, self-tests the adapters on them and publishes a pin-vs-latest decision table. Pins are never changed automatically.
- **`versions.yaml`** with every upstream pin and base image digest, checked against the build files.

### Upstream notes

- **j60870 is pinned one release behind the latest (1.7.2, not 1.8.0)**: 1.8.0 closes the connection when it receives a read command and adds no protocol functionality. It is built and self-tested as a candidate (`make candidate-openmuc`, weekly by the Upstream candidates workflow) with that bug listed in `tests/known-bugs.tsv`; everything except read passes on it. Details in [docs/COMPATIBILITY.md](docs/COMPATIBILITY.md).
- Every other upstream and base image is on its latest release: lib60870-C v2.4.1, Debian 13, Temurin 25, Gson 2.14.0.
- Normalized values are exchanged as raw 16-bit numbers because lib60870 and j60870 scale the floating-point form differently.

### Not in this release

- wendy512/iec104, Eclipse OneOFour and Fraunhofer iec104-python adapters.
- TLS, file transfer, interrogation groups, redundancy.
- Published images: this version has only been built and tested locally (self-tests on linux/arm64; linux/amd64 builds under emulation).

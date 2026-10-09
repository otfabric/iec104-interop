# iec104-interop Releases

## How a release is made

1. `make ci` on the commit to release.
2. Update `interop_version` in `versions.yaml` and the notes below.
3. Tag `vX.Y.Z` and push the tag. The release workflow runs one job on an amd64 runner and one on an arm64 runner: each validates, builds every image natively with `ADAPTER_VERSION` set to the tag, runs the smoke and cross-stack self-tests on those images and pushes exactly them to GHCR as `<image>:vX.Y.Z-amd64` and `-arm64`. A last job joins the two into the multi-arch `<image>:vX.Y.Z` and creates the GitHub release with `manifest.json` (upstream pins and the digests of the multi-arch images) and `checksums.txt`.
4. Consumers pin the digests from `manifest.json`.

Tags are immutable. A fix is a new version.

| What changed | Version |
|--------------|---------|
| Contract or fixture format changed incompatibly | major |
| New operation, behaviour, fixture, adapter or document field | minor |
| Upstream pin, base image or bug fix with unchanged contract | patch |

---

## Unreleased (planned v0.2.0)

## Summary

A third independent reference implementation, and file transfer. Container
contract 1.1: additions only, documents keep `schemaVersion` `"1.0"`.

## Changes

### Added

- **`wendy512` adapter** — [wendy512/iec104](https://github.com/wendy512/iec104) v1.0.4 on its engine [go-iecp5](https://github.com/wendy512/go-iecp5) v1.2.6, Go, client and server. Image `ghcr.io/otfabric/iec104-interop-wendy512`. It has the eight operations of contract 1.0; its upstream has no time-tagged commands, no file transfer and no STARTDT/STOPDT notification on the server, and the image declares those features false.
- **File transfer in monitor direction** — `files` in the fixture format (content defined by a rule, not stored), two files in the baseline fixture, the `file-get` client operation with a `file` object in its result (length, octets received, sections, SHA-256), ASDU documents for F_FR/F_SR/F_SC/F_LS/F_AF/F_SG, and the `file-transfer` server event. Served by the lib60870 adapter; downloaded by the lib60870 and openmuc adapters.
- **Features** `fileServer`, `fileClient` and `dataTransferEvents` in the capability document, and an optional `upstream.engine`.
- **Capability-aware self-tests** — a case in `tests/cases.tsv` names the features it needs from the server and from the client and is skipped, and counted as skipped, for a pairing that lacks one. Every time-tagged command case has a twin without time tag.
- **Upstream candidates workflow** probes wendy512/iec104 and go-iecp5 as well; `make candidate-wendy512` does it locally.
- `compose.yaml` and `make run-wendy512` serve the third station on 127.0.0.1:2406.

### Changed

- `fileTransfer` is now true for lib60870 and openmuc (it was false for both in v0.1.0).
- `make interop` covers nine pairings: 643 checks, 62 skipped for undeclared capabilities.
- The release `manifest.json` says `contract: "1.1"` and lists the third image and its upstream versions.

### Upstream notes

- go-iecp5 v1.2.6 drops the command types with CP56Time2a and F_SG_NA_1 on reception; its handlers for system commands receive an ASDU whose address is already consumed; its refusals carry no P/N bit; its client reconnects after a lost connection whatever `SetAutoReconnect` says. The adapter works around what it can and declares the rest. Details in [docs/COMPATIBILITY.md](docs/COMPATIBILITY.md).
- The default branch of go-iecp5 declares another module path and cannot be built; only its release tags are probed.
- j60870 stays on 1.7.2 for the reason given under v0.1.0.

### For consumers

- Nothing changes for a consumer of the two existing images unless it asserted `fileTransfer == false`.
- A consumer that iterates over reference images should read `print-capabilities` and skip time-tagged commands and the data-transfer events for `wendy512`.

### Not in this release

- Eclipse OneOFour and Fraunhofer iec104-python adapters.
- File transfer in control direction, the file directory, a second file server.
- TLS, interrogation groups, redundancy.

---

## v0.1.0

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

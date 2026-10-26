# iec104-interop Releases

## How a release is made

1. Write the notes of the version below and set `interop_version` in `versions.yaml` to it, in the same change. `make validate` (and CI) fails when RELEASE.md has no notes for `interop_version`.
2. `make ci` on the commit to release.
3. Tag `vX.Y.Z` and push the tag. The release workflow runs one job on an amd64 runner and one on an arm64 runner: each validates (including the notes of the tag), builds every image natively with `ADAPTER_VERSION` set to the tag, runs the smoke and cross-stack self-tests on those images and pushes exactly them to GHCR as `<image>:vX.Y.Z-amd64` and `-arm64`. A last job joins the two into the multi-arch `<image>:vX.Y.Z` and creates the GitHub release with `manifest.json` (upstream pins and the digests of the multi-arch images) and `checksums.txt`.
4. Consumers pin the digests from `manifest.json`.

The notes are the body of the GitHub release, followed by the image digests:
everything after the heading `## vX.Y.Z` up to the next line that is exactly
`---` (`scripts/release-notes.sh vX.Y.Z` prints it). This is the format of
every otfabric repository and the rule of the shared release workflow, so:
one section per version, newest first, each ending with a `---` line, and no
`---` line inside a section.

Tags are immutable. A fix is a new version.

| What changed | Version |
|--------------|---------|
| Contract or fixture format changed incompatibly | major |
| New operation, behaviour, fixture, adapter or document field | minor |
| Upstream pin, base image or bug fix with unchanged contract | patch |

---

## v0.3.0

**Date:** 2026-10-26

## Summary

A fourth independent reference implementation, cB-Sigma/iec60870, and with
it the first one with application-layer security: the A-Profile of
IEC 62351-5 (IEC TS 60870-5-7). Container contract 1.2: additions only,
documents keep `schemaVersion` `"1.0"`.

## Changes

### Added

- **`cbsigma` adapter** — [cB-Sigma/iec60870](https://github.com/cB-Sigma/iec60870) v0.3.0, Go, client and server. Image `ghcr.io/otfabric/iec104-interop-cbsigma`. Every operation of contract 1.1, time-tagged commands, file transfer as station and as controlling station, the data-transfer events, and the security flags.
- **Security flags** (contract 1.2), for images with the feature `secureAuthentication`: `--secure`, `--security-cert`, `--security-key`, `--security-trust` (CA certificates are authorities, other certificates pins of self-signed ones), `--association-id`; `--mal` and `--dpa` for the client, `--roles` for the server. The `ready` event says `secure`, the result document `secured`, and the error `security-failed` means no association. A mirror with cause 17 (not authorized) counts as a negative confirmation; with `--roles`, a request the roles do not permit is refused that way.
- **Secure self-tests** — `tests/secure-cases.tsv` runs for every pairing that declares `secureAuthentication`, against key material made with openssl for the run (`make_identities` in `scripts/lib.sh`): authority and pinned certificates, HMAC-SHA-256 and AES-256-GCM, other association identities, a viewer refused with cause 17 and an operator executed, untrusted stations on either side, a client without security. The smoke test checks the usage errors of the flags on every image and the `ready` event of a secure server.
- **Upstream candidates workflow** probes cB-Sigma/iec60870 as well; `make candidate-cbsigma` does it locally.
- `compose.yaml` and `make run-cbsigma` serve the fourth station on 127.0.0.1:2407.

### Changed

- `secureAuthentication` is a required feature of the capability document; lib60870, openmuc and wendy512 declare it false.
- The fixture rules say what a station does with a file deactivation (nothing) and leave the NOS of the last-section F_LS_NA_1 to the library; the agreement check no longer compares that field.
- **OpenMUC j60870 1.7.2 → 1.8.0**, the latest release. 1.8.0 cannot decode a read command (C_RD_NA_1) and closes the connection on one; instead of staying a release behind for that, the bug is listed in `tests/known-bugs.tsv`, so the read cases of openmuc pairings count as known upstream bugs. 1.8.0 adds no protocol functionality a test can use. Details in [docs/COMPATIBILITY.md](docs/COMPATIBILITY.md).
- The rule for upstream pins is the latest release, also when it has a bug: the bug goes into `tests/known-bugs.tsv` ([CONTRIBUTING.md](CONTRIBUTING.md)).
- `make interop` covers sixteen pairings: 1172 checks passed, 23 known j60870 read bugs, 88 skipped for undeclared capabilities.
- The release `manifest.json` says `contract: "1.2"` and lists the fourth image and its upstream version.
- **Release notes come from RELEASE.md**, as in every otfabric repository: the section of the tag, up to its `---` line, is the body of the GitHub release (followed by the image digests), and the release is titled `iec104-interop vX.Y.Z`. `scripts/release-notes.sh` extracts it; `make validate`, CI and the release workflow fail when the notes of the version are missing.

### Upstream notes

- cB-Sigma/iec60870 v0.3.0 sends one less than lib60870 as the NOS of the last section, cuts segments of 200 octets unless told otherwise, deactivates a file after a refused selection, has no STOPDT notification, takes a timer of 0 for its default and asks for the file write permission for the acknowledgement of a download. Details in [docs/COMPATIBILITY.md](docs/COMPATIBILITY.md).

### For consumers

- Nothing changes for a consumer of the three existing images unless it validates capability documents against a copy of the old schema, or reads from the openmuc image: its j60870 1.8.0 closes the connection on a read command (as a server) and on a refused read (as a client).
- A consumer that tests IEC 62351-5 mounts key material into the cbsigma image; see [README.md](README.md#using-the-images-from-a-librarys-tests) and the [security flags](docs/CONTAINER_CONTRACT.md#security-flags).

### Not in this release

- Eclipse OneOFour and Fraunhofer iec104-python adapters.
- TLS (IEC 62351-3) flags.

---

## v0.2.0

**Date:** 2026-10-09

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

---

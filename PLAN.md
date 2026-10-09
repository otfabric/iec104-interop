# Plan

Delivery in phases, each with an acceptance gate. Status as of 2026-10-09.

| Phase | Scope | Status |
|-------|-------|--------|
| 1 | Foundation: contract, fixture format, validation, version manifest, tooling | done |
| 2 | lib60870 adapter, client and server | done |
| 3 | OpenMUC adapter; cross-stack self-test | done |
| 4 | First published images; consumption from go-iec104 | done: v0.1.0 |
| 5 | wendy512, Eclipse OneOFour, Fraunhofer adapters | wendy512 done; the other two not started |
| 6 | Expanded coverage and hardening | partly: see below |
| 7 | Release qualification | workflow written, not exercised |

## Phase 1 - Foundation (done)

- Container contract 1.0 ([docs/CONTAINER_CONTRACT.md](docs/CONTAINER_CONTRACT.md)), schemas for the capability and client result documents
- Fixture format 1.0 with schema, cross-field validation and the baseline fixture ([docs/FIXTURES.md](docs/FIXTURES.md))
- `versions.yaml` and a check that it matches the build files
- Makefile, `compose.yaml`, CI workflow

**Gate:** fixtures validate and the repository builds with no reference to any consumer. Met.

## Phase 2 - lib60870 (done)

- C adapter on lib60870-C v2.4.1, pinned by commit, both roles, all eight client operations
- Smoke test of the contract: command surface, capabilities, baked fixture, readiness, events, exit codes, SIGTERM

**Gate:** client and server operate against each other using lib60870 alone. Met (29 smoke checks).

## Phase 3 - OpenMUC (done)

- Java adapter on j60870 1.7.2, both roles, same operations
- Cross-stack self-test over all four pairings: expected outcome per case, and identical result documents across pairings

**Gate:** both stacks satisfy the baseline contract in every direction. Met (249 checks). Findings are in [docs/COMPATIBILITY.md](docs/COMPATIBILITY.md), notably that j60870 1.8.0 cannot receive a read command, which is why 1.7.2 is pinned.

## Phase 4 - First published images (done)

- `v0.1.0`: the release workflow built linux/amd64 and linux/arm64, pushed both images to GHCR and attached `manifest.json` with the digests.
- go-iec104 pins those digests and runs its suite against them in CI.

**Gate:** go-iec104 runs its bidirectional tests against the published images without a checkout of this repository. Met.

## Phase 5 - Additional adapters (wendy512 done)

### wendy512/iec104 (done)

Evaluation: both roles exist; the latest tags are v1.0.4 (Apache-2.0) and, for its engine go-iecp5, v1.2.6 (LGPL-3.0); the engine descends from thinkgos/go-iecp5 and shares nothing with lib60870 or j60870, so it counts as a third independent implementation; pure Go, so both architectures build without emulation of a compiler.

- Go adapter, both roles, the eight operations of contract 1.0. The server uses wendy512/iec104, the client its engine directly.
- Not available in the upstream and declared so: time-tagged commands, file transfer, the STARTDT/STOPDT server events.

**Gate:** smoke test and cross-stack self-test for the roles it declares. Met (29 smoke checks; 643 cross-stack checks over nine pairings, 62 skipped for undeclared capabilities). Findings are in [docs/COMPATIBILITY.md](docs/COMPATIBILITY.md).

### Remaining

In priority order. Each needs the evaluation first; none blocks a release.

| Adapter | To establish before writing it |
|---------|--------------------------------|
| Eclipse OneOFour (Java, P1) | Whether the source still builds with a current JDK and Maven; which roles it provides; its licence per component |
| Fraunhofer iec104-python (P2) | Packaging (wheels per architecture); declare `independentEngine: false` because it wraps lib60870 |

An adapter joins `ADAPTERS` in the Makefile and the CI matrix once it passes `make smoke-<adapter>` and the cross-stack self-test for the roles it declares.

## Phase 6 - Expanded coverage (partly done)

Done: the upstream candidates workflow (weekly probe of the latest lib60870, j60870 and wendy512/iec104 with go-iecp5, with a pin-vs-latest decision table); capability-aware self-tests (a case names the features it needs and is skipped for a pairing that lacks one); file transfer in monitor direction (contract 1.1: `files` in fixtures, the `file-get` operation, a file server in the lib60870 adapter); commands of six types with and without time tag, select-before-operate and deactivation, counter interrogation, clock synchronization, time-tagged reports on command, `k`/`w` at 1, the idle test, concurrent sessions.

Open, each as a fixture or operation that every published adapter can serve:

- Further type identifications (bitstring command, packed points, protection events, parameters)
- Sequence-of-elements (SQ = 1) answers to an interrogation
- Interrogation groups
- Spontaneous data to all sessions, for redundancy scenarios
- File transfer in control direction (upload), the directory, and a file server on a second stack
- TLS, where at least two stacks support it

## Phase 7 - Release qualification (open)

- Image scanning and SBOMs attached to releases
- A compatibility report generated from the self-test documents

# Plan

Delivery in phases, each with an acceptance gate. Status as of 2026-10-09.

| Phase | Scope | Status |
|-------|-------|--------|
| 1 | Foundation: contract, fixture format, validation, version manifest, tooling | done |
| 2 | lib60870 adapter, client and server | done |
| 3 | OpenMUC adapter; cross-stack self-test | done |
| 4 | First published images; consumption from go-iec104 | images not published yet; consumer suite exists and passes against local builds |
| 5 | wendy512, Eclipse OneOFour, Fraunhofer adapters | not started |
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

## Phase 4 - First published images (open)

Remaining:

1. Tag `v0.1.0`; the release workflow builds linux/amd64 and linux/arm64, pushes to GHCR and attaches `manifest.json` with the digests.
2. Make the two GHCR packages public (or grant the consumer's CI read access).
3. In go-iec104, replace the `:dev` default by the released digests and enable its interop workflow on push.

go-iec104 already carries its suite (`interop/`, `make interop`) and passes it against locally built images in both directions.

**Gate:** go-iec104 runs its bidirectional tests against the published images without a checkout of this repository.

## Phase 5 - Additional adapters (not started)

In priority order. Each needs the evaluation first; none blocks a release.

| Adapter | To establish before writing it |
|---------|--------------------------------|
| wendy512/iec104 (Go, P1) | Whether a server role exists and is usable; pin a tagged release or a commit; map its ASDU model to the documents |
| Eclipse OneOFour (Java, P1) | Whether the source still builds with a current JDK and Maven; which roles it provides; its licence per component |
| Fraunhofer iec104-python (P2) | Packaging (wheels per architecture); declare `independentEngine: false` because it wraps lib60870 |

An adapter joins `ADAPTERS` in the Makefile and the CI matrix once it passes `make smoke-<adapter>` and the cross-stack self-test for the roles it declares.

## Phase 6 - Expanded coverage (partly done)

Done: the upstream candidates workflow (weekly probe of the latest lib60870 and j60870 with a pin-vs-latest decision table); commands of six types with and without time tag, select-before-operate and deactivation, counter interrogation, clock synchronization, time-tagged reports on command, `k`/`w` at 1, the idle test, concurrent sessions.

Open, each as a fixture or operation that every published adapter can serve:

- Further type identifications (bitstring command, packed points, protection events, parameters)
- Sequence-of-elements (SQ = 1) answers to an interrogation
- Interrogation groups
- Spontaneous data to all sessions, for redundancy scenarios
- TLS, where both stacks support it

## Phase 7 - Release qualification (open)

- Exercise the release workflow end to end on the first tag
- Image scanning and SBOMs attached to releases
- A compatibility report generated from the self-test documents

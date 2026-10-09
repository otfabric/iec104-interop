# Requirements

What this repository must do and must not do. [PLAN.md](PLAN.md) says in
which order; [docs/CONTAINER_CONTRACT.md](docs/CONTAINER_CONTRACT.md) and
[docs/FIXTURES.md](docs/FIXTURES.md) are the specifications that follow from
it.

## Purpose

Independent, reproducible interoperability validation for IEC 60870-5-104
libraries: established third-party implementations, packaged as containers
that all behave the same way, so that a library can be tested against real
implementation diversity without owning any of it.

It is not another IEC 104 implementation and not a general protocol-testing
framework.

## R1 - Repository isolation

- R1.1 This repository does not import, build, link against or depend on any
  library under test, and contains no test logic or expectation specific to
  one. In particular it knows nothing about `otfabric/go-iec104`.
- R1.2 A consumer depends on published images and the documented contract
  only. It does not need a checkout of this repository to run its tests.
- R1.3 No third-party code from here is copied into a consumer, and the
  contract does not shape a consumer's public API.

| Responsibility | Here | Consumer |
|----------------|:----:|:--------:|
| Third-party implementations, adapters, Dockerfiles | yes | no |
| Fixtures, fixture schema and validation | yes | no |
| Container contract, capability declarations | yes | no |
| Image builds and publishing, licence compliance | yes | no |
| Self-tests of the reference stacks | yes | no |
| Scenarios and assertions about a library | no | yes |
| Compatibility claims and release qualification of a library | no | yes |

## R2 - Reference implementations

- R2.1 Exactly these five are in scope, in this order of priority:

  | Priority | Implementation | Language | Independent engine |
  |----------|----------------|----------|:------------------:|
  | P0 | MZ Automation lib60870 | C | yes |
  | P0 | OpenMUC j60870 | Java | yes |
  | P1 | wendy512/iec104 | Go | yes |
  | P1 | Eclipse OneOFour | Java | yes |
  | P2 | Fraunhofer iec104-python | Python / C++ | no: wraps lib60870 |

- R2.2 Each is its own image. No image contains two implementations.
- R2.3 An adapter that wraps another adapter's protocol engine says so
  (`upstream.independentEngine: false`) and does not count as an additional
  independent implementation.
- R2.4 An adapter is published only when it builds from pinned sources and
  passes the smoke test and the cross-stack self-test for the roles it
  declares. An upstream that cannot be made to do so is documented and left
  out; it does not block the others.

## R3 - Container contract

- R3.1 Every image has the same entry points: `server`, `client`,
  `print-capabilities`, `print-fixture`, with the same flags and semantics.
- R3.2 Servers signal readiness when they actually accept IEC 104
  connections, by a file and an event. Nothing relies on delays.
- R3.3 Machine-readable output is JSON on stdout with stable field names;
  diagnostics go to stderr; exit codes have fixed meanings.
- R3.4 Every operation is bounded by a configurable timeout.
- R3.5 Servers stop cleanly on SIGTERM and exit non-zero when they cannot
  start.
- R3.6 Capabilities are machine-readable and declared only for behaviour the
  self-tests exercise.

## R4 - Fixtures

- R4.1 Fixtures describe the simulated station and its observable behaviour,
  independent of any implementation: addresses, types, values, quality,
  command targets and effects.
- R4.2 Fixtures contain no assertions, no vendor callback names and no names
  from any library under test.
- R4.3 Dynamic behaviour is deterministic and triggered by requests, not by
  timers.
- R4.4 Fixtures are validated against a schema, in CI and by each adapter at
  start.
- R4.5 A type an adapter cannot serve is not silently replaced by another;
  the fixture format only allows what every published adapter supports.

## R5 - Reproducibility

- R5.1 Every upstream is pinned to a commit or exact version, every base
  image to a digest. `versions.yaml` records all of them and is checked
  against the build files.
- R5.2 Releases are immutable tags; images are published with their digests.
  Nothing a release depends on floats.
- R5.3 Images are published for linux/amd64 and linux/arm64. An adapter that
  cannot support one documents it.

## R6 - Self-tests

- R6.1 The reference stacks are tested against each other in every supported
  direction; the results are identical documents, not just successes.
- R6.2 These results are about the references and are never presented as
  results for a consumer.
- R6.3 Malformed frames, invalid sequence numbers and timer-boundary cases
  are out of scope: reference implementations are not misused as fault
  injectors. That testing belongs in a library's own unit, integration and
  fuzz tests.

## R7 - Licensing and safety

- R7.1 Licence notices of all bundled software are preserved in the images;
  GPL source obligations are met by pinned, recorded sources.
- R7.2 GPL components stay in the adapter images and never reach a
  consumer's code.
- R7.3 Test services are exposed on loopback or private networks by default.

## R8 - Scope discipline

- R8.1 Adapters are small and explicit. No generic interoperability
  framework, no protocol-independent abstraction without a concrete need.
- R8.2 Structure is added when an implementation requires it, not before.

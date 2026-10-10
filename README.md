# iec104-interop — IEC 60870-5-104 (IEC 104) Interoperability Testing with Docker

A Docker-based interoperability testing framework for IEC 60870-5-104 (IEC 104), providing independent SCADA client and server implementations, deterministic fixtures, and automated protocol compatibility testing.
Test IEC 104 libraries against established implementations including MZ Automation lib60870 and OpenMUC j60870. Reference implementations are independently containerized, enabling reproducible testing across platforms without integrating third-party protocol stacks into production applications.

| Adapter | Implementation | Language | Server | Client | Image |
|---------|----------------|----------|:------:|:------:|-------|
| `lib60870` | [MZ Automation lib60870-C](https://github.com/mz-automation/lib60870) v2.4.1 | C | yes | yes | `ghcr.io/otfabric/iec104-interop-lib60870` |
| `openmuc` | [OpenMUC j60870](https://www.openmuc.org/iec-60870-5-104/) 1.7.2 | Java | yes | yes | `ghcr.io/otfabric/iec104-interop-openmuc` |
| `wendy512` | [wendy512/iec104](https://github.com/wendy512/iec104) v1.0.4 on [go-iecp5](https://github.com/wendy512/go-iecp5) v1.2.6 | Go | yes | yes | `ghcr.io/otfabric/iec104-interop-wendy512` |

The stacks do not all do the same: file transfer is served by lib60870 only,
and wendy512 has no time-tagged commands. Each image says what it supports
(`print-capabilities`); see [docs/CAPABILITIES.md](docs/CAPABILITIES.md).

Planned: Eclipse OneOFour (Java) and Fraunhofer iec104-python. See
[PLAN.md](PLAN.md).

**Docs:** [docs/CONTAINER_CONTRACT.md](docs/CONTAINER_CONTRACT.md) (commands, JSON documents, exit codes) · [docs/FIXTURES.md](docs/FIXTURES.md) (the simulated station and its behaviour) · [docs/CAPABILITIES.md](docs/CAPABILITIES.md) · [docs/COMPATIBILITY.md](docs/COMPATIBILITY.md) (pins, cross-stack matrix, findings) · [COVERAGE.md](COVERAGE.md) · [REQUIREMENTS.md](REQUIREMENTS.md) · [PLAN.md](PLAN.md) · [RELEASE.md](RELEASE.md)

## Table of contents

- [iec104-interop — IEC 60870-5-104 (IEC 104) Interoperability Testing with Docker](#iec104-interop--iec-60870-5-104-iec-104-interoperability-testing-with-docker)
  - [Table of contents](#table-of-contents)
  - [What this repository is, and is not](#what-this-repository-is-and-is-not)
  - [Quick start](#quick-start)
  - [Using the images from a library's tests](#using-the-images-from-a-librarys-tests)
  - [Repository layout](#repository-layout)
  - [Development](#development)
  - [Security](#security)
  - [License](#license)

## What this repository is, and is not

It owns the reference implementations, their adapters, the fixtures, the
container contract, the images and the self-tests that show the references
agree with each other.

It does not contain, import, build or know about any library under test.
[otfabric/go-iec104](https://github.com/otfabric/go-iec104) is a consumer: it
pulls the images and owns its own scenarios, assertions and compatibility
claims. A passing self-test here says the *references* interoperate; it says
nothing about a consumer.

The fixtures describe what a station is; the adapters make each
implementation behave that way; the consumer decides what to assert.

## Quick start

Needs Docker and `jq`.

```sh
make images      # build the adapter images for this machine
make smoke       # container contract of each image
make interop     # every client against every server
```

A reference server on loopback, and a reference client against it:

```sh
make run-lib60870        # 127.0.0.1:2404, baseline fixture, Ctrl-C to stop
```

```sh
docker run --rm --add-host=host.docker.internal:host-gateway \
  ghcr.io/otfabric/iec104-interop-openmuc:dev \
  client interrogate --host host.docker.internal --port 2404 | jq .
```

```json
{
  "schemaVersion": "1.0",
  "adapter": "openmuc",
  "operation": "interrogate",
  "ok": true,
  "confirmations": [{ "cot": 7, "negative": false }],
  "terminated": true,
  "asdus": [
    { "type": "C_IC_NA_1", "cot": 7, "objects": [{ "ioa": 0, "qoi": 20 }] },
    { "type": "M_SP_NA_1", "cot": 20, "objects": [{ "ioa": 100, "value": true, "quality": [] }] }
  ]
}
```

(abridged). Every image has the same commands:

```text
server               a controlled station serving a fixture
client <operation>   connect, interrogate, counter-interrogate, read, clock-sync,
                     test-command, command, monitor, file-get
print-capabilities   what this adapter supports, as JSON
print-fixture        the fixture baked into the image
```

## Using the images from a library's tests

A consumer needs Docker, the image references and its own test code. It does
not clone this repository.

**Library as client, reference as server.** Start the server, wait for the
readiness file (never for a delay), connect:

```sh
docker run -d --name ref -p 127.0.0.1:2404:2404 ghcr.io/otfabric/iec104-interop-lib60870:<tag> server
until docker exec ref test -f /run/iec104-interop/ready; do sleep 0.1; done
```

The server's stdout is a JSON Lines stream of what it received
(`interrogation`, `command`, `clock-sync`, ...), for asserting what the
library actually put on the wire.

**Library as server, reference as client.** Serve the fixture with the
library, run a client operation, assert on the result document and exit code:

```sh
docker run --rm --add-host=host.docker.internal:host-gateway \
  ghcr.io/otfabric/iec104-interop-openmuc:<tag> \
  client command --type C_SC_NA_1 --ioa 500 --value false \
  --host host.docker.internal --port <port of the library's server>
```

**Expected values** come from the image: `print-fixture baseline` prints the
fixture it was built with, so expectations cannot drift from the image.

**What a stack supports** comes from the image too: `print-capabilities`.
Skip, do not fail, a scenario that needs a feature an image declares false.

**Pin by digest** in release qualification
(`ghcr.io/otfabric/iec104-interop-lib60870@sha256:...`); the digests are in
each release's `manifest.json`.

## Repository layout

```text
iec104-interop/
├── adapters/
│   ├── lib60870/        C adapter: Dockerfile, CMakeLists.txt, src/
│   ├── openmuc/         Java adapter: Dockerfile, pom.xml, src/
│   └── wendy512/        Go adapter: Dockerfile, go.mod, *.go
├── fixtures/
│   ├── schema/          JSON Schema of the fixture format
│   └── baseline/        the baseline station
├── schemas/             JSON Schemas of the capability and client result documents
├── tests/cases.tsv      self-test cases: operation, expected outcome, features needed
├── tests/known-bugs.tsv bugs of upstream versions that are built as candidates
├── scripts/             validate, smoke, cross-stack self-test
├── docs/                contract, fixtures, capabilities, compatibility
├── versions.yaml        every upstream pin and base image digest
├── compose.yaml         the reference servers on loopback
└── Makefile
```

## Development

```sh
make help        # all targets
make ci          # validate, lint, build, smoke, cross-stack self-test, document schemas
make validate    # fixtures against the schema and the cross-field rules
make buildx      # check the other architecture locally, under emulation (CI builds both natively)
make candidate-openmuc   # self-test the adapter on the latest j60870 instead of the pin
```

The **Upstream candidates** workflow runs that probe weekly for lib60870,
j60870 and wendy512/iec104 with go-iecp5 and reports pin versus latest in
its job summary.

`make smoke ADAPTERS=lib60870` and `VERBOSE=1 make interop` narrow and
expand what is run and shown. See [CONTRIBUTING.md](CONTRIBUTING.md) for
adding an adapter, a fixture or a client operation.

## Security

The servers are unauthenticated IEC 60870-5-104 stations that accept
commands from anyone who can reach them. They are test fixtures:
`compose.yaml` and the `run-*` targets publish them on loopback only, and
they must never be attached to a plant network. See [SECURITY.md](SECURITY.md).

## License

The repository's own code and documentation are MIT-licensed, except the
adapter sources under `adapters/`, which are GPL-3.0-or-later: two of them
link GPL-3.0 libraries, the third an Apache-2.0 and an LGPL-3.0 one. The
images contain those libraries and are distributed under GPL-3.0. See [LICENSE](LICENSE) and
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

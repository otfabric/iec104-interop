# Contributing to iec104-interop

## Ground rules

- **Nothing here may know about a library under test.** No imports, no build steps, no expectations, no names from `go-iec104` or any other consumer. See [REQUIREMENTS.md](REQUIREMENTS.md) R1.
- **The contract is the product.** Adapters differ inside; their commands, documents and exit codes are identical. A change to [docs/CONTAINER_CONTRACT.md](docs/CONTAINER_CONTRACT.md) or [docs/FIXTURES.md](docs/FIXTURES.md) lands in every published adapter in the same change.
- **Declare only what is tested.** A capability is `true` when the self-tests exercise it for that adapter.
- **Pin everything.** Upstreams by commit or exact version, base images by digest, all mirrored in [versions.yaml](versions.yaml).

## Setup

Docker (with buildx), `jq`, GNU make and Python 3. Nothing else: compilers, Maven and the JDK run inside the image builds.

```sh
make ci     # validate, lint, build, smoke, cross-stack self-test, document schemas
```

Run `make` for all targets.

## Changing an adapter

1. Edit under `adapters/<adapter>/`.
2. `make image-<adapter> smoke-<adapter>`.
3. `make interop`: the result documents of all pairings must stay identical. If two stacks disagree, find out which one is right by the standard before touching the expectation.
4. If behaviour visible to consumers changed, update the contract or fixture documentation in the same change.

Adapter sources link GPL-3.0 libraries: keep the `SPDX-License-Identifier: GPL-3.0-or-later` header.

## Moving an upstream pin

1. Change the pin in the Dockerfile (and `pom.xml`, or `go.mod` with `go mod tidy`) and in `versions.yaml`; `scripts/check-versions.sh` must pass.
2. `make ci`.
3. Record the new version, and anything it changed, in [docs/COMPATIBILITY.md](docs/COMPATIBILITY.md), [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) and [RELEASE.md](RELEASE.md).

The rule is the latest upstream release. A newer release that fails the self-tests is not adopted as the pin, but it is not dropped either: list its bug in [tests/known-bugs.tsv](tests/known-bugs.tsv), build it as a candidate (`make candidate-openmuc` is the model) and record in [docs/COMPATIBILITY.md](docs/COMPATIBILITY.md) what the older pin costs. When the candidate run reports that the known bug no longer reproduces, move the pin.

The **Upstream candidates** workflow does this probe every week for every stack and summarizes it as a decision table; `make candidate-openmuc`, `make candidate-lib60870 LIB60870_CANDIDATE_SHA=<commit>` and `make candidate-wendy512` run the same thing locally.

## Registries and retries

CI does not pull from Docker Hub: its anonymous rate limit and its token service fail too often on shared runners. The workflows set `BASE_REGISTRY=mirror.gcr.io/library` (base images, which the Dockerfiles pin by digest, so the mirror cannot change what is built) and `TOOLS_REGISTRY=mirror.gcr.io` (BuildKit, QEMU handlers, shellcheck). Locally both default to Docker Hub.

Every command that talks to a registry runs through `scripts/retry.sh`, which repeats it with a growing delay when the output shows a rate limit, a 5xx or a timeout, and returns any other failure at once. Use it for new registry or download steps instead of an action that cannot retry; `make publish-arch`, `make publish-manifest` and `make digests` are what the release workflow calls in place of the Docker push actions.

Both architectures are built and self-tested natively, on an amd64 and an arm64 runner; nothing in CI runs under emulation. `make buildx-setup` and `make buildx` remain for checking the other architecture from one machine.

## Adding a client operation or a server behaviour

1. Specify it first: the operation, its flags, when it succeeds and its result fields in the contract; the station behaviour in [docs/FIXTURES.md](docs/FIXTURES.md).
2. Implement it in every published adapter.
3. Add cases to [tests/cases.tsv](tests/cases.tsv) with the expected outcome, and a value check in `scripts/interop.sh` if the outcome line cannot show what matters.
4. Add the feature to the capability documents and to [docs/CAPABILITIES.md](docs/CAPABILITIES.md) and [COVERAGE.md](COVERAGE.md).
5. Extend the schemas in `schemas/` if documents gained fields.

Additive changes bump the contract's minor version; anything a consumer could break on bumps the major version.

## Adding a fixture

See [docs/FIXTURES.md](docs/FIXTURES.md#adding-a-fixture).

## Adding an adapter

1. Evaluate the upstream: does it build from a pinned source on both architectures, which roles does it provide, what is its licence, and is its protocol engine its own? Write the answers into [PLAN.md](PLAN.md) before writing code.
2. Create `adapters/<name>/` with a Dockerfile built from the repository root, following the existing ones: pinned build stage, minimal runtime stage, non-root user, `/fixtures`, `/licenses`, the readiness file, the health check, `ENTRYPOINT ["/usr/local/bin/iec104-interop"]`.
3. Implement the contract. For a role the upstream does not support, the command exits 2 with a message and `roles` says `false`. What the upstream cannot do within a role is a feature declared `false`, and the cases that need it name it in their `needs` column: never fake a behaviour the library does not have.
4. `scripts/smoke.sh <name>` must pass, then `scripts/interop.sh lib60870 openmuc wendy512 <name>`.
5. Add it to `ADAPTERS` in the Makefile, to the workflows, `versions.yaml`, `scripts/check-versions.sh`, the notices and the documentation tables.

Do not build a shared adapter framework. Three adapters in three languages share a contract, not code.

## Style

- Shell: `bash`, `set -uo pipefail`, shellcheck-clean (`make lint`). No associative arrays or other bash 4+ features in code paths macOS's `/bin/bash` could run.
- C: C11, `-Wall -Wextra -Werror`.
- Java: 25, `-Xlint:all -Werror`.
- Documentation states what was verified. If something is assumed or untested, say so.

## License

By contributing you agree that your contributions are licensed as the part of the repository they touch: MIT, or GPL-3.0-or-later under `adapters/`.

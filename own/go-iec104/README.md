# iec104-interop-otfabric

[go-iec104](https://github.com/otfabric/go-iec104), OT Fabric's own IEC 104
stack, behind the [container contract](../../docs/CONTAINER_CONTRACT.md), so
that other stacks can test against it. Anything that uses an
`iec104-interop-<adapter>` image can use this one in its place. Unlike those, it
is not a reference: its tags are go-iec104 versions, and no harness release or
`manifest.json` contains it.

**For testing only.** It runs an unauthenticated IEC 104 server unless `--secure`
or `--tls` is given. Do not expose it to a network you do not control. Old images stay
available, so check
[go-iec104 advisories](https://github.com/otfabric/go-iec104/security/advisories)
for the version you pull.

**Not an independent implementation.** The image declares
`upstream.independentEngine: false`, and `results.json` records
`independent: false`. A result against it says nothing independent about
go-iec104 when OT Fabric produces it. A result you publish, naming the
implementation and version, is independent evidence, and we would like to hear
about it.

## Use

```sh
docker run -d --name go-iec104 -p 127.0.0.1:2404:2404 ghcr.io/otfabric/iec104-interop-otfabric:<tag> server
until docker exec go-iec104 test -f /run/iec104-interop/ready; do sleep 0.1; done
```

`<tag>` is a go-iec104 version such as `v0.1.2`, or `latest`, the newest one
published. A rebuild of a version replaces its tag, so pin the digest if you need
an exact build. The digest is in the summary of the
[workflow run](https://github.com/otfabric/iec104-interop/actions/workflows/own-go-iec104.yml),
and `docker run --rm <image> version` prints the versions.

| | |
|---|---|
| Upstream | go-iec104, the latest release when the image was built |
| Licence | MIT; every module's licence is in the image under `/licenses` |
| Adapter name | `otfabric` (the name in documents, as the schema allows) |
| Language | Go |
| Protocol engine | OT Fabric's own |

## Why it is apart from the other adapters

`REQUIREMENTS.md` R1: nothing under `adapters/`, `scripts/`, `tests/` or the
top-level `Makefile` knows a library under test, and CI enforces that. This
directory and `.github/workflows/own-go-iec104.yml` are the one exception. The
scripts take the image through `ADAPTER_IMAGE_otfabric`.

No release loop can form (`otfabric/engineering` ADR-0012). This image is built
by hand, from the latest go-iec104 release, and nothing is committed: this
repository has no pin of go-iec104 to bump. No release or required check of
go-iec104 needs the image, and go-iec104 commits no pin of it. It is not in a
harness release or `manifest.json`.

## Publishing

Run the **Own stack (go-iec104)** workflow by hand, from `main`. It finds the
latest go-iec104 release and the latest harness release, and prints them. It
builds the image, runs the smoke test and the cross-stack self-test on both
architectures, then publishes `:<go-iec104 version>` and `:latest`. Run from a
branch, it stops before publishing. Nothing runs it automatically, so after a
go-iec104 release the image stays on the old version until someone does.

```sh
make -f own/go-iec104/Makefile image smoke interop    # from the repository root
```

## What it cannot do

Everything in the contract is implemented and declared `true`.

With `server --redundancy-group`, a STARTDT that takes over from a started
connection leaves the old link started inside go-iec104: only queued events stop
going to it, and they go back to it when the connection that took over stops. The
adapter therefore keeps the queued event until a connection starts, and ignores the
requests of such a connection, so that nothing is sent on it, except an answer that
was already being sent at that moment. A STARTDT act on it is then confirmed by the
library without making it the started connection again; a STOPDT act first does
that.

`client redundancy` runs each step itself with two clients, not with
`client.Group`. go-iec104 closes a connection on an I frame outside its started
time, before any handler sees it. That counts as one stray, however many the
station sent, and the frame is not in `asdus`.

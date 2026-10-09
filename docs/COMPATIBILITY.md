# Compatibility

Which upstream versions the adapters are built on, what the reference stacks
were verified to do against each other, and what was found on the way.

Everything here is about the reference stacks. It makes no statement about
any library that consumes the images.

## Pinned upstreams

The rule is the latest upstream release. An adapter stays behind only for a
bug that removes coverage, and then the latest release is still built and
self-tested as a candidate.

| Adapter | Upstream | Pinned | Latest | Pin | Engine |
|---------|----------|--------|--------|-----|--------|
| `lib60870` | [MZ Automation lib60870-C](https://github.com/mz-automation/lib60870) | v2.4.1 | v2.4.1 | commit `7a388e3e133999e1ca77ba7521d55d074b7cd2bc` | independent, C |
| `openmuc` | [OpenMUC j60870](https://www.openmuc.org/iec-60870-5-104/) | 1.7.2 | 1.8.0 | Maven Central `org.openmuc:j60870:1.7.2` | independent, Java |

Everything else is on its latest release as of 2026-10-09: Debian 13
(trixie), Eclipse Temurin 25, Maven 3.9, Gson 2.14.0.

The pins live in the Dockerfiles and `adapters/openmuc/pom.xml` and are
mirrored in [versions.yaml](../versions.yaml); `scripts/check-versions.sh`
keeps them in step. The **Upstream candidates** workflow
(`.github/workflows/candidate.yml`, weekly and on demand) resolves the latest
release of each stack and the tip of lib60870, builds the adapters on them,
runs the smoke and cross-stack self-tests and writes a pin-vs-latest decision
table to its job summary. It never changes a pin.

### Why j60870 is one release behind

1.8.0 cannot receive a read command (first finding below). Staying on 1.7.2
keeps the read procedure covered on the second independent engine; moving to
1.8.0 would remove it and gain nothing a test can use:

| Change in 1.8.0 over 1.7.2 | Relevance to interoperability testing |
|----------------------------|----------------------------------------|
| Validation of decoded and constructed ASDUs (counts, SQ on commands, unknown cause) | Causes the read regression. Otherwise only affects malformed traffic |
| `ASdu.buildCotResponse` helper | API convenience; nothing new on the wire |
| Pluggable logging (`org.openmuc.j60870.logging`) | none |
| Allowed-client check compares addresses instead of strings | none here |
| Listener kept across repeated STOPDT/STARTDT on a client | none here: one cycle per client run |
| `C_TS_NA_1` test pattern byte order corrected (AA 55) | none: the type is not used by IEC 60870-5-104 or by the contract |

No type identification, service or transport option was added. The source
difference is 447 added and 106 removed lines, most of the rest being
copyright years.

`make candidate-openmuc` builds the adapter on 1.8.0 and runs the same
self-tests with the read bug listed in
[tests/known-bugs.tsv](../tests/known-bugs.tsv). Result on 2026-10-09: the
smoke test passes (29 of 29) and the cross-stack self-test has 226 passes, no
failure and 11 known-bug hits, all of them read cases. So apart from read,
1.8.0 behaves exactly as 1.7.2 does in every pairing.

The pin moves to the latest release as soon as a release decodes C_RD_NA_1:
the candidate run then fails with "known bug did not reproduce", which is the
signal.

## Cross-stack matrix

`make interop` runs every case of [tests/cases.tsv](../tests/cases.tsv) in
all four pairings and requires, besides the expected outcome, that the four
result documents are identical apart from time tags.

| Client → Server | lib60870 | openmuc |
|-----------------|:--------:|:-------:|
| **lib60870** | pass | pass |
| **openmuc** | pass | pass |

Verified on linux/arm64 with the pins above: 30 cases × 4 pairings, the value
checks and the session checks.

Covered: STARTDT/STOPDT; general interrogation (station, broadcast address,
unknown station, unsupported group); counter interrogation; read of a
status, a measurement, a counter and an unknown address; clock
synchronization; test command; single, double, regulating step and the
three set-point commands, with and without time tag, direct, select only,
select-and-execute and cancelled; refusals for an unknown object, an object
of another type, a state that is not permitted and an execute without the
required select; flow control with `k = w = 1` on both sides; an idle
connection kept alive by test frames; three sessions at once.

## Findings

### j60870 1.8.0 cannot receive a read command

j60870 1.8.0 (2026-07-17) validates every decoded ASDU and requires exactly
one set of information elements per object when SQ = 0. Its own decoder
produces zero sets for C_RD_NA_1, which has no information elements. The
result is an `IllegalArgumentException` in the connection reader and a closed
connection:

```text
java.lang.IllegalArgumentException: ASDU with SQ=0 must contain exactly one
information element set per information object. Invalid number of sets at
informationObjects[0]: 0
```

Effect: a 1.8.0 server drops the connection on any read command, and a
1.8.0 client drops it when a read is refused (the refusal is the mirrored
C_RD_NA_1). 1.7.2 does not have the check and handles both.

The adapter is therefore released on **1.7.2**; see
[above](#why-j60870-is-one-release-behind) for what that costs (nothing) and
how the latest release is tracked.

### Normalized values are scaled differently

lib60870 converts between the 16-bit NVA and its floating-point API with
`(raw + 0.5) / 32767.5`; j60870 uses `raw / 32768`. Passing 0.5 through both
gives 16383 on one wire and 16384 on the other.

The contract therefore exchanges normalized values as the raw 16-bit number
(fixture `value`, document `value`, `--value`), and the lib60870 adapter
searches for a float that its library encodes as exactly that number.
`normalized` in documents is always `value / 32768`.

### j60870 decodes an object without elements to no rows

Related to the first finding and present in 1.7.2 as well: a received
C_RD_NA_1 has an information object with an empty element table. The adapter
reports it as one object with only `ioa`, like lib60870 does.

### j60870 idle test cannot be disabled

`setMaxIdleTime` requires 1000 ms or more and `t3 >= t1`. The adapter rejects
`--t3` below `--t1` as a usage error rather than run with another value than
the one asked for.

### lib60870 cannot set a quality on a bitstring

`BitString32_create` takes no quality descriptor. The fixture format does not
allow one on `M_BO_NA_1` for any adapter.

## Platforms

| Platform | Status |
|----------|--------|
| linux/arm64 | Built and self-tested locally |
| linux/amd64 | Builds (checked locally under emulation with `make buildx`); self-tested by CI, of which no run is recorded yet |

Both adapters are plain C and Java with no architecture-specific code.

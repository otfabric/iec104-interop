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
| `wendy512` | [wendy512/iec104](https://github.com/wendy512/iec104) | v1.0.4 | v1.0.4 | Go module `github.com/wendy512/iec104@v1.0.4` (commit `8fb65c83865c94cf37a89d12c9c42dd8c1807cc4`), checksum in `go.sum` | independent, Go |
| | its engine [wendy512/go-iecp5](https://github.com/wendy512/go-iecp5) | v1.2.6 | v1.2.6 | Go module `github.com/wendy512/go-iecp5@v1.2.6` (commit `2269ee79a5e137847bbdc811be71a1064d303560`), checksum in `go.sum` | |

Everything else is on its latest release as of 2026-10-09: Debian 13
(trixie), Eclipse Temurin 25, Maven 3.9, Gson 2.14.0, Go 1.27.2.

wendy512/iec104 v1.0.4 asks for go-iecp5 v1.2.5; v1.2.6 is a later tag on
the same commit, so the adapter builds on the latest tag without a change in
code.

The pins live in the Dockerfiles, `adapters/openmuc/pom.xml` and
`adapters/wendy512/go.mod` and are mirrored in
[versions.yaml](../versions.yaml); `scripts/check-versions.sh` keeps them in
step. The **Upstream candidates** workflow
(`.github/workflows/candidate.yml`, weekly and on demand) resolves the latest
release of each stack and the tip of lib60870, builds the adapters on them,
runs the smoke and cross-stack self-tests and writes a pin-vs-latest decision
table to its job summary. It never changes a pin.

The tip of go-iecp5 is not probed: its default branch (commit `4499b147`,
2026-05-23) declares the module path `github.com/juanjorosendo/go-iecp5` in
its `go.mod`, so it cannot be built as `github.com/wendy512/go-iecp5`. Only
its release tags can be.

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
all nine pairings and requires, besides the expected outcome, that the
result documents of all pairings are identical apart from time tags. A case
that needs a feature a pairing does not declare is skipped for that pairing
and counted as such.

| Client → Server | lib60870 | openmuc | wendy512 |
|-----------------|:--------:|:-------:|:--------:|
| **lib60870** | pass | pass ¹ | pass ² |
| **openmuc** | pass | pass ¹ | pass ² |
| **wendy512** | pass ² | pass ¹ ² | pass ² |

¹ without file transfer: the openmuc server does not serve files.
² without time-tagged commands and file transfer: see the
[capabilities](CAPABILITIES.md#features) of wendy512.

Verified on linux/arm64 with the pins above on 2026-10-09: 39 cases, the
value checks and the session checks over 9 pairings; 643 checks passed, none
failed, 62 were skipped for a capability that is not declared.

Covered by every pairing: STARTDT/STOPDT; general interrogation (station, broadcast address,
unknown station, unsupported group); counter interrogation; read of a
status, a measurement, a counter and an unknown address; clock
synchronization; test command; single, double, regulating step and the
three set-point commands, with and without time tag, direct, select only,
select-and-execute and cancelled; refusals for an unknown object, an object
of another type, a state that is not permitted and an execute without the
required select; flow control with `k = w = 1` on both sides; an idle
connection kept alive by test frames; three sessions at once. (The commands
with time tag only where footnote 2 does not apply.)

Covered where declared: the download of a one-section and of a three-section
file with the content and the SHA-256 the fixture defines, and the refusal of
an unknown file address and of a wrong file name. Clients lib60870 and
openmuc, server lib60870.

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

### go-iecp5 drops types it has no length for

go-iecp5 v1.2.6 looks up the length of an information object by type
(`infoObjSize`) before it hands an ASDU to the application. The table has no
entry for the commands with CP56Time2a (C_SC_TA_1 .. C_SE_TC_1, types 58..63)
nor for F_SG_NA_1, and an ASDU of a type without entry is logged and dropped:

```text
asdu UnmarshalBinary failed,asdu: type identification unknown
```

Effect: a go-iecp5 station does not answer a time-tagged command at all (the
other stacks confirm it, and would answer a type they do not know with cause
44), and a go-iecp5 controlling station can send one but never sees the
confirmation. It cannot receive a file either. The adapter declares
`timeTaggedCommands`, `fileTransfer`, `fileServer` and `fileClient` false,
and [tests/cases.tsv](../tests/cases.tsv) has every time-tagged command case
a second time without time tag, so that the qualifier, the set points and
select-and-execute are still covered in every pairing.

### go-iecp5 handlers get a consumed ASDU

Decoding consumes the ASDU in go-iecp5 (`DecodeInfoObjAddr` and the `Get...`
methods cut what they read off the front). The server decodes a system
command before it calls the handler, so in the handlers for interrogation,
counter interrogation, read and clock synchronization the ASDU has already
lost its information object address. A mirror made from it with
`ASDU.Reply` or `ASDU.SendReplyMirror` announces one object and carries
none, or only the qualifier.

The adapter rebuilds the request from the handler's arguments and mirrors
that. For process commands, which reach the application undecoded, it clones
before it decodes.

### go-iecp5 refusals have no P/N bit

`ASDU.SendReplyMirror` changes the cause and nothing else, and the server's
own refusals (unknown cause, common address 0, a non-zero address in a
system command) use it: they go out with cause 44..47 and P/N = 0. The
adapter checks the common address and the cause itself and sets the bit;
only what go-iecp5 refuses before the handler runs is outside its reach, and
none of that is fixture behaviour. A consumer should not rely on the P/N bit
of a refusal with cause 44..47 from this stack, which is why the contract
counts those causes as negative whatever the bit says.

### go-iecp5 request and data helpers

The helpers that build an ASDU (`asdu.Single`, `asdu.InterrogationCmd`, ...)
always send originator address 0, and `asdu.IntegratedTotals` refuses cause
5, which the answer to a read of a counter needs. The adapter's station
builds its data ASDUs from the `Append...` primitives; its client keeps the
helpers and puts the originator in on the way out.

`asdu.TestCommandCP56Time2a` sends the fixed test word 0x55AA where
C_TS_TA_1 has a test sequence counter; the adapter builds that request
itself to send the counter of the contract.

### go-iecp5 does not report STARTDT and STOPDT

The server confirms STARTDT and STOPDT without a callback, so the adapter
has no `data-transfer-started` and `data-transfer-stopped` events
(`dataTransferEvents` false).

The client has a callback for STARTDT con and none for STOPDT con. It does
refuse to send while data transfer is stopped, and it checks that before it
encodes; the adapter asks it to send an ASDU that cannot be encoded and
reads from the error which state the connection is in. Nothing goes on the
wire for that.

### go-iecp5 client reconnects after a lost connection

`ClientOption.SetAutoReconnect(false)` only covers a failed dial. After an
established connection ends, the client dials again half a second to a
second later. The adapter closes the client in the connection-lost callback.

### go-iecp5 timing and windows

- A queued ASDU is sent when the connection loop next wakes up: on a
  received frame or on its 100 ms tick. A request and its answer therefore
  take up to 100 ms each on an otherwise idle connection; a client operation
  of this adapter takes about 0.2 s where the others take a few
  milliseconds.
- Read from the source, not asserted by a test: the send window is checked
  with `outstanding <= k`, so `k + 1` I frames can be unacknowledged.
- Every timer must be between 1 and 255 seconds (`t3` up to 48 hours), so
  the idle test cannot be switched off. The adapter rejects other values as
  a usage error.
- The connect timeout is `t0`; the adapter sets it from
  `--connect-timeout-ms`, raised to the minimum of one second.
- A time tag with the IV bit is decoded to the zero time; the adapter
  reports it as `timeInvalid` with that time.

### wendy512/iec104 and go-iecp5: which layer the adapter uses

wendy512/iec104 is a convenience layer over go-iecp5. Its server is used as
is. Its client is not: it has no select, qualifier, deactivation or STOPDT,
it opens and closes a TCP connection to test the address before the real
one, and it does not expose the engine underneath. The adapter's client
drives go-iecp5's `cs104.Client`, the same engine, directly.

The server does not report whether it could listen, and go-iecp5's client
does not report a failed dial, other than through the log; the adapter
watches the library log for those two messages.

## Platforms

| Platform | Status |
|----------|--------|
| linux/amd64 | Built and self-tested natively by CI and by the release workflow |
| linux/arm64 | Built and self-tested natively by CI and by the release workflow (arm64 runner); also locally |

The adapters are plain C, Java and Go with no architecture-specific code.

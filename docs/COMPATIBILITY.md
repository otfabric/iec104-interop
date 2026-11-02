# Compatibility

Which upstream versions the adapters are built on, what the reference stacks
were verified to do against each other, and what was found on the way.

Everything here is about the reference stacks. It makes no statement about
any library that consumes the images.

## Pinned upstreams

The rule is the latest upstream release, also when it has a bug: the bug is
listed in [tests/known-bugs.tsv](../tests/known-bugs.tsv) and described
below, and the cases it explains count as known bugs instead of failures.

| Adapter | Upstream | Pinned | Latest | Pin | Engine |
|---------|----------|--------|--------|-----|--------|
| `lib60870` | [MZ Automation lib60870-C](https://github.com/mz-automation/lib60870) | v2.4.1 | v2.4.1 | commit `7a388e3e133999e1ca77ba7521d55d074b7cd2bc` | independent, C |
| `openmuc` | [OpenMUC j60870](https://www.openmuc.org/iec-60870-5-104/) | 1.8.0 | 1.8.0 | Maven Central `org.openmuc:j60870:1.8.0` | independent, Java |
| `wendy512` | [wendy512/iec104](https://github.com/wendy512/iec104) | v1.0.4 | v1.0.4 | Go module `github.com/wendy512/iec104@v1.0.4` (commit `8fb65c83865c94cf37a89d12c9c42dd8c1807cc4`), checksum in `go.sum` | independent, Go |
| | its engine [wendy512/go-iecp5](https://github.com/wendy512/go-iecp5) | v1.2.6 | v1.2.6 | Go module `github.com/wendy512/go-iecp5@v1.2.6` (commit `2269ee79a5e137847bbdc811be71a1064d303560`), checksum in `go.sum` | |
| `cbsigma` | [cB-Sigma/iec60870](https://github.com/cB-Sigma/iec60870) | v0.3.0 | v0.3.0 | Go module `github.com/cB-Sigma/iec60870@v0.3.0` (commit `d0cad7ab6b8905d21dca12f4f7364e788c80ae17`), checksum in `go.sum` | independent, Go |

Everything else is on its latest release as of 2026-10-09: Debian 13
(trixie), Eclipse Temurin 25, Maven 3.9, Gson 2.14.0, Go 1.27.2. Mbed TLS,
compiled into lib60870-C for TLS, is pinned to 3.6.7, the latest of the 3.6 line
that lib60870-C v2.4.1 builds with, by the SHA-256 of its release tarball.

wendy512/iec104 v1.0.4 asks for go-iecp5 v1.2.5; v1.2.6 is a later tag on
the same commit, so the adapter builds on the latest tag without a change in
code.

The pins live in the Dockerfiles, `adapters/openmuc/pom.xml`,
`adapters/wendy512/go.mod` and `adapters/cbsigma/go.mod` and are mirrored in
[versions.yaml](../versions.yaml); `scripts/check-versions.sh` keeps them in
step. The **Upstream** workflow
(`.github/workflows/upstream.yml`, daily and on demand) resolves the latest
release of each stack and the tip of lib60870 (`scripts/latest-upstream.py`),
builds the adapters on those that are newer than the pin, runs the smoke and
cross-stack self-tests and writes a pin-vs-latest table to its job summary
and to the issue "Upstream releases: pins behind the latest". It never
changes a pin.

The tip of go-iecp5 is not probed: its default branch (commit `4499b147`,
2026-05-23) declares the module path `github.com/juanjorosendo/go-iecp5` in
its `go.mod`, so it cannot be built as `github.com/wendy512/go-iecp5`. Only
its release tags can be.

### j60870 1.8.0

1.8.0 (2026-07-17) cannot receive a read command (first finding below). The
adapter is on 1.8.0 anyway, with the bug listed in
[tests/known-bugs.tsv](../tests/known-bugs.tsv): the read cases of the
pairings with openmuc count as known bugs, and every other case must pass as
it did on 1.7.2. Until v0.3.0 of this repository the adapter was pinned to
1.7.2 for this bug. A fix is proposed upstream in
[gythialy/j60870#75](https://github.com/gythialy/j60870/pull/75).

What 1.8.0 changes over 1.7.2, and whether a test could use it:

| Change in 1.8.0 over 1.7.2 | Relevance to interoperability testing |
|----------------------------|----------------------------------------|
| Validation of decoded and constructed ASDUs (counts, SQ on commands, unknown cause) | Causes the read regression. Otherwise only affects malformed traffic, which the contract does not send |
| `ASdu.buildCotResponse` helper | API convenience; nothing new on the wire |
| Pluggable logging (`org.openmuc.j60870.logging`) | none |
| Allowed-client check compares addresses instead of strings | none here |
| Listener kept across repeated STOPDT/STARTDT on a client | none here: one cycle per client run |
| `C_TS_NA_1` test pattern byte order corrected (AA 55) | none: the type is not used by IEC 60870-5-104 or by the contract |

No type identification, service or transport option was added, so 1.8.0
brings no new coverage. The source difference is 447 added and 106 removed
lines, most of the rest being copyright years.

When a release decodes C_RD_NA_1 again, `make interop` fails with "known bug
did not reproduce", which is the signal to remove the entry.

## Cross-stack matrix

`make interop` runs every case of [tests/cases.tsv](../tests/cases.tsv) and
[tests/feature-cases.tsv](../tests/feature-cases.tsv) in all sixteen pairings and
requires, besides the expected outcome, that the result documents of all pairings
are identical apart from time tags and the fields
[listed below](#contract-13-and-the-stacks). A case that needs a feature a pairing
does not declare is skipped for that pairing and counted as such.

| Client → Server | lib60870 | openmuc | wendy512 | cbsigma |
|-----------------|:--------:|:-------:|:--------:|:-------:|
| **lib60870** | pass | pass ³ | pass ¹ | pass |
| **openmuc** | pass ³ | pass ³ | pass ¹ ³ | pass ³ |
| **wendy512** | pass ¹ | pass ¹ ³ | pass ¹ | pass ¹ |
| **cbsigma** | pass | pass ³ | pass ¹ | pass ² |

¹ without what wendy512 does not declare, as client or as station: time-tagged
commands, downloading, taking an upload, and TLS and redundancy as a station. See
the [capabilities](CAPABILITIES.md#features).
² and the cases of [tests/secure-cases.tsv](../tests/secure-cases.tsv): the
security flags, which only cbsigma has.
³ except read: a known bug of j60870 1.8.0 (see
[the finding](#j60870-180-cannot-receive-a-read-command)).

Verified on linux/amd64 and linux/arm64 with the pins above on 2026-11-02, by CI: 39
cases and 29 feature cases, the value checks and the session checks over 16 pairings,
and the 10 secure cases for cbsigma against itself; 2188 checks, of which 2153 passed
and 35 were the known read bug of j60870 1.8.0, none failed, and 262 were skipped for
a capability that is not declared. With the own image as a fifth stack, on linux/arm64
locally: 3596 checks, of which 3552 passed and 44 were that bug, none failed, 351 skipped.

Covered by every pairing: STARTDT/STOPDT; general interrogation (station, broadcast address,
unknown station, unsupported group); counter interrogation; read of a
status, a measurement, a counter and an unknown address (not with openmuc,
see footnote 4); clock
synchronization; test command; single, double, regulating step and the
three set-point commands, with and without time tag, direct, select only,
select-and-execute and cancelled; refusals for an unknown object, an object
of another type, a state that is not permitted and an execute without the
required select; flow control with `k = w = 1` on both sides; an idle
connection kept alive by test frames; three sessions at once. (The commands
with time tag only where footnote 2 does not apply.)

Covered where declared: the download of a one-section and of a three-section
file with the content and the SHA-256 the fixture defines, and the refusal of
an unknown file address and of a wrong file name. Interrogation groups and a
quality on a bitstring; mutual TLS, and its refusal of a certificate from another
authority, either way, and of a station without TLS; the upload of three sizes with
the event, a read-back and the directory afterwards, and four refusals; the directory;
a redundancy switchover by STOPDT and by takeover.


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

The adapter is released on 1.8.0 with this bug listed as known; see
[above](#j60870-180).

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

### lib60870: TLS, files and redundancy in v2.4.1

- **`BitString32_create` takes no quality descriptor.** `BitString32_createEx` does;
  the fixture `extended` has a quality on `M_BO_NA_1` and the adapter uses it.
- **Minimum key length.** The default of `TLSConfiguration` is 1024 bits, which
  refuses P-256 keys; the adapter sets 256. The self pairing negotiates TLS 1.3, and
  TLS 1.2 was checked against OpenSSL.
- **Handshakes have no timeout.** The client's in `TLSSocket_create`, which the
  adapter wraps with `--wrap` to keep the contract's bound; the server's runs in
  the accept thread (`cs104_slave.c:4026`), so a silent client holds up other
  connections.
- **Takeover closes the old connection** (`cs104_slave.c:1683-1703`) without
  a deactivation callback.
- **The file server** swallows a directory call (`file_server.c:851-856`), so
  a plugin in front of it answers; it does not check an upload's checksum
  (`file_server.c:439`, `:466`), which the adapter's plugin does; a whole transfer has a timeout of 3000 ms (`:924`).
- **The client delivers I frames regardless of its state**, also after STOPDT.

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
`timeTaggedCommands`, `fileTransfer`, `fileClient` and `fileUploadServer` false,
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

### cB-Sigma/iec60870 and the other stacks

What the cbsigma adapter met, and how it deals with it:

- **The NOS of the last section differs.** At the end of a file
  (F_LS_NA_1, LSQ 1) lib60870 sends one more than the number of sections,
  cB-Sigma the number of the last section. Every client accepts both. The
  standard's text is not available to this project, so neither is called
  wrong: [FIXTURES.md](FIXTURES.md) leaves the field to the library and the
  agreement check of the self-tests does not compare it.
- **Segments of 200 octets.** cB-Sigma's file server cuts sections into
  segments of 200 octets by default; the fixture format says 236, which the
  adapter sets.
- **Deactivation after a refusal.** cB-Sigma's client sends a file
  deactivation (F_SC_NA_1, SCQ 3) after any failed download, a refused
  selection included. The station answers a deactivation with nothing,
  whatever file it names; when the adapter answered one for an unknown file
  with a refusal, whether the client saw it depended on timing.
- **No STOPDT notification.** The library calls back when STARTDT is
  confirmed but not when STOPDT is; the adapter watches the link's state
  every 2 ms to emit `data-transfer-stopped`.
- **Timers of 0 are defaults.** A timer of 0 in `cs104.LinkConfig` means
  "use the default", so the idle test cannot be switched off: `--t3 0` is a
  usage error.
- **Normalized values** are carried as `raw / 32768`, as the contract's
  `normalized` field is.
- **Pins are for self-signed certificates.** A pinned certificate is
  accepted only when it is self-signed; a certificate an authority signed is
  checked against the authorities, pinned or not. A certificate with
  CA:TRUE in `--security-trust` is an authority, so a pin must not be a CA
  certificate (openssl's `req -x509` makes one unless told otherwise).
- **A secure station drops what is not secured.** With `--secure` a client
  without security gets its STARTDT confirmed and then no answer at all.
- Read from the source, not asserted by a test: cB-Sigma maps F_AF_NA_1,
  the acknowledgement of a download, to the FileWrite permission, so with
  `--roles viewer` a download is likely refused at its end. The secure
  cases do not download files.

### Contract 1.3 and the stacks

What the adapters met when they got groups, TLS, upload, directory and
redundancy, none of it a failure of a comparison.

- **j60870 1.8.0.** No listener is called after `stopDataTransfer`; a server in
  STOPDT closes on an I or S frame (`Connection.java:341-345`); the TLS handshake
  happens on first use, so the adapter forces it; `needClientAuth` takes a wrapper
  around the socket factory; the STARTDT confirmation is buffered (`:388`) before
  its callback (`:393`) and flush (`:395`), so an adapter must not send from there.
- **go-iecp5 v1.2.6.** The random `select` in `handlerLoop` loses the last ASDU
  when the peer closes (`cs104/server_session.go:336-339`), so the adapter reads
  a file's last acknowledgement from the `RX Raw` log line. I frames received in
  STOPDT are dropped (`cs104/client.go:372-376`). `asdu/filet.go` is a TODO.
  A takeover's strays are counted from the STARTDT act.
  The TCP connect and the TLS handshake of its client are one call under t0 that says
  nothing until both are over; the adapter learns that TCP is up from a
  `tls.Config.ClientSessionCache` whose `Get` always misses, so the ClientHello
  advertises session tickets that are never used.
- **cB-Sigma/iec60870 v0.3.0.** `cs104.Group` closes stopped connections before the
  STARTDT confirmation (`group.go:123-131`) and resends answers on reclaim
  (`:140-157`), neither of which fits the contract, so the adapter drives
  `cs104.Accept` with a group of its own. `FileServer` accepts any F_FR.
  `PutFile` has a fixed section size and sends a deactivation after a refusal. A
  client link closes on a stray with `ErrFrame`; a TLS connection counts as opened after
  the handshake. A negative F_AF_NA_1 carries the error in the high four bits
  (0x24, 0x22), which the contract allows.
- **F_AF_NA_1 and F_DR_TA_1 fields.** NOS of the file acknowledgement differs
  between stacks, and the sequence and time-invalid bits of F_DR_TA_1 are not
  the same either, so the agreement check does not compare those (`NORMALIZE` in
  `scripts/lib.sh`).
- **Takeover.** What happens to the connection taken over is left to the stack and not
  compared. So is the error code of a client that does not reach a station
  over TLS: every case checks no STARTDT confirmation and no ASDU instead.

## Platforms

| Platform | Status |
|----------|--------|
| linux/amd64 | Built and self-tested natively by CI and by the release workflow |
| linux/arm64 | Built and self-tested natively by CI and by the release workflow (arm64 runner); also locally |

The adapters are plain C, Java and Go with no architecture-specific code.

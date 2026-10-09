# Container contract

Version: **1.1** (`contract_version` in [versions.yaml](../versions.yaml))

Every adapter image exposes the same commands, flags, JSON documents and exit
codes. Consumers depend on this document, not on any adapter's source. A
change that breaks it bumps the major version; an addition bumps the minor
one. Documents carry `schemaVersion` `"1.0"`: it names the compatible line
and changes only with the major version.

| Version | Change |
|---------|--------|
| 1.1 | The `file-get` operation, its `file` result and the file transfer ASDU documents; the `file-transfer` server event; `files` in fixtures; the features `fileServer`, `fileClient` and `dataTransferEvents` |
| 1.0 | First release |

Not every adapter has every operation or event. What an image lacks it says
in [`print-capabilities`](#print-capabilities), and an operation it does not
list in `clientOperations` is a usage error (exit 2).

What a *station* does with a request is defined by its fixture: see
[FIXTURES.md](FIXTURES.md).

## Contents

- [Image layout](#image-layout)
- [Commands](#commands)
- [`server`](#server)
- [`client`](#client)
- [ASDU documents](#asdu-documents)
- [`print-capabilities`](#print-capabilities)
- [`print-fixture`](#print-fixture)
- [Exit codes](#exit-codes)
- [Output rules](#output-rules)

## Image layout

| Item | Value |
|------|-------|
| Image | `ghcr.io/otfabric/iec104-interop-<adapter>` |
| Entrypoint | `iec104-interop` (`/usr/local/bin/iec104-interop`) |
| Default command | `server` |
| Port | 2404/tcp |
| User | non-root, uid 10001 |
| Fixtures | `/fixtures/<name>/fixture.json` (baked in; mount over `/fixtures` to replace) |
| Readiness file | `/run/iec104-interop/ready` |
| Health check | built in: `test -f /run/iec104-interop/ready` |
| Licences | `/licenses/` |

## Commands

```text
iec104-interop server [flags]
iec104-interop client <operation> --host HOST [flags]
iec104-interop print-capabilities
iec104-interop print-fixture [NAME]
iec104-interop version
```

Flags are `--name value`; switches are bare `--name`. An unknown flag is a
usage error (exit 2), never ignored.

### APCI flags

Accepted by `server` and by every `client` operation. Times are in seconds.

| Flag | Default | Meaning |
|------|---------|---------|
| `--k N` | 12 | Maximum unacknowledged I frames sent |
| `--w N` | 8 | Acknowledge after this many received I frames |
| `--t0 S` | 30 | Connection establishment timeout |
| `--t1 S` | 15 | Acknowledgement / confirmation timeout |
| `--t2 S` | 10 | Acknowledgement delay, `t2 < t1` |
| `--t3 S` | 20 | Idle time before TESTFR |

`1 <= w <= k <= 32767`, `0 < t2 < t1`. Whether `--t3 0` (idle test off) is
accepted is adapter specific: lib60870 accepts it, OpenMUC requires
`t3 >= t1`, wendy512 requires `t3 >= 1` and every timer at most 255 seconds.
A value the library cannot run with is a usage error, never replaced by
another one. See [CAPABILITIES.md](CAPABILITIES.md).

## `server`

Runs a controlled station that serves one fixture until it receives SIGTERM
or SIGINT.

| Flag | Default | Meaning |
|------|---------|---------|
| `--fixture PATH` | `/fixtures/baseline/fixture.json` | Fixture to serve |
| `--bind-address ADDR` | `0.0.0.0` | Listen address |
| `--port N` | 2404 | Listen port |
| `--ready-file PATH` | `/run/iec104-interop/ready` | Readiness file |
| APCI flags | | |

Lifecycle:

1. The fixture is loaded and validated. An invalid fixture ends the process
   with exit 4 before anything listens.
2. The listener is opened. Failure ends the process with a non-zero exit.
3. The readiness file is written and the `ready` event is printed. From here
   on the station accepts IEC 60870-5-104 connections. **Readiness is the
   file (or the event), not the open port and not a delay.**
4. On SIGTERM/SIGINT the readiness file is removed, connections are closed,
   the `stopped` event is printed and the process exits 0.

Every connection is an independent session with its own STARTDT/STOPDT state.
Up to 16 simultaneous connections are accepted. The station state (point
values, selections) is shared by all connections and lives as long as the
process: restart the container to return to the fixture.

### Server events

One JSON object per line on stdout.

| `event` | Other fields | When |
|---------|--------------|------|
| `ready` | `adapter`, `address`, `fixture`, `commonAddress` | Once, when the station accepts connections |
| `connection-opened` | `peer` (`ip:port`) | A controlling station connected |
| `data-transfer-started` | `peer` | STARTDT confirmed. Only with the feature `dataTransferEvents` |
| `data-transfer-stopped` | `peer` | STOPDT confirmed. Only with the feature `dataTransferEvents` |
| `connection-closed` | `peer` | The connection ended |
| `interrogation` | `commonAddress`, `qoi`, `accepted` | C_IC_NA_1 received |
| `counter-interrogation` | `commonAddress`, `qcc`, `accepted` | C_CI_NA_1 received |
| `read` | `ioa`, `accepted` | C_RD_NA_1 received |
| `clock-sync` | `time` | C_CS_NA_1 received |
| `command` | `type`, `ioa`, `cot`, `value`, `select`, `outcome` | A process command for the station was received |
| `file-transfer` | `ioa`, `name`, `success` | A file transfer ended: acknowledged by the controlling station (`success` true) or not. Only with the feature `fileServer` |
| `stopped` | | Once, after a clean stop |

`command.type` is the type identification on the wire (`C_SC_TA_1` for a
time-tagged single command). `command.outcome` is one of `executed`,
`selected`, `deactivated`, `rejected` (negative confirmation) and
`unknown-ioa` (no `value`/`select` in that case). `command.value` has the
representation of [ASDU documents](#asdu-documents).

Requests that are refused before they reach the station model (unknown
common address, unknown type, invalid cause) produce no event.

The order of events of one connection follows the protocol. No event is
emitted for test frames, acknowledgements or the test command.

## `client`

Runs one bounded operation as a controlling station and prints one result
document.

```text
iec104-interop client <operation> --host HOST [--port N] [common flags] [operation flags]
```

Every operation: connect, send STARTDT act and wait for the confirmation, run
the operation, close the connection.

### Common flags

| Flag | Default | Meaning |
|------|---------|---------|
| `--host HOST` | required | Station to connect to |
| `--port N` | 2404 | |
| `--common-address N` | 1 | Common address of the requests |
| `--originator-address N` | 0 | Originator address of the requests |
| `--connect-timeout-ms N` | 5000 | TCP connect timeout |
| `--timeout-ms N` | 5000 | Timeout of each wait for the station (STARTDT con, confirmation, termination, read answer) |
| `--collect-ms N` | 0 | After a successful operation, keep receiving for this long before closing |
| APCI flags | | |

### Operations

| Operation | Flags | Sends | Succeeds when |
|-----------|-------|-------|---------------|
| `connect` | `--hold-ms N` (0) | STARTDT act; after the hold, STOPDT act | STOPDT con is received. During the hold the connection is idle, so `t3` and TESTFR run |
| `interrogate` | `--qoi N` (20) | C_IC_NA_1 activation | Positive activation confirmation, then activation termination |
| `counter-interrogate` | `--qcc N` (5) | C_CI_NA_1 activation; `qcc` is the raw qualifier octet | Positive activation confirmation, then activation termination |
| `read` | `--ioa N` (required) | C_RD_NA_1 | An ASDU with cause 5 (request) containing the address arrives |
| `clock-sync` | `--time T` (now) | C_CS_NA_1 activation; `T` like `2026-01-02T03:04:05.678Z` | Positive activation confirmation |
| `test-command` | | C_TS_TA_1 activation | Positive activation confirmation |
| `command` | see below | A process command | See below |
| `monitor` | `--duration-ms N` (1000), `--max-asdus N` (0: no limit) | Nothing | The duration passed or the number of ASDUs was received, with the connection still up |
| `file-get` | `--ioa N` (required), `--name N` (1) | The file transfer procedure in monitor direction: see [below](#file-get) | The whole file arrived, every checksum and length matched and the file was acknowledged. Only with the feature `fileClient` |

`command` flags:

| Flag | Default | Meaning |
|------|---------|---------|
| `--type T` | required | `C_SC_NA_1`, `C_DC_NA_1`, `C_RC_NA_1`, `C_SE_NA_1`, `C_SE_NB_1` or `C_SE_NC_1` |
| `--ioa N` | required | Information object address |
| `--value V` | required | `C_SC`: `true`/`false` (also `1`/`0`, `on`/`off`). `C_DC`, `C_RC`: the two-bit state 0..3. `C_SE_NA`: raw NVA -32768..32767. `C_SE_NB`: -32768..32767. `C_SE_NC`: a decimal number |
| `--mode M` | `direct` | See below |
| `--qualifier N` | 0 | QU of `C_SC`/`C_DC`/`C_RC`, QL of the set points |
| `--with-time` | off | Send the CP56Time2a variant (`C_SC_TA_1`, ...) with the current time |

| Mode | Sequence | Succeeds when |
|------|----------|---------------|
| `direct` | execute | Positive confirmation, then activation termination |
| `select` | select | Positive confirmation |
| `sbo` | select, wait for its confirmation, execute | Both confirmations positive, then activation termination |
| `cancel` | select, wait for its confirmation, deactivate | Both confirmations positive (causes 7 and 9) |

A value that is syntactically valid but not permitted by the protocol (a
double command state of 0 or 3) is sent as is: refusing it is the station's
job.

### `file-get`

Downloads one file with the procedure of IEC 60870-5-101 7.4.11. Every ASDU
has cause 13 (file transfer); `--ioa` is the address of the file and `--name`
its name of file.

| Step | Controlling station | Controlled station |
|-----:|---------------------|--------------------|
| 1 | F_SC_NA_1, SCQ 1 (select file) | F_FR_NA_1 (file ready) with the length of the file |
| 2 | F_SC_NA_1, SCQ 2 (request file) | F_SR_NA_1 (section ready) with the length of section 1 |
| 3 | F_SC_NA_1, SCQ 6 (request section) | F_SG_NA_1 segments, then F_LS_NA_1 with LSQ 3 (last segment) and the checksum of the section |
| 4 | F_AF_NA_1, AFQ 3 (section acknowledged) | F_SR_NA_1 for the next section: back to step 3. After the last section F_LS_NA_1 with LSQ 1 (last section) and the checksum of the file |
| 5 | F_AF_NA_1, AFQ 1 (file acknowledged) | |

A checksum is the sum of the octets modulo 256. The client checks the
checksum and the length of every section and of the file and answers a
mismatch with AFQ 4 or 2 (not acknowledged) and the error `failed`.

A station that refuses the selection (unknown address, wrong name) answers
with the mirrored F_SC_NA_1 and cause 47, or with a file ready whose FRQ has
the negative bit: the error is `negative-confirmation`. The refusal is in
`asdus`; `confirmations` stays empty, since it is not a mirror with cause 7
or 9.

### Result document

Exactly one JSON object on one line of stdout, for every invocation that got
past argument parsing, including failures.

| Field | Type | Meaning |
|-------|------|---------|
| `schemaVersion` | string | `"1.0"` |
| `adapter` | string | Adapter that ran the operation |
| `operation` | string | As given on the command line |
| `ok` | boolean | The operation succeeded as defined above |
| `error` | object or null | `{ "code", "message" }` when `ok` is false |
| `connected` | boolean | The TCP connection was established |
| `startdtConfirmed` | boolean | STARTDT con was received |
| `stopdtConfirmed` | boolean | `connect` only: STOPDT con was received |
| `confirmations` | array | `{ "cot", "negative" }` for each mirror of the request that confirms or refuses it, in order |
| `terminated` | boolean | An activation termination of the request was received |
| `file` | object | `file-get` only: `ioa`, `name`, `length` (announced by file ready), `received` (octets that arrived), `sections` (sections called) and `sha256` (lower-case hex of what arrived) |
| `asdus` | array | Every ASDU received, in order: see [ASDU documents](#asdu-documents) |
| `elapsedMs` | number | Wall-clock duration |

A mirror counts as a confirmation when it has the type of the request and
cause 7, 9 or 44..47. `negative` is true when the P/N bit is set or the cause
is 44..47.

`error.code`:

| Code | Meaning | Exit |
|------|---------|------|
| `connect-failed` | No TCP connection | 3 |
| `startdt-timeout` | No STARTDT con within `--timeout-ms` | 1 |
| `stopdt-timeout` | `connect`: no STOPDT con | 1 |
| `timeout` | The station did not answer in time | 1 |
| `negative-confirmation` | The station refused: negative confirmation or cause 44..47 | 1 |
| `connection-lost` | The connection closed during the operation | 1 |
| `failed` | Anything else; see `message` | 1 |

`message` is for people and not part of the contract.

## ASDU documents

```json
{
  "type": "M_ME_TF_1", "typeId": 36,
  "cot": 11, "negative": false, "test": false,
  "originator": 0, "commonAddress": 1,
  "sequence": false, "count": 1,
  "objects": [ { "ioa": 202, "value": 49.5, "quality": [], "time": "2026-10-09T15:35:12.345Z" } ]
}
```

| Field | Meaning |
|-------|---------|
| `type` | Mnemonic of the type identification, or `null` for a type the adapter does not model |
| `typeId` | Type identification as a number |
| `cot` | Cause of transmission (6 bits) |
| `negative`, `test` | P/N and T bits |
| `originator`, `commonAddress` | |
| `sequence` | SQ bit |
| `count` | Number of objects or elements announced |
| `objects` | One entry per information object; with SQ = 1 one entry per element, addresses expanded. Empty when `type` is `null` |

Every object has `ioa`. The other fields depend on the type; a type with
CP56Time2a adds `time`, and `timeInvalid: true` when the IV bit of the time
tag is set.

| Types | Fields |
|-------|--------|
| `M_SP_NA_1`, `M_SP_TB_1` | `value` boolean, `quality` |
| `M_DP_NA_1`, `M_DP_TB_1` | `value` 0..3, `quality` |
| `M_ST_NA_1`, `M_ST_TB_1` | `value` -64..63, `transient` boolean, `quality` |
| `M_BO_NA_1`, `M_BO_TB_1` | `value` 0..4294967295, `quality` |
| `M_ME_NA_1`, `M_ME_TD_1` | `value` raw NVA -32768..32767, `normalized` = `value` / 32768, `quality` |
| `M_ME_NB_1`, `M_ME_TE_1` | `value` -32768..32767, `quality` |
| `M_ME_NC_1`, `M_ME_TF_1` | `value` number (IEEE 754 single precision), `quality` |
| `M_IT_NA_1`, `M_IT_TB_1` | `value` 32-bit signed, `sequence` 0..31, `quality` |
| `C_SC_NA_1`, `C_SC_TA_1` | `value` boolean, `select`, `qualifier` |
| `C_DC_NA_1`, `C_DC_TA_1`, `C_RC_NA_1`, `C_RC_TA_1` | `value` 0..3, `select`, `qualifier` |
| `C_SE_NA_1`, `C_SE_TA_1` | `value` raw NVA, `normalized`, `select`, `qualifier` |
| `C_SE_NB_1`, `C_SE_TB_1` | `value`, `select`, `qualifier` |
| `C_SE_NC_1`, `C_SE_TC_1` | `value` number, `select`, `qualifier` |
| `C_IC_NA_1` | `qoi` |
| `C_CI_NA_1` | `qcc` raw qualifier octet |
| `C_RD_NA_1` | |
| `C_CS_NA_1` | `time` |
| `C_TS_TA_1` | `counter`, `time` |
| `M_EI_NA_1` | `coi` |
| `F_FR_NA_1` | `nof`, `lof`, `frq` |
| `F_SR_NA_1` | `nof`, `nos`, `lof`, `srq` |
| `F_SC_NA_1` | `nof`, `nos`, `scq` |
| `F_LS_NA_1` | `nof`, `nos`, `lsq`, `chs` |
| `F_AF_NA_1` | `nof`, `nos`, `afq` |
| `F_SG_NA_1` | `nof`, `nos`, `data` (the segment, lower-case hex) |

`nof` and `nos` are the name of file and of section, `lof` the length of the
file or section; the qualifiers (`frq`, `srq`, `scq`, `lsq`, `afq`) are the
raw octets and `chs` is the checksum.

An adapter reports what its library hands it. A library that drops a type on
reception leaves no trace of it in `asdus`: wendy512 cannot receive the
CP56Time2a command types or F_SG_NA_1 and declares so in its capabilities.

`quality` is an array of flag abbreviations in this order: `OV`, `BL`, `SB`,
`NT`, `IV`; for integrated totals `CY`, `CA`, `IV`. An empty array means
good.

`time` is UTC, `YYYY-MM-DDTHH:MM:SS.mmmZ`. Adapters put UTC on the wire.

Normalized values are exchanged as the raw 16-bit number because the
libraries disagree on the scaling of the floating-point form (division by
32768 or by 32767.5). `normalized` is a convenience and always `value / 32768`.

Numbers are JSON numbers; a single-precision value is printed with the digits
of its double-precision conversion, so compare numerically.

## `print-capabilities`

Prints one JSON object on one line and exits 0. See
[CAPABILITIES.md](CAPABILITIES.md) for the fields and the values of each
adapter. Needs no network and no fixture.

## `print-fixture`

`print-fixture [NAME]` (default `baseline`) writes the fixture baked into the
image to stdout, byte for byte, and exits 0. An unknown name exits 1.

A consumer reads the expected values from the image it tests against, so it
needs no checkout of this repository and cannot drift from the image.

## Exit codes

| Code | Meaning |
|------|---------|
| 0 | Success. `server`: clean stop after SIGTERM/SIGINT |
| 1 | The operation failed (see `error.code`); `server`: could not listen |
| 2 | Usage error: unknown command, operation or flag, missing or invalid value. Nothing was sent |
| 3 | `client`: the connection could not be established |
| 4 | `server`: the fixture is missing or invalid |

## Output rules

- **stdout carries JSON only**: server events as JSON Lines, one client
  result, one capability document. `print-fixture` and `version` are the
  exceptions and print their own format.
- **stderr carries diagnostics** for people. Nothing on stderr is part of the
  contract. Library logging goes there too.
- Field names and exit codes are stable within a major contract version.
  Fields may be added in a minor version; consumers ignore fields they do not
  know.
- Every wait is bounded: by `--timeout-ms`, `--connect-timeout-ms`, the
  duration flags or the APCI timers. No client operation runs indefinitely.

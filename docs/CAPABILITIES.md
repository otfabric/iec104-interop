# Capabilities

`print-capabilities` tells a consumer what an adapter image can do, so that a
test suite can skip what a stack does not support instead of failing on it.

A capability is declared `true` only when the self-tests of this repository
exercise it for that adapter. An upstream library having an API for something
is not enough.

## Document

One JSON object on one line ([schema](../schemas/capabilities.schema.json)):

```json
{
  "schemaVersion": "1.0",
  "adapter": "lib60870",
  "adapterVersion": "v0.1.0",
  "protocol": "iec60870-5-104",
  "upstream": {
    "name": "lib60870-C",
    "version": "v2.4.1",
    "revision": "7a388e3e133999e1ca77ba7521d55d074b7cd2bc",
    "license": "GPL-3.0",
    "language": "C",
    "independentEngine": true
  },
  "roles": { "server": true, "client": true },
  "features": { "generalInterrogation": true, "...": false },
  "pointTypes": ["M_SP_NA_1", "..."],
  "commandTypes": ["C_SC_NA_1", "..."],
  "clientOperations": ["connect", "..."]
}
```

| Field | Meaning |
|-------|---------|
| `adapter` | Adapter name; the image is `iec104-interop-<adapter>` |
| `adapterVersion` | Release of this repository the image was built from (`dev` for a local build) |
| `upstream` | The implementation inside: name, exact version and revision, licence, language |
| `upstream.engine` | Optional: the protocol engine (`name`, `version`, `license`) when the upstream is a layer over another project, as wendy512/iec104 is over go-iecp5 |
| `upstream.independentEngine` | `false` when the adapter wraps the protocol engine of another adapter, or is OT Fabric's own stack (`own/`). Results from such an adapter do not count as an additional independent implementation |
| `roles` | Which of `server` and `client` the image provides |
| `features` | See below; every value is a boolean |
| `pointTypes` | Point types a fixture may use with this adapter |
| `commandTypes` | Command types a fixture may use; the CP56Time2a variant of each is accepted too when `timeTaggedCommands` is true |
| `clientOperations` | Operations of `client` |

## Features

| Feature | Meaning | lib60870 | openmuc | wendy512 | cbsigma |
|---------|---------|:--------:|:-------:|:--------:|:-------:|
| `generalInterrogation` | Station interrogation (QOI 20), both roles | yes | yes | yes | yes |
| `groupInterrogation` | Interrogation groups 1..16 (QOI 21..36): a station answers the groups of its fixture and refuses the others | yes | yes | yes | yes |
| `counterInterrogation` | General counter request, read | yes | yes | yes | yes |
| `read` | Read command, including the refusal of an unknown address | yes | yes | yes | yes |
| `clockSync` | Clock synchronization command | yes | yes | yes | yes |
| `testCommand` | Test command with time tag (C_TS_TA_1) | yes | yes | yes | yes |
| `directCommands` | Execute without select | yes | yes | yes | yes |
| `selectBeforeOperate` | Select, execute, and `selectRequired` objects | yes | yes | yes | yes |
| `commandDeactivation` | Cancelling a selection (cause 8) | yes | yes | yes | yes |
| `timeTaggedCommands` | The CP56Time2a command types, as station and as controlling station | yes | yes | no ² | yes |
| `spontaneousOnCommand` | The time-tagged report after an executed command | yes | yes | yes | yes |
| `apciParameters` | The `--k --w --t0..--t3` flags | yes | yes ¹ | yes ¹ | yes ¹ |
| `multipleConnections` | Independent simultaneous sessions | yes | yes | yes | yes |
| `pointQualityOnBitstring` | A quality descriptor other than good on `M_BO_NA_1` | yes | yes | yes | yes |
| `tls` | IEC 62351-3 transport security in both roles: `tlsServer` and `tlsClient` | yes | yes | no ³ | yes |
| `tlsServer` | The [TLS flags](CONTAINER_CONTRACT.md#transport-security-flags) on `server` | yes | yes | no ³ | yes |
| `tlsClient` | The TLS flags on `client` | yes | yes | yes | yes |
| `dataTransferEvents` | The server events `data-transfer-started` and `data-transfer-stopped` | yes | yes | no ⁴ | yes ⁵ |
| `fileTransfer` | The file transfer types 120..125 in ASDU documents | yes | yes | no ⁶ | yes |
| `fileServer` | The station serves the `files` of a fixture | yes | yes ⁷ | yes ⁷ | yes |
| `fileClient` | The `file-get` operation | yes | yes ⁷ | no ⁶ | yes |
| `fileUploadServer` | The station takes the `uploads` of a fixture | yes | yes ⁷ | no ⁶ | yes |
| `fileUploadClient` | The `file-put` operation | yes | yes ⁷ | yes ⁷ | yes |
| `fileDirectoryServer` | The station answers a directory call with F_DR_TA_1 | yes | yes ⁷ | yes ⁷ | yes |
| `fileDirectoryClient` | The `file-dir` operation | yes | yes ⁷ | yes ⁷ | yes |
| `redundancyServer` | `server --redundancy-group`: one group of connections, one started | yes | yes | no ⁸ | yes |
| `redundancyClient` | The `redundancy` operation | yes | yes | yes | yes |
| `secureAuthentication` | The [security flags](CONTAINER_CONTRACT.md#security-flags): the A-Profile of IEC 62351-5, as station and as controlling station | no | no | no | yes ⁹ |

¹ j60870 cannot switch the idle test off and requires `t3 >= t1`; go-iecp5
requires every timer between 1 and 255 seconds; cB-Sigma/iec60870 takes a
timer of 0 for its default, so it cannot switch the idle test off either.
`--t3 0` is a usage error for these three adapters.

² go-iecp5 v1.2.6 encodes C_SC_TA_1 .. C_SE_TC_1 but has no length for them, so it
drops them on reception: a station never sees the command and a controlling
station never sees its confirmation.

³ go-iecp5 v1.2.6 has no station over TLS. `cs104/server.go:71` calls
`net.Listen("tcp", ...)`; the `TLSConfig` field (`server.go:28`) is never read.
`SrvSession` has unexported fields and an unexported `run`
(`server_session.go:27-58`, `:157`), so an adapter cannot hand it a TLS
connection, and `NewServerSpecial` dials out instead of listening
(`server_special.go:100`). wendy512/iec104 v1.0.4 (`server/core.go`) adds nothing.
Its client does TLS (`ClientOption.SetTLSConfig`), so `tlsClient` is true.

⁴ go-iecp5 v1.2.6 answers STARTDT and STOPDT without telling the application
(`cs104/server_session.go:303-311`).

⁵ cB-Sigma/iec60870 tells the application when STARTDT is confirmed, but
not when STOPDT is: the adapter watches the state of the link and emits
`data-transfer-stopped` when it changes.

⁶ go-iecp5 v1.2.6 cannot receive F_SG_NA_1: `asdu/identifier.go:167-233` has no
entry for it, `fixInfoObjSize` in `asdu/asdu.go` fails in `UnmarshalBinary`, and
the ASDU is dropped (`cs104/client.go:433`, `cs104/server_session.go:341`). So
a controlling station cannot download and a station cannot take an upload.
What it needs to receive for the other direction it can, which is why those
are true.

⁷ The upstream has the information elements but no procedure: j60870 1.8.0 and
go-iecp5 v1.2.6 leave it to the application, so the adapter runs it
(`FileStation.java`, `files.go`). j60870's own `sendDirectory` cannot be used:
it sends cause 13 (`Connection.java:1541`) and writes the elements of
`ASdu.java:533-543`, so the adapter builds the F_DR_TA_1 itself.

⁸ go-iecp5 v1.2.6 keeps the started state in a local variable of `run()`
(`cs104/server_session.go:170`, `:305`, `:311`) with no callback and queues in
`Send()` regardless (`:531`), so a group of connections cannot follow which is
started. The client only needs STARTDT and STOPDT on two connections, so
`redundancyClient` is true.

⁹ The adapter is the only one with the A-Profile, so the secure cases of the
self-tests run cB-Sigma against itself; their value is for consumers that
pair it with another implementation. The flags give a station one
certificate and one set of trusted authorities and pins, so the association
is authenticated with certificates only.

OT Fabric's own image (`own/go-iec104`, not a reference) declares every feature
in this table.

A capability is a property of the adapter and of the upstream version in the
image. The self-tests read it from the image and skip what a pairing lacks
(the `needs` column of [tests/cases.tsv](../tests/cases.tsv) and
[tests/feature-cases.tsv](../tests/feature-cases.tsv)), so "no" here
means "not tested", never "tested and failing".

## Rules for consumers

- Read the capabilities from the image under test, not from this page.
- Skip, do not fail, a scenario that needs a feature the adapter declares
  `false`.
- Count independent implementations by `upstream.independentEngine`.

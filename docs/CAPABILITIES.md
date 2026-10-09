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
| `upstream.independentEngine` | `false` when the adapter wraps the protocol engine of another adapter. Results from such an adapter do not count as an additional independent implementation |
| `roles` | Which of `server` and `client` the image provides |
| `features` | See below; every value is a boolean |
| `pointTypes` | Point types a fixture may use with this adapter |
| `commandTypes` | Command types a fixture may use; the CP56Time2a variant of each is accepted too when `timeTaggedCommands` is true |
| `clientOperations` | Operations of `client` |

## Features

| Feature | Meaning | lib60870 | openmuc | wendy512 |
|---------|---------|:--------:|:-------:|:--------:|
| `generalInterrogation` | Station interrogation (QOI 20), both roles | yes | yes | yes |
| `groupInterrogation` | Interrogation groups 1..16 (the station refuses them) | no | no | no |
| `counterInterrogation` | General counter request, read | yes | yes | yes |
| `read` | Read command, including the refusal of an unknown address | yes | yes | yes |
| `clockSync` | Clock synchronization command | yes | yes | yes |
| `testCommand` | Test command with time tag (C_TS_TA_1) | yes | yes | yes |
| `directCommands` | Execute without select | yes | yes | yes |
| `selectBeforeOperate` | Select, execute, and `selectRequired` objects | yes | yes | yes |
| `commandDeactivation` | Cancelling a selection (cause 8) | yes | yes | yes |
| `timeTaggedCommands` | The CP56Time2a command types, as station and as controlling station | yes | yes | no ³ |
| `spontaneousOnCommand` | The time-tagged report after an executed command | yes | yes | yes |
| `apciParameters` | The `--k --w --t0..--t3` flags | yes | yes ¹ | yes ¹ |
| `multipleConnections` | Independent simultaneous sessions | yes | yes | yes |
| `pointQualityOnBitstring` | A quality descriptor other than good on `M_BO_NA_1` | no | no ² | no ² |
| `tls` | IEC 62351-3 transport security | no | no | no |
| `dataTransferEvents` | The server events `data-transfer-started` and `data-transfer-stopped` | yes | yes | no ⁴ |
| `fileTransfer` | The file transfer types 120..125 in ASDU documents | yes | yes | no ⁵ |
| `fileServer` | The station serves the `files` of a fixture | yes | no ⁶ | no ⁵ |
| `fileClient` | The `file-get` operation | yes | yes | no ⁵ |

¹ j60870 cannot switch the idle test off and requires `t3 >= t1`; go-iecp5
requires every timer between 1 and 255 seconds. `--t3 0` is a usage error
for both adapters.

² j60870 and go-iecp5 could; the feature is declared for the fixture format
as a whole, which only allows what every adapter can serve.

³ go-iecp5 encodes C_SC_TA_1 .. C_SE_TC_1 but has no length for them, so it
drops them on reception: a station never sees the command and a controlling
station never sees its confirmation.

⁴ go-iecp5 answers STARTDT and STOPDT without telling the application.

⁵ go-iecp5 has no file transfer procedure and drops F_SG_NA_1 on reception.

⁶ j60870 has the information elements but no procedure; the adapter's client
runs the procedure itself, its server does not.

A capability is a property of the adapter and of the upstream version in the
image. The self-tests read it from the image and skip what a pairing lacks
(the `needs` column of [tests/cases.tsv](../tests/cases.tsv)), so "no" here
means "not tested", never "tested and failing".

## Rules for consumers

- Read the capabilities from the image under test, not from this page.
- Skip, do not fail, a scenario that needs a feature the adapter declares
  `false`.
- Count independent implementations by `upstream.independentEngine`.

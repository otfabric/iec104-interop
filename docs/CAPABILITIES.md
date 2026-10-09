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
| `upstream.independentEngine` | `false` when the adapter wraps the protocol engine of another adapter. Results from such an adapter do not count as an additional independent implementation |
| `roles` | Which of `server` and `client` the image provides |
| `features` | See below; every value is a boolean |
| `pointTypes` | Point types a fixture may use with this adapter |
| `commandTypes` | Command types a fixture may use; the CP56Time2a variant of each is accepted too when `timeTaggedCommands` is true |
| `clientOperations` | Operations of `client` |

## Features

| Feature | Meaning | lib60870 | openmuc |
|---------|---------|:--------:|:-------:|
| `generalInterrogation` | Station interrogation (QOI 20), both roles | yes | yes |
| `groupInterrogation` | Interrogation groups 1..16 (the station refuses them) | no | no |
| `counterInterrogation` | General counter request, read | yes | yes |
| `read` | Read command, including the refusal of an unknown address | yes | yes |
| `clockSync` | Clock synchronization command | yes | yes |
| `testCommand` | Test command with time tag (C_TS_TA_1) | yes | yes |
| `directCommands` | Execute without select | yes | yes |
| `selectBeforeOperate` | Select, execute, and `selectRequired` objects | yes | yes |
| `commandDeactivation` | Cancelling a selection (cause 8) | yes | yes |
| `timeTaggedCommands` | The CP56Time2a command types | yes | yes |
| `spontaneousOnCommand` | The time-tagged report after an executed command | yes | yes |
| `apciParameters` | The `--k --w --t0..--t3` flags | yes | yes ¹ |
| `multipleConnections` | Independent simultaneous sessions | yes | yes |
| `pointQualityOnBitstring` | A quality descriptor other than good on `M_BO_NA_1` | no | no ² |
| `tls` | IEC 62351-3 transport security | no | no |
| `fileTransfer` | File transfer types 120..127 | no | no |

¹ j60870 cannot switch the idle test off and requires `t3 >= t1`; `--t3 0`
is a usage error for this adapter.

² j60870 could; the feature is declared for the fixture format as a whole,
which only allows what every adapter can serve.

## Rules for consumers

- Read the capabilities from the image under test, not from this page.
- Skip, do not fail, a scenario that needs a feature the adapter declares
  `false`.
- Count independent implementations by `upstream.independentEngine`.

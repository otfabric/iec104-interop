# Fixtures

A fixture describes a simulated IEC 60870-5-104 controlled station: its
points, their initial values and the commands that act on them. It contains
no assertions and no implementation detail. Every server adapter serves the
same fixture the same way; this document says what "the same way" is.

- Format: JSON, [schema](../fixtures/schema/iec104-fixture.schema.json), `schemaVersion` `"1.0"`
- Location: `fixtures/<name>/fixture.json`, baked into every image under `/fixtures`
- Validation: `make validate` (schema plus the cross-field rules below); adapters apply the same rules at start and exit 4 on a violation

## Format

```json
{
  "schemaVersion": "1.0",
  "name": "baseline",
  "station": { "commonAddress": 1 },
  "points": [
    { "ioa": 100, "type": "M_SP_NA_1", "value": true },
    { "ioa": 110, "type": "M_SP_NA_1", "value": false, "quality": ["NT", "IV"] },
    { "ioa": 300, "type": "M_IT_NA_1", "value": 123456, "sequence": 1 }
  ],
  "commands": [
    { "ioa": 500, "type": "C_SC_NA_1", "target": 100, "reportCause": 11 },
    { "ioa": 510, "type": "C_SC_NA_1", "target": 100, "selectRequired": true }
  ]
}
```

### Points

| Field | Meaning |
|-------|---------|
| `ioa` | Information object address, 1..16777215, unique |
| `type` | The type without time tag: see the table |
| `value` | Initial value |
| `quality` | Optional array of flags; absent means good |
| `transient` | `M_ST_NA_1` only |
| `sequence` | `M_IT_NA_1` only, 0..31 |

| `type` | `value` | `quality` flags |
|--------|---------|-----------------|
| `M_SP_NA_1` | boolean | `BL` `SB` `NT` `IV` |
| `M_DP_NA_1` | 0..3 | `BL` `SB` `NT` `IV` |
| `M_ST_NA_1` | -64..63 | `OV` `BL` `SB` `NT` `IV` |
| `M_BO_NA_1` | 0..4294967295 | none (lib60870 cannot set one) |
| `M_ME_NA_1` | raw NVA, -32768..32767 | `OV` `BL` `SB` `NT` `IV` |
| `M_ME_NB_1` | -32768..32767 | `OV` `BL` `SB` `NT` `IV` |
| `M_ME_NC_1` | number | `OV` `BL` `SB` `NT` `IV` |
| `M_IT_NA_1` | 32-bit signed | `CY` `CA` `IV` |

Use values a single-precision float represents exactly for `M_ME_NC_1`
(230.5, not 230.1), so that every stack prints the same number.

### Commands

| Field | Meaning |
|-------|---------|
| `ioa` | Address of the command object; unique, and not the address of a point |
| `type` | The type without time tag. The CP56Time2a variant is accepted for the same object |
| `target` | Address of the point the command acts on; its type must match |
| `reportCause` | Cause of transmission of the report after execution: 11 (return information, remote command; the default) or 3 (spontaneous) |
| `selectRequired` | When true, an execute is refused unless the object is selected. Default false |

| Command | Target | Effect of an execute |
|---------|--------|----------------------|
| `C_SC_NA_1` | `M_SP_NA_1` | value := command state |
| `C_DC_NA_1` | `M_DP_NA_1` | value := command state (1 or 2) |
| `C_RC_NA_1` | `M_ST_NA_1` | value := value - 1 (lower) or + 1 (higher) |
| `C_SE_NA_1` | `M_ME_NA_1` | value := raw NVA |
| `C_SE_NB_1` | `M_ME_NB_1` | value := scaled value |
| `C_SE_NC_1` | `M_ME_NC_1` | value := float value |

Quality flags of the target are not changed by a command.

## Station behaviour

`CA` is `station.commonAddress`. A request is *for the station* when its
common address is `CA` or the broadcast address 65535. Answers always carry
`CA`.

### Refusals common to all requests

| Condition | Answer |
|-----------|--------|
| Common address is neither `CA` nor 65535 | Mirror with cause 46 and P/N = 1 |
| Type identification the station does not serve | Mirror with cause 44 and P/N = 1 |
| Process command with a cause other than 6 or 8 | Mirror with cause 45 and P/N = 1 |

What a station answers to a *system* command with an unexpected cause is left
to the adapter's library and is not part of the fixture behaviour.

### General interrogation (C_IC_NA_1)

| Request | Answer |
|---------|--------|
| QOI 20 | Activation confirmation; every point that is not an integrated total, with cause 20; activation termination |
| Any other QOI | Negative activation confirmation |

Points are sent without time tag, in ascending address order. Consecutive
points of the same type share an ASDU (SQ = 0); a change of type starts a new
one. The baseline therefore yields nine data ASDUs, two of them with two
objects.

### Counter interrogation (C_CI_NA_1)

| Request | Answer |
|---------|--------|
| QCC 5 (general request, read) | Activation confirmation; every integrated total with cause 37; activation termination |
| Any other QCC | Negative activation confirmation |

### Read (C_RD_NA_1, cause 5)

| Request | Answer |
|---------|--------|
| Address of a point (integrated totals included) | The point with cause 5, without time tag |
| Any other address | Mirror with cause 47 and P/N = 1 |

### Clock synchronization (C_CS_NA_1) and test command (C_TS_TA_1)

Positive activation confirmation. The station does not change its clock: time
tags it generates are the current UTC time.

### Process commands

For a command of type `T` (with or without time tag) to address `A`, cause 6
(activation) or 8 (deactivation):

| Condition, first match wins | Answer | Effect |
|-----------------------------|--------|--------|
| No command object at `A`, or its type is not `T` | Mirror with cause 47 and P/N = 1 | none |
| Cause 8 | Deactivation confirmation (cause 9) | selection cleared |
| Value not permitted (`C_DC`/`C_RC` state 0 or 3) | Negative activation confirmation | none |
| S/E = select | Activation confirmation | object selected |
| S/E = execute, `selectRequired`, object not selected | Negative activation confirmation | none |
| S/E = execute, step command at the limit (-64 or 63) | Negative activation confirmation | selection cleared |
| S/E = execute | Activation confirmation, **report**, activation termination, in this order | target updated, selection cleared |

The **report** is the target point with its CP56Time2a type (`M_SP_TB_1`,
...), the current time, cause `reportCause` and originator 0. It is sent on
the connection that issued the command, not to other connections.

A selection has no timeout and is shared by all connections. The time tag of
a time-tagged command is not evaluated.

### What a station never does

- Send anything unsolicited: no cyclic or background data, no end of
  initialization. Every ASDU is the answer to a request, which keeps tests
  free of timing assumptions.
- Change state without a command. Restart the container to return to the
  fixture.

## The baseline fixture

Station 1.

| IOA | Type | Value | Quality |
|----:|------|-------|---------|
| 100 | `M_SP_NA_1` | true | |
| 101 | `M_DP_NA_1` | 2 (on) | |
| 102 | `M_ST_NA_1` | 5 | |
| 103 | `M_BO_NA_1` | 2863311530 (0xAAAAAAAA) | |
| 110 | `M_SP_NA_1` | false | NT, IV |
| 111 | `M_SP_NA_1` | true | BL, SB |
| 200 | `M_ME_NA_1` | 16384 (0.5) | |
| 201 | `M_ME_NB_1` | 1234 | |
| 202 | `M_ME_NC_1` | 230.5 | |
| 210 | `M_ME_NB_1` | -32768 | OV |
| 211 | `M_ME_NB_1` | -1 | |
| 300 | `M_IT_NA_1` | 123456, sequence 1 | |

| IOA | Command | Target | Report cause | Select required |
|----:|---------|-------:|-------------:|-----------------|
| 500 | `C_SC_NA_1` | 100 | 11 | no |
| 501 | `C_DC_NA_1` | 101 | 3 | no |
| 502 | `C_SE_NC_1` | 202 | 11 | no |
| 503 | `C_SE_NA_1` | 200 | 11 | no |
| 504 | `C_SE_NB_1` | 201 | 11 | no |
| 505 | `C_RC_NA_1` | 102 | 11 | no |
| 510 | `C_SC_NA_1` | 100 | 11 | yes |

## Adding a fixture

1. Create `fixtures/<name>/fixture.json`; `name` must equal the directory.
2. `make validate`.
3. Use only types and features every published adapter declares
   ([CAPABILITIES.md](CAPABILITIES.md)); a fixture must not need
   adapter-specific behaviour.
4. Rebuild the images (`make images`): fixtures are baked in.
5. If the fixture is meant for the self-tests, add cases for it; a fixture
   nothing exercises is not verified.

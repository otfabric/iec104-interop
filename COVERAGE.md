# Coverage

What a consumer can exercise against the published adapters today, by the
test categories of the plan. "Cross-stack" is what this repository's own
self-test covers between the reference stacks.

| Category | Available to a consumer | Cross-stack self-test |
|----------|-------------------------|-----------------------|
| TCP connection lifecycle | connect, disconnect; reconnect by restarting the server container | connect, disconnect |
| Data transfer lifecycle | STARTDT, STOPDT (`client connect`), TESTFR via the idle test | yes |
| APCI | I/S/U frames in normal operation; `k`, `w`, `t0`..`t3` settable on both roles | default windows and `k = w = 1`; `t3` idle test |
| ASDU | Types of the fixture format; cause, P/N, common address, originator; SQ = 0 | yes |
| Monitoring | `M_SP`, `M_DP`, `M_ST`, `M_BO`, `M_ME_NA/NB/NC`, `M_IT`, each with and without CP56Time2a; quality flags | yes |
| General interrogation | Activation, data, termination; broadcast address; unknown station; unsupported group | yes |
| Commands | `C_SC`, `C_DC`, `C_RC`, `C_SE_NA/NB/NC` with and without time tag; direct, select, select-and-execute, deactivation; refusals | yes; with time tag between lib60870 and openmuc |
| Spontaneous reporting | Time-tagged report of the target after a command (causes 3 and 11) | yes |
| Time synchronization | `C_CS_NA_1`, and the received time in the server's event stream | yes |
| Counter interrogation | General request, read; unsupported qualifier refused | yes |
| Read | A status, a measurement, a counter; unknown address refused | yes |
| Test command | `C_TS_TA_1` | yes |
| Multiple connections | Up to 16 independent sessions per server | three concurrent sessions |
| Recovery | Server restart (`docker restart`) returns the station to the fixture | no |
| APCI timers | `t3` and `t1` observable through the idle test; `t0` on the client | idle test only |
| Extended types | none beyond the above | no |
| Redundancy | no: reports go to the commanding connection only | no |
| File transfer | Download (monitor direction): select, call, sections, segments, checksums, acknowledgements; refusal of an unknown file. Server: lib60870. Client: lib60870, openmuc | yes, between those |
| Secure transport | no | no |

## Not provided on purpose

Malformed frames, wrong sequence numbers, missing acknowledgements and
precise timer boundaries. A reference implementation is a correct peer, not a
fault injector; a library tests these with scripted peers of its own.

## Per adapter

| | lib60870 | openmuc | wendy512 |
|---|:---:|:---:|:---:|
| Everything above except the rows below | yes | yes | yes |
| Commands with time tag | yes | yes | no |
| File transfer, server | yes | no | no |
| File transfer, client | yes | yes | no |
| `data-transfer-started` / `-stopped` server events | yes | yes | no |
| Idle test disabled (`--t3 0`) | yes | no | no |

The reasons are in [docs/CAPABILITIES.md](docs/CAPABILITIES.md) and
[docs/COMPATIBILITY.md](docs/COMPATIBILITY.md). Read them from the image
(`print-capabilities`) rather than from this table.

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
| Commands | `C_SC`, `C_DC`, `C_RC`, `C_SE_NA/NB/NC` with and without time tag; direct, select, select-and-execute, deactivation; refusals | yes |
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
| File transfer | no | no |
| Secure transport | no | no |

## Not provided on purpose

Malformed frames, wrong sequence numbers, missing acknowledgements and
precise timer boundaries. A reference implementation is a correct peer, not a
fault injector; a library tests these with scripted peers of its own.

## Per adapter

Both published adapters cover every row above identically, with one
difference: OpenMUC cannot run with the idle test disabled (`--t3 0`). See
[docs/CAPABILITIES.md](docs/CAPABILITIES.md).

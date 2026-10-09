# Security Policy

## Reporting a Vulnerability

If you discover a security vulnerability in `iec104-interop`, please report it responsibly.

**Do not open a public GitHub issue for security vulnerabilities.**

Instead, please email **security@otfabric.com**, or open a private advisory on GitHub (the [Security](https://github.com/otfabric/iec104-interop/security) tab → **Advisories** → **Report a vulnerability**), with a description, steps to reproduce and the affected image tag or digest.

Vulnerabilities in the bundled implementations themselves (lib60870-C, j60870) belong upstream; report them there, and tell us so that the pin can be moved.

## Supported Versions

| Version | Supported |
|---------|-----------|
| Latest release | Yes |
| Older releases | No; pull the latest images |

## Security Considerations

### These are test fixtures, not products

The images run **unauthenticated, unencrypted IEC 60870-5-104 stations** that accept interrogations and commands from anyone who can reach them, and clients that send commands to whatever address they are given. That is their purpose.

- **Never attach a server to a plant, control or other production network**
- **Never point a client at a real station**: `command`, `clock-sync` and the other operations act on what they are sent to
- `compose.yaml` and the `run-*` Makefile targets publish ports on `127.0.0.1` only. Keep it that way; the self-tests use private Docker networks and publish nothing
- There is no TLS and no access control, and none is planned beyond what a future fixture needs to test a consumer's TLS support

### Image hardening

- Processes run as a non-root user (uid 10001)
- Base images are pinned by digest; upstream sources by commit or exact version ([versions.yaml](versions.yaml))
- The runtime images contain the adapter, its runtime libraries and the fixtures; build tools stay in the build stages
- The images are rebuilt, not patched: a base image or upstream fix arrives with a new release

### Supply chain

- Upstream sources are fetched during the image build from GitHub (lib60870) and Maven Central (j60870, Gson) at pinned versions; the lib60870 checkout is verified against the pinned commit hash
- Consumers should pin images by digest, taken from the `manifest.json` of a release

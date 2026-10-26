// SPDX-License-Identifier: GPL-3.0-or-later

// Command iec104-interop is the iec104-interop adapter for
// cB-Sigma/iec60870, a Go implementation of IEC 60870-5-104 with the
// application-layer security of IEC 62351-5 (IEC TS 60870-5-7).
//
// It implements the container contract in docs/CONTAINER_CONTRACT.md.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	adapter          = "cbsigma"
	schemaVersion    = "1.0"
	defaultFixture   = "/fixtures/baseline/fixture.json"
	defaultReadyFile = "/run/iec104-interop/ready"

	exitOK              = 0
	exitOperationFailed = 1
	exitUsage           = 2
	exitConnectFailed   = 3
	exitFixtureInvalid  = 4
)

// Set by the linker; see the Dockerfile.
var (
	adapterVersion  = "dev"
	upstreamVersion = "unknown" // cB-Sigma/iec60870
)

const usage = `usage: iec104-interop <command>

  server [--fixture PATH] [--bind-address ADDR] [--port N] [--ready-file PATH] [APCI] [SECURITY]
         [--roles ROLE,...]
  client <operation> --host HOST [--port N] [--common-address N] [--originator-address N]
         [--timeout-ms N] [--connect-timeout-ms N] [--collect-ms N] [APCI] [SECURITY]
         [--mal N] [--dpa N] [operation flags]
      connect             [--hold-ms N]
      interrogate         [--qoi N]
      counter-interrogate [--qcc N]
      read                --ioa N
      clock-sync          [--time 2026-01-02T03:04:05.678Z]
      test-command
      command             --type T --ioa N --value V [--mode direct|select|sbo|cancel]
                          [--qualifier N] [--with-time]
      monitor             [--duration-ms N] [--max-asdus N]
      file-get            --ioa N [--name N]
  print-capabilities
  print-fixture [NAME]
  version

  APCI:     [--k N] [--w N] [--t0 S] [--t1 S] [--t2 S] [--t3 S]
  SECURITY: [--secure --security-cert PATH --security-key PATH --security-trust PATH
            [--association-id N]]

See docs/CONTAINER_CONTRACT.md in https://github.com/otfabric/iec104-interop
`

var outMu sync.Mutex

// emit prints one JSON document on a single line of stdout.
func emit(doc any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		logf("cannot encode document: %v", err)
		return
	}
	outMu.Lock()
	defer outMu.Unlock()
	_, _ = os.Stdout.Write(buf.Bytes())
}

func logf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "["+adapter+"] "+format+"\n", a...)
}

func printCapabilities() int {
	emit(object{
		"schemaVersion":  schemaVersion,
		"adapter":        adapter,
		"adapterVersion": adapterVersion,
		"protocol":       "iec60870-5-104",
		"upstream": object{
			"name":     "cB-Sigma/iec60870",
			"version":  upstreamVersion,
			"revision": "github.com/cB-Sigma/iec60870@" + upstreamVersion,
			"license":  "Apache-2.0",
			"language": "Go",
			// Written from the IEC documents for SigmaM2M; shares no code
			// with the other adapters' libraries.
			"independentEngine": true,
		},
		"roles": object{"server": true, "client": true},
		"features": object{
			"generalInterrogation":    true,
			"groupInterrogation":      false,
			"counterInterrogation":    true,
			"read":                    true,
			"clockSync":               true,
			"testCommand":             true,
			"directCommands":          true,
			"selectBeforeOperate":     true,
			"commandDeactivation":     true,
			"timeTaggedCommands":      true,
			"spontaneousOnCommand":    true,
			"apciParameters":          true,
			"multipleConnections":     true,
			"pointQualityOnBitstring": false,
			// The library has TLS (iotsec); the contract has no flags for it.
			"tls":                  false,
			"dataTransferEvents":   true,
			"fileTransfer":         true,
			"fileServer":           true,
			"fileClient":           true,
			"secureAuthentication": true,
		},
		"pointTypes": []string{"M_SP_NA_1", "M_DP_NA_1", "M_ST_NA_1", "M_BO_NA_1", "M_ME_NA_1", "M_ME_NB_1",
			"M_ME_NC_1", "M_IT_NA_1"},
		"commandTypes": []string{"C_SC_NA_1", "C_DC_NA_1", "C_RC_NA_1", "C_SE_NA_1", "C_SE_NB_1", "C_SE_NC_1"},
		"clientOperations": []string{"connect", "interrogate", "counter-interrogate", "read", "clock-sync",
			"test-command", "command", "monitor", "file-get"},
	})
	return exitOK
}

func printFixture(name string) int {
	if strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") {
		logf("print-fixture: '%s' is not a fixture name", name)
		return exitUsage
	}
	raw, err := os.ReadFile(filepath.Join("/fixtures", name, "fixture.json"))
	if err != nil {
		logf("print-fixture: no fixture named '%s' in this image", name)
		return exitOperationFailed
	}
	_, _ = os.Stdout.Write(raw)
	return exitOK
}

func run(argv []string) (int, error) {
	if len(argv) < 1 {
		fmt.Fprint(os.Stderr, usage)
		return exitUsage, nil
	}
	switch argv[0] {
	case "server":
		return runServer(&args{rest: argv[1:]})
	case "client":
		if len(argv) < 2 {
			fmt.Fprint(os.Stderr, usage)
			return exitUsage, nil
		}
		return runClient(argv[1], &args{rest: argv[2:]})
	case "print-capabilities":
		return printCapabilities(), nil
	case "print-fixture":
		name := "baseline"
		if len(argv) > 1 {
			name = argv[1]
		}
		return printFixture(name), nil
	case "version":
		fmt.Printf("%s %s (cB-Sigma/iec60870 %s)\n", adapter, adapterVersion, upstreamVersion)
		return exitOK, nil
	case "help", "--help", "-h":
		fmt.Print(usage)
		return exitOK, nil
	}
	fmt.Fprint(os.Stderr, usage)
	return exitUsage, nil
}

func main() {
	code, err := run(os.Args[1:])
	var ue usageError
	switch {
	case errors.As(err, &ue):
		logf("%s", ue.msg)
		code = exitUsage
	case err != nil:
		logf("fatal: %v", err)
		code = exitOperationFailed
	}
	os.Exit(code)
}

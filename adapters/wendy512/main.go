// SPDX-License-Identifier: GPL-3.0-or-later

// Command iec104-interop is the iec104-interop adapter for wendy512/iec104
// and its protocol engine wendy512/go-iecp5.
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
	adapter          = "wendy512"
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
	upstreamVersion = "unknown" // wendy512/iec104
	engineVersion   = "unknown" // wendy512/go-iecp5
)

const usage = `usage: iec104-interop <command>

  server [--fixture PATH] [--bind-address ADDR] [--port N] [--ready-file PATH] [APCI]
  client <operation> --host HOST [--port N] [--common-address N] [--originator-address N]
         [--timeout-ms N] [--connect-timeout-ms N] [--collect-ms N] [APCI] [operation flags]
      connect             [--hold-ms N]
      interrogate         [--qoi N]
      counter-interrogate [--qcc N]
      read                --ioa N
      clock-sync          [--time 2026-01-02T03:04:05.678Z]
      test-command
      command             --type T --ioa N --value V [--mode direct|select|sbo|cancel]
                          [--qualifier N] [--with-time]
      monitor             [--duration-ms N] [--max-asdus N]
  print-capabilities
  print-fixture [NAME]
  version

  APCI: [--k N] [--w N] [--t0 S] [--t1 S] [--t2 S] [--t3 S]

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
			"name":     "wendy512/iec104",
			"version":  upstreamVersion,
			"revision": "github.com/wendy512/iec104@" + upstreamVersion,
			"license":  "Apache-2.0",
			"language": "Go",
			// Its engine go-iecp5 descends from thinkgos/go-iecp5 and shares
			// no code with the other adapters' libraries.
			"independentEngine": true,
			"engine":            object{"name": "wendy512/go-iecp5", "version": engineVersion, "license": "LGPL-3.0"},
		},
		"roles": object{"server": true, "client": true},
		"features": object{
			"generalInterrogation": true,
			"groupInterrogation":   false,
			"counterInterrogation": true,
			"read":                 true,
			"clockSync":            true,
			"testCommand":          true,
			"directCommands":       true,
			"selectBeforeOperate":  true,
			"commandDeactivation":  true,
			// go-iecp5 can send C_SC_TA_1 ... C_SE_TC_1 but has no size for
			// them and drops them on reception, as a station and as the
			// mirror a controlling station waits for.
			"timeTaggedCommands":      false,
			"spontaneousOnCommand":    true,
			"apciParameters":          true,
			"multipleConnections":     true,
			"pointQualityOnBitstring": false,
			"tls":                     false,
			// The server has no hook for STARTDT and STOPDT.
			"dataTransferEvents": false,
			// No file transfer procedure, and F_SG_NA_1 is dropped on reception.
			"fileTransfer": false,
			"fileServer":   false,
			"fileClient":   false,
		},
		"pointTypes": []string{"M_SP_NA_1", "M_DP_NA_1", "M_ST_NA_1", "M_BO_NA_1", "M_ME_NA_1", "M_ME_NB_1",
			"M_ME_NC_1", "M_IT_NA_1"},
		"commandTypes": []string{"C_SC_NA_1", "C_DC_NA_1", "C_RC_NA_1", "C_SE_NA_1", "C_SE_NB_1", "C_SE_NC_1"},
		"clientOperations": []string{"connect", "interrogate", "counter-interrogate", "read", "clock-sync",
			"test-command", "command", "monitor"},
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
		fmt.Printf("%s %s (wendy512/iec104 %s, go-iecp5 %s)\n", adapter, adapterVersion, upstreamVersion, engineVersion)
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

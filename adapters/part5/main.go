// SPDX-License-Identifier: GPL-3.0-or-later

// Command iec104-interop is the iec104-interop adapter for
// pascaldekloe/part5.
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

	"github.com/pascaldekloe/part5/session"
)

const (
	adapter          = "part5"
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
	upstreamVersion = "unknown" // pascaldekloe/part5, the tag
	upstreamCommit  = "unknown" // the commit of that tag
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
			"name":     "pascaldekloe/part5",
			"version":  upstreamVersion,
			"revision": upstreamCommit,
			"license":  "CC0-1.0",
			"language": "Go",
			// Its own APCI session engine and ASDU codec; it shares no code
			// with the other adapters' libraries.
			"independentEngine": true,
		},
		"roles": object{"server": true, "client": true},
		"features": object{
			"generalInterrogation": true,
			"groupInterrogation":   true,
			"counterInterrogation": true,
			"read":                 true,
			"clockSync":            true,
			"testCommand":          true,
			"directCommands":       true,
			"selectBeforeOperate":  true,
			"commandDeactivation":  true,
			"timeTaggedCommands":   true,
			"spontaneousOnCommand": true,
			"apciParameters":       true,
			"multipleConnections":  true,
			// part5 has the quality descriptor on every bitstring (info.BitsQual).
			"pointQualityOnBitstring": true,
			// part5 has no TLS of its own.
			"tls":       false,
			"tlsServer": false,
			"tlsClient": false,
			// part5 reports every level change of a session on Station.Level.
			"dataTransferEvents": true,
			// part5 has the file transfer type identifications but no
			// information elements or procedure for them.
			"fileTransfer":        false,
			"fileServer":          false,
			"fileClient":          false,
			"fileUploadServer":    false,
			"fileUploadClient":    false,
			"fileDirectoryServer": false,
			"fileDirectoryClient": false,
			// part5 has no redundancy group of connections.
			"redundancyServer": false,
			"redundancyClient": false,
			// No IEC 62351-5.
			"secureAuthentication": false,
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
		fmt.Printf("%s %s (pascaldekloe/part5 %s, %s)\n", adapter, adapterVersion, upstreamVersion, upstreamCommit)
		return exitOK, nil
	case "help", "--help", "-h":
		fmt.Print(usage)
		return exitOK, nil
	}
	fmt.Fprint(os.Stderr, usage)
	return exitUsage, nil
}

func main() {
	// part5 logs every APDU it sends and receives to stderr.
	session.Trace = os.Getenv("IEC104_INTEROP_DEBUG") != ""
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

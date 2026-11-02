// SPDX-License-Identifier: MIT

// Command iec104-interop is the iec104-interop-otfabric image: go-iec104 behind
// the container contract (docs/CONTAINER_CONTRACT.md).
//
// It is OT Fabric's own stack, not an independent implementation: results
// against it are never independent evidence for go-iec104 (README.md).
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/otfabric/go-iec104/apci"
)

const (
	adapterName   = "otfabric"
	schemaVersion = "1.0"
	fixtureRoot   = "/fixtures"
)

// Set with -ldflags by the Dockerfile.
var (
	adapterVersion   = "dev"
	upstreamVersion  = "dev"
	upstreamRevision = "dev"
)

const (
	exitOK             = 0
	exitFailed         = 1
	exitUsage          = 2
	exitConnect        = 3
	exitInvalidFixture = 4
)

type object = map[string]any

var (
	stdoutMu sync.Mutex
	stdout   io.Writer = os.Stdout
)

// emit prints one JSON document on one line of stdout.
func emit(doc any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		logf("cannot encode a document: %v", err)
		return
	}
	stdoutMu.Lock()
	defer stdoutMu.Unlock()
	_, _ = stdout.Write(buf.Bytes())
}

func logf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "iec104-interop: "+format+"\n", a...)
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		return usage("no command")
	}
	switch args[0] {
	case "server":
		return runServer(parseFlags(args[1:]))
	case "client":
		if len(args) < 2 {
			return usage("client needs an operation")
		}
		return runClient(args[1], parseFlags(args[2:]))
	case "print-capabilities":
		emit(capabilities())
		return exitOK
	case "print-fixture":
		return printFixture(args[1:])
	case "version":
		fmt.Fprintf(stdout, "%s %s (go-iec104 %s)\n", adapterName, adapterVersion, upstreamVersion)
		return exitOK
	}
	return usage("unknown command " + args[0])
}

func usage(message string) int {
	logf("%s", message)
	return exitUsage
}

func printFixture(args []string) int {
	name := "baseline"
	switch {
	case len(args) == 1:
		name = args[0]
	case len(args) > 1:
		return usage("print-fixture takes at most one name")
	}
	if strings.ContainsAny(name, "/\\") {
		logf("no fixture named %s", name)
		return exitFailed
	}
	b, err := os.ReadFile(fixtureRoot + "/" + name + "/fixture.json")
	if err != nil {
		logf("%v", err)
		return exitFailed
	}
	_, _ = stdout.Write(b)
	return exitOK
}

// capabilities is the document of docs/CAPABILITIES.md. A feature is true only
// when the self-tests exercise it for this adapter.
func capabilities() object {
	return object{
		"schemaVersion":  schemaVersion,
		"adapter":        adapterName,
		"adapterVersion": adapterVersion,
		"protocol":       "iec60870-5-104",
		"upstream": object{
			"name":              "go-iec104",
			"version":           upstreamVersion,
			"revision":          upstreamRevision,
			"license":           "MIT",
			"language":          "Go",
			"independentEngine": false,
		},
		"roles": map[string]bool{"server": true, "client": true},
		"features": map[string]bool{
			"generalInterrogation":    true,
			"groupInterrogation":      true,
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
			"pointQualityOnBitstring": true,
			"tls":                     true,
			"tlsServer":               true,
			"tlsClient":               true,
			"dataTransferEvents":      true,
			"fileTransfer":            true,
			"fileServer":              true,
			"fileClient":              true,
			"secureAuthentication":    true,
		},
		"pointTypes": []string{"M_SP_NA_1", "M_DP_NA_1", "M_ST_NA_1", "M_BO_NA_1", "M_ME_NA_1", "M_ME_NB_1",
			"M_ME_NC_1", "M_IT_NA_1"},
		"commandTypes": []string{"C_SC_NA_1", "C_DC_NA_1", "C_RC_NA_1", "C_SE_NA_1", "C_SE_NB_1", "C_SE_NC_1"},
		"clientOperations": []string{"connect", "interrogate", "counter-interrogate", "read", "clock-sync",
			"test-command", "command", "monitor", "file-get"},
	}
}

// switches take no value; every other flag does.
var switches = map[string]bool{"secure": true, "tls": true, "with-time": true}

// flags holds the flags of a command. A command takes what it needs and finish
// refuses the rest: an unknown flag is a usage error, never ignored.
type flags struct {
	values map[string]string
	err    error
}

func parseFlags(args []string) *flags {
	f := &flags{values: map[string]string{}}
	for i := 0; i < len(args); i++ {
		name, isFlag := strings.CutPrefix(args[i], "--")
		switch {
		case !isFlag:
			f.fail("unexpected argument %s", args[i])
			return f
		case switches[name]:
			f.values[name] = ""
		case i+1 == len(args):
			f.fail("%s needs a value", args[i])
			return f
		default:
			f.values[name] = args[i+1]
			i++
		}
	}
	return f
}

func (f *flags) fail(format string, a ...any) {
	if f.err == nil {
		f.err = fmt.Errorf(format, a...)
	}
}

func (f *flags) take(name string) (string, bool) {
	v, ok := f.values[name]
	delete(f.values, name)
	return v, ok
}

// finish returns the first usage error, including any flag nobody took.
func (f *flags) finish() error {
	for name := range f.values {
		f.fail("unknown flag --%s", name)
	}
	return f.err
}

func (f *flags) has(name string) bool {
	_, ok := f.take(name)
	return ok
}

func (f *flags) text(name, def string) string {
	if s, ok := f.take(name); ok {
		return s
	}
	return def
}

// integer returns an integer flag within lo..hi, or def without one.
func (f *flags) integer(name string, def, lo, hi int) int {
	s, ok := f.take(name)
	if !ok {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil || v < lo || v > hi {
		f.fail("--%s %s is not in %d..%d", name, s, lo, hi)
		return def
	}
	return v
}

// required is integer for a flag without a default.
func (f *flags) required(name string, lo, hi int) int {
	if _, ok := f.values[name]; !ok {
		f.fail("--%s is required", name)
		return 0
	}
	return f.integer(name, 0, lo, hi)
}

func (f *flags) milliseconds(name string, def, lo int) time.Duration {
	return time.Duration(f.integer(name, def, lo, 1<<31-1)) * time.Millisecond
}

// apci reads the APCI flags, in the seconds of the contract. A value the library
// cannot run with is a usage error, never replaced by another one.
func (f *flags) apci() apci.Params {
	second := func(name string, def int) time.Duration {
		return time.Duration(f.integer(name, def, 0, 1<<31-1)) * time.Second
	}
	p := apci.Params{
		K: f.integer("k", 12, 0, 1<<31-1), W: f.integer("w", 8, 0, 1<<31-1),
		T0: second("t0", 30), T1: second("t1", 15), T2: second("t2", 10), T3: second("t3", 20),
	}
	if err := p.Validate(); err != nil {
		f.fail("%v", err)
	}
	return p
}

func parseBool(s string) (value, ok bool) {
	switch strings.ToLower(s) {
	case "true", "1", "on":
		return true, true
	case "false", "0", "off":
		return false, true
	}
	return false, false
}

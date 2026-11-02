// SPDX-License-Identifier: MIT

package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/otfabric/go-iec104/asdu"
	"github.com/otfabric/go-iec104/client"
)

// The repository's fixtures are not in the build stage, so these tests carry
// their own. scripts/smoke.sh and scripts/interop.sh test against the real ones.
const testFixture = `{
  "schemaVersion": "1.0", "name": "test", "station": {"commonAddress": 1},
  "points": [
    {"ioa": 100, "type": "M_SP_NA_1", "value": true, "quality": ["BL"]},
    {"ioa": 200, "type": "M_ME_NC_1", "value": 1.5},
    {"ioa": 300, "type": "M_ST_NA_1", "value": 63, "transient": true},
    {"ioa": 700, "type": "M_IT_NA_1", "value": 7, "sequence": 3, "quality": ["CY"]},
    {"ioa": 800, "type": "M_BO_NA_1", "value": 4042322160, "quality": ["OV", "NT"], "groups": [2, 16]},
    {"ioa": 801, "type": "M_BO_NA_1", "value": 1, "groups": [2]},
    {"ioa": 802, "type": "M_SP_NA_1", "value": true, "groups": [16]}
  ],
  "commands": [
    {"ioa": 1000, "type": "C_SC_NA_1", "target": 100, "reportCause": 3},
    {"ioa": 1001, "type": "C_SC_NA_1", "target": 100, "selectRequired": true},
    {"ioa": 1002, "type": "C_RC_NA_1", "target": 300}
  ],
  "files": [{"ioa": 2000, "name": 1, "size": 1000, "sectionSize": 300}],
  "uploads": [{"ioa": 3000, "name": 1, "maxSize": 8192}, {"ioa": 4000, "name": 2, "maxSize": 100}, {"ioa": 5000, "name": 1, "maxSize": 100}]
}`

type output struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (o *output) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.Write(p)
}

// take returns the documents written so far and forgets them.
func (o *output) take(t *testing.T) []object {
	t.Helper()
	o.mu.Lock()
	defer o.mu.Unlock()
	var docs []object
	scan := bufio.NewScanner(&o.buf)
	scan.Buffer(nil, 1<<24)
	for scan.Scan() {
		var d object
		if err := json.Unmarshal(scan.Bytes(), &d); err != nil {
			t.Fatalf("not one JSON document per line: %q: %v", scan.Text(), err)
		}
		docs = append(docs, d)
	}
	o.buf.Reset()
	return docs
}

func capture(t *testing.T) *output {
	t.Helper()
	o := &output{}
	stdoutMu.Lock()
	previous := stdout
	stdout = o
	stdoutMu.Unlock()
	t.Cleanup(func() {
		stdoutMu.Lock()
		stdout = previous
		stdoutMu.Unlock()
	})
	return o
}

func writeFixture(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	return strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
}

func TestCapabilitiesAreNotIndependent(t *testing.T) {
	out := capture(t)
	if code := run([]string{"print-capabilities"}); code != exitOK {
		t.Fatalf("exit %d", code)
	}
	docs := out.take(t)
	if len(docs) != 1 {
		t.Fatalf("%d documents", len(docs))
	}
	upstream := docs[0]["upstream"].(object)
	if upstream["independentEngine"] != false || upstream["name"] != "go-iec104" {
		t.Fatalf("upstream %v", upstream)
	}
}

func TestUsageErrors(t *testing.T) {
	capture(t)
	for _, args := range [][]string{
		nil,
		{"nonsense"},
		{"client"},
		{"client", "nonsense", "--host", "h"},
		{"client", "connect"},
		{"client", "connect", "--host", "h", "--unknown", "1"},
		{"client", "connect", "--host", "h", "--k"},
		{"client", "connect", "--host", "h", "--k", "0"},
		{"client", "connect", "--host", "h", "--w", "13"},
		{"client", "connect", "--host", "h", "--t1", "0"},
		{"client", "connect", "--host", "h", "--security-cert", "c"},
		{"client", "connect", "--host", "h", "--mal", "4"},
		{"client", "read", "--host", "h"},
		{"client", "command", "--host", "h", "--type", "M_SP_NA_1", "--ioa", "1", "--value", "1"},
		{"client", "command", "--host", "h", "--type", "C_SC_NA_1", "--ioa", "1", "--value", "maybe"},
		{"client", "command", "--host", "h", "--type", "C_SC_NA_1", "--ioa", "1", "--value", "1", "--mode", "x"},
		{"client", "command", "--host", "h", "--type", "C_SC_NA_1", "--ioa", "1", "--value", "1", "--qualifier", "32"},
		{"client", "clock-sync", "--host", "h", "--time", "noon"},
		{"server", "--port", "0"},
		{"server", "--secure"},
		{"server", "--roles", "VIEWER"},
		{"client", "file-put", "--host", "h", "--ioa", "1"},
		{"client", "file-put", "--host", "h", "--size", "1"},
		{"client", "file-put", "--host", "h", "--ioa", "1", "--size", "0"},
		{"client", "file-put", "--host", "h", "--ioa", "1", "--size", "65537"},
		{"client", "file-put", "--host", "h", "--ioa", "1", "--size", "100", "--section-size", "101"},
		{"client", "file-put", "--host", "h", "--ioa", "1", "--size", "255", "--section-size", "1"},
		{"client", "file-dir", "--host", "h", "--ioa", "1"},
		{"server", "--tls"},
		{"server", "--tls-trust", "t"},
		{"client", "connect", "--host", "h", "--tls"},
		{"client", "connect", "--host", "h", "--tls-cert", "c"},
		{"client", "connect", "--host", "h", "--tls", "--tls-cert", "/no/such", "--tls-key", "/no/such", "--tls-trust", "/no/such"},
	} {
		if code := run(args); code != exitUsage {
			t.Errorf("%v: exit %d, want %d", args, code, exitUsage)
		}
	}
}

func TestInvalidFixtures(t *testing.T) {
	capture(t)
	for name, content := range map[string]string{
		"not json":     `{`,
		"schema":       `{"schemaVersion": "2.0", "name": "x", "station": {"commonAddress": 1}, "points": [{"ioa": 1, "type": "M_SP_NA_1", "value": true}]}`,
		"address":      `{"schemaVersion": "1.0", "name": "x", "station": {"commonAddress": 65535}, "points": [{"ioa": 1, "type": "M_SP_NA_1", "value": true}]}`,
		"duplicate":    `{"schemaVersion": "1.0", "name": "x", "station": {"commonAddress": 1}, "points": [{"ioa": 1, "type": "M_SP_NA_1", "value": true}, {"ioa": 1, "type": "M_SP_NA_1", "value": true}]}`,
		"value":        `{"schemaVersion": "1.0", "name": "x", "station": {"commonAddress": 1}, "points": [{"ioa": 1, "type": "M_DP_NA_1", "value": 4}]}`,
		"quality":      `{"schemaVersion": "1.0", "name": "x", "station": {"commonAddress": 1}, "points": [{"ioa": 1, "type": "M_SP_NA_1", "value": true, "quality": ["OV"]}]}`,
		"bit quality":  `{"schemaVersion": "1.0", "name": "x", "station": {"commonAddress": 1}, "points": [{"ioa": 1, "type": "M_BO_NA_1", "value": 1, "quality": ["CY"]}]}`,
		"group 0":      `{"schemaVersion": "1.0", "name": "x", "station": {"commonAddress": 1}, "points": [{"ioa": 1, "type": "M_SP_NA_1", "value": true, "groups": [0]}]}`,
		"group 17":     `{"schemaVersion": "1.0", "name": "x", "station": {"commonAddress": 1}, "points": [{"ioa": 1, "type": "M_SP_NA_1", "value": true, "groups": [17]}]}`,
		"group twice":  `{"schemaVersion": "1.0", "name": "x", "station": {"commonAddress": 1}, "points": [{"ioa": 1, "type": "M_SP_NA_1", "value": true, "groups": [1, 1]}]}`,
		"total group":  `{"schemaVersion": "1.0", "name": "x", "station": {"commonAddress": 1}, "points": [{"ioa": 1, "type": "M_IT_NA_1", "value": 1, "groups": [1]}]}`,
		"target":       `{"schemaVersion": "1.0", "name": "x", "station": {"commonAddress": 1}, "points": [{"ioa": 1, "type": "M_SP_NA_1", "value": true}], "commands": [{"ioa": 2, "type": "C_DC_NA_1", "target": 1}]}`,
		"report cause": `{"schemaVersion": "1.0", "name": "x", "station": {"commonAddress": 1}, "points": [{"ioa": 1, "type": "M_SP_NA_1", "value": true}], "commands": [{"ioa": 2, "type": "C_SC_NA_1", "target": 1, "reportCause": 5}]}`,
		"file":         `{"schemaVersion": "1.0", "name": "x", "station": {"commonAddress": 1}, "points": [{"ioa": 1, "type": "M_SP_NA_1", "value": true}], "files": [{"ioa": 2, "name": 1, "size": 10, "sectionSize": 11}]}`,
		"upload size":  `{"schemaVersion": "1.0", "name": "x", "station": {"commonAddress": 1}, "points": [{"ioa": 1, "type": "M_SP_NA_1", "value": true}], "uploads": [{"ioa": 2, "name": 1, "maxSize": 65537}]}`,
		"upload name":  `{"schemaVersion": "1.0", "name": "x", "station": {"commonAddress": 1}, "points": [{"ioa": 1, "type": "M_SP_NA_1", "value": true}], "uploads": [{"ioa": 2, "name": 0, "maxSize": 1}]}`,
		"upload file":  `{"schemaVersion": "1.0", "name": "x", "station": {"commonAddress": 1}, "points": [{"ioa": 1, "type": "M_SP_NA_1", "value": true}], "files": [{"ioa": 2, "name": 1, "size": 10, "sectionSize": 10}], "uploads": [{"ioa": 2, "name": 1, "maxSize": 1}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if code := run([]string{"server", "--port", freePort(t), "--fixture", writeFixture(t, content)}); code != exitInvalidFixture {
				t.Fatalf("exit %d, want %d", code, exitInvalidFixture)
			}
		})
	}
}

func TestConnectFailed(t *testing.T) {
	out := capture(t)
	code := run([]string{"client", "connect", "--host", "127.0.0.1", "--port", freePort(t), "--connect-timeout-ms", "500"})
	docs := out.take(t)
	if code != exitConnect || len(docs) != 1 || docs[0]["error"].(object)["code"] != "connect-failed" {
		t.Fatalf("exit %d, %v", code, docs)
	}
}

// startServer runs the server until the test ends and returns the common client
// flags.
func startServer(t *testing.T, extra ...string) []string {
	t.Helper()
	port := freePort(t)
	readyFile := filepath.Join(t.TempDir(), "ready")
	done := make(chan int, 1)
	go func() {
		done <- run(append([]string{"server", "--bind-address", "127.0.0.1", "--port", port,
			"--fixture", writeFixture(t, testFixture), "--ready-file", readyFile}, extra...))
	}()
	t.Cleanup(func() {
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
		select {
		case code := <-done:
			if code != exitOK {
				t.Errorf("the server exited with %d", code)
			}
		case <-time.After(10 * time.Second):
			t.Error("the server did not stop")
		}
		if _, err := os.Stat(readyFile); err == nil {
			t.Error("the ready file is still there")
		}
	})
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(readyFile); err == nil {
			return []string{"--host", "127.0.0.1", "--port", port, "--timeout-ms", "3000"}
		}
		if time.Now().After(deadline) {
			t.Fatal("the server did not get ready")
		}
	}
}

// operate runs one client operation and returns its result and the server's
// events, once the server has printed the event that ends the connection.
func operate(t *testing.T, out *output, common []string, op string, args ...string) (object, []object) {
	t.Helper()
	code := run(append(append([]string{"client", op}, common...), args...))
	var result object
	var events []object
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		for _, d := range out.take(t) {
			if _, isEvent := d["event"]; isEvent {
				events = append(events, d)
			} else {
				result = d
			}
		}
		if len(events) > 0 && events[len(events)-1]["event"] == "connection-closed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: no connection-closed event: %v", op, events)
		}
	}
	wantOK := code == exitOK
	if result == nil || result["ok"] != wantOK {
		t.Fatalf("%s: exit %d, result %v", op, code, result)
	}
	return result, events
}

// names lists the events of the connection without the connection itself.
func names(events []object) string {
	var n []string
	for _, e := range events {
		n = append(n, e["event"].(string))
	}
	return strings.Join(n, " ")
}

func find(t *testing.T, events []object, name string) object {
	t.Helper()
	for _, e := range events {
		if e["event"] == name {
			return e
		}
	}
	t.Fatalf("no %s event in %s", name, names(events))
	return nil
}

// list is a JSON array of objects.
func list(v any) []object {
	var out []object
	for _, o := range v.([]any) {
		out = append(out, o.(object))
	}
	return out
}

func objects(t *testing.T, r object) []object {
	t.Helper()
	var all []object
	for _, a := range list(r["asdus"]) {
		all = append(all, list(a["objects"])...)
	}
	return all
}

func errorCode(r object) string {
	if e, ok := r["error"].(object); ok {
		return e["code"].(string)
	}
	return ""
}

// writeCertificate signs a certificate for the name with the parent, or itself,
// and writes it and its key as name.pem and name.key in dir.
func writeCertificate(t *testing.T, dir, name string, isCA bool, parent *x509.Certificate, parentKey *ecdsa.PrivateKey) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: isCA, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign}
	if parent == nil {
		parent, parentKey = template, key
	}
	der, err := x509.CreateCertificate(rand.Reader, template, parent, key.Public(), parentKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	der8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	for file, block := range map[string]*pem.Block{name + ".pem": {Type: "CERTIFICATE", Bytes: der}, name + ".key": {Type: "PRIVATE KEY", Bytes: der8}} {
		if err := os.WriteFile(filepath.Join(dir, file), pem.EncodeToMemory(block), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return cert, key
}

// tlsIdentities writes an authority with a station and a controlling station, a
// self-signed rogue and an authority that signed nothing.
func tlsIdentities(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	ca, caKey := writeCertificate(t, dir, "ca", true, nil, nil)
	writeCertificate(t, dir, "station", false, ca, caKey)
	writeCertificate(t, dir, "scada", false, ca, caKey)
	writeCertificate(t, dir, "rogue", true, nil, nil)
	writeCertificate(t, dir, "other-ca", true, nil, nil)
	return dir
}

func tlsFlags(dir, name, trust string) []string {
	return []string{"--tls", "--tls-cert", filepath.Join(dir, name+".pem"), "--tls-key", filepath.Join(dir, name+".key"),
		"--tls-trust", filepath.Join(dir, trust+".pem")}
}

// attempt runs one client operation that may fail before a session exists, so
// that no server event can be waited for.
func attempt(t *testing.T, out *output, common []string, op string, args ...string) (int, object) {
	t.Helper()
	code := run(append(append([]string{"client", op}, common...), args...))
	for _, d := range out.take(t) {
		if _, isEvent := d["event"]; !isEvent {
			return code, d
		}
	}
	t.Fatalf("%s: no result, exit %d", op, code)
	return code, nil
}

func TestTLSWithSecureIsAUsageError(t *testing.T) {
	capture(t)
	dir := tlsIdentities(t)
	both := append(tlsFlags(dir, "scada", "ca"), "--secure", "--security-cert", filepath.Join(dir, "scada.pem"),
		"--security-key", filepath.Join(dir, "scada.key"), "--security-trust", filepath.Join(dir, "ca.pem"))
	for _, args := range [][]string{append([]string{"client", "connect", "--host", "h"}, both...), append([]string{"server"}, both...)} {
		if code := run(args); code != exitUsage {
			t.Errorf("%v: exit %d, want %d", args, code, exitUsage)
		}
	}
}

func TestTLSKeyThatIsNotAKey(t *testing.T) {
	capture(t)
	dir := tlsIdentities(t)
	args := []string{"--tls", "--tls-cert", filepath.Join(dir, "scada.pem"), "--tls-key", filepath.Join(dir, "scada.pem"),
		"--tls-trust", filepath.Join(dir, "ca.pem")}
	for _, a := range [][]string{append([]string{"client", "connect", "--host", "h"}, args...), append([]string{"server"}, args...)} {
		if code := run(a); code != exitUsage {
			t.Errorf("%v: exit %d, want %d", a, code, exitUsage)
		}
	}
}

func TestTLSStation(t *testing.T) {
	out := capture(t)
	dir := tlsIdentities(t)
	common := startServer(t, tlsFlags(dir, "station", "ca")...)
	ready := out.take(t)
	if len(ready) != 1 || ready[0]["tls"] != true {
		t.Fatalf("ready event %v", ready)
	}

	t.Run("mutual", func(t *testing.T) {
		r, events := operate(t, out, common, "counter-interrogate", tlsFlags(dir, "scada", "ca")...)
		if r["tls"] != true || r["terminated"] != true {
			t.Errorf("result %v", r)
		}
		find(t, events, "data-transfer-started")
	})

	// A station name that is not in the certificate is no reason to refuse it.
	t.Run("no host name check", func(t *testing.T) {
		code, r := attempt(t, out, common, "connect", tlsFlags(dir, "scada", "ca")...)
		if code != exitOK || r["tls"] != true {
			t.Errorf("exit %d, result %v", code, r)
		}
	})

	for name, identity := range map[string][]string{
		"client not trusted":  {"rogue", "ca"},
		"station not trusted": {"scada", "other-ca"},
	} {
		t.Run(name, func(t *testing.T) {
			code, r := attempt(t, out, common, "counter-interrogate", tlsFlags(dir, identity[0], identity[1])...)
			if code != exitFailed || errorCode(r) != "tls-failed" || r["tls"] != false || r["connected"] != true || r["startdtConfirmed"] != false {
				t.Errorf("exit %d, result %v", code, r)
			}
			if len(list(r["asdus"])) != 0 {
				t.Errorf("result %v", r)
			}
		})
	}

	t.Run("client without TLS", func(t *testing.T) {
		code, r := attempt(t, out, common, "counter-interrogate")
		if code == exitOK || r["startdtConfirmed"] != false {
			t.Errorf("exit %d, result %v", code, r)
		}
		if _, present := r["tls"]; present {
			t.Errorf("tls is of --tls only: %v", r)
		}
	})
}

func TestTLSClientAgainstStationWithoutIt(t *testing.T) {
	out := capture(t)
	dir := tlsIdentities(t)
	common := startServer(t)
	out.take(t)
	code, r := attempt(t, out, common, "counter-interrogate", tlsFlags(dir, "scada", "ca")...)
	if code != exitFailed || errorCode(r) != "tls-failed" || r["tls"] != false {
		t.Errorf("exit %d, result %v", code, r)
	}
}

func TestTLSStationThatCannotBeReached(t *testing.T) {
	out := capture(t)
	dir := tlsIdentities(t)
	code, r := attempt(t, out, []string{"--host", "127.0.0.1", "--port", freePort(t)}, "connect", tlsFlags(dir, "scada", "ca")...)
	if code != exitConnect || errorCode(r) != "connect-failed" || r["connected"] != false {
		t.Errorf("exit %d, result %v", code, r)
	}
}

func TestAgainstOwnServer(t *testing.T) {
	out := capture(t)
	common := startServer(t)
	if docs := out.take(t); len(docs) != 1 || docs[0]["event"] != "ready" {
		t.Fatalf("not the ready event first: %v", docs)
	}

	t.Run("connect", func(t *testing.T) {
		r, events := operate(t, out, common, "connect", "--hold-ms", "10")
		if r["stopdtConfirmed"] != true {
			t.Errorf("result %v", r)
		}
		want := "connection-opened data-transfer-started data-transfer-stopped connection-closed"
		if names(events) != want {
			t.Errorf("events %q, want %q", names(events), want)
		}
	})

	t.Run("no STOPDT", func(t *testing.T) {
		r, events := operate(t, out, common, "test-command")
		if _, present := r["stopdtConfirmed"]; present {
			t.Errorf("stopdtConfirmed is of connect only: %v", r)
		}
		want := "connection-opened data-transfer-started connection-closed"
		if names(events) != want {
			t.Errorf("events %q, want %q", names(events), want)
		}
	})

	t.Run("interrogate", func(t *testing.T) {
		r, events := operate(t, out, common, "interrogate")
		if r["terminated"] != true {
			t.Errorf("result %v", r)
		}
		if got := objects(t, r); len(got) != 8 { // interrogation twice, six points
			t.Errorf("objects %v", got)
		}
		if e := find(t, events, "interrogation"); e["accepted"] != true {
			t.Errorf("event %v", e)
		}
	})

	t.Run("group interrogation", func(t *testing.T) {
		r, events := operate(t, out, common, "interrogate", "--qoi", "36")
		if r["terminated"] != true {
			t.Errorf("result %v", r)
		}
		var answer []string
		for _, a := range list(r["asdus"])[1:2] {
			answer = append(answer, fmt.Sprint(a["type"], "/", a["cot"], "/", a["count"]))
		}
		if strings.Join(answer, " ") != "M_BO_NA_1/36/1" {
			t.Errorf("answer %v", answer)
		}
		if e := find(t, events, "interrogation"); e["accepted"] != true || e["qoi"] != float64(36) {
			t.Errorf("event %v", e)
		}
		r, _ = operate(t, out, common, "interrogate", "--qoi", "22")
		if len(objects(t, r)) != 4 { // the two points of the group share an ASDU
			t.Errorf("group 2: %v", r)
		}
	})

	t.Run("empty group", func(t *testing.T) {
		r, events := operate(t, out, common, "interrogate", "--qoi", "30")
		if errorCode(r) != "negative-confirmation" {
			t.Errorf("result %v", r)
		}
		if e := find(t, events, "interrogation"); e["accepted"] != false {
			t.Errorf("event %v", e)
		}
	})

	t.Run("qualifier that is no group", func(t *testing.T) {
		r, _ := operate(t, out, common, "interrogate", "--qoi", "37")
		if errorCode(r) != "negative-confirmation" {
			t.Errorf("result %v", r)
		}
	})

	t.Run("bitstring quality", func(t *testing.T) {
		r, _ := operate(t, out, common, "read", "--ioa", "800")
		o := objects(t, r)[0]
		if o["value"] != float64(4042322160) || fmt.Sprint(o["quality"]) != "[OV NT]" {
			t.Errorf("object %v", o)
		}
	})

	t.Run("broadcast", func(t *testing.T) {
		r, events := operate(t, out, common, "interrogate", "--common-address", "65535")
		for _, a := range list(r["asdus"]) {
			if a["commonAddress"] != float64(1) {
				t.Errorf("answered from %v, not the station's address", a["commonAddress"])
			}
		}
		if e := find(t, events, "interrogation"); e["commonAddress"] != float64(65535) {
			t.Errorf("event %v", e)
		}
	})

	t.Run("counter interrogate", func(t *testing.T) {
		r, events := operate(t, out, common, "counter-interrogate")
		if r["terminated"] != true {
			t.Errorf("result %v", r)
		}
		found := false
		for _, o := range objects(t, r) {
			if o["ioa"] == float64(700) {
				found = o["sequence"] == float64(3) && fmt.Sprint(o["quality"]) == "[CY]"
			}
		}
		if !found {
			t.Errorf("no integrated total in %v", r)
		}
		find(t, events, "counter-interrogation")
	})

	t.Run("read", func(t *testing.T) {
		r, events := operate(t, out, common, "read", "--ioa", "100")
		a := list(r["asdus"])[0]
		if a["type"] != "M_SP_NA_1" || a["cot"] != float64(5) {
			t.Errorf("answer %v", a)
		}
		if e := find(t, events, "read"); e["accepted"] != true || e["ioa"] != float64(100) {
			t.Errorf("event %v", e)
		}
		r, events = operate(t, out, common, "read", "--ioa", "999")
		if errorCode(r) != "negative-confirmation" {
			t.Errorf("result %v", r)
		}
		if e := find(t, events, "read"); e["accepted"] != false {
			t.Errorf("event %v", e)
		}
	})

	t.Run("clock sync", func(t *testing.T) {
		_, events := operate(t, out, common, "clock-sync", "--time", "2026-01-02T03:04:05.678Z")
		if e := find(t, events, "clock-sync"); e["time"] != "2026-01-02T03:04:05.678Z" {
			t.Errorf("event %v", e)
		}
	})

	command := func(args ...string) []string { return append([]string{"--type", "C_SC_NA_1"}, args...) }

	t.Run("direct command", func(t *testing.T) {
		r, events := operate(t, out, common, "command", command("--ioa", "1000", "--value", "false")...)
		if r["terminated"] != true {
			t.Errorf("result %v", r)
		}
		var causes []string
		for _, a := range list(r["asdus"]) {
			causes = append(causes, fmt.Sprint(a["cot"]))
		}
		if strings.Join(causes, " ") != "7 3 10" {
			t.Errorf("causes %v, want confirmation, report, termination", causes)
		}
		if e := find(t, events, "command"); e["outcome"] != "executed" || e["value"] != false {
			t.Errorf("event %v", e)
		}
	})

	t.Run("time tag", func(t *testing.T) {
		r, events := operate(t, out, common, "command", command("--ioa", "1000", "--value", "true", "--with-time")...)
		if a := list(r["asdus"])[0]; a["type"] != "C_SC_TA_1" {
			t.Errorf("answer %v", a)
		}
		if e := find(t, events, "command"); e["type"] != "C_SC_TA_1" {
			t.Errorf("event %v", e)
		}
	})

	t.Run("select required", func(t *testing.T) {
		r, events := operate(t, out, common, "command", command("--ioa", "1001", "--value", "true")...)
		if errorCode(r) != "negative-confirmation" {
			t.Errorf("result %v", r)
		}
		if e := find(t, events, "command"); e["outcome"] != "rejected" {
			t.Errorf("event %v", e)
		}
		r, _ = operate(t, out, common, "command", command("--ioa", "1001", "--value", "true", "--mode", "sbo")...)
		if r["terminated"] != true || len(list(r["confirmations"])) != 2 {
			t.Errorf("result %v", r)
		}
		r, events = operate(t, out, common, "command", command("--ioa", "1001", "--value", "true", "--mode", "cancel")...)
		if len(list(r["confirmations"])) != 2 {
			t.Errorf("result %v", r)
		}
		var outcomes []string
		for _, e := range events {
			if e["event"] == "command" {
				outcomes = append(outcomes, e["outcome"].(string))
			}
		}
		if strings.Join(outcomes, " ") != "selected deactivated" {
			t.Errorf("outcomes %v", outcomes)
		}
	})

	t.Run("unknown command", func(t *testing.T) {
		r, events := operate(t, out, common, "command", command("--ioa", "4242", "--value", "true")...)
		if errorCode(r) != "negative-confirmation" {
			t.Errorf("result %v", r)
		}
		if e := find(t, events, "command"); e["outcome"] != "unknown-ioa" {
			t.Errorf("event %v", e)
		}
	})

	t.Run("step limit", func(t *testing.T) {
		r, _ := operate(t, out, common, "command", "--type", "C_RC_NA_1", "--ioa", "1002", "--value", "2")
		if errorCode(r) != "negative-confirmation" {
			t.Errorf("a step above 63 must be refused: %v", r)
		}
	})

	t.Run("monitor", func(t *testing.T) {
		r, _ := operate(t, out, common, "monitor", "--duration-ms", "10")
		if len(list(r["asdus"])) != 0 {
			t.Errorf("result %v", r)
		}
	})

	t.Run("file", func(t *testing.T) {
		r, events := operate(t, out, common, "file-get", "--ioa", "2000", "--name", "1")
		content := (&fileSpec{IOA: 2000, Size: 1000}).content()
		sum := sha256.Sum256(content)
		f := r["file"].(object)
		if f["length"] != float64(1000) || f["received"] != float64(1000) || f["sections"] != float64(4) ||
			f["sha256"] != hex.EncodeToString(sum[:]) {
			t.Errorf("file %v", f)
		}
		if e := find(t, events, "file-transfer"); e["success"] != true {
			t.Errorf("event %v", e)
		}
		r, _ = operate(t, out, common, "file-get", "--ioa", "2000", "--name", "9")
		if r["ok"] != false || errorCode(r) != "negative-confirmation" {
			t.Errorf("result %v", r)
		}
	})

	directory := func(t *testing.T) string {
		t.Helper()
		r, _ := operate(t, out, common, "file-dir")
		var entries []string
		for _, e := range list(r["directory"]) {
			entries = append(entries, fmt.Sprint(e["ioa"], ":", e["nof"], ":", e["lof"], ":", e["sof"]))
		}
		return strings.Join(entries, " ")
	}

	t.Run("directory", func(t *testing.T) {
		if got := directory(t); got != "2000:1:1000:32" {
			t.Errorf("directory %s", got)
		}
	})

	t.Run("upload", func(t *testing.T) {
		r, events := operate(t, out, common, "file-put", "--ioa", "3000", "--size", "5000", "--section-size", "2048")
		f := r["file"].(object)
		sum := sha256.Sum256(fileContent(3000, 5000))
		want := hex.EncodeToString(sum[:])
		if f["length"] != float64(5000) || f["received"] != float64(5000) || f["sections"] != float64(3) || f["sha256"] != want {
			t.Errorf("file %v", f)
		}
		e := find(t, events, "file-received")
		if e["ioa"] != float64(3000) || e["name"] != float64(1) || e["length"] != float64(5000) || e["sha256"] != want || e["success"] != true {
			t.Errorf("event %v", e)
		}
		for _, e := range events {
			if e["event"] == "file-transfer" {
				t.Errorf("an upload is not a download: %v", e)
			}
		}
		r, events = operate(t, out, common, "file-get", "--ioa", "3000")
		if g := r["file"].(object); g["sha256"] != want {
			t.Errorf("the stored file reads back as %v", g)
		}
		find(t, events, "file-transfer")
		if got := directory(t); got != "2000:1:1000:0 3000:1:5000:32" {
			t.Errorf("directory %s", got)
		}
		// A second upload replaces the file.
		operate(t, out, common, "file-put", "--ioa", "3000", "--size", "10")
		if got := directory(t); got != "2000:1:1000:0 3000:1:10:32" {
			t.Errorf("directory %s", got)
		}
	})

	t.Run("refused upload", func(t *testing.T) {
		for _, args := range [][]string{{"--ioa", "3999"}, {"--ioa", "3000", "--name", "9"}, {"--ioa", "2000"}, {"--ioa", "5000", "--size", "101"}} {
			if len(args) == 2 || args[2] == "--name" {
				args = append(args, "--size", "10")
			}
			r, events := operate(t, out, common, "file-put", args...)
			if errorCode(r) != "negative-confirmation" || len(list(r["confirmations"])) != 0 {
				t.Errorf("%v: result %v", args, r)
			}
			if f := r["file"].(object); f["received"] != float64(0) || f["sections"] != float64(0) {
				t.Errorf("%v: file %v", args, f)
			}
			for _, e := range events {
				if e["event"] == "file-received" {
					t.Errorf("%v: a refused upload is not received: %v", args, e)
				}
			}
		}
	})

	address := func(t *testing.T) string {
		t.Helper()
		return net.JoinHostPort(common[1], common[3])
	}
	transfer := func(t *testing.T) (*client.Client, chan *asdu.ASDU) {
		t.Helper()
		got := make(chan *asdu.ASDU, 16)
		c, err := client.New(address(t), client.WithHandler(client.HandlerFunc(func(a *asdu.ASDU) { got <- a })))
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Connect(context.Background()); err != nil {
			t.Fatal(err)
		}
		return c, got
	}
	step := func(t *testing.T, c *client.Client, got chan *asdu.ASDU, objs ...asdu.InformationObject) asdu.InformationObject {
		t.Helper()
		for _, o := range objs {
			if err := c.Send(context.Background(), asdu.New(asdu.CauseFileTransfer, 1, o)); err != nil {
				t.Fatal(err)
			}
		}
		select {
		case a := <-got:
			return a.First()
		case <-time.After(3 * time.Second):
			t.Fatal("no answer from the station")
			return nil
		}
	}
	// waitReceived returns the file-received event.
	waitReceived := func(t *testing.T) object {
		t.Helper()
		for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			for _, d := range out.take(t) {
				if d["event"] == "file-received" {
					return d
				}
			}
		}
		t.Fatal("no file-received event")
		return nil
	}

	t.Run("upload with a wrong checksum", func(t *testing.T) {
		c, got := transfer(t)
		defer func() { _ = c.Close() }()
		out.take(t)
		step(t, c, got, asdu.FileReady{IOA: 4000, Name: 2, Length: 3})
		step(t, c, got, asdu.SectionReady{IOA: 4000, Name: 2, Section: 1, Length: 3})
		ack := step(t, c, got, asdu.FileSegment{IOA: 4000, Name: 2, Section: 1, Data: []byte{1, 2, 3}},
			asdu.FileLastSegment{IOA: 4000, Name: 2, Section: 1, Qualifier: asdu.LastSectionNoDeactivation, Checksum: 7})
		if a, ok := ack.(asdu.FileAck); !ok || a.Qualifier != asdu.AckSectionNegative {
			t.Errorf("answer %#v, want a negative acknowledgement", ack)
		}
		sum := sha256.Sum256([]byte{1, 2, 3})
		if e := waitReceived(t); e["success"] != false || e["length"] != float64(3) || e["sha256"] != hex.EncodeToString(sum[:]) {
			t.Errorf("event %v", e)
		}
		if got := directory(t); strings.Contains(got, "4000:") {
			t.Errorf("a file that failed is stored: %s", got)
		}
	})

	t.Run("upload cut off", func(t *testing.T) {
		c, got := transfer(t)
		out.take(t)
		step(t, c, got, asdu.FileReady{IOA: 4000, Name: 2, Length: 3})
		_ = c.Close()
		if e := waitReceived(t); e["success"] != false || e["ioa"] != float64(4000) {
			t.Errorf("event %v", e)
		}
	})
}

// SPDX-License-Identifier: MIT

package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
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
  "files": [{"ioa": 2000, "name": 1, "size": 1000, "sectionSize": 300}]
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
func startServer(t *testing.T) []string {
	t.Helper()
	port := freePort(t)
	readyFile := filepath.Join(t.TempDir(), "ready")
	done := make(chan int, 1)
	go func() {
		done <- run([]string{"server", "--bind-address", "127.0.0.1", "--port", port,
			"--fixture", writeFixture(t, testFixture), "--ready-file", readyFile})
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
}

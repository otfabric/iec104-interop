// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	iec "github.com/cB-Sigma/iec60870"
	"github.com/cB-Sigma/iec60870/cs104"
	"github.com/cB-Sigma/iec60870/sec"
)

// The controlling station of the contract: one bounded operation per
// invocation, on cB-Sigma's cs104.Link, with --secure under its A-Profile.
// cs104.Client runs the same link but answers for itself what the contract
// wants to see (it waits for terminations and drops late confirmations);
// the file transfer is the library's procedure, iec60870.GetFile.

// failure is a failed operation: it becomes the error object of the result.
type failure struct{ code, message string }

func (f *failure) Error() string { return f.message }

func failf(code, format string, a ...any) *failure {
	return &failure{code, strings.TrimSpace(sprintf(format, a...))}
}

// session is what the receive side shares with the operation.
type session struct {
	mu            sync.Mutex
	changed       chan struct{} // closed and replaced on every change
	asdus         []object
	confirmations []object
	closed        bool
	terminated    bool
	negativeCot   int
	readAnswered  bool
	expectType    iec.TypeID
	expectRead    bool
	expectIOA     int
	// file-get
	fileRefused bool
	fileLength  int
	fileData    []byte
}

func (s *session) update(f func()) {
	s.mu.Lock()
	f()
	close(s.changed)
	s.changed = make(chan struct{})
	s.mu.Unlock()
}

// waitFor waits until cond holds, the connection closes or the timeout passes.
func (s *session) waitFor(cond func() bool, timeout time.Duration) bool {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		s.mu.Lock()
		ok, closed, changed := cond(), s.closed, s.changed
		s.mu.Unlock()
		if ok || closed {
			return ok
		}
		select {
		case <-changed:
		case <-deadline.C:
			s.mu.Lock()
			defer s.mu.Unlock()
			return cond()
		}
	}
}

func (s *session) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *session) onASDU(raw []byte, a *iec.ASDU, decodeErr error) {
	doc := asduJSON(raw, a, decodeErr)
	cot := doc["cot"].(int)
	typ := iec.TypeID(raw[0])
	s.update(func() {
		s.asdus = append(s.asdus, doc)
		switch {
		case s.expectType != 0 && typ == s.expectType:
			unknown := cot >= 44 && cot <= 47 || cot == int(iec.CauseNotAuthorized)
			if cot == 7 || cot == 9 || unknown {
				negative := doc["negative"].(bool) || unknown
				s.confirmations = append(s.confirmations, object{"cot": cot, "negative": negative})
				if negative {
					s.negativeCot = cot
				}
			} else if cot == 10 {
				s.terminated = true
			}
		case s.expectRead && cot == 5:
			for _, o := range doc["objects"].([]object) {
				if o["ioa"] == s.expectIOA {
					s.readAnswered = true
				}
			}
		}
		if decodeErr != nil || a == nil || !typ.File() || len(a.Objects) == 0 {
			return
		}
		o := a.Objects[0]
		switch {
		case cot >= 44 && cot <= 47 || cot == int(iec.CauseNotAuthorized) || a.Negative,
			typ == iec.FFrNa1 && o.Qualifier&iec.FRQNegative != 0:
			s.fileRefused = true
			s.negativeCot = cot
		case typ == iec.FFrNa1:
			s.fileLength = int(o.Length)
		case typ == iec.FSgNa1:
			s.fileData = append(s.fileData, o.Segment...)
		}
	})
}

func (s *session) awaitConfirmation(n int, timeout time.Duration) error {
	if !s.waitFor(func() bool { return len(s.confirmations) >= n }, timeout) {
		if s.isClosed() {
			return failf("connection-lost", "connection closed while waiting for confirmation %d", n)
		}
		return failf("timeout", "no confirmation %d from the station", n)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.negativeCot != 0 {
		return failf("negative-confirmation", "the station refused the request (cause %d)", s.negativeCot)
	}
	return nil
}

func (s *session) awaitTermination(timeout time.Duration) error {
	if !s.waitFor(func() bool { return s.terminated || s.negativeCot != 0 }, timeout) {
		if s.isClosed() {
			return failf("connection-lost", "connection closed while waiting for termination")
		}
		return failf("timeout", "no activation termination from the station")
	}
	return nil
}

// peer is the station as iec60870's procedures see it.
type peer struct {
	link     *cs104.Link
	ca       uint16
	oa       uint8
	taps     iec.Taps
	sections int // sections called
}

func (p *peer) Send(ctx context.Context, a *iec.ASDU) error {
	a.CA, a.Orig = p.ca, p.oa
	if a.Type == iec.FScNa1 && len(a.Objects) == 1 && a.Objects[0].Qualifier&0x0f == iec.SCQRequestSection {
		p.sections++
	}
	raw, err := a.Encode(params, time.UTC)
	if err != nil {
		return err
	}
	return p.link.Send(ctx, raw)
}

func (p *peer) Taps() *iec.Taps    { return &p.taps }
func (p *peer) MaxASDU() int       { return cs104.MaxASDU }
func (p *peer) Params() iec.Params { return params }
func (p *peer) request(ctx context.Context, t iec.TypeID, c iec.Cause, o iec.Object) error {
	return p.Send(ctx, &iec.ASDU{Type: t, Cause: c, Objects: []iec.Object{o}})
}

// processCommand is a process command of the contract, parsed once and sent
// with either state of the select bit.
type processCommand struct {
	typ       iec.TypeID
	ioa       int
	qualifier int
	value     float64
}

func (c *processCommand) send(ctx context.Context, p *peer, cause iec.Cause, sel, withTime bool) error {
	typ := c.typ
	o := iec.Object{IOA: uint32(c.ioa), Value: c.value, Select: sel, Qualifier: uint8(c.qualifier)}
	if withTime {
		typ, o.Time = timeTaggedCommand[c.typ], time.Now()
	}
	return p.request(ctx, typ, cause, o)
}

func parseBool(s string) (bool, bool) {
	switch strings.ToLower(s) {
	case "true", "1", "on":
		return true, true
	case "false", "0", "off":
		return false, true
	}
	return false, false
}

func parseCommand(a *args) (*processCommand, error) {
	typeArg := a.str("--type", "")
	value := a.str("--value", "")
	ioa, err := a.num("--ioa", 0)
	if err != nil {
		return nil, err
	}
	qualifier, err := a.num("--qualifier", 0)
	if err != nil {
		return nil, err
	}
	typ, known := commandTypes[typeArg]
	if !known || ioa < 1 || value == "" {
		return nil, usagef("command: --type (C_SC_NA_1, C_DC_NA_1, C_RC_NA_1, C_SE_NA_1, C_SE_NB_1, " +
			"C_SE_NC_1), --ioa and --value are required")
	}
	c := &processCommand{typ: typ, ioa: ioa, qualifier: qualifier}
	ok := true
	switch typ {
	case iec.CScNa1:
		var b bool
		if b, ok = parseBool(value); b {
			c.value = 1
		}
	case iec.CDcNa1, iec.CRcNa1:
		n, err := strconv.Atoi(value)
		ok, c.value = err == nil && n >= 0 && n <= 3, float64(n)
	case iec.CSeNa1:
		n, err := strconv.Atoi(value)
		ok, c.value = err == nil && n >= -32768 && n <= 32767, float64(n)/32768
	case iec.CSeNb1:
		n, err := strconv.Atoi(value)
		ok, c.value = err == nil && n >= -32768 && n <= 32767, float64(n)
	default:
		f, err := strconv.ParseFloat(value, 32)
		ok, c.value = err == nil, float64(float32(f))
	}
	if !ok {
		return nil, usagef("command: --value '%s' is not valid for %s", value, typeArg)
	}
	return c, nil
}

func runClient(op string, a *args) (int, error) {
	host := a.str("--host", "")
	var nums [6]int
	for i, f := range []struct {
		name string
		def  int
	}{{"--port", 2404}, {"--common-address", 1}, {"--originator-address", 0}, {"--timeout-ms", 5000},
		{"--connect-timeout-ms", 5000}, {"--collect-ms", 0}} {
		v, err := a.num(f.name, f.def)
		if err != nil {
			return 0, err
		}
		nums[i] = v
	}
	port, ca, oa := nums[0], uint16(nums[1]), uint8(nums[2])
	timeout := time.Duration(nums[3]) * time.Millisecond
	connectTimeout := time.Duration(nums[4]) * time.Millisecond
	collect := time.Duration(nums[5]) * time.Millisecond
	if host == "" {
		return 0, usagef("client: --host is required")
	}

	// Operation arguments are parsed before connecting so that a usage error
	// never touches the network.
	var (
		qoi, qcc           = 20, 5
		ioa, hold, maxASDU int
		fileName           = 1
		duration           = 1000
		clock              = time.Now()
		mode               = "direct"
		withTime           bool
		command            *processCommand
		err                error
	)
	switch op {
	case "connect":
		hold, err = a.num("--hold-ms", 0)
	case "interrogate":
		qoi, err = a.num("--qoi", 20)
	case "counter-interrogate":
		qcc, err = a.num("--qcc", 5)
	case "read":
		if ioa, err = a.num("--ioa", 0); err == nil && ioa < 1 {
			err = usagef("read: --ioa is required")
		}
	case "clock-sync":
		if t := a.str("--time", ""); t != "" {
			if clock, err = time.Parse(time.RFC3339Nano, t); err != nil {
				err = usagef("clock-sync: --time must look like 2026-01-02T03:04:05.678Z")
			}
		}
	case "test-command":
	case "command":
		if command, err = parseCommand(a); err != nil {
			return 0, err
		}
		mode = a.str("--mode", "direct")
		withTime = a.flag("--with-time")
		if mode != "direct" && mode != "select" && mode != "sbo" && mode != "cancel" {
			err = usagef("command: --mode must be direct, select, sbo or cancel")
		}
	case "monitor":
		if duration, err = a.num("--duration-ms", 1000); err == nil {
			maxASDU, err = a.num("--max-asdus", 0)
		}
	case "file-get":
		if ioa, err = a.num("--ioa", 0); err == nil {
			fileName, err = a.num("--name", 1)
		}
		if err == nil && (ioa < 1 || ioa > 0xffffff || fileName < 1 || fileName > 65535) {
			err = usagef("file-get: --ioa is required; --name is 1..65535")
		}
	default:
		err = usagef("client: unknown operation %s", op)
	}
	if err != nil {
		return 0, err
	}
	cfg, err := a.apci()
	if err != nil {
		return 0, err
	}
	security, err := a.securityFlags()
	if err != nil {
		return 0, err
	}
	if err := a.algorithms(security); err != nil {
		return 0, err
	}
	if err := a.finish("client " + op); err != nil {
		return 0, err
	}

	begin := time.Now()
	s := &session{changed: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var sl *sec.SecureLink
	if security != nil {
		if sl, err = security.link(true, ca); err != nil {
			return 0, usagef("security: %v", err)
		}
		cfg.Security = sl
		go sl.Run(ctx)
	}
	p := &peer{ca: ca, oa: oa}

	var (
		connected, started, stopdt, secured bool
		exitCode                            = exitOperationFailed
	)
	operation := func() error {
		nc, err := (&net.Dialer{Timeout: connectTimeout}).DialContext(ctx, "tcp",
			net.JoinHostPort(host, strconv.Itoa(port)))
		if err != nil {
			exitCode = exitConnectFailed
			return failf("connect-failed", "cannot connect to the station: %v", err)
		}
		connected = true
		link := cs104.Open(ctx, nc, cfg)
		p.link = link
		go func() {
			for raw := range link.ASDUs() {
				if len(raw) < params.Header() {
					continue
				}
				a, err := iec.DecodeASDU(raw, params, time.UTC)
				s.onASDU(raw, a, err)
				if err == nil {
					p.taps.Offer(a)
				}
			}
			<-link.Done()
			s.update(func() { s.closed = true })
		}()

		sctx, scancel := context.WithTimeout(ctx, timeout)
		err = link.StartDT(sctx)
		scancel()
		if err != nil {
			if s.waitFor(func() bool { return false }, 20*time.Millisecond); s.isClosed() {
				return failf("connection-lost", "connection closed while waiting for STARTDT con")
			}
			return failf("startdt-timeout", "no STARTDT con from the station")
		}
		started = true

		if sl != nil {
			ep := sl.Endpoint()
			deadline := time.Now().Add(timeout)
			for !ep.KeysValid() {
				if s.isClosed() {
					return failf("connection-lost", "connection closed while the secure session was set up")
				}
				if time.Now().After(deadline) {
					return failf("security-failed", "no secure session with the station: %s", ep.Status().State)
				}
				time.Sleep(5 * time.Millisecond)
			}
			secured = true
		}

		activation := iec.CauseActivation
		expect := func(t iec.TypeID) { s.update(func() { s.expectType = t }) }
		var err2 error
		switch op {
		case "connect":
			if hold > 0 {
				s.waitFor(func() bool { return false }, time.Duration(hold)*time.Millisecond)
			}
			if s.isClosed() {
				return failf("connection-lost", "connection closed while idle")
			}
			sctx, scancel := context.WithTimeout(ctx, timeout)
			err := link.StopDT(sctx)
			scancel()
			if err != nil {
				if s.waitFor(func() bool { return false }, 20*time.Millisecond); s.isClosed() {
					return failf("connection-lost", "connection closed while waiting for STOPDT con")
				}
				return failf("stopdt-timeout", "no STOPDT con from the station")
			}
			stopdt = true
		case "interrogate":
			expect(iec.CIcNa1)
			if err2 = p.request(ctx, iec.CIcNa1, activation, iec.Object{Value: float64(qoi)}); err2 == nil {
				if err2 = s.awaitConfirmation(1, timeout); err2 == nil {
					err2 = s.awaitTermination(timeout)
				}
			}
		case "counter-interrogate":
			expect(iec.CCiNa1)
			if err2 = p.request(ctx, iec.CCiNa1, activation, iec.Object{Value: float64(qcc)}); err2 == nil {
				if err2 = s.awaitConfirmation(1, timeout); err2 == nil {
					err2 = s.awaitTermination(timeout)
				}
			}
		case "read":
			s.update(func() { s.expectType, s.expectRead, s.expectIOA = iec.CRdNa1, true, ioa })
			if err2 = p.request(ctx, iec.CRdNa1, iec.CauseRequest, iec.Object{IOA: uint32(ioa)}); err2 != nil {
				break
			}
			if !s.waitFor(func() bool { return s.readAnswered || s.negativeCot != 0 }, timeout) {
				if s.isClosed() {
					return failf("connection-lost", "connection closed while waiting for the read answer")
				}
				return failf("timeout", "no answer to the read command")
			}
			s.mu.Lock()
			negative := s.negativeCot
			s.mu.Unlock()
			if negative != 0 {
				return failf("negative-confirmation", "the station refused the request (cause %d)", negative)
			}
		case "clock-sync":
			expect(iec.CCsNa1)
			if err2 = p.request(ctx, iec.CCsNa1, activation, iec.Object{Time: clock}); err2 == nil {
				err2 = s.awaitConfirmation(1, timeout)
			}
		case "test-command":
			expect(iec.CTsTa1)
			if err2 = p.request(ctx, iec.CTsTa1, activation, iec.Object{Value: 0x4938, Time: time.Now()}); err2 == nil {
				err2 = s.awaitConfirmation(1, timeout)
			}
		case "command":
			if withTime {
				expect(timeTaggedCommand[command.typ])
			} else {
				expect(command.typ)
			}
			n := 0
			if mode != "direct" {
				if err2 = command.send(ctx, p, activation, true, withTime); err2 != nil {
					break
				}
				n++
				if err2 = s.awaitConfirmation(n, timeout); err2 != nil {
					break
				}
				if mode == "cancel" {
					if err2 = command.send(ctx, p, iec.CauseDeactivation, true, withTime); err2 != nil {
						break
					}
					n++
					if err2 = s.awaitConfirmation(n, timeout); err2 != nil {
						break
					}
				}
			}
			if mode == "direct" || mode == "sbo" {
				if err2 = command.send(ctx, p, activation, false, withTime); err2 != nil {
					break
				}
				if err2 = s.awaitConfirmation(n+1, timeout); err2 == nil {
					err2 = s.awaitTermination(timeout)
				}
			}
		case "file-get":
			_, err := iec.GetFile(ctx, p, uint32(ioa), uint16(fileName), timeout)
			if err != nil {
				s.mu.Lock()
				refused, cot := s.fileRefused, s.negativeCot
				s.mu.Unlock()
				switch {
				case refused:
					return failf("negative-confirmation", "the station refused the file (cause %d)", cot)
				case s.isClosed():
					return failf("connection-lost", "connection closed during the file transfer")
				case errors.Is(err, iec.ErrNoAnswer):
					return failf("timeout", "%v", err)
				}
				return failf("failed", "%v", err)
			}
		default: // monitor
			s.waitFor(func() bool { return maxASDU > 0 && len(s.asdus) >= maxASDU }, time.Duration(duration)*time.Millisecond)
			if s.isClosed() {
				return failf("connection-lost", "connection closed while monitoring")
			}
		}
		if err2 != nil {
			var f *failure
			if !errors.As(err2, &f) && s.isClosed() {
				return failf("connection-lost", "%v", err2)
			}
			return err2
		}
		// Optionally keep listening for what the station sends afterwards.
		if collect > 0 {
			s.waitFor(func() bool { return false }, collect)
		}
		exitCode = exitOK
		return nil
	}

	err = operation()
	if p.link != nil {
		p.link.Close()
	}

	r := object{
		"schemaVersion": schemaVersion,
		"adapter":       adapter,
		"operation":     op,
		"ok":            err == nil,
		"error":         nil,
	}
	if err != nil {
		var f *failure
		if !errors.As(err, &f) {
			f = &failure{"failed", err.Error()}
		}
		r["error"] = object{"code": f.code, "message": f.message}
	}
	s.mu.Lock()
	r["connected"] = connected
	r["startdtConfirmed"] = started
	if op == "connect" {
		r["stopdtConfirmed"] = stopdt
	}
	if security != nil {
		r["secured"] = secured
	}
	r["confirmations"] = append([]object{}, s.confirmations...)
	r["terminated"] = s.terminated
	if op == "file-get" {
		sum := sha256.Sum256(s.fileData)
		r["file"] = object{"ioa": ioa, "name": fileName, "length": s.fileLength, "received": len(s.fileData),
			"sections": p.sections, "sha256": hex.EncodeToString(sum[:])}
	}
	r["asdus"] = append([]object{}, s.asdus...)
	s.mu.Unlock()
	r["elapsedMs"] = time.Since(begin).Milliseconds()
	emit(r)
	return exitCode, nil
}

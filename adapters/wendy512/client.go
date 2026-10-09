// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wendy512/go-iecp5/asdu"
	"github.com/wendy512/go-iecp5/cs104"
)

// The controlling station of the contract: one bounded operation per
// invocation.
//
// It drives go-iecp5's cs104.Client directly. The client of wendy512/iec104
// wraps the same engine but hides it, and has no select, qualifier,
// deactivation or STOPDT, which the contract needs.

// failure is a failed operation: it becomes the error object of the result.
type failure struct{ code, message string }

func (f *failure) Error() string { return f.message }

func failf(code, format string, a ...any) *failure {
	return &failure{code, strings.TrimSpace(sprintf(format, a...))}
}

// session is what the receive side of go-iecp5 shares with the operation.
type session struct {
	mu            sync.Mutex
	changed       chan struct{} // closed and replaced on every change
	asdus         []object
	confirmations []object
	connected     bool
	started       bool
	closed        bool
	terminated    bool
	negativeCot   int
	readAnswered  bool
	expectType    asdu.TypeID
	expectRead    bool
	expectIOA     int
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

func (s *session) onASDU(a *asdu.ASDU) error {
	doc := asduJSON(a)
	cot := int(a.Coa.Cause)
	s.update(func() {
		s.asdus = append(s.asdus, doc)
		switch {
		case s.expectType != 0 && a.Type == s.expectType:
			unknown := cot >= 44 && cot <= 47
			if cot == 7 || cot == 9 || unknown {
				negative := a.Coa.IsNegative || unknown
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
	})
	return nil
}

// go-iecp5 has one handler per system command and one for everything else.
func (s *session) InterrogationHandler(_ asdu.Connect, a *asdu.ASDU) error        { return s.onASDU(a) }
func (s *session) CounterInterrogationHandler(_ asdu.Connect, a *asdu.ASDU) error { return s.onASDU(a) }
func (s *session) ReadHandler(_ asdu.Connect, a *asdu.ASDU) error                 { return s.onASDU(a) }
func (s *session) TestCommandHandler(_ asdu.Connect, a *asdu.ASDU) error          { return s.onASDU(a) }
func (s *session) ClockSyncHandler(_ asdu.Connect, a *asdu.ASDU) error            { return s.onASDU(a) }
func (s *session) ResetProcessHandler(_ asdu.Connect, a *asdu.ASDU) error         { return s.onASDU(a) }
func (s *session) DelayAcquisitionHandler(_ asdu.Connect, a *asdu.ASDU) error     { return s.onASDU(a) }
func (s *session) ASDUHandler(_ asdu.Connect, a *asdu.ASDU) error                 { return s.onASDU(a) }

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

// withOriginator is the connection the go-iecp5 request helpers send on.
// They build every request with originator 0; this puts the right one in.
type withOriginator struct {
	*cs104.Client
	oa asdu.OriginAddr
}

func (w withOriginator) Send(a *asdu.ASDU) error {
	a.OrigAddr = w.oa
	return w.Client.Send(a)
}

// stopConfirmed reports whether STOPDT con arrived. go-iecp5 has no
// notification for it. It does refuse to send while data transfer is stopped,
// and it checks that before it encodes: an ASDU that cannot be encoded
// (cause 0) tells the two states apart without anything being sent.
func stopConfirmed(c *cs104.Client) bool {
	probe := asdu.NewASDU(c.Params(), asdu.Identifier{})
	return errors.Is(c.Send(probe), cs104.ErrNotActive)
}

// processCommand is a process command of the contract, parsed once and sent
// with either state of the select bit.
type processCommand struct {
	typ       asdu.TypeID
	ioa       int
	qualifier int
	boolean   bool
	integer   int
	real      float32
}

func (p *processCommand) send(c asdu.Connect, cause asdu.Cause, ca asdu.CommonAddr, sel, withTime bool) error {
	typ, now := p.typ, time.Time{}
	if withTime {
		typ, now = timeTaggedCommand[p.typ], time.Now()
	}
	coa := asdu.CauseOfTransmission{Cause: cause}
	ioa := asdu.InfoObjAddr(p.ioa)
	qoc := asdu.QualifierOfCommand{Qual: asdu.QOCQual(p.qualifier), InSelect: sel}
	qos := asdu.QualifierOfSetpointCmd{Qual: asdu.QOSQual(p.qualifier), InSelect: sel}
	switch p.typ {
	case asdu.C_SC_NA_1:
		return asdu.SingleCmd(c, typ, coa, ca, asdu.SingleCommandInfo{Ioa: ioa, Value: p.boolean, Qoc: qoc, Time: now})
	case asdu.C_DC_NA_1:
		return asdu.DoubleCmd(c, typ, coa, ca,
			asdu.DoubleCommandInfo{Ioa: ioa, Value: asdu.DoubleCommand(p.integer), Qoc: qoc, Time: now})
	case asdu.C_RC_NA_1:
		return asdu.StepCmd(c, typ, coa, ca,
			asdu.StepCommandInfo{Ioa: ioa, Value: asdu.StepCommand(p.integer), Qoc: qoc, Time: now})
	case asdu.C_SE_NA_1:
		return asdu.SetpointCmdNormal(c, typ, coa, ca,
			asdu.SetpointCommandNormalInfo{Ioa: ioa, Value: asdu.Normalize(p.integer), Qos: qos, Time: now})
	case asdu.C_SE_NB_1:
		return asdu.SetpointCmdScaled(c, typ, coa, ca,
			asdu.SetpointCommandScaledInfo{Ioa: ioa, Value: int16(p.integer), Qos: qos, Time: now})
	default: // C_SE_NC_1
		return asdu.SetpointCmdFloat(c, typ, coa, ca,
			asdu.SetpointCommandFloatInfo{Ioa: ioa, Value: p.real, Qos: qos, Time: now})
	}
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
	p := &processCommand{typ: typ, ioa: ioa, qualifier: qualifier}
	ok := true
	switch typ {
	case asdu.C_SC_NA_1:
		p.boolean, ok = parseBool(value)
	case asdu.C_DC_NA_1, asdu.C_RC_NA_1:
		p.integer, err = strconv.Atoi(value)
		ok = err == nil && p.integer >= 0 && p.integer <= 3
	case asdu.C_SE_NA_1, asdu.C_SE_NB_1:
		p.integer, err = strconv.Atoi(value)
		ok = err == nil && p.integer >= -32768 && p.integer <= 32767
	default:
		var f float64
		f, err = strconv.ParseFloat(value, 32)
		p.real, ok = float32(f), err == nil
	}
	if !ok {
		return nil, usagef("command: --value '%s' is not valid for %s", value, typeArg)
	}
	return p, nil
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
	port, ca, oa := nums[0], asdu.CommonAddr(nums[1]), asdu.OriginAddr(nums[2])
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
	if err := a.finish("client " + op); err != nil {
		return 0, err
	}
	// go-iecp5 dials with t0, which it wants between 1 and 255 seconds.
	cfg.ConnectTimeout0 = min(max(connectTimeout, cs104.ConnectTimeout0Min), cs104.ConnectTimeout0Max)

	begin := time.Now()
	s := &session{changed: make(chan struct{})}
	lib := newLibraryLog()
	connectFailed := lib.seen("connect failed, %v")
	params := *asdu.ParamsWide

	opt := cs104.NewOption().SetConfig(cfg).SetParams(&params).SetAutoReconnect(false)
	if err := opt.AddRemoteServer("tcp://" + net.JoinHostPort(host, strconv.Itoa(port))); err != nil {
		return 0, usagef("client: --host %s: %v", host, err)
	}
	c := cs104.NewClient(s, opt)
	c.LogMode(true)
	c.SetLogProvider(lib)
	c.SetOnConnectHandler(func(c *cs104.Client) {
		s.update(func() { s.connected = true })
		c.SendStartDt()
	})
	c.SetServerActiveHandler(func(*cs104.Client) { s.update(func() { s.started = true }) })
	c.SetConnectionLostHandler(func(c *cs104.Client) {
		// Without this go-iecp5 dials again half a second later, whatever
		// SetAutoReconnect says: that only covers a failed dial.
		_ = c.Close()
		s.update(func() { s.closed = true })
	})
	conn := withOriginator{c, oa}

	exitCode := exitOperationFailed
	stopdt := false
	operation := func() error {
		if err := c.Start(); err != nil {
			exitCode = exitConnectFailed
			return failf("connect-failed", "cannot connect to the station: %v", err)
		}
		up := make(chan struct{})
		go func() {
			s.waitFor(func() bool { return s.connected }, cfg.ConnectTimeout0+2*time.Second)
			close(up)
		}()
		select {
		case <-up:
		case <-connectFailed:
		}
		if !s.waitFor(func() bool { return s.connected }, 0) {
			exitCode = exitConnectFailed
			return failf("connect-failed", "cannot connect to the station")
		}
		if !s.waitFor(func() bool { return s.started }, timeout) {
			if s.isClosed() {
				return failf("connection-lost", "connection closed while waiting for STARTDT con")
			}
			return failf("startdt-timeout", "no STARTDT con from the station")
		}

		activation := asdu.CauseOfTransmission{Cause: asdu.Activation}
		expect := func(t asdu.TypeID) { s.update(func() { s.expectType = t }) }
		var err error
		switch op {
		case "connect":
			if hold > 0 {
				s.waitFor(func() bool { return false }, time.Duration(hold)*time.Millisecond)
			}
			if s.isClosed() {
				return failf("connection-lost", "connection closed while idle")
			}
			c.SendStopDt()
			deadline := time.Now().Add(timeout)
			for !stopConfirmed(c) {
				if s.isClosed() {
					return failf("connection-lost", "connection closed while waiting for STOPDT con")
				}
				if time.Now().After(deadline) {
					return failf("stopdt-timeout", "no STOPDT con from the station")
				}
				time.Sleep(5 * time.Millisecond)
			}
			// A connection that just dropped also reads as "not active".
			if s.waitFor(func() bool { return false }, 50*time.Millisecond); s.isClosed() {
				return failf("connection-lost", "connection closed while waiting for STOPDT con")
			}
			stopdt = true
		case "interrogate":
			expect(asdu.C_IC_NA_1)
			if err = asdu.InterrogationCmd(conn, activation, ca, asdu.QualifierOfInterrogation(qoi)); err == nil {
				if err = s.awaitConfirmation(1, timeout); err == nil {
					err = s.awaitTermination(timeout)
				}
			}
		case "counter-interrogate":
			expect(asdu.C_CI_NA_1)
			if err = asdu.CounterInterrogationCmd(conn, activation, ca, asdu.ParseQualifierCountCall(byte(qcc))); err == nil {
				if err = s.awaitConfirmation(1, timeout); err == nil {
					err = s.awaitTermination(timeout)
				}
			}
		case "read":
			s.update(func() { s.expectType, s.expectRead, s.expectIOA = asdu.C_RD_NA_1, true, ioa })
			if err = asdu.ReadCmd(conn, asdu.CauseOfTransmission{Cause: asdu.Request}, ca, asdu.InfoObjAddr(ioa)); err != nil {
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
			expect(asdu.C_CS_NA_1)
			if err = asdu.ClockSynchronizationCmd(conn, activation, ca, clock); err == nil {
				err = s.awaitConfirmation(1, timeout)
			}
		case "test-command":
			// asdu.TestCommandCP56Time2a has a fixed test word; the
			// contract's counter is built by hand.
			expect(asdu.C_TS_TA_1)
			t := asdu.NewASDU(&params, asdu.Identifier{
				Type: asdu.C_TS_TA_1, Variable: asdu.VariableStruct{Number: 1}, Coa: activation, CommonAddr: ca,
			})
			_ = t.AppendInfoObjAddr(asdu.InfoObjAddrIrrelevant)
			t.AppendUint16(0x4938).AppendCP56Time2a(time.Now(), time.UTC)
			if err = conn.Send(t); err == nil {
				err = s.awaitConfirmation(1, timeout)
			}
		case "command":
			if withTime {
				expect(timeTaggedCommand[command.typ])
			} else {
				expect(command.typ)
			}
			n := 0
			if mode != "direct" {
				if err = command.send(conn, asdu.Activation, ca, true, withTime); err != nil {
					break
				}
				n++
				if err = s.awaitConfirmation(n, timeout); err != nil {
					break
				}
				if mode == "cancel" {
					if err = command.send(conn, asdu.Deactivation, ca, true, withTime); err != nil {
						break
					}
					n++
					if err = s.awaitConfirmation(n, timeout); err != nil {
						break
					}
				}
			}
			if mode == "direct" || mode == "sbo" {
				if err = command.send(conn, asdu.Activation, ca, false, withTime); err != nil {
					break
				}
				if err = s.awaitConfirmation(n+1, timeout); err == nil {
					err = s.awaitTermination(timeout)
				}
			}
		default: // monitor
			s.waitFor(func() bool { return maxASDU > 0 && len(s.asdus) >= maxASDU }, time.Duration(duration)*time.Millisecond)
			if s.isClosed() {
				return failf("connection-lost", "connection closed while monitoring")
			}
		}
		if err != nil {
			return err
		}
		// Optionally keep listening for what the station sends afterwards.
		if collect > 0 {
			s.waitFor(func() bool { return false }, collect)
		}
		exitCode = exitOK
		return nil
	}

	err = operation()
	_ = c.Close()

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
			code := "failed"
			if s.isClosed() {
				code = "connection-lost"
			}
			f = &failure{code, err.Error()}
		}
		r["error"] = object{"code": f.code, "message": f.message}
	}
	s.mu.Lock()
	r["connected"] = s.connected
	r["startdtConfirmed"] = s.started
	if op == "connect" {
		r["stopdtConfirmed"] = stopdt
	}
	r["confirmations"] = append([]object{}, s.confirmations...)
	r["terminated"] = s.terminated
	r["asdus"] = append([]object{}, s.asdus...)
	s.mu.Unlock()
	r["elapsedMs"] = time.Since(begin).Milliseconds()
	emit(r)
	return exitCode, nil
}

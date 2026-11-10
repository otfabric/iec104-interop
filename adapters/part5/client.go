// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"encoding/binary"
	"errors"
	"math"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/pascaldekloe/part5"
	"github.com/pascaldekloe/part5/info"
	"github.com/pascaldekloe/part5/session"
)

// The controlling station of the contract: one bounded operation per
// invocation, on part5's session engine. The requests come from part5's
// Command builders where it has one (single, double and regulating step
// commands, the three set-points, interrogation); the others are built from
// part5's element types.

// failure is a failed operation: it becomes the error object of the result.
type failure struct{ code, message string }

func (f *failure) Error() string { return f.message }

func failf(code, format string, a ...any) *failure {
	return &failure{code, strings.TrimSpace(sprintf(format, a...))}
}

// tracker is what the operation learns from the received ASDUs.
type tracker struct {
	l             *link
	asdus         []object
	confirmations []object
	terminated    bool
	negativeCot   int
	readAnswered  bool
	expectType    info.TypeID
	expectRead    bool
	expectIOA     int
}

func (t *tracker) onASDU(raw []byte) {
	doc, u, ok := asduJSON(raw)
	if !ok {
		return
	}
	cot := int(u.Cause & 0x3f)
	t.l.update(func() {
		t.asdus = append(t.asdus, doc)
		switch {
		case t.expectType != 0 && u.Type == t.expectType:
			unknown := cot >= 44 && cot <= 47
			if cot == 7 || cot == 9 || cot == 17 || unknown {
				negative := u.Cause&info.NegFlag != 0 || unknown || cot == 17
				t.confirmations = append(t.confirmations, object{"cot": cot, "negative": negative})
				if negative {
					t.negativeCot = cot
				}
			} else if cot == 10 {
				t.terminated = true
			}
		case t.expectRead && cot == 5:
			for _, o := range doc["objects"].([]object) {
				if o["ioa"] == t.expectIOA {
					t.readAnswered = true
				}
			}
		}
	})
}

func (t *tracker) awaitConfirmation(n int, timeout time.Duration) error {
	if !t.l.waitFor(func() bool { return len(t.confirmations) >= n }, timeout) {
		if t.l.isClosed() {
			return failf("connection-lost", "connection closed while waiting for confirmation %d", n)
		}
		return failf("timeout", "no confirmation %d from the station", n)
	}
	t.l.mu.Lock()
	defer t.l.mu.Unlock()
	if t.negativeCot != 0 {
		return failf("negative-confirmation", "the station refused the request (cause %d)", t.negativeCot)
	}
	return nil
}

func (t *tracker) awaitTermination(timeout time.Duration) error {
	if !t.l.waitFor(func() bool { return t.terminated || t.negativeCot != 0 }, timeout) {
		if t.l.isClosed() {
			return failf("connection-lost", "connection closed while waiting for termination")
		}
		return failf("timeout", "no activation termination from the station")
	}
	return nil
}

// processCommand is a process command of the contract, parsed once and sent
// with either state of the select bit.
type processCommand struct {
	typ       info.TypeID
	ioa       int
	qualifier int
	boolean   bool
	integer   int
	real      float32
}

// unit builds the command with part5's Command builders, which make an
// activation without time tag; the cause and the time tag are set after.
func (p *processCommand) unit(x part5.Exchange[oa8, ca16, ioa3], cause info.Cause, sel, withTime bool) unit {
	cmd := x.Command()
	addr, _ := params.ObjAddrN(uint(p.ioa))
	var cq info.CmdQual
	cq.SetAdditional(uint(p.qualifier))
	var sq info.SetPtQual
	sq.SetN(uint(p.qualifier))
	if sel {
		cq.FlagSelect()
		sq.FlagSelect()
	}
	var u unit
	switch p.typ {
	case info.C_SC_NA_1:
		var pt info.SinglePt = info.Off
		if p.boolean {
			pt = info.On
		}
		u = cmd.SingleCmd(addr, pt, cq)
	case info.C_DC_NA_1:
		u = cmd.DoubleCmd(addr, info.DoublePt(p.integer), cq)
	case info.C_RC_NA_1:
		u = cmd.RegulCmd(addr, info.Regul(p.integer), cq)
	case info.C_SE_NA_1:
		var n info.Norm
		n.SetInt16(int16(p.integer))
		u = cmd.NormSetPt(addr, n, sq)
	case info.C_SE_NB_1:
		u = cmd.ScaledSetPt(addr, int16(p.integer), sq)
	default: // C_SE_NC_1
		u = cmd.FloatSetPt(addr, p.real, sq)
	}
	u.Cause = cause
	if withTime {
		u.Type = timeTaggedCommand[p.typ]
		tag := cp56(time.Now())
		u.Info = append(u.Info, tag[:]...)
	}
	return u
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
	if !known || ioa < 1 || ioa > 0xFFFFFF || value == "" {
		return nil, usagef("command: --type (C_SC_NA_1, C_DC_NA_1, C_RC_NA_1, C_SE_NA_1, C_SE_NB_1, " +
			"C_SE_NC_1), --ioa and --value are required")
	}
	p := &processCommand{typ: typ, ioa: ioa, qualifier: qualifier}
	ok := true
	switch typ {
	case info.C_SC_NA_1:
		p.boolean, ok = parseBool(value)
		ok = ok && qualifier >= 0 && qualifier <= 31
	case info.C_DC_NA_1, info.C_RC_NA_1:
		p.integer, err = strconv.Atoi(value)
		ok = err == nil && p.integer >= 0 && p.integer <= 3 && qualifier >= 0 && qualifier <= 31
	case info.C_SE_NA_1, info.C_SE_NB_1:
		p.integer, err = strconv.Atoi(value)
		ok = err == nil && p.integer >= -32768 && p.integer <= 32767 && qualifier >= 0 && qualifier <= 127
	default:
		var f float64
		f, err = strconv.ParseFloat(value, 32)
		p.real, ok = float32(f), err == nil && !math.IsInf(f, 0) && qualifier >= 0 && qualifier <= 127
	}
	if !ok {
		return nil, usagef("command: --value '%s' or --qualifier %d is not valid for %s", value, qualifier, typeArg)
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
	port, caN, oaN := nums[0], nums[1], nums[2]
	timeout := time.Duration(nums[3]) * time.Millisecond
	connectTimeout := time.Duration(nums[4]) * time.Millisecond
	collect := time.Duration(nums[5]) * time.Millisecond
	if host == "" {
		return 0, usagef("client: --host is required")
	}
	if port < 1 || port > 65535 || caN < 1 || caN > 65535 || oaN < 0 || oaN > 255 || nums[3] < 1 || nums[4] < 1 || nums[5] < 0 {
		return 0, usagef("client: --port, --common-address (1..65535), --originator-address (0..255) or a time is out of range")
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
		if qoi, err = a.num("--qoi", 20); err == nil && (qoi < 0 || qoi > 255) {
			err = usagef("interrogate: --qoi is an octet")
		}
	case "counter-interrogate":
		if qcc, err = a.num("--qcc", 5); err == nil && (qcc < 0 || qcc > 255) {
			err = usagef("counter-interrogate: --qcc is an octet")
		}
	case "read":
		if ioa, err = a.num("--ioa", 0); err == nil && (ioa < 1 || ioa > 0xFFFFFF) {
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
		for i, v := range a.rest {
			if v == "--with-time" {
				withTime = true
				a.rest = append(a.rest[:i], a.rest[i+1:]...)
				break
			}
		}
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

	begin := time.Now()
	x := part5.Exchange[oa8, ca16, ioa3]{}
	x.OrigAddr, _ = params.OrigAddrN(uint(oaN))
	x.ComAddr, _ = params.ComAddrN(uint(caN))

	t := &tracker{}
	var (
		connected, started, stopdt bool
		exitCode                   = exitOperationFailed
	)
	operation := func() error {
		dialer := net.Dialer{Timeout: min(connectTimeout, cfg.ConnectTimeout)}
		conn, err := dialer.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
		if err != nil {
			exitCode = exitConnectFailed
			return failf("connect-failed", "cannot connect to the station: %v", err)
		}
		connected = true
		t.l = newLink(cfg, conn, nil, t.onASDU)
		l := t.l

		// part5 sends STARTDT act for Target Up and reports Up on STARTDT
		// con. It does not run t1 for STARTDT itself, so the adapter waits.
		if err := l.target(session.Up); err != nil {
			return failf("connection-lost", "connection closed before STARTDT act")
		}
		if !l.waitFor(func() bool { return l.ups > 0 }, timeout) {
			if l.isClosed() {
				return failf("connection-lost", "connection closed while waiting for STARTDT con")
			}
			return failf("startdt-timeout", "no STARTDT con from the station")
		}
		started = true

		expect := func(typ info.TypeID) { l.update(func() { t.expectType = typ }) }
		send := func(u unit) error {
			if err := l.send(u, timeout); err != nil {
				if l.isClosed() {
					return failf("connection-lost", "connection closed before %s was sent", u.Type)
				}
				return failf("failed", "%s: %v", u.Type, err)
			}
			return nil
		}
		single := func(typ info.TypeID, cause info.Cause, body ...byte) unit {
			u := x.NewDataUnit(typ, 1, cause)
			appendIOA(&u, 0)
			u.Info = append(u.Info, body...)
			return u
		}
		switch op {
		case "connect":
			if hold > 0 {
				l.waitFor(func() bool { return false }, time.Duration(hold)*time.Millisecond)
			}
			if l.isClosed() {
				return failf("connection-lost", "connection closed while idle")
			}
			// part5 sends STOPDT act for Target Down and reports Down on
			// STOPDT con.
			if err := l.target(session.Down); err != nil {
				return failf("connection-lost", "connection closed before STOPDT act")
			}
			if !l.waitFor(func() bool { return l.downs > 0 }, timeout) {
				if l.isClosed() {
					return failf("connection-lost", "connection closed while waiting for STOPDT con")
				}
				return failf("stopdt-timeout", "no STOPDT con from the station")
			}
			// A session that closes is reported Down too.
			if l.endsSoon() {
				return failf("connection-lost", "connection closed while waiting for STOPDT con")
			}
			stopdt = true
		case "interrogate":
			expect(info.C_IC_NA_1)
			// part5's InroGroup takes groups 1..16; any other QOI is built
			// from its interrogation command with the octet replaced.
			u := x.Command().Inro()
			u.Info[len(u.Info)-1] = byte(qoi)
			if err = send(u); err == nil {
				if err = t.awaitConfirmation(1, timeout); err == nil {
					err = t.awaitTermination(timeout)
				}
			}
		case "counter-interrogate":
			expect(info.C_CI_NA_1)
			if err = send(single(info.C_CI_NA_1, info.Act, byte(qcc))); err == nil {
				if err = t.awaitConfirmation(1, timeout); err == nil {
					err = t.awaitTermination(timeout)
				}
			}
		case "read":
			l.update(func() { t.expectType, t.expectRead, t.expectIOA = info.C_RD_NA_1, true, ioa })
			u := x.NewDataUnit(info.C_RD_NA_1, 1, info.Req)
			appendIOA(&u, ioa)
			if err = send(u); err != nil {
				break
			}
			if !l.waitFor(func() bool { return t.readAnswered || t.negativeCot != 0 }, timeout) {
				if l.isClosed() {
					return failf("connection-lost", "connection closed while waiting for the read answer")
				}
				return failf("timeout", "no answer to the read command")
			}
			l.mu.Lock()
			negative := t.negativeCot
			l.mu.Unlock()
			if negative != 0 {
				return failf("negative-confirmation", "the station refused the request (cause %d)", negative)
			}
		case "clock-sync":
			expect(info.C_CS_NA_1)
			tag := cp56(clock)
			if err = send(single(info.C_CS_NA_1, info.Act, tag[:]...)); err == nil {
				err = t.awaitConfirmation(1, timeout)
			}
		case "test-command":
			// part5's TestCmd is C_TS_NA_1, which IEC 104 does not use; the
			// contract's C_TS_TA_1 is built from the element types.
			expect(info.C_TS_TA_1)
			tag := cp56(time.Now())
			body := binary.LittleEndian.AppendUint16(nil, 0x4938)
			if err = send(single(info.C_TS_TA_1, info.Act, append(body, tag[:]...)...)); err == nil {
				err = t.awaitConfirmation(1, timeout)
			}
		case "command":
			if withTime {
				expect(timeTaggedCommand[command.typ])
			} else {
				expect(command.typ)
			}
			n := 0
			if mode != "direct" {
				if err = send(command.unit(x, info.Act, true, withTime)); err != nil {
					break
				}
				n++
				if err = t.awaitConfirmation(n, timeout); err != nil {
					break
				}
				if mode == "cancel" {
					if err = send(command.unit(x, info.Deact, true, withTime)); err != nil {
						break
					}
					n++
					if err = t.awaitConfirmation(n, timeout); err != nil {
						break
					}
				}
			}
			if mode == "direct" || mode == "sbo" {
				if err = send(command.unit(x, info.Act, false, withTime)); err != nil {
					break
				}
				if err = t.awaitConfirmation(n+1, timeout); err == nil {
					err = t.awaitTermination(timeout)
				}
			}
		default: // monitor
			l.waitFor(func() bool { return maxASDU > 0 && len(t.asdus) >= maxASDU }, time.Duration(duration)*time.Millisecond)
			if l.isClosed() {
				return failf("connection-lost", "connection closed while monitoring")
			}
		}
		if err != nil {
			return err
		}
		if collect > 0 {
			l.waitFor(func() bool { return false }, collect)
		}
		exitCode = exitOK
		return nil
	}

	err = operation()
	if t.l != nil {
		t.l.close()
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
	r["connected"] = connected
	r["startdtConfirmed"] = started
	if op == "connect" {
		r["stopdtConfirmed"] = stopdt
	}
	if t.l != nil {
		t.l.mu.Lock()
	}
	r["confirmations"] = append([]object{}, t.confirmations...)
	r["terminated"] = t.terminated
	r["asdus"] = append([]object{}, t.asdus...)
	if t.l != nil {
		t.l.mu.Unlock()
	}
	r["elapsedMs"] = time.Since(begin).Milliseconds()
	emit(r)
	return exitCode, nil
}

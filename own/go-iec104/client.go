// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	iec104 "github.com/otfabric/go-iec104"
	"github.com/otfabric/go-iec104/asdu"
	"github.com/otfabric/go-iec104/client"
	"github.com/otfabric/go-iec104/sec"
)

// The counter of the test command, the same in every adapter, since the self-tests
// compare the result documents of all pairings.
const testCounter = 0x4938

// The controlling station of the contract: one bounded operation per invocation.
// It sends with Client.Send and judges what comes back itself, since the contract
// wants to see more than the request methods say (every ASDU, and a confirmation
// as it arrives). The file transfer is the library's procedure, Client.GetFile.

// failure is a failed operation: it becomes the error object of the result.
type failure struct{ code, message string }

func (f *failure) Error() string { return f.message }

func failf(code, format string, a ...any) *failure {
	return &failure{code, fmt.Sprintf(format, a...)}
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
	expectType    asdu.TypeID
	expectRead    bool
	expectIOA     asdu.IOA
	readAnswered  bool
	// file-get
	fileLength   int
	fileSections int
	fileData     []byte
}

func (s *session) update(f func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f()
	close(s.changed)
	s.changed = make(chan struct{})
}

// waitFor waits until cond holds, the connection closes or the time passes.
func (s *session) waitFor(cond func() bool, d time.Duration) bool {
	deadline := time.NewTimer(d)
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

func (s *session) sleep(d time.Duration) { s.waitFor(func() bool { return false }, d) }

func (s *session) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// refusal is a cause that says the station refused, whatever the P/N bit says.
func refusal(c asdu.Cause) bool { return c.IsUnknown() || c == asdu.CauseNotAuthorized }

func (s *session) HandleASDU(a *asdu.ASDU) {
	doc := asduJSON(a)
	s.update(func() {
		s.asdus = append(s.asdus, doc)
		switch {
		case a.Type == s.expectType:
			switch {
			case a.Cause == asdu.CauseActivationCon || a.Cause == asdu.CauseDeactivationCon || refusal(a.Cause):
				negative := a.Negative || refusal(a.Cause)
				s.confirmations = append(s.confirmations, object{"cot": int(a.Cause), "negative": negative})
				if negative {
					s.negativeCot = int(a.Cause)
				}
			case a.Cause == asdu.CauseActivationTerm:
				s.terminated = true
			}
		case s.expectRead && a.Cause == asdu.CauseRequest:
			for _, o := range a.Objects {
				if o.Address() == s.expectIOA {
					s.readAnswered = true
				}
			}
		}
		if a.Negative || refusal(a.Cause) {
			return
		}
		switch o := a.First().(type) {
		case asdu.FileReady:
			if o.Qualifier&asdu.FileNegative == 0 {
				s.fileLength = int(o.Length)
			}
		case asdu.SectionReady:
			if o.Qualifier&asdu.FileNegative == 0 {
				s.fileSections++
			}
		case asdu.FileSegment:
			s.fileData = append(s.fileData, o.Data...)
		}
	})
}

// peer is the station, as an operation talks to it.
type peer struct {
	*session
	client  *client.Client
	ca      asdu.CommonAddr
	oa      uint8
	timeout time.Duration
}

func (p *peer) send(cause asdu.Cause, obj asdu.InformationObject) error {
	a := asdu.New(cause, p.ca, obj)
	a.Originator = p.oa
	ctx, cancel := context.WithTimeout(context.Background(), p.timeout)
	defer cancel()
	return p.client.Send(ctx, a)
}

func (p *peer) expect(t asdu.TypeID) { p.update(func() { p.expectType = t }) }

func (p *peer) confirmation(n int) error {
	if !p.waitFor(func() bool { return len(p.confirmations) >= n }, p.timeout) {
		if p.isClosed() {
			return failf("connection-lost", "the connection closed while waiting for confirmation %d", n)
		}
		return failf("timeout", "no confirmation %d from the station", n)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.negativeCot != 0 {
		return failf("negative-confirmation", "the station refused the request (cause %d)", p.negativeCot)
	}
	return nil
}

func (p *peer) termination() error {
	if !p.waitFor(func() bool { return p.terminated || p.negativeCot != 0 }, p.timeout) {
		if p.isClosed() {
			return failf("connection-lost", "the connection closed while waiting for the termination")
		}
		return failf("timeout", "no activation termination from the station")
	}
	return nil
}

// activate sends an activation that ends in a termination.
func (p *peer) activate(obj asdu.InformationObject) error {
	p.expect(asdu.TypeOf(obj))
	if err := p.send(asdu.CauseActivation, obj); err != nil {
		return err
	}
	if err := p.confirmation(1); err != nil {
		return err
	}
	return p.termination()
}

// confirmed sends an activation that ends in its confirmation.
func (p *peer) confirmed(obj asdu.InformationObject) error {
	p.expect(asdu.TypeOf(obj))
	if err := p.send(asdu.CauseActivation, obj); err != nil {
		return err
	}
	return p.confirmation(1)
}

func (p *peer) read(ioa asdu.IOA) error {
	p.update(func() {
		p.expectType, p.expectRead, p.expectIOA = asdu.C_RD_NA_1, true, ioa
	})
	if err := p.send(asdu.CauseRequest, asdu.Read{IOA: ioa}); err != nil {
		return err
	}
	if !p.waitFor(func() bool { return p.readAnswered || p.negativeCot != 0 }, p.timeout) {
		if p.isClosed() {
			return failf("connection-lost", "the connection closed while waiting for the read answer")
		}
		return failf("timeout", "no answer to the read command")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.negativeCot != 0 {
		return failf("negative-confirmation", "the station refused the request (cause %d)", p.negativeCot)
	}
	return nil
}

func (p *peer) stopDT() error {
	ctx, cancel := context.WithTimeout(context.Background(), p.timeout)
	defer cancel()
	if err := p.client.StopDT(ctx); err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, iec104.ErrTimeout) {
			return failf("stopdt-timeout", "no STOPDT con from the station")
		}
		return failf("connection-lost", "the connection closed while waiting for STOPDT con: %v", err)
	}
	return nil
}

// fileError says what a failed Client.GetFile means.
func (p *peer) fileError(err error) error {
	var negative *iec104.NegativeError
	switch {
	case errors.As(err, &negative):
		return failf("negative-confirmation", "the station refused the file: %v", err)
	case errors.Is(err, iec104.ErrConnectionLost), errors.Is(err, iec104.ErrClosed), errors.Is(err, iec104.ErrNotConnected):
		return failf("connection-lost", "the connection closed during the file transfer: %v", err)
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, iec104.ErrTimeout):
		return failf("timeout", "%v", err)
	}
	return failf("failed", "%v", err)
}

// processCommand is a process command of the contract, parsed once and sent with
// either state of the select bit.
type processCommand struct {
	typ       string
	ioa       asdu.IOA
	qualifier uint8
	value     string
	withTime  bool
	mode      string
}

// object is the information object of the command. The value was checked by
// parseCommand.
func (c *processCommand) object(sel bool) asdu.InformationObject {
	var tag asdu.Timestamp
	if c.withTime {
		tag = asdu.Now()
	}
	n, _ := strconv.Atoi(c.value)
	qu := asdu.CommandQualifier(c.qualifier)
	switch c.typ {
	case "C_SC_NA_1":
		on, _ := parseBool(c.value)
		return asdu.SingleCommand{IOA: c.ioa, Value: on, Qualifier: qu, Select: sel, Time: tag}
	case "C_DC_NA_1":
		return asdu.DoubleCommand{IOA: c.ioa, Value: asdu.DoubleState(n), Qualifier: qu, Select: sel, Time: tag}
	case "C_RC_NA_1":
		return asdu.StepCommand{IOA: c.ioa, Value: asdu.StepDirection(n), Qualifier: qu, Select: sel, Time: tag}
	case "C_SE_NA_1":
		return asdu.SetpointNormalized{IOA: c.ioa, Value: asdu.Normalized(n), Qualifier: c.qualifier, Select: sel, Time: tag}
	case "C_SE_NB_1":
		return asdu.SetpointScaled{IOA: c.ioa, Value: int16(n), Qualifier: c.qualifier, Select: sel, Time: tag}
	}
	v, _ := strconv.ParseFloat(c.value, 32)
	return asdu.SetpointFloat{IOA: c.ioa, Value: float32(v), Qualifier: c.qualifier, Select: sel, Time: tag}
}

func parseCommand(f *flags) *processCommand {
	c := &processCommand{typ: f.text("type", ""), value: f.text("value", ""), withTime: f.has("with-time"),
		mode: f.text("mode", "direct"), ioa: asdu.IOA(f.required("ioa", 0, 0xFFFFFF)),
		qualifier: uint8(f.integer("qualifier", 0, 0, 255))}
	if _, known := commandTarget[c.typ]; !known {
		f.fail("--type %q is not a command type", c.typ)
	}
	if c.value == "" {
		f.fail("--value is required")
	}
	valid := false
	n, err := strconv.Atoi(c.value)
	switch c.typ {
	case "C_SC_NA_1":
		_, valid = parseBool(c.value)
	case "C_DC_NA_1", "C_RC_NA_1":
		valid = err == nil && n >= 0 && n <= 3
	case "C_SE_NA_1", "C_SE_NB_1":
		valid = err == nil && n >= -32768 && n <= 32767
	default:
		_, ferr := strconv.ParseFloat(c.value, 32)
		valid = ferr == nil
	}
	if !valid {
		f.fail("--value %q is not valid for %s", c.value, c.typ)
	}
	switch c.mode {
	case "direct", "select", "sbo", "cancel":
	default:
		f.fail("--mode %q: want direct, select, sbo or cancel", c.mode)
	}
	if f.err == nil {
		if _, err := asdu.New(asdu.CauseActivation, 1, c.object(false)).Encode(asdu.IEC104); err != nil {
			f.fail("command: %v", err)
		}
	}
	return c
}

func (c *processCommand) run(p *peer) error {
	p.expect(asdu.TypeOf(c.object(false)))
	n := 0
	if c.mode != "direct" {
		if err := p.send(asdu.CauseActivation, c.object(true)); err != nil {
			return err
		}
		n++
		if err := p.confirmation(n); err != nil {
			return err
		}
	}
	switch c.mode {
	case "cancel":
		if err := p.send(asdu.CauseDeactivation, c.object(true)); err != nil {
			return err
		}
		return p.confirmation(n + 1)
	case "direct", "sbo":
		if err := p.send(asdu.CauseActivation, c.object(false)); err != nil {
			return err
		}
		if err := p.confirmation(n + 1); err != nil {
			return err
		}
		return p.termination()
	}
	return nil
}

// parseOperation reads the flags of an operation, before anything touches the
// network, and returns what it does.
func parseOperation(op string, f *flags) (func(*peer) error, *int, *int) {
	switch op {
	case "connect":
		hold := f.milliseconds("hold-ms", 0, 0)
		return func(p *peer) error {
			p.sleep(hold)
			if p.isClosed() {
				return failf("connection-lost", "the connection closed while idle")
			}
			return p.stopDT()
		}, nil, nil
	case "interrogate":
		qoi := f.integer("qoi", 20, 0, 255)
		return func(p *peer) error { return p.activate(asdu.Interrogation{Qualifier: asdu.QOI(qoi)}) }, nil, nil
	case "counter-interrogate":
		qcc := f.integer("qcc", 5, 0, 255)
		return func(p *peer) error {
			return p.activate(asdu.CounterInterrogation{Request: uint8(qcc & 0x3f), Freeze: uint8(qcc >> 6)})
		}, nil, nil
	case "read":
		ioa := f.required("ioa", 1, 0xFFFFFF)
		return func(p *peer) error { return p.read(asdu.IOA(ioa)) }, nil, nil
	case "clock-sync":
		clock := time.Now()
		if s, ok := f.take("time"); ok {
			var err error
			if clock, err = time.Parse(time.RFC3339Nano, s); err != nil {
				f.fail("--time must look like 2026-01-02T03:04:05.678Z")
			}
		}
		return func(p *peer) error { return p.confirmed(asdu.ClockSync{Time: asdu.At(clock)}) }, nil, nil
	case "test-command":
		return func(p *peer) error { return p.confirmed(asdu.TestCommand{Counter: testCounter, Time: asdu.Now()}) }, nil, nil
	case "command":
		return parseCommand(f).run, nil, nil
	case "monitor":
		duration := f.milliseconds("duration-ms", 1000, 0)
		maxASDUs := f.integer("max-asdus", 0, 0, 1<<31-1)
		return func(p *peer) error {
			p.waitFor(func() bool { return maxASDUs > 0 && len(p.asdus) >= maxASDUs }, duration)
			if p.isClosed() {
				return failf("connection-lost", "the connection closed while monitoring")
			}
			return nil
		}, nil, nil
	case "file-get":
		ioa := f.required("ioa", 1, 0xFFFFFF)
		name := f.integer("name", 1, 1, 65535)
		return func(p *peer) error {
			if _, err := p.client.GetFile(context.Background(), p.ca, asdu.IOA(ioa), uint16(name)); err != nil {
				return p.fileError(err)
			}
			return nil
		}, &ioa, &name
	}
	f.fail("unknown operation %s", op)
	return nil, nil, nil
}

// reachDialer says when the TCP connection stands, which is where a station that
// cannot be reached ends and what TLS can get wrong begins.
type reachDialer struct {
	net.Dialer
	reached func()
}

func (d *reachDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	conn, err := d.Dialer.DialContext(ctx, network, address)
	if err == nil {
		d.reached()
	}
	return conn, err
}

// secure waits for the association the endpoint runs once data transfer started.
func (p *peer) secure(ep *sec.Endpoint) error {
	deadline := time.NewTimer(p.timeout)
	defer deadline.Stop()
	for {
		changed := ep.Changed()
		if ep.Ready() {
			return nil
		}
		if err := ep.Err(); err != nil {
			return failf("security-failed", "no secure session with the station: %v", err)
		}
		if p.isClosed() {
			return failf("connection-lost", "the connection closed while the secure session was set up")
		}
		select {
		case <-changed:
		case <-time.After(10 * time.Millisecond): // for the end of the connection
		case <-deadline.C:
			return failf("security-failed", "no secure session with the station within the timeout")
		}
	}
}

func runClient(op string, f *flags) int {
	begin := time.Now()
	host := f.text("host", "")
	if host == "" {
		f.fail("--host is required")
	}
	port := f.integer("port", 2404, 1, 65535)
	ca := asdu.CommonAddr(f.integer("common-address", 1, 0, 65535))
	oa := uint8(f.integer("originator-address", 0, 0, 255))
	connectTimeout := f.milliseconds("connect-timeout-ms", 5000, 1)
	timeout := f.milliseconds("timeout-ms", 5000, 1)
	collect := f.milliseconds("collect-ms", 0, 0)
	params := f.apci()
	secure := f.security()
	f.algorithms(secure)
	transport := f.transport()
	if secure != nil && transport != nil {
		f.fail("--tls with --secure is not in the contract")
	}
	operation, fileIOA, fileName := parseOperation(op, f)
	if err := f.finish(); err != nil {
		return usage(err.Error())
	}

	s := &session{changed: make(chan struct{})}
	var connected, started, stopped, secured bool
	// The dial has --connect-timeout-ms and the handshake --timeout-ms.
	connecting, stopConnecting := context.WithCancel(context.Background())
	defer stopConnecting()
	options := []client.Option{
		client.WithParams(params), client.WithOriginator(oa), client.WithHandler(s),
		client.WithAutoStart(false), client.WithRequestTimeout(timeout),
		client.WithDialer(&reachDialer{Dialer: net.Dialer{Timeout: connectTimeout}, reached: func() {
			connected = true
			time.AfterFunc(timeout, stopConnecting)
		}}),
		client.WithStateHandler(func(state iec104.State, _ error) {
			if state == iec104.StateDisconnected {
				s.update(func() { s.closed = true })
			}
		}),
	}
	var ep *sec.Endpoint
	if secure != nil {
		var err error
		if ep, err = secure.endpoint(sec.Controlling, ca, oa); err != nil {
			return usage(err.Error())
		}
		options = append(options, client.WithSecurity(ep))
	}
	if transport != nil {
		options = append(options, client.WithTLS(transport.client()))
	}
	c, err := client.New(net.JoinHostPort(host, strconv.Itoa(port)), options...)
	if err != nil {
		return usage(err.Error())
	}
	p := &peer{session: s, client: c, ca: ca, oa: oa, timeout: timeout}

	exit := exitFailed
	err = func() error {
		if err := c.Connect(connecting); err != nil {
			if transport != nil && connected {
				return failf("tls-failed", "no TLS session with the station: %v", err)
			}
			exit = exitConnect
			return failf("connect-failed", "cannot connect to the station: %v", err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		if err := c.StartDT(ctx); err != nil {
			if transport != nil {
				// With TLS 1.3 a client learns only here that its certificate was refused.
				return failf("tls-failed", "no STARTDT con over the TLS session: %v", err)
			}
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, iec104.ErrTimeout) {
				return failf("startdt-timeout", "no STARTDT con from the station")
			}
			return failf("connection-lost", "the connection closed while waiting for STARTDT con: %v", err)
		}
		started = true

		if ep != nil {
			if err := p.secure(ep); err != nil {
				return err
			}
			secured = true
		}
		if err := operation(p); err != nil {
			return err
		}
		stopped = op == "connect"
		s.sleep(collect)
		exit = exitOK
		return nil
	}()
	// When Close returns, the handler has seen everything that was received.
	_ = c.Close()

	r := object{"schemaVersion": schemaVersion, "adapter": adapterName, "operation": op, "ok": err == nil, "error": nil}
	if err != nil {
		var f *failure
		if !errors.As(err, &f) {
			code := "failed"
			if s.isClosed() || errors.Is(err, iec104.ErrConnectionLost) {
				code = "connection-lost"
			}
			f = &failure{code, strings.TrimSpace(err.Error())}
		}
		r["error"] = object{"code": f.code, "message": f.message}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r["connected"], r["startdtConfirmed"] = connected, started
	if op == "connect" {
		r["stopdtConfirmed"] = stopped
	}
	if secure != nil {
		r["secured"] = secured
	}
	if transport != nil {
		r["tls"] = started
	}
	r["confirmations"], r["terminated"] = append([]object{}, s.confirmations...), s.terminated
	if op == "file-get" {
		sum := sha256.Sum256(s.fileData)
		r["file"] = object{"ioa": *fileIOA, "name": *fileName, "length": s.fileLength, "received": len(s.fileData),
			"sections": s.fileSections, "sha256": hex.EncodeToString(sum[:])}
	}
	r["asdus"] = append([]object{}, s.asdus...)
	r["elapsedMs"] = time.Since(begin).Milliseconds()
	emit(r)
	return exit
}

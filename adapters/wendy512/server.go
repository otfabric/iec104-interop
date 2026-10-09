// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"errors"
	"net"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/wendy512/go-iecp5/asdu"
	"github.com/wendy512/go-iecp5/cs104"
	"github.com/wendy512/iec104/server"
)

const (
	broadcast = asdu.GlobalCommonAddr
	// Objects per ASDU when answering an interrogation; small enough for every type.
	maxObjects = 20
)

// station is the controlled station of the contract, driven by a fixture.
type station struct {
	mu     sync.Mutex // guards the point values and selections of fx
	fx     *fixture
	ca     asdu.CommonAddr
	params *asdu.Params
}

func event(name string) object { return object{"event": name} }

func peer(c asdu.Connect) string { return c.UnderlyingConn().RemoteAddr().String() }

func runServer(a *args) (int, error) {
	fixturePath := a.str("--fixture", defaultFixture)
	bind := a.str("--bind-address", "0.0.0.0")
	readyFile := a.str("--ready-file", defaultReadyFile)
	port, err := a.num("--port", 2404)
	if err != nil {
		return 0, err
	}
	cfg, err := a.apci()
	if err != nil {
		return 0, err
	}
	if err := a.finish("server"); err != nil {
		return 0, err
	}
	if port < 1 || port > 65535 {
		return 0, usagef("--port: %d is not a TCP port", port)
	}

	fx, err := loadFixture(fixturePath)
	if err != nil {
		logf("fixture %s: %v", fixturePath, err)
		return exitFixtureInvalid, nil
	}
	_ = os.Remove(readyFile)

	params := *asdu.ParamsWide // time tags are UTC on the wire
	st := &station{fx: fx, ca: asdu.CommonAddr(fx.Station.CommonAddr), params: &params}
	lib := newLibraryLog()
	listening, failed := lib.seen("server run"), lib.seen("server run failed, %v")

	srv := server.New(&server.Settings{
		Host: bind, Port: port, Cfg104: &cfg, Params: &params,
		LogCfg: &server.LogCfg{Enable: true, LogProvider: lib},
	}, st)
	srv.SetOnConnectionHandler(func(c asdu.Connect) {
		e := event("connection-opened")
		e["peer"] = peer(c)
		emit(e)
	})
	srv.SetConnectionLostHandler(func(c asdu.Connect) {
		e := event("connection-closed")
		e["peer"] = peer(c)
		emit(e)
	})

	// Start does not report whether it could listen; the library log does.
	srv.Start()
	select {
	case <-listening:
	case <-failed:
		logf("cannot listen on %s:%d", bind, port)
		return exitOperationFailed, nil
	case <-time.After(10 * time.Second):
		logf("the listener on %s:%d did not come up", bind, port)
		return exitOperationFailed, nil
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)

	if err := os.WriteFile(readyFile, []byte("ready\n"), 0o644); err != nil {
		logf("warning: cannot write ready file %s", readyFile)
	}
	ready := event("ready")
	ready["adapter"] = adapter
	ready["address"] = net.JoinHostPort(bind, strconv.Itoa(port))
	ready["fixture"] = fx.Name
	ready["commonAddress"] = fx.Station.CommonAddr
	emit(ready)

	select {
	case <-stop:
	case <-failed:
		logf("the listener died")
		return exitOperationFailed, nil
	}
	_ = os.Remove(readyFile)
	// Stop waits for every session to end; do not let one hold up the exit.
	done := make(chan struct{})
	go func() { srv.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
	}
	emit(event("stopped"))
	return exitOK, nil
}

// send queues an ASDU. go-iecp5 queues 16 times k ASDUs per connection and
// refuses more; wait for room instead of losing the answer.
func send(c asdu.Connect, a *asdu.ASDU) {
	deadline := time.Now().Add(15 * time.Second)
	for {
		err := c.Send(a)
		if err == nil {
			return
		}
		if !errors.Is(err, cs104.ErrBufferFulled) || time.Now().After(deadline) {
			logf("send %s failed: %v", a.Type, err)
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// rebuild returns the request as it was received. go-iecp5 decodes a system
// command before it calls the handler, and decoding consumes the ASDU: what
// the handler gets has lost its information object, so a mirror made from it
// (ASDU.Reply, ASDU.SendReplyMirror) would be malformed.
func rebuild(pack *asdu.ASDU, body func(*asdu.ASDU)) *asdu.ASDU {
	a := asdu.NewASDU(pack.Params, pack.Identifier)
	body(a)
	return a
}

// reply sends the mirror of a request. Unlike ASDU.SendReplyMirror it can set
// the P/N bit.
func reply(c asdu.Connect, request *asdu.ASDU, cause asdu.Cause, negative bool, ca asdu.CommonAddr) {
	r := request.Clone()
	r.Coa.Cause = cause
	r.Coa.IsNegative = negative
	r.CommonAddr = ca
	send(c, r)
}

func (s *station) reject(c asdu.Connect, request *asdu.ASDU, cause asdu.Cause) {
	reply(c, request, cause, true, request.CommonAddr)
}

func (s *station) confirm(c asdu.Connect, request *asdu.ASDU, negative bool) {
	reply(c, request, asdu.ActivationCon, negative, s.ca)
}

// forStation refuses a request for another station and reports whether the
// request is for this one.
func (s *station) forStation(c asdu.Connect, request *asdu.ASDU) bool {
	if request.CommonAddr == s.ca || request.CommonAddr == broadcast {
		return true
	}
	s.reject(c, request, asdu.UnknownCA)
	return false
}

// pointASDU is an ASDU with the given points, all of one type.
func (s *station) pointASDU(pts []*point, timed bool, cause asdu.Cause, oa asdu.OriginAddr) *asdu.ASDU {
	typ := pts[0].typ
	if timed {
		typ = timeTagged[typ]
	}
	a := asdu.NewASDU(s.params, asdu.Identifier{
		Type:     typ,
		Variable: asdu.VariableStruct{Number: byte(len(pts))},
		Coa:      asdu.CauseOfTransmission{Cause: cause},
		OrigAddr: oa, CommonAddr: s.ca,
	})
	now := time.Now()
	for _, p := range pts {
		if err := appendPoint(a, p, timed, now); err != nil {
			logf("cannot encode point %d: %v", p.IOA, err)
		}
	}
	return a
}

// sendPoints sends the integrated totals (counters) or everything else, in
// ascending address order, consecutive points of one type sharing an ASDU.
func (s *station) sendPoints(c asdu.Connect, cause asdu.Cause, oa asdu.OriginAddr, counters bool) {
	var out []*asdu.ASDU
	var batch []*point
	s.mu.Lock()
	for _, p := range s.fx.Points {
		if (p.typ == asdu.M_IT_NA_1) != counters {
			continue
		}
		if len(batch) > 0 && (p.typ != batch[0].typ || len(batch) >= maxObjects) {
			out = append(out, s.pointASDU(batch, false, cause, oa))
			batch = nil
		}
		batch = append(batch, p)
	}
	if len(batch) > 0 {
		out = append(out, s.pointASDU(batch, false, cause, oa))
	}
	s.mu.Unlock()
	for _, a := range out {
		send(c, a)
	}
}

// OnInterrogation handles C_IC_NA_1. go-iecp5 passes cause 6 and 8.
func (s *station) OnInterrogation(c asdu.Connect, pack *asdu.ASDU, qoi asdu.QualifierOfInterrogation) error {
	request := rebuild(pack, func(a *asdu.ASDU) {
		_ = a.AppendInfoObjAddr(asdu.InfoObjAddrIrrelevant)
		a.AppendBytes(byte(qoi))
	})
	if !s.forStation(c, request) {
		return nil
	}
	if request.Coa.Cause != asdu.Activation {
		s.reject(c, request, asdu.UnknownCOT)
		return nil
	}
	accepted := qoi == asdu.QOIStation
	e := event("interrogation")
	e["commonAddress"] = int(request.CommonAddr)
	e["qoi"] = int(qoi)
	e["accepted"] = accepted
	emit(e)
	if !accepted {
		s.confirm(c, request, true)
		return nil
	}
	s.confirm(c, request, false)
	s.sendPoints(c, asdu.InterrogatedByStation, request.OrigAddr, false)
	reply(c, request, asdu.ActivationTerm, false, s.ca)
	return nil
}

// OnCounterInterrogation handles C_CI_NA_1 with cause 6.
func (s *station) OnCounterInterrogation(c asdu.Connect, pack *asdu.ASDU, qcc asdu.QualifierCountCall) error {
	request := rebuild(pack, func(a *asdu.ASDU) {
		_ = a.AppendInfoObjAddr(asdu.InfoObjAddrIrrelevant)
		a.AppendBytes(qcc.Value())
	})
	if !s.forStation(c, request) {
		return nil
	}
	// Only "general request counter" with "read" (no freeze, no reset).
	accepted := qcc.Request == asdu.QCCTotal && qcc.Freeze == asdu.QCCFrzRead
	e := event("counter-interrogation")
	e["commonAddress"] = int(request.CommonAddr)
	e["qcc"] = int(qcc.Value())
	e["accepted"] = accepted
	emit(e)
	if !accepted {
		s.confirm(c, request, true)
		return nil
	}
	s.confirm(c, request, false)
	s.sendPoints(c, asdu.RequestByGeneralCounter, request.OrigAddr, true)
	reply(c, request, asdu.ActivationTerm, false, s.ca)
	return nil
}

// OnRead handles C_RD_NA_1 with cause 5.
func (s *station) OnRead(c asdu.Connect, pack *asdu.ASDU, ioa asdu.InfoObjAddr) error {
	request := rebuild(pack, func(a *asdu.ASDU) { _ = a.AppendInfoObjAddr(ioa) })
	if !s.forStation(c, request) {
		return nil
	}
	var answer *asdu.ASDU
	s.mu.Lock()
	if p := s.fx.point(int(ioa)); p != nil {
		answer = s.pointASDU([]*point{p}, false, asdu.Request, request.OrigAddr)
	}
	s.mu.Unlock()
	e := event("read")
	e["ioa"] = int(ioa)
	e["accepted"] = answer != nil
	emit(e)
	if answer == nil {
		s.reject(c, request, asdu.UnknownIOA)
		return nil
	}
	send(c, answer)
	return nil
}

// OnClockSync handles C_CS_NA_1 with cause 6.
func (s *station) OnClockSync(c asdu.Connect, pack *asdu.ASDU, t time.Time) error {
	request := rebuild(pack, func(a *asdu.ASDU) {
		_ = a.AppendInfoObjAddr(asdu.InfoObjAddrIrrelevant)
		a.AppendCP56Time2a(t, time.UTC)
	})
	if !s.forStation(c, request) {
		return nil
	}
	e := event("clock-sync")
	e["time"] = formatTime(t)
	emit(e)
	s.confirm(c, request, false)
	return nil
}

// OnResetProcess handles C_RP_NA_1, which the station does not serve.
func (s *station) OnResetProcess(c asdu.Connect, pack *asdu.ASDU, qrp asdu.QualifierOfResetProcessCmd) error {
	request := rebuild(pack, func(a *asdu.ASDU) {
		_ = a.AppendInfoObjAddr(asdu.InfoObjAddrIrrelevant)
		a.AppendBytes(byte(qrp))
	})
	if s.forStation(c, request) {
		s.reject(c, request, asdu.UnknownTypeID)
	}
	return nil
}

// OnDelayAcquisition handles C_CD_NA_1, which the station does not serve.
func (s *station) OnDelayAcquisition(c asdu.Connect, pack *asdu.ASDU, msec uint16) error {
	request := rebuild(pack, func(a *asdu.ASDU) {
		_ = a.AppendInfoObjAddr(asdu.InfoObjAddrIrrelevant)
		a.AppendUint16(msec)
	})
	if s.forStation(c, request) {
		s.reject(c, request, asdu.UnknownTypeID)
	}
	return nil
}

// OnTestCommand is never called: go-iecp5 answers C_TS_NA_1 itself.
func (s *station) OnTestCommand(asdu.Connect, *asdu.ASDU) error { return nil }

// OnASDU handles everything go-iecp5 has no handler for. Returning an error
// would make it send a mirror with cause 44 but without the P/N bit.
func (s *station) OnASDU(c asdu.Connect, pack *asdu.ASDU) error {
	request := pack.Clone() // decoding pack consumes it
	if !s.forStation(c, request) {
		return nil
	}
	cause := request.Coa.Cause
	switch request.Type {
	case asdu.C_TS_TA_1:
		if cause != asdu.Activation {
			s.reject(c, request, asdu.UnknownCOT)
			return nil
		}
		s.confirm(c, request, false)
	case asdu.C_SC_NA_1, asdu.C_DC_NA_1, asdu.C_RC_NA_1, asdu.C_SE_NA_1, asdu.C_SE_NB_1, asdu.C_SE_NC_1:
		if cause != asdu.Activation && cause != asdu.Deactivation {
			s.reject(c, request, asdu.UnknownCOT)
			return nil
		}
		s.command(c, request, pack)
	default:
		s.reject(c, request, asdu.UnknownTypeID)
	}
	return nil
}

func (s *station) command(c asdu.Connect, request, pack *asdu.ASDU) {
	var (
		ioa        asdu.InfoObjAddr
		value      float64
		eventValue any
		sel        bool
		valid      = true
	)
	switch pack.Type {
	case asdu.C_SC_NA_1:
		i := pack.GetSingleCmd()
		ioa, sel, eventValue = i.Ioa, i.Qoc.InSelect, i.Value
		if i.Value {
			value = 1
		}
	case asdu.C_DC_NA_1:
		i := pack.GetDoubleCmd()
		ioa, sel, value, eventValue = i.Ioa, i.Qoc.InSelect, float64(i.Value), int(i.Value)
		valid = i.Value == asdu.DCOOn || i.Value == asdu.DCOOff
	case asdu.C_RC_NA_1:
		i := pack.GetStepCmd()
		ioa, sel, value, eventValue = i.Ioa, i.Qoc.InSelect, float64(i.Value), int(i.Value)
		valid = i.Value == asdu.SCOStepDown || i.Value == asdu.SCOStepUP
	case asdu.C_SE_NA_1:
		i := pack.GetSetpointNormalCmd()
		ioa, sel, value, eventValue = i.Ioa, i.Qos.InSelect, float64(i.Value), int(i.Value)
	case asdu.C_SE_NB_1:
		i := pack.GetSetpointCmdScaled()
		ioa, sel, value, eventValue = i.Ioa, i.Qos.InSelect, float64(i.Value), int(i.Value)
	default: // C_SE_NC_1
		i := pack.GetSetpointFloatCmd()
		ioa, sel, value, eventValue = i.Ioa, i.Qos.InSelect, float64(i.Value), float64(i.Value)
	}

	e := event("command")
	e["type"] = typeNames[request.Type]
	e["ioa"] = int(ioa)
	e["cot"] = int(request.Coa.Cause)

	var outcome string
	var report *asdu.ASDU
	s.mu.Lock()
	cmd := s.fx.command(int(ioa))
	if cmd == nil || cmd.typ != request.Type {
		s.mu.Unlock()
		e["outcome"] = "unknown-ioa"
		emit(e)
		s.reject(c, request, asdu.UnknownIOA)
		return
	}
	e["value"] = eventValue
	e["select"] = sel
	switch {
	case request.Coa.Cause == asdu.Deactivation:
		cmd.selected = false
		outcome = "deactivated"
	case !valid:
		outcome = "rejected"
	case sel:
		cmd.selected = true
		outcome = "selected"
	case cmd.SelectRequired && !cmd.selected:
		outcome = "rejected"
	default:
		cmd.selected = false
		t := cmd.target
		next := value
		if request.Type == asdu.C_RC_NA_1 {
			next = t.value - 1
			if value == 2 {
				next = t.value + 1
			}
		}
		if request.Type == asdu.C_RC_NA_1 && (next < -64 || next > 63) {
			outcome = "rejected" // the step position is at its limit
		} else {
			t.value = next
			report = s.pointASDU([]*point{t}, true, asdu.Cause(*cmd.ReportCause), 0)
			outcome = "executed"
		}
	}
	s.mu.Unlock()
	e["outcome"] = outcome
	emit(e)

	switch outcome {
	case "deactivated":
		reply(c, request, asdu.DeactivationCon, false, s.ca)
	case "rejected":
		s.confirm(c, request, true)
	default:
		s.confirm(c, request, false)
		if report != nil {
			// Confirmation, then the new state of the target, then termination.
			send(c, report)
			reply(c, request, asdu.ActivationTerm, false, s.ca)
		}
	}
}

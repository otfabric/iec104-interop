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

	"github.com/pascaldekloe/part5/info"
	"github.com/pascaldekloe/part5/session"
)

const (
	broadcast = 65535
	// Objects per ASDU when answering an interrogation; small enough for every type.
	maxObjects = 20
	// Simultaneous connections the station takes (contract: up to 16).
	maxConnections = 16
	// How long the station waits for part5 to take an ASDU: at most until the
	// window of k I frames opens again, which t1 bounds.
	sendWait = 20 * time.Second
)

// station is the controlled station of the contract, driven by a fixture.
//
// part5 has a session engine for either side of a connection and no station
// application, so the adapter accepts the connections, gives each to
// session.TCP and answers the requests from the fixture.
type station struct {
	mu  sync.Mutex // guards the point values and selections of fx
	fx  *fixture
	ca  uint
	cfg session.TCPConfig
}

func event(name string) object { return object{"event": name} }

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

	address := net.JoinHostPort(bind, strconv.Itoa(port))
	ln, err := net.Listen("tcp", address)
	if err != nil {
		logf("cannot listen on %s: %v", address, err)
		return exitOperationFailed, nil
	}
	st := &station{fx: fx, ca: uint(fx.Station.CommonAddr), cfg: cfg}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)

	if err := os.WriteFile(readyFile, []byte("ready\n"), 0o644); err != nil {
		logf("warning: cannot write ready file %s", readyFile)
	}
	ready := event("ready")
	ready["adapter"] = adapter
	ready["address"] = address
	ready["fixture"] = fx.Name
	ready["commonAddress"] = fx.Station.CommonAddr
	emit(ready)

	var (
		connMu sync.Mutex
		links  = map[*link]bool{}
		wg     sync.WaitGroup
	)
	acceptDone := make(chan error, 1)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				acceptDone <- err
				return
			}
			connMu.Lock()
			if len(links) >= maxConnections {
				connMu.Unlock()
				logf("refused %s: %d connections open", conn.RemoteAddr(), maxConnections)
				_ = conn.Close()
				continue
			}
			p := conn.RemoteAddr().String()
			e := event("connection-opened")
			e["peer"] = p
			emit(e)
			var l *link
			started := false
			l = newLink(cfg, conn, func(lv session.Level) { st.onLevel(l, &started, lv) },
				func(raw []byte) { st.onASDU(l, raw) })
			links[l] = true
			connMu.Unlock()
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-l.done
				connMu.Lock()
				delete(links, l)
				connMu.Unlock()
				e := event("connection-closed")
				e["peer"] = p
				emit(e)
			}()
		}
	}()

	select {
	case <-stop:
	case err := <-acceptDone:
		logf("the listener died: %v", err)
		return exitOperationFailed, nil
	}
	_ = os.Remove(readyFile)
	_ = ln.Close()
	connMu.Lock()
	open := make([]*link, 0, len(links))
	for l := range links {
		open = append(open, l)
	}
	connMu.Unlock()
	for _, l := range open {
		go l.close()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
	}
	emit(event("stopped"))
	return exitOK, nil
}

// onLevel turns the level reports of part5 into the data transfer events.
// part5 reports Down once when a connection opens, Up on STARTDT act, Down on
// STOPDT act, and Down again when a started session closes, which is not a
// STOPDT.
func (s *station) onLevel(l *link, started *bool, lv session.Level) {
	var e object
	switch {
	case lv == session.Up && !*started:
		*started = true
		e = event("data-transfer-started")
	case lv == session.Down && *started:
		*started = false
		if l.endsSoon() {
			return
		}
		e = event("data-transfer-stopped")
	default:
		return
	}
	e["peer"] = l.peer
	emit(e)
}

func (s *station) send(l *link, u unit) {
	if err := l.send(u, sendWait); err != nil && !errors.Is(err, errLinkClosed) {
		logf("%s: send %s: %v", l.peer, u.Type, err)
	}
}

// mirror sends the request back with another cause, from this station.
func (s *station) mirror(l *link, request unit, cause info.Cause, negative bool, ca uint) {
	r := request
	r.Info = append([]byte(nil), request.Info...)
	r.Cause = cause | request.Cause&info.TestFlag
	if negative {
		r.Cause |= info.NegFlag
	}
	r.Addr, _ = params.ComAddrN(ca)
	s.send(l, r)
}

func (s *station) reject(l *link, request unit, cause info.Cause) {
	s.mirror(l, request, cause, true, request.Addr.N())
}

func (s *station) confirm(l *link, request unit, negative bool) {
	s.mirror(l, request, info.Actcon, negative, s.ca)
}

// forStation refuses a request for another station and reports whether the
// request is for this one.
func (s *station) forStation(l *link, request unit) bool {
	if ca := request.Addr.N(); ca == s.ca || ca == broadcast {
		return true
	}
	s.reject(l, request, info.UnkAddr)
	return false
}

// pointUnit is an ASDU with the given points, all of one type.
func (s *station) pointUnit(pts []*point, timed bool, cause info.Cause, oa uint) unit {
	typ := pts[0].typ
	if timed {
		typ = timeTagged[typ]
	}
	u := newUnit(typ, len(pts), cause, oa, s.ca)
	now := time.Now()
	for _, p := range pts {
		appendPoint(&u, p, timed, now)
	}
	return u
}

// sendPoints sends the points chosen by include, in ascending address order,
// consecutive points of one type sharing an ASDU.
func (s *station) sendPoints(l *link, cause info.Cause, oa uint, include func(*point) bool) {
	var out []unit
	var batch []*point
	s.mu.Lock()
	for _, p := range s.fx.Points {
		if !include(p) {
			continue
		}
		if len(batch) > 0 && (p.typ != batch[0].typ || len(batch) >= maxObjects) {
			out = append(out, s.pointUnit(batch, false, cause, oa))
			batch = nil
		}
		batch = append(batch, p)
	}
	if len(batch) > 0 {
		out = append(out, s.pointUnit(batch, false, cause, oa))
	}
	s.mu.Unlock()
	for _, u := range out {
		s.send(l, u)
	}
}

// onASDU answers one request. part5 hands I frames over only while data
// transfer is started.
func (s *station) onASDU(l *link, raw []byte) {
	request := params.NewDataUnit()
	if err := request.Adopt(raw); err != nil {
		// part5 refuses type 0, cause 0 and common address 0.
		logf("%s: cannot read ASDU % x: %v", l.peer, raw, err)
		return
	}
	request.Info = append([]byte(nil), request.Info...)
	cause := request.Cause & 0x3f
	size, served := controlSize[request.Type]
	if !served {
		if s.forStation(l, request) {
			s.reject(l, request, info.UnkType)
		}
		return
	}
	if !s.forStation(l, request) {
		return
	}
	elems, ok := elements(request, size)
	if !ok || len(elems) != 1 {
		logf("%s: %s with %d octets for %d objects", l.peer, request.Type, len(request.Info), request.Enc.Count())
		s.reject(l, request, info.UnkInfo)
		return
	}
	e := elems[0]
	switch request.Type {
	case info.C_IC_NA_1:
		if cause != info.Act {
			s.reject(l, request, info.UnkCause)
			return
		}
		s.interrogation(l, request, int(e.body[0]))
	case info.C_CI_NA_1:
		if cause != info.Act {
			s.reject(l, request, info.UnkCause)
			return
		}
		s.counterInterrogation(l, request, int(e.body[0]))
	case info.C_RD_NA_1:
		if cause != info.Req {
			s.reject(l, request, info.UnkCause)
			return
		}
		s.read(l, request, e.ioa)
	case info.C_CS_NA_1:
		if cause != info.Act {
			s.reject(l, request, info.UnkCause)
			return
		}
		tag := info.CP56Time2a(e.body[:7])
		ev := event("clock-sync")
		ev["time"] = formatTime(tag.Within20thCentury(time.UTC))
		emit(ev)
		s.confirm(l, request, false)
	case info.C_TS_TA_1:
		if cause != info.Act {
			s.reject(l, request, info.UnkCause)
			return
		}
		s.confirm(l, request, false)
	default: // process commands
		if cause != info.Act && cause != info.Deact {
			s.reject(l, request, info.UnkCause)
			return
		}
		s.command(l, request, e)
	}
}

func (s *station) interrogation(l *link, request unit, qoi int) {
	// QOI 21..36 are the groups 1..16, and the cause of the answer is the QOI.
	group := qoi - 20
	accepted := qoi == 20 || (group >= 1 && group <= 16 && s.fx.hasGroup(group))
	e := event("interrogation")
	e["commonAddress"] = int(request.Addr.N())
	e["qoi"] = qoi
	e["accepted"] = accepted
	emit(e)
	if !accepted {
		s.confirm(l, request, true)
		return
	}
	s.confirm(l, request, false)
	include := func(p *point) bool { return p.inGroup(group) }
	if qoi == 20 {
		include = func(p *point) bool { return p.typ != info.M_IT_NA_1 }
	}
	s.sendPoints(l, info.Cause(qoi), request.Orig.N(), include)
	s.mirror(l, request, info.Actterm, false, s.ca)
}

func (s *station) counterInterrogation(l *link, request unit, qcc int) {
	// Only "general request counter" (RQT 5) with "read" (FRZ 0).
	accepted := qcc == 5
	e := event("counter-interrogation")
	e["commonAddress"] = int(request.Addr.N())
	e["qcc"] = qcc
	e["accepted"] = accepted
	emit(e)
	if !accepted {
		s.confirm(l, request, true)
		return
	}
	s.confirm(l, request, false)
	s.sendPoints(l, info.Reqcogen, request.Orig.N(), func(p *point) bool { return p.typ == info.M_IT_NA_1 })
	s.mirror(l, request, info.Actterm, false, s.ca)
}

func (s *station) read(l *link, request unit, ioa int) {
	var answer *unit
	s.mu.Lock()
	if p := s.fx.point(ioa); p != nil {
		u := s.pointUnit([]*point{p}, false, info.Req, request.Orig.N())
		answer = &u
	}
	s.mu.Unlock()
	e := event("read")
	e["ioa"] = ioa
	e["accepted"] = answer != nil
	emit(e)
	if answer == nil {
		s.reject(l, request, info.UnkInfo)
		return
	}
	s.send(l, *answer)
}

func (s *station) command(l *link, request unit, el element) {
	plain := plainCommand[request.Type]
	value, eventValue, sel, _ := commandValue(request.Type, el.body)
	valid := true
	switch plain {
	case info.C_DC_NA_1, info.C_RC_NA_1:
		valid = value == 1 || value == 2
	}

	e := event("command")
	e["type"] = typeNames[request.Type]
	e["ioa"] = el.ioa
	e["cot"] = int(request.Cause & 0x3f)

	var outcome string
	var report *unit
	s.mu.Lock()
	cmd := s.fx.command(el.ioa)
	if cmd == nil || cmd.typ != plain {
		s.mu.Unlock()
		e["outcome"] = "unknown-ioa"
		emit(e)
		s.reject(l, request, info.UnkInfo)
		return
	}
	e["value"] = eventValue
	e["select"] = sel
	switch {
	case request.Cause&0x3f == info.Deact:
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
		if plain == info.C_RC_NA_1 {
			next = t.value - 1
			if value == 2 {
				next = t.value + 1
			}
		}
		if plain == info.C_RC_NA_1 && (next < -64 || next > 63) {
			outcome = "rejected" // the step position is at its limit
		} else {
			t.value = next
			u := s.pointUnit([]*point{t}, true, info.Cause(*cmd.ReportCause), 0)
			report = &u
			outcome = "executed"
		}
	}
	s.mu.Unlock()
	e["outcome"] = outcome
	emit(e)

	switch outcome {
	case "deactivated":
		s.mirror(l, request, info.Deactcon, false, s.ca)
	case "rejected":
		s.confirm(l, request, true)
	default:
		s.confirm(l, request, false)
		if report != nil {
			// Confirmation, then the new state of the target, then termination.
			s.send(l, *report)
			s.mirror(l, request, info.Actterm, false, s.ca)
		}
	}
}
